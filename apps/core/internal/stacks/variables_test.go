package stacks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/httpx"
)

// hostKey writes a host age key file, as sops-nix keeps it, with a new
// identity and returns its path.
func hostKey(t *testing.T, dir string) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keys.txt")
	content := "# created: 2026-11-16T12:00:00Z\n# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// secretsManifest is immich at version 4, declaring secrets.
func secretsManifest(secrets string) string {
	return strings.Replace(v1Manifest, `"version": 1,`, `"version": 4, "secrets": [`+secrets+`],`, 1)
}

// v7Secrets is immich (version 7) declaring only required secret variables;
// secrets are {"name": …} objects.
func v7Secrets(secrets string) string {
	return v7Vars(strings.ReplaceAll(secrets, `{"name"`, `{"kind":"secret","required":true,"name"`))
}

func varByName(t *testing.T, s Stack, name string) StackVariable {
	t.Helper()
	for _, v := range s.Variables {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no variable %s in %+v", name, s.Variables)
	return StackVariable{}
}

func missingOf(t *testing.T, err error) []string {
	t.Helper()
	wantCode(t, err, 409, "variables_not_ready")
	var p *httpx.Problem
	errors.As(err, &p)
	m, _ := p.Ext["missing"].([]string)
	return m
}

func TestVariablesAreStoredEncryptedToTheHostKey(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVariable(ctx, "immich", "LOG_LEVEL", StackVariableSet{Value: "plain-value"}); err != nil {
		t.Fatal(err)
	}
	yes := true
	s, err := svc.SetVariable(ctx, "immich", "TOKEN", StackVariableSet{Value: "hunter2", Secret: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if v := varByName(t, s, "LOG_LEVEL"); v.Secret || v.Declared || !v.Set || v.Value != "plain-value" || !v.InEnv || v.UpdatedAt == nil {
		t.Errorf("plain variable = %+v", v)
	}
	if v := varByName(t, s, "TOKEN"); !v.Secret || !v.Set || v.Value != "" {
		t.Errorf("secret variable = %+v", v)
	}

	raw, err := os.ReadFile(svc.varsFile("immich"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("hunter2")) || bytes.Contains(raw, []byte("plain-value")) || bytes.Contains(raw, []byte("LOG_LEVEL")) {
		t.Error("variables.age holds plain text")
	}
	if st, _ := os.Stat(svc.varsFile("immich")); st.Mode().Perm() != 0o600 {
		t.Errorf("variables.age mode = %v", st.Mode())
	}

	// Decryption tries every identity in the key file.
	other, _ := age.GenerateX25519Identity()
	key, _ := os.ReadFile(svc.host.AgeKeyFile)
	both := filepath.Join(t.TempDir(), "keys.txt")
	_ = os.WriteFile(both, append([]byte(other.String()+"\n"), key...), 0o600)
	svc.host.AgeKeyFile = both
	svc.varsMu.Lock()
	vars, err := svc.loadVars("immich")
	svc.varsMu.Unlock()
	if err != nil || len(vars) != 2 || vars[0].Name != "LOG_LEVEL" || vars[1].Value != "hunter2" || !vars[1].Secret {
		t.Fatalf("loaded = %+v, %v", vars, err)
	}
	// A key that is not the host's reads nothing.
	only := filepath.Join(t.TempDir(), "keys.txt")
	_ = os.WriteFile(only, []byte(other.String()+"\n"), 0o600)
	svc.host.AgeKeyFile = only
	svc.varsMu.Lock()
	_, err = svc.loadVars("immich")
	svc.varsMu.Unlock()
	if err == nil {
		t.Error("variables decrypted with a key they were not encrypted to")
	}
}

func TestSetVariableRefusals(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	_, err := svc.SetVariable(ctx, "immich", "KEY", StackVariableSet{Value: "x"})
	wantCode(t, err, 404, "unknown_stack")

	fs.set("aaaaaaa1", v7Secrets(`{"name":"DB_PASSWORD"}`))
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"key", "MY-KEY", "1KEY", "../ENV", "env", "A" + strings.Repeat("B", 64)} {
		_, err := svc.SetVariable(ctx, "immich", bad, StackVariableSet{Value: "x"})
		wantCode(t, err, 422, "invalid_variable_name")
	}
	_, err = svc.SetVariable(ctx, "immich", "KEY", StackVariableSet{Value: ""})
	wantCode(t, err, 422, "invalid_value")
	_, err = svc.SetVariable(ctx, "immich", "KEY", StackVariableSet{Value: strings.Repeat("x", maxVarValue+1)})
	wantCode(t, err, 422, "invalid_value")

	// A secret stays one, and a declared secret is one from the start.
	no, yes := false, true
	_, err = svc.SetVariable(ctx, "immich", "DB_PASSWORD", StackVariableSet{Value: "x", Secret: &no})
	wantCode(t, err, 422, "secret_stays_secret")
	if _, err := svc.SetVariable(ctx, "immich", "TOKEN", StackVariableSet{Value: "x", Secret: &yes}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SetVariable(ctx, "immich", "TOKEN", StackVariableSet{Value: "y", Secret: &no})
	wantCode(t, err, 422, "secret_stays_secret")
	s, err := svc.SetVariable(ctx, "immich", "TOKEN", StackVariableSet{Value: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if v := varByName(t, s, "TOKEN"); !v.Secret || v.Value != "" {
		t.Errorf("TOKEN = %+v", v)
	}

	_, err = svc.DeleteVariable(ctx, "immich", "NOPE")
	wantCode(t, err, 404, "unknown_variable")
	_, err = svc.DeleteVariable(ctx, "immich", "no")
	wantCode(t, err, 422, "invalid_variable_name")
	s, err = svc.DeleteVariable(ctx, "immich", "TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Variables) != 1 || s.Variables[0].Name != "DB_PASSWORD" || s.Variables[0].Set {
		t.Errorf("variables after the removal = %+v", s.Variables)
	}
}

func TestEnvSafe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value string
		safe  bool
	}{
		{"abc123", true},
		{"hex-0f9e_base64url~", true},
		{"with inner space", true},
		{"a=b#c;d", true},
		{"päßwörd✓", true},
		{"", true},
		{`say "hi"`, false},
		{"it's", false},
		{`back\slash`, false},
		{"$HOME", false},
		{"cost$5", false},
		{"two\nlines", false},
		{"crlf\r", false},
		{"tab\there", false},
		{" leading", false},
		{"trailing ", false},
		{" nbsp", false},
		{"nul\x00", false},
		{"bad\xffutf8", false},
	} {
		if got := envSafe(tc.value); got != tc.safe {
			t.Errorf("envSafe(%q) = %v, want %v", tc.value, got, tc.safe)
		}
	}
}

func TestRenderWritesAFilePerVariableAndOnlySafeOnesToEnv(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	dir := svc.varsDir("mail")
	render := func(vars ...storedVar) {
		t.Helper()
		svc.varsMu.Lock()
		defer svc.varsMu.Unlock()
		if err := svc.renderVarsLocked("mail", vars, true); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Nothing stored: env exists, empty.
	render()
	if read("env") != "" {
		t.Errorf("empty env = %q", read("env"))
	}
	before, _ := os.Stat(dir)

	render(storedVar{Name: "WG_KEY", Value: "multi\nline"}, storedVar{Name: "API_KEY", Value: "k3y"}, storedVar{Name: "QUOTED", Value: `a"b`})
	if read("WG_KEY") != "multi\nline" || read("API_KEY") != "k3y" || read("QUOTED") != `a"b` {
		t.Error("a variable's file does not hold its exact value")
	}
	if got := read("env"); got != "API_KEY=k3y\n" {
		t.Errorf("env = %q", got)
	}
	for _, n := range []string{"env", "WG_KEY"} {
		if st, _ := os.Stat(filepath.Join(dir, n)); st.Mode().Perm() != 0o400 {
			t.Errorf("%s mode = %v", n, st.Mode())
		}
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v", st.Mode())
	}

	// A removed variable's file goes, and so does anything else left there;
	// the directory a running stack has bound stays the same one.
	_ = os.WriteFile(filepath.Join(dir, ".API_KEY.tmp"), nil, 0o600)
	render(storedVar{Name: "API_KEY", Value: "n3w"})
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"API_KEY", "env"}) {
		t.Errorf("files = %v", names)
	}
	if read("env") != "API_KEY=n3w\n" {
		t.Errorf("env = %q", read("env"))
	}
	if after, _ := os.Stat(dir); !os.SameFile(before, after) {
		t.Error("the directory was recreated")
	}
}

func TestNspawnArgsBindVariablesFromVersionFour(t *testing.T) {
	t.Parallel()
	v3 := mustParse(t, `{"version":3,"name":"mail"}`)
	v4 := mustParse(t, `{"version":4,"name":"mail"}`)
	a3 := nspawnArgs("mail", "/state/mail", "/profiles/mail", "/run/homelab-stack-vars/mail", v3, nil)
	a4 := nspawnArgs("mail", "/state/mail", "/profiles/mail", "/run/homelab-stack-vars/mail", v4, nil)
	bind := "--bind-ro=/run/homelab-stack-vars/mail:/run/secrets:idmap"
	if slices.Contains(a3, bind) || !slices.Contains(a4, bind) {
		t.Errorf("version 3: %v\nversion 4: %v", a3, a4)
	}
	// The update to version 4 must restart the container.
	if slices.Equal(a3, a4) {
		t.Error("the arguments of version 3 and 4 do not differ")
	}
	for _, a := range append(a3, a4...) {
		if strings.Contains(a, "sops") {
			t.Errorf("a sops bind is left: %s", a)
		}
	}
}

func TestDeclaredSecretsMustHaveValuesBeforeABuild(t *testing.T) {
	t.Parallel()
	svc, fs, h := newServiceOn(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", v7Secrets(`{"name":"DB_PASSWORD","description":"Database password"},{"name":"API_KEY"}`))
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Approve(ctx, "immich", "aaaaaaa1")
	if err != nil {
		t.Fatal(err)
	}
	if s.VariablesReady || len(s.Variables) != 2 || s.Variables[0].Name != "DB_PASSWORD" || s.Variables[0].Description != "Database password" ||
		!s.Variables[0].Declared || !s.Variables[0].Secret || s.Variables[0].Set || s.Variables[1].Name != "API_KEY" {
		t.Fatalf("variables = %+v ready=%v", s.Variables, s.VariablesReady)
	}
	_, err = svc.Start(ctx, "immich")
	if m := missingOf(t, err); !slices.Equal(m, []string{"DB_PASSWORD", "API_KEY"}) {
		t.Errorf("missing = %v", m)
	}
	if _, err := svc.SetVariable(ctx, "immich", "DB_PASSWORD", StackVariableSet{Value: "pw"}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Start(ctx, "immich")
	if m := missingOf(t, err); !slices.Equal(m, []string{"API_KEY"}) || !strings.Contains(err.Error(), "API_KEY") {
		t.Errorf("missing = %v, %v", m, err)
	}
	if _, err := svc.SetVariable(ctx, "immich", "API_KEY", StackVariableSet{Value: "k"}); err != nil {
		t.Fatal(err)
	}
	s = finished(t, svc, svc.Start)
	if s.Operation.Error != "" || !s.VariablesReady {
		t.Fatalf("start = %+v ready=%v", s.Operation, s.VariablesReady)
	}
	if b, _ := os.ReadFile(filepath.Join(svc.varsDir("immich"), "DB_PASSWORD")); string(b) != "pw" {
		t.Errorf("DB_PASSWORD file = %q", b)
	}

	// A new commit drops API_KEY and declares NEW_ONE. It asks for no new
	// grant, so it is approved at once, and waits for the value.
	fs.set("bbbbbbb2", v7Secrets(`{"name":"DB_PASSWORD"},{"name":"NEW_ONE"}`))
	s, err = svc.Fetch(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved.Rev != "bbbbbbb2" || s.VariablesReady {
		t.Fatalf("approved = %s ready = %v", s.Approved.Rev, s.VariablesReady)
	}
	if v := varByName(t, s, "API_KEY"); v.Declared || !v.Set || !v.Secret {
		t.Errorf("dropped secret = %+v", v)
	}
	if v := varByName(t, s, "NEW_ONE"); !v.Declared || v.Set {
		t.Errorf("new secret = %+v", v)
	}
	_, err = svc.Update(ctx, "immich")
	if m := missingOf(t, err); !slices.Equal(m, []string{"NEW_ONE"}) {
		t.Errorf("missing = %v", m)
	}
	if q, err := svc.UpdateAll(ctx); err != nil || len(q) != 0 {
		t.Errorf("update all queued %v, %v", q, err)
	}
	svc.monitorOnce(ctx, map[string]int{})
	h.mu.Lock()
	problem := h.problems[updateScope+"immich"]
	h.mu.Unlock()
	if !strings.Contains(problem, "waits for values of NEW_ONE") {
		t.Errorf("update problem = %q", problem)
	}
}
