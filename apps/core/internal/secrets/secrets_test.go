package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type memRot struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (r *memRot) Rotations(context.Context) (map[string]time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]time.Time{}
	for k, v := range r.m {
		out[k] = v
	}
	return out, nil
}
func (r *memRot) SetRotation(_ context.Context, n string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]time.Time{}
	}
	r.m[n] = at
	return nil
}

const repo = "/repo"

type harness struct {
	svc  *Service
	fake *cmdrunnertest.Fake
	rot  *memRot
	dir  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	fake := cmdrunnertest.NewFake()
	rot := &memRot{}
	s := New(fake, filepath.Join(dir, "registry.json"), repo, rot, quiet())
	s.pendingPath = filepath.Join(dir, "run", "secrets-pending")
	s.now = func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }
	return &harness{svc: s, fake: fake, rot: rot, dir: dir}
}

// writeRegistry writes a registry with two secrets; caddy's value file exists.
func (h *harness) writeRegistry(t *testing.T) {
	t.Helper()
	caddy := filepath.Join(h.dir, "caddy-token")
	if err := os.WriteFile(caddy, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := []map[string]any{
		{"name": "caddy/CLOUDFLARE_API_TOKEN", "description": "cf", "services": []string{"caddy.service"}, "runPath": caddy},
		{"name": "immich/DB_PASSWORD", "description": "db", "runPath": filepath.Join(h.dir, "absent")},
	}
	data, _ := json.Marshal(reg)
	if err := os.WriteFile(h.svc.registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// scriptGit scripts a clean checkout on branch main with an origin remote.
func (h *harness) scriptGit() {
	g := func(args ...string) *cmdrunnertest.Rule {
		return h.fake.On(append([]string{"git", "-C", repo}, args...)...)
	}
	g("rev-parse", "--abbrev-ref").Reply("main\n")
	g("status").Reply("")
	g("add").Reply("")
	g("commit").Reply("")
	g("rev-parse", "HEAD").Reply("abc123\n")
	g("remote").Reply("origin\n")
	g("push").Reply("")
	h.fake.On("sops").Reply("")
	h.fake.On("chown").Reply("")
}

func (h *harness) pending() bool { return h.svc.DeployPending() }

// ---- git preflight / commit (ported from the old daemon) ----

func TestCommitTreatsPushFailureAsWarning(t *testing.T) {
	t.Parallel()
	s := New(nil, "", repo, nil, quiet())
	var calls [][]string
	run := func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "rev-parse":
			if len(args) > 1 && args[1] == "--abbrev-ref" {
				return "feat/remote-access\n", nil
			}
			return "abc123\n", nil
		case "status":
			return "", nil
		case "remote":
			return "origin\n", nil
		case "push":
			return "fatal: network unavailable", errors.New("exit 128")
		default:
			return "", nil
		}
	}
	committed := false
	res, err := s.commit("secrets: rotate wireguard key", run, func() { committed = true })
	if err != nil {
		t.Fatalf("push failure must not fail a committed secret: %v", err)
	}
	if !res.PushPending || len(res.Warnings) != 1 || res.Branch != "feat/remote-access" || res.Commit != "abc123" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !committed {
		t.Fatal("local commit callback was not run before returning push warning")
	}
	want := []string{"push", "--set-upstream", "origin", "HEAD"}
	if got := calls[len(calls)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("push args = %v, want %v", got, want)
	}
}

func TestCommitWithoutRemoteIsPushPending(t *testing.T) {
	t.Parallel()
	s := New(nil, "", repo, nil, quiet())
	run := func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "main\n", nil
		case "remote":
			return "\n", nil
		case "push":
			t.Fatal("must not push without a remote")
		}
		return "", nil
	}
	res, err := s.commit("m", run, func() {})
	if err != nil || !res.PushPending || !strings.Contains(res.Warnings[0], "no Git remote") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestCommitRejectsDetachedHeadBeforeMutation(t *testing.T) {
	t.Parallel()
	s := New(nil, "", repo, nil, quiet())
	var calls [][]string
	run := func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "HEAD\n", nil
	}
	if _, err := s.commit("message", run, func() {}); err == nil {
		t.Fatal("expected detached HEAD rejection")
	}
	if len(calls) != 1 || calls[0][0] != "rev-parse" {
		t.Fatalf("mutation occurred before rejection: %v", calls)
	}
}

func TestCommitRejectsPreexistingSecretEdit(t *testing.T) {
	t.Parallel()
	s := New(nil, "", repo, nil, quiet())
	run := func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "main\n", nil
		case "status":
			if len(args) > 2 && args[2] == "--" {
				return " M nixos/secrets/secrets.yaml\n", nil
			}
			return "", nil
		default:
			t.Fatalf("unexpected mutation command: %v", args)
			return "", nil
		}
	}
	if _, err := s.commit("message", run, func() {}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommitRejectsPreexistingStagedChanges(t *testing.T) {
	t.Parallel()
	s := New(nil, "", repo, nil, quiet())
	run := func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "main\n", nil
		case "status":
			if len(args) == 2 && args[1] == "--porcelain" {
				return "M  daemon/main.go\n?? untracked.txt\n", nil
			}
			return "", nil
		default:
			t.Fatalf("unexpected mutation command: %v", args)
			return "", nil
		}
	}
	if _, err := s.commit("message", run, func() {}); err == nil || !strings.Contains(err.Error(), "staged") {
		t.Fatalf("err = %v", err)
	}
}

func TestUntrackedFilesDoNotBlockPreflight(t *testing.T) {
	t.Parallel()
	s := New(nil, "", repo, nil, quiet())
	run := func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "main\n", nil
		case "status":
			if len(args) == 2 {
				return "?? scratch.txt\n M tracked-unstaged.go\n", nil
			}
			return "", nil
		}
		return "", nil
	}
	if _, err := s.preflight(run); err != nil {
		t.Fatalf("untracked/unstaged files must not block: %v", err)
	}
}

// ---- repo path, registry, resolver ----

func TestResolveRepoPath(t *testing.T) {
	t.Parallel()
	cases := []struct{ flake, want string }{
		{"/home/alice/homelab#homelab", "/home/alice/homelab"},
		{"/home/alice/homelab", "/home/alice/homelab"},
		{"", "."},
		{"#host", "."},
	}
	for _, c := range cases {
		if got := ResolveRepoPath(c.flake); got != c.want {
			t.Errorf("ResolveRepoPath(%q) = %q, want %q", c.flake, got, c.want)
		}
	}
}

func TestResolver(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.writeRegistry(t)
	kv := filepath.Join(h.dir, "kv")
	_ = os.WriteFile(kv, []byte("DB_PASSWORD=hunter2\n"), 0o600)
	// rewrite immich entry to a KEY=VALUE file
	data, _ := json.Marshal([]map[string]any{
		{"name": "caddy/CLOUDFLARE_API_TOKEN", "runPath": filepath.Join(h.dir, "caddy-token")},
		{"name": "immich/DB_PASSWORD", "runPath": kv},
	})
	_ = os.WriteFile(h.svc.registryPath, data, 0o600)

	r := h.svc.Resolver()
	if got := r("caddy/CLOUDFLARE_API_TOKEN"); got != "caddy/CLOUDFLARE_API_TOKEN=abcdef" {
		t.Fatalf("exact match (bare value gets name=) = %q", got)
	}
	if got := r("CLOUDFLARE_API_TOKEN"); got != "CLOUDFLARE_API_TOKEN=abcdef" {
		t.Fatalf("trailing match = %q", got)
	}
	if got := r("DB_PASSWORD"); got != "DB_PASSWORD=hunter2" {
		t.Fatalf("kv file = %q", got)
	}
	if got := r("nope"); got != "" {
		t.Fatalf("unknown = %q", got)
	}
	var _ cmdrunner.SecretResolver = r
}

func TestListAndGet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	l, err := h.svc.List(ctx) // no registry yet
	if err != nil || l.Secrets == nil || len(l.Secrets) != 0 || l.DeployPending {
		t.Fatalf("empty list = %+v err %v", l, err)
	}
	h.writeRegistry(t)
	_ = h.rot.SetRotation(ctx, "caddy/CLOUDFLARE_API_TOKEN", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	h.svc.setPending(true)

	l, err = h.svc.List(ctx)
	if err != nil || len(l.Secrets) != 2 || !l.DeployPending {
		t.Fatalf("list = %+v err %v", l, err)
	}
	c := l.Secrets[0]
	if !c.Present || c.Preview != "abc•••" || c.ModifiedAt == nil || !c.ModifiedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("caddy = %+v", c)
	}
	i := l.Secrets[1]
	if i.Present || i.Preview != "" || i.ModifiedAt != nil || i.Services == nil {
		t.Fatalf("immich = %+v", i)
	}
	if b, _ := json.Marshal(l); strings.Contains(string(b), "abcdef") {
		t.Fatal("secret value leaked into the listing")
	}

	got, err := h.svc.Get(ctx, "immich/DB_PASSWORD")
	if err != nil || got.Name != "immich/DB_PASSWORD" {
		t.Fatalf("get = %+v err %v", got, err)
	}
	if _, err := h.svc.Get(ctx, "nope/X"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListBadRegistry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_ = os.WriteFile(h.svc.registryPath, []byte("{not json"), 0o600)
	if _, err := h.svc.List(context.Background()); err == nil {
		t.Fatal("expected parse error")
	}
}

// rewriteRegistry replaces the registry with entries whose JSON is the same
// size as the current file and puts the old modification time back, so only a
// fresh read of the file can see the change.
func (h *harness) rewriteRegistry(t *testing.T, descA, descB string) {
	t.Helper()
	info, err := os.Stat(h.svc.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]Entry{{Name: "a/X", Description: descA}, {Name: "b/Y", Description: descB}})
	if int64(len(data)) != info.Size() {
		t.Fatalf("rewrite changes the size: %d -> %d", info.Size(), len(data))
	}
	if err := os.WriteFile(h.svc.registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(h.svc.registryPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func descriptions(t *testing.T, s *Service) string {
	t.Helper()
	l, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range l.Secrets {
		out = append(out, e.Description)
	}
	return strings.Join(out, ",")
}

func TestRegistryReadOnlyWhenFileChanges(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	data, _ := json.Marshal([]Entry{{Name: "a/X", Description: "one"}, {Name: "b/Y", Description: "two"}})
	if err := os.WriteFile(h.svc.registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := descriptions(t, h.svc); got != "one,two" {
		t.Fatalf("first read = %q", got)
	}

	// Same size and modification time: the cached registry is served.
	h.rewriteRegistry(t, "uno", "dos")
	if got := descriptions(t, h.svc); got != "one,two" {
		t.Fatalf("unchanged size and mtime re-read the file: %q", got)
	}

	// A new modification time is a change.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(h.svc.registryPath, later, later); err != nil {
		t.Fatal(err)
	}
	if got := descriptions(t, h.svc); got != "uno,dos" {
		t.Fatalf("after the file changed = %q", got)
	}
}

// ---- Set / Delete ----

func TestSetEncryptsCommitsAndFlagsDeploy(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.scriptGit()
	const value = `s3"cr<et>&é`
	res, err := h.svc.Set(context.Background(), "caddy", "CLOUDFLARE_API_TOKEN", value)
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "main" || res.Commit != "abc123" || res.PushPending || len(res.Warnings) != 0 {
		t.Fatalf("res = %+v", res)
	}
	sops := h.fake.CallsMatching("sops")
	if len(sops) != 1 {
		t.Fatalf("sops calls = %d", len(sops))
	}
	c := sops[0]
	wantArgs := []string{"set", "--value-stdin", repo + "/nixos/secrets/secrets.yaml", `["caddy"]["CLOUDFLARE_API_TOKEN"]`}
	if !reflect.DeepEqual(c.Args, wantArgs) {
		t.Fatalf("sops args = %v", c.Args)
	}
	if !contains(c.Env, "SOPS_AGE_KEY_FILE=/var/lib/sops-age/keys.txt") || !contains(c.Env, "SOPS_CONFIG="+repo+"/nixos/.sops.yaml") {
		t.Fatalf("sops env = %v", c.Env)
	}
	in, _ := io.ReadAll(c.Stdin)
	var decoded string
	if err := json.Unmarshal(in, &decoded); err != nil || decoded != value {
		t.Fatalf("stdin %q does not decode to the value (%v)", in, err)
	}
	for _, call := range h.fake.Calls() {
		if strings.Contains(strings.Join(call.Args, " "), "cr<et>") {
			t.Fatalf("secret value leaked into argv: %v", call.Args)
		}
	}
	if _, ok := h.rot.m["caddy/CLOUDFLARE_API_TOKEN"]; !ok {
		t.Fatal("rotation time not recorded")
	}
	if !h.pending() {
		t.Fatal("deploy-pending flag not set")
	}
	commit := h.fake.CallsMatching("git", "-C", repo, "commit")
	// The checkout belongs to whoever runs the test: git runs as them, and
	// secrets.yaml goes back to them.
	usr, group := h.svc.repoOwner()
	if len(commit) != 1 || commit[0].Args[4] != "secrets: rotate caddy/CLOUDFLARE_API_TOKEN via dashboard" || commit[0].AsUser != usr {
		t.Fatalf("commit call = %+v", commit)
	}
	if len(h.fake.CallsMatching("chown", usr+":"+group)) != 1 && usr != "" {
		t.Fatal("ownership of secrets.yaml not restored")
	}
}

func TestSetRefusedByPreflightTouchesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("git", "-C", repo, "rev-parse", "--abbrev-ref").Reply("HEAD\n")
	_, err := h.svc.Set(context.Background(), "caddy", "K", "v")
	var pf *PreflightError
	if !errors.As(err, &pf) {
		t.Fatalf("err = %v", err)
	}
	if len(h.fake.CallsMatching("sops")) != 0 || h.pending() || len(h.rot.m) != 0 {
		t.Fatal("a refused change must not touch sops, the flag or the rotation store")
	}
}

func TestSetSopsFailureStopsBeforeCommit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	g := func(args ...string) *cmdrunnertest.Rule {
		return h.fake.On(append([]string{"git", "-C", repo}, args...)...)
	}
	g("rev-parse").Reply("main\n")
	g("status").Reply("")
	h.fake.On("sops").Fail("no age key")
	_, err := h.svc.Set(context.Background(), "caddy", "K", "v")
	if err == nil || !strings.Contains(err.Error(), "no age key") {
		t.Fatalf("err = %v", err)
	}
	if len(h.fake.CallsMatching("git", "-C", repo, "commit")) != 0 || h.pending() {
		t.Fatal("must not commit or flag a deploy when sops failed")
	}
}

func TestSetRejectsInvalidNames(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.scriptGit()
	for _, c := range [][2]string{{`a"b`, "K"}, {"svc", `K"]["x`}, {"", "K"}, {"svc", ""}, {"../x", "K"}, {"svc", "a b"}} {
		_, err := h.svc.Set(context.Background(), c[0], c[1], "v")
		var in *InvalidNameError
		if !errors.As(err, &in) {
			t.Errorf("Set(%q,%q) err = %v", c[0], c[1], err)
		}
	}
	if len(h.fake.Calls()) != 0 {
		t.Fatalf("commands ran for invalid names: %v", h.fake.Calls())
	}
}

func TestDeleteWipesWithEmptyString(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.scriptGit()
	if _, err := h.svc.Delete(context.Background(), "caddy", "CLOUDFLARE_API_TOKEN"); err != nil {
		t.Fatal(err)
	}
	in, _ := io.ReadAll(h.fake.CallsMatching("sops")[0].Stdin)
	if string(in) != `""` {
		t.Fatalf("stdin = %q", in)
	}
	if !h.pending() || len(h.rot.m) != 0 {
		t.Fatalf("pending=%v rotations=%v (wipe flags a deploy but is not a rotation)", h.pending(), h.rot.m)
	}
	if c := h.fake.CallsMatching("git", "-C", repo, "commit"); c[0].Args[4] != "secrets: wipe caddy/CLOUDFLARE_API_TOKEN via dashboard" {
		t.Fatalf("commit = %v", c[0].Args)
	}
}

// ---- store ----

func TestRotationStoreRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, _ := storetest.Open(t)
	rs := NewRotationStore(st)
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if err := rs.SetRotation(ctx, "a/B", at); err != nil {
		t.Fatal(err)
	}
	at2 := at.Add(time.Hour)
	if err := rs.SetRotation(ctx, "a/B", at2); err != nil {
		t.Fatal(err)
	}
	m, err := rs.Rotations(ctx)
	if err != nil || len(m) != 1 || !m["a/B"].Equal(at2) {
		t.Fatalf("rotations = %v err %v", m, err)
	}
}

// ---- HTTP ----

// serveAPI registers the secrets operations on a real huma API and serves
// one request through it.
func serveAPI(t *testing.T, h *harness) func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	NewHTTP(h.svc).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	return func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}
}

func TestHTTPMapping(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.writeRegistry(t)
	do := serveAPI(t, h)

	w, _ := do("GET", "/api/secrets", "")
	var l SecretsList
	if err := json.Unmarshal(w.Body.Bytes(), &l); w.Code != http.StatusOK || err != nil || len(l.Secrets) != 2 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	w, _ = do("GET", "/api/secrets/caddy/CLOUDFLARE_API_TOKEN", "")
	var st SecretStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); w.Code != http.StatusOK || err != nil || st.Name != "caddy/CLOUDFLARE_API_TOKEN" || !st.Present {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	h.fake.On("git", "-C", repo, "rev-parse", "--abbrev-ref").Reply("HEAD\n")
	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"unknown secret", "GET", "/api/secrets/x/Y", "", http.StatusNotFound, "not_found"},
		{"empty value", "PUT", "/api/secrets/caddy/K", `{"value":""}`, http.StatusUnprocessableEntity, "bad_request"},
		{"bad name", "PUT", `/api/secrets/a"b/K`, `{"value":"v"}`, http.StatusBadRequest, "invalid_name"},
		{"preflight", "PUT", "/api/secrets/caddy/K", `{"value":"v"}`, http.StatusConflict, "git_preflight"},
		{"delete preflight", "DELETE", "/api/secrets/caddy/K", "", http.StatusConflict, "git_preflight"},
	} {
		if w, m := do(c.method, c.path, c.body); w.Code != c.status || m["code"] != c.code {
			t.Errorf("%s: got %d %v, want %d %s: %s", c.name, w.Code, m["code"], c.status, c.code, w.Body)
		}
	}
}

func TestHTTPSetSuccessShape(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.scriptGit()
	w, _ := serveAPI(t, h)("PUT", "/api/secrets/caddy/K", `{"value":"v","description":"ignored"}`)
	var m SecretMutation
	if err := json.Unmarshal(w.Body.Bytes(), &m); w.Code != http.StatusOK || err != nil {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if !m.DeployPending || m.Branch != "main" || m.Commit != "abc123" || m.Warnings == nil {
		t.Fatalf("mutation = %+v", m)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
