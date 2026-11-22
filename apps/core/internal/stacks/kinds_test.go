package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// immichStorage is v1Manifest's one storage entry.
const immichStorage = `"storage": [{"host_path": "/pool/immich", "mount": "/pool/immich", "read_only": false}],`

// v5Manifest is immich at version 5: its storage gone, vars declared.
func v5Manifest(vars string) string {
	m := strings.Replace(v1Manifest, immichStorage, `"variables": [`+vars+`],`, 1)
	return strings.Replace(m, `"version": 1,`, `"version": 5,`, 1)
}

// v7Vars is immich (version 7) declaring vars instead of its one path.
func v7Vars(vars string) string {
	i := strings.Index(immichManifest, `"variables": [`)
	j := strings.Index(immichManifest[i:], "],\n") + i
	return immichManifest[:i] + `"variables": [` + vars + immichManifest[j:]
}

// photosPath is immich's storage as a version 5 path variable.
const photosPath = `{"name":"PHOTOS","kind":"path","description":"Photo library","required":true,"default":"/pool/immich","mount":"/pool/immich"}`

func strp(s string) *string { return &s }

func TestVersionFiveVariables(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":5,"name":"media","variables":[
		{"name":"MEDIA_DIR","kind":"path","description":"Library","required":true,"default":"/pool/media","mount":"/pool/media"},
		{"name":"DOWNLOADS","kind":"path","mount":"/downloads","read_only":true},
		{"name":"TZ","kind":"env","default":"Europe/Stockholm"},
		{"name":"VPN_KEY","kind":"secret","required":true}]}`)
	want := []Variable{
		{Name: "MEDIA_DIR", Kind: VarPath, Description: "Library", Required: true, Default: strp("/pool/media"), Mount: "/pool/media"},
		{Name: "DOWNLOADS", Kind: VarPath, Mount: "/downloads", ReadOnly: true},
		{Name: "TZ", Kind: VarEnv, Default: strp("Europe/Stockholm")},
		{Name: "VPN_KEY", Kind: VarSecret, Required: true},
	}
	if !reflect.DeepEqual(m.Vars(), want) {
		t.Errorf("vars = %+v", m.Vars())
	}
	if again := mustParse(t, encode(m)); !reflect.DeepEqual(again, m) {
		t.Errorf("round trip changed it: %s", encode(m))
	}
	if !strings.Contains(encode(m), `{"name":"TZ","kind":"env","description":"","required":false,"default":"Europe/Stockholm"}`) {
		t.Errorf("encoded = %s", encode(m))
	}
	// Paths are storage grants; env and secret variables are not grants.
	wantGrants := []Grant{
		{GrantStorage, "MEDIA_DIR: /pool/media (default) at /pool/media, read-write"},
		{GrantStorage, "DOWNLOADS at /downloads, read-only, host path chosen by you"},
	}
	if got := m.Grants(); !reflect.DeepEqual(got, wantGrants) {
		t.Errorf("grants = %v", got)
	}
	if plain := mustParse(t, `{"version":5,"name":"a","variables":[]}`); strings.Contains(encode(plain), "variables") {
		t.Errorf("encoded = %s", encode(plain))
	}

	v5 := func(vars string) string { return `{"version":5,"name":"a","variables":[` + vars + `]}` }
	for _, tc := range []struct{ name, in, want string }{
		{"variables in version 4", `{"version":4,"name":"a","variables":[{"name":"A","kind":"env"}]}`, "variables need version 5"},
		{"secrets in version 5", `{"version":5,"name":"a","secrets":[{"name":"KEY"}]}`, `kind "secret"`},
		{"storage in version 5", `{"version":5,"name":"a","storage":[{"host_path":"/pool/x","mount":"/x"}]}`, `kind "path"`},
		{"unknown kind", v5(`{"name":"A","kind":"file"}`), "variables[0].kind"},
		{"no kind", v5(`{"name":"A"}`), "variables[0].kind"},
		{"bad name", v5(`{"name":"a","kind":"env"}`), "variables[0].name"},
		{"name twice", v5(`{"name":"A","kind":"env"},{"name":"A","kind":"secret"}`), `variables[1].name: "A" is listed twice`},
		{"default on a secret", v5(`{"name":"A","kind":"secret","default":"x"}`), "a secret has no default"},
		{"empty default", v5(`{"name":"A","kind":"env","default":""}`), "variables[0].default: empty"},
		{"mount on env", v5(`{"name":"A","kind":"env","mount":"/x"}`), "only for a path"},
		{"read_only on a secret", v5(`{"name":"A","kind":"secret","read_only":true}`), "only for a path"},
		{"path without a mount", v5(`{"name":"A","kind":"path"}`), "variables[0].mount: a path needs"},
		{"path mount relative", v5(`{"name":"A","kind":"path","mount":"x"}`), "must be absolute"},
		{"path mount over /etc", v5(`{"name":"A","kind":"path","mount":"/etc/x"}`), "under /etc"},
		{"path mount twice", v5(`{"name":"A","kind":"path","mount":"/x"},{"name":"B","kind":"path","mount":"/x"}`), `variables[1].mount: "/x" is used twice`},
		{"path default top-level", v5(`{"name":"A","kind":"path","mount":"/x","default":"/pool"}`), "top-level"},
		{"path default under /etc", v5(`{"name":"A","kind":"path","mount":"/x","default":"/etc/x"}`), "under /etc"},
		{"path default with an option", v5(`{"name":"A","kind":"path","mount":"/x","default":"/pool/x:ro"}`), "plain path"},
		{"unknown field", v5(`{"name":"A","kind":"env","value":"x"}`), "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	stored := []storedVar{{Name: "TZ", Value: "UTC"}, {Name: "MEDIA", Value: "/pool/chosen"}, {Name: "BAD", Value: "/etc/x"}}
	for _, tc := range []struct {
		d           Variable
		value, from string
	}{
		{Variable{Name: "TZ", Kind: VarEnv, Default: strp("Europe/Stockholm")}, "UTC", SourceStored},
		{Variable{Name: "LANG", Kind: VarEnv, Default: strp("en_US")}, "en_US", SourceDefault},
		{Variable{Name: "LANG", Kind: VarEnv}, "", SourceUnset},
		{Variable{Name: "KEY", Kind: VarSecret}, "", SourceUnset},
		{Variable{Name: "MEDIA", Kind: VarPath, Mount: "/m", Default: strp("/pool/media")}, "/pool/chosen", SourceStored},
		{Variable{Name: "OTHER", Kind: VarPath, Mount: "/m", Default: strp("/pool/media")}, "/pool/media", SourceDefault},
		// A stored path a stack may not be given is no value, and the
		// default does not quietly take its place.
		{Variable{Name: "BAD", Kind: VarPath, Mount: "/m", Default: strp("/pool/media")}, "", SourceUnset},
	} {
		if v, src := resolve(tc.d, stored); v != tc.value || src != tc.from {
			t.Errorf("resolve(%s) = %q, %s; want %q, %s", tc.d.Name, v, src, tc.value, tc.from)
		}
	}
}

func TestOnlyRequiredVariablesGateABuild(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":5,"name":"a","variables":[
		{"name":"KEY","kind":"secret","required":true},
		{"name":"TOKEN","kind":"secret"},
		{"name":"TZ","kind":"env","required":true,"default":"UTC"},
		{"name":"HOST","kind":"env","required":true},
		{"name":"DATA","kind":"path","required":true,"mount":"/data"},
		{"name":"CACHE","kind":"path","mount":"/cache"},
		{"name":"MEDIA","kind":"path","required":true,"mount":"/media","default":"/pool/media"}]}`)
	for _, tc := range []struct {
		name    string
		vars    []storedVar
		missing []string
	}{
		{"nothing stored: required ones without a default", nil, []string{"KEY", "HOST", "DATA"}},
		{"all required stored", []storedVar{{Name: "KEY", Value: "k"}, {Name: "HOST", Value: "h"}, {Name: "DATA", Value: "/pool/data"}}, nil},
		{"a bad stored path", []storedVar{{Name: "KEY", Value: "k"}, {Name: "HOST", Value: "h"}, {Name: "DATA", Value: "/tmp/x"}}, []string{"DATA"}},
	} {
		if got := missingIn(m, tc.vars); !slices.Equal(got, tc.missing) {
			t.Errorf("%s: missing = %v, want %v", tc.name, got, tc.missing)
		}
	}
}

func TestNewGrantsForPathVariables(t *testing.T) {
	t.Parallel()
	v4 := mustParse(t, secretsManifest(""))
	path := func(fields string) Manifest {
		return mustParse(t, v5Manifest(`{"name":"PHOTOS","kind":"path","required":true,`+fields+`}`))
	}
	same := path(`"default":"/pool/immich","mount":"/pool/immich"`)
	for _, tc := range []struct {
		name           string
		approved, next Manifest
		want           []Grant
	}{
		{"v4 storage to a v5 path with the same bind", v4, same, nil},
		{"v4 storage to a read-only path", v4, path(`"default":"/pool/immich","mount":"/pool/immich","read_only":true`), nil},
		{"v4 storage to a path with another default", v4, path(`"default":"/pool/photos","mount":"/pool/immich"`),
			[]Grant{{GrantStorage, "PHOTOS: /pool/photos (default) at /pool/immich, read-write"}}},
		{"v4 storage to a path without a default", v4, path(`"mount":"/pool/immich"`),
			[]Grant{{GrantStorage, "PHOTOS at /pool/immich, read-write, host path chosen by you"}}},
		{"a path's default changes: the operator's directory stays", same, path(`"default":"/pool/photos","mount":"/pool/immich"`), nil},
		{"a path at a new mount", same, path(`"default":"/pool/immich","mount":"/photos"`),
			[]Grant{{GrantStorage, "PHOTOS: /pool/immich (default) at /photos, read-write"}}},
		{"read-only to read-write", path(`"default":"/pool/immich","mount":"/pool/immich","read_only":true`), same,
			[]Grant{{GrantStorage, "PHOTOS: /pool/immich (default) at /pool/immich, read-write"}}},
		{"read-write to read-only", same, path(`"default":"/pool/immich","mount":"/pool/immich","read_only":true`), nil},
		{"nothing approved", Manifest{}, same, same.Grants()},
	} {
		got := NewGrants(tc.approved, tc.next)
		if tc.name != "nothing approved" {
			got = slices.DeleteFunc(got, func(g Grant) bool { return g.Kind != GrantStorage })
		}
		if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStorageAndPathBindsGiveTheSameArguments(t *testing.T) {
	t.Parallel()
	v4 := mustParse(t, secretsManifest(""))
	v5 := mustParse(t, v5Manifest(photosPath))
	args := func(m Manifest, vars []storedVar) []string {
		t.Helper()
		b, err := hostBinds(m, vars, nil)
		if err != nil {
			t.Fatal(err)
		}
		return nspawnArgs("immich", "/state/immich", "/profiles/immich", "/vars/immich", "", m, b)
	}
	a4 := args(v4, nil)
	if !slices.Equal(a4, args(v5, nil)) || !slices.Equal(a4, args(v5, []storedVar{{Name: "PHOTOS", Value: "/pool/immich"}})) {
		t.Errorf("v4: %v\nv5: %v", a4, args(v5, nil))
	}
	if !slices.Contains(a4, "--bind=/pool/immich:/pool/immich:idmap") {
		t.Errorf("args = %v", a4)
	}
	if a := args(v5, []storedVar{{Name: "PHOTOS", Value: "/pool/chosen"}}); !slices.Contains(a, "--bind=/pool/chosen:/pool/immich:idmap") {
		t.Errorf("a stored path is not bound: %v", a)
	}

	opt := mustParse(t, v5Manifest(`{"name":"EXTRA","kind":"path","mount":"/extra","read_only":true}`))
	if a := args(opt, nil); slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, "/extra") }) {
		t.Errorf("an unset optional path is bound: %v", a)
	}
	if a := args(opt, []storedVar{{Name: "EXTRA", Value: "/pool/extra"}}); !slices.Contains(a, "--bind-ro=/pool/extra:/extra:idmap") {
		t.Errorf("a set optional path is not bound: %v", a)
	}
	req := mustParse(t, v5Manifest(`{"name":"DATA","kind":"path","required":true,"mount":"/data"}`))
	if _, err := hostBinds(req, nil, nil); err == nil || !strings.Contains(err.Error(), "DATA has no value") {
		t.Errorf("err = %v", err)
	}
}

func TestEnvDefaultsAreDeliveredAndPathsAreNot(t *testing.T) {
	t.Parallel()
	m := mustParse(t, v5Manifest(photosPath+`,{"name":"TZ","kind":"env","default":"UTC"},{"name":"LANG","kind":"env","default":"en"},{"name":"NONE","kind":"env"}`))
	got := delivered(m, []storedVar{{Name: "PHOTOS", Value: "/pool/immich"}, {Name: "LANG", Value: "sv"}, {Name: "OWN", Value: "x"}})
	names := map[string]string{}
	for _, v := range got {
		names[v.Name] = v.Value
	}
	if !reflect.DeepEqual(names, map[string]string{"LANG": "sv", "OWN": "x", "TZ": "UTC"}) {
		t.Errorf("delivered = %v", names)
	}
}

func TestApprovalStoresPathDefaultsThatOutliveAChangedDefault(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", v7Vars(photosPath+`,{"name":"TZ","kind":"env"},{"name":"EXTRA","kind":"path","mount":"/extra"}`))
	s, err := svc.Add(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if v := varByName(t, s, "PHOTOS"); v.Set || v.Source != StackVariableSourceDefault || v.Kind != StackVariableKindPath ||
		!v.Required || v.Mount != "/pool/immich" || v.ReadOnly == nil || *v.ReadOnly || *v.Default != "/pool/immich" || v.Secret || v.InEnv {
		t.Errorf("before approval: %+v", v)
	}
	if !s.VariablesReady {
		t.Error("a required path with a default is not ready")
	}
	if s, err = svc.Approve(ctx, "immich", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	if v := varByName(t, s, "PHOTOS"); !v.Set || v.Source != StackVariableSourceStored || v.Value != "/pool/immich" {
		t.Errorf("after approval: %+v", v)
	}
	// An env variable without a value is not stored: the flake's own value holds.
	if v := varByName(t, s, "TZ"); v.Set || v.Source != StackVariableSourceUnset || v.Kind != StackVariableKindEnv || v.Mount != "" || v.ReadOnly != nil {
		t.Errorf("TZ = %+v", v)
	}
	if v := varByName(t, s, "EXTRA"); v.Set || v.Source != StackVariableSourceUnset || v.Required {
		t.Errorf("EXTRA = %+v", v)
	}

	// A commit moving the default to another directory asks nothing (the
	// mount is the same), is approved at once, and the stack keeps its data.
	fs.set("bbbbbbb2", strings.Replace(v7Vars(photosPath), `"default":"/pool/immich"`, `"default":"/pool/photos"`, 1))
	if s, err = svc.Fetch(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if s.Approved.Rev != "bbbbbbb2" {
		t.Fatalf("approved = %s, new grants %v", s.Approved.Rev, s.NewGrants)
	}
	if v := varByName(t, s, "PHOTOS"); v.Value != "/pool/immich" || v.Source != StackVariableSourceStored || *v.Default != "/pool/photos" {
		t.Errorf("after the changed default: %+v", v)
	}
	// TZ was never stored, so once no manifest declares it, it is gone.
	if slices.ContainsFunc(s.Variables, func(v StackVariable) bool { return v.Name == "TZ" }) {
		t.Errorf("variables = %+v", s.Variables)
	}
}

func TestSetVariableFollowsTheDeclaredKind(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", v7Vars(photosPath+`,{"name":"TZ","kind":"env"},{"name":"KEY","kind":"secret"}`))
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{"/pool/photos": true}
	svc.statDir = func(p string) error {
		if !dirs[p] {
			return errors.New("not a directory")
		}
		return nil
	}
	yes, no := true, false
	for _, tc := range []struct {
		name, v, value string
		secret         *bool
		code           string
	}{
		{"path under /etc", "PHOTOS", "/etc/photos", nil, "invalid_path"},
		{"path relative", "PHOTOS", "pool/photos", nil, "invalid_path"},
		{"path whole pool", "PHOTOS", "/pool", nil, "invalid_path"},
		{"path missing on the host", "PHOTOS", "/pool/nope", nil, "invalid_path"},
		{"secret path", "PHOTOS", "/pool/photos", &yes, "kind_mismatch"},
		{"secret env", "TZ", "UTC", &yes, "kind_mismatch"},
		{"plain secret", "KEY", "k", &no, "secret_stays_secret"},
	} {
		_, err := svc.SetVariable(ctx, "immich", tc.v, StackVariableSet{Value: tc.value, Secret: tc.secret})
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		wantCode(t, err, 422, tc.code)
	}
	s, err := svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/photos", Secret: &no})
	if err != nil {
		t.Fatal(err)
	}
	if v := varByName(t, s, "PHOTOS"); v.Value != "/pool/photos" || v.Secret || v.Source != StackVariableSourceStored {
		t.Errorf("PHOTOS = %+v", v)
	}
	if s, err = svc.SetVariable(ctx, "immich", "TZ", StackVariableSet{Value: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if v := varByName(t, s, "TZ"); v.Value != "UTC" || v.Secret {
		t.Errorf("TZ = %+v", v)
	}
	// The path is not delivered as a value; the env variable is.
	if _, err := os.Stat(filepath.Join(svc.varsDir("immich"), "PHOTOS")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the path was delivered as a file: %v", err)
	}
	if env, _ := os.ReadFile(filepath.Join(svc.varsDir("immich"), "env")); string(env) != "TZ=UTC\n" {
		t.Errorf("env = %q", env)
	}
}

// bootsOf is how often the immich container was booted.
func bootsOf(h *fakeHost) []string {
	var out []string
	for _, c := range h.CallsMatching("systemd-run", "--unit=homelab-stack-immich") {
		out = append(out, argv(c))
	}
	return out
}

func TestAChangedPathNeedsARestart(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	ctx := context.Background()
	svc.statDir = func(string) error { return nil }
	release(t, svc, fs, "bbbbbbb2", v7Vars(photosPath))
	s := finished(t, svc, svc.Start)
	if s.RestartNeeded || !strings.Contains(bootsOf(h)[0], "--bind=/pool/immich:/pool/immich:idmap") {
		t.Fatalf("restart=%v boots=%v", s.RestartNeeded, bootsOf(h))
	}
	s, err := svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/photos"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.RestartNeeded {
		t.Fatal("a changed path does not ask for a restart")
	}
	// Nothing restarted on its own; the next update restarts the container
	// with the new bind, although its manifest binds the same.
	if n := len(bootsOf(h)); n != 1 {
		t.Errorf("booted %d times", n)
	}
	release(t, svc, fs, "ccccccc3", strings.Replace(v7Vars(photosPath), `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	s = finished(t, svc, svc.Update)
	boots := bootsOf(h)
	if s.Operation.Error != "" || s.RestartNeeded || len(boots) != 2 || !strings.Contains(boots[1], "--bind=/pool/photos:/pool/immich:idmap") {
		t.Errorf("update = %+v restart=%v boots=%v", s.Operation, s.RestartNeeded, boots)
	}
	// Going back to the stored directory the container has needs none.
	if s, err = svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/immich"}); err != nil || !s.RestartNeeded {
		t.Fatalf("restart=%v, %v", s.RestartNeeded, err)
	}
	if s, err = svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/photos"}); err != nil || s.RestartNeeded {
		t.Errorf("restart=%v, %v", s.RestartNeeded, err)
	}
}
