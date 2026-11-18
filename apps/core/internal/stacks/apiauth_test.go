package stacks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

// apiManifest is immich at version 3 with the given api scopes.
func apiManifest(name string, scopes ...string) string {
	m := immichManifest
	m = strings.Replace(m, `"name": "immich"`, `"name": "`+name+`"`, 1)
	if len(scopes) > 0 {
		b, _ := json.Marshal(scopes)
		m = strings.Replace(m, `"gpu": true,`, `"gpu": true, "api": `+string(b)+`,`, 1)
	}
	return m
}

// install adds and approves a stack from manifest at rev.
func install(t *testing.T, svc *Service, fs *fakeSource, rev, manifest string) {
	t.Helper()
	ctx := context.Background()
	fs.set(rev, manifest)
	s, err := svc.Add(ctx, src, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, s.Name, rev); err != nil {
		t.Fatal(err)
	}
}

// tokenOf reads the token from a stack's env file.
func tokenOf(t *testing.T, svc *Service, name string) string {
	t.Helper()
	b, err := os.ReadFile(svc.apiEnvFile(name))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "HOMELAB_API_TOKEN="); ok {
			return v
		}
	}
	t.Fatalf("no token in %s", b)
	return ""
}

type callerOut struct {
	Body struct {
		Stack string `json:"stack"`
	}
}

// testAPIs registers every stacks operation on both APIs, the way the
// daemon does, plus a read operation that answers with the caller.
func testAPIs(svc *Service) (op, stack http.Handler, a httpx.APIs) {
	opMux, stackMux := http.NewServeMux(), http.NewServeMux()
	a = httpx.APIs{Operator: httpx.NewAPI(opMux, "t", "1"), Stack: httpx.NewAPI(stackMux, "t stack", "1")}
	a.Stack.UseMiddleware(APIAuth(a.Stack, svc))
	NewHTTP(svc).Register(a)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{OperationID: "whoami", Method: http.MethodGet, Path: "/whoami"},
		func(ctx context.Context, _ *struct{}) (*callerOut, error) {
			out := &callerOut{}
			out.Body.Stack, _ = httpx.CallerStack(ctx)
			return out, nil
		})
	return opMux, stackMux, a
}

func call(h http.Handler, method, path, token, from string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = from + ":40000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestStackAPIAuth(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	install(t, svc, fs, "aaaaaaa1", apiManifest("ctl", APIRead, APIControl))
	install(t, svc, fs, "aaaaaaa2", apiManifest("reader", APIRead))
	install(t, svc, fs, "aaaaaaa3", apiManifest("plain"))
	op, stack, a := testAPIs(svc)

	ctl, reader := tokenOf(t, svc, "ctl"), tokenOf(t, svc, "reader")
	ctlAddr, readerAddr := "10.99.0.2", "10.99.1.2"
	if _, err := os.Stat(svc.apiEnvFile("plain")); err == nil {
		t.Fatal("a stack without an api grant got a token")
	}

	// Every operation of the package, tried with each token: operator
	// operations do not exist on the stack listener, the others run only
	// with their scope granted.
	ops := 0
	for path, item := range a.Operator.OpenAPI().Paths {
		for method, o := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodDelete: item.Delete,
			http.MethodPut: item.Put, http.MethodPatch: item.Patch,
		} {
			if o == nil || o.OperationID == "whoami" {
				continue
			}
			ops++
			url := "/api" + strings.NewReplacer("{name}", "nope", "{id}", "999999", "{var}", "KEY").Replace(path)
			scope := httpx.OperationScope(o)
			for _, c := range []struct {
				who, token, from string
				scopes           []string
			}{
				{"ctl", ctl, ctlAddr, []string{APIRead, APIControl}},
				{"reader", reader, readerAddr, []string{APIRead}},
			} {
				status, body := call(stack, method, url, c.token, c.from)
				code, _ := body["code"].(string)
				switch {
				case scope == httpx.ScopeOperator:
					if status != http.StatusNotFound || code != "not_found" {
						t.Errorf("%s %s (operator) with %s's token: %d %s, want 404 not_found", method, path, c.who, status, code)
					}
				case !strings.Contains(strings.Join(c.scopes, ","), string(scope)):
					if status != http.StatusForbidden || code != "scope_not_granted" {
						t.Errorf("%s %s (%s) with %s's token: %d %s, want 403 scope_not_granted", method, path, scope, c.who, status, code)
					}
				default:
					// Reached the handler, which knows no stack "nope".
					if status == http.StatusUnauthorized || status == http.StatusForbidden || code == "not_found" || code == "scope_not_granted" {
						t.Errorf("%s %s (%s) with %s's token: %d %s, want the handler's answer", method, path, scope, c.who, status, code)
					}
				}
			}
		}
	}
	if ops != 21 {
		t.Errorf("tried %d operations, want the 21 of the stacks package", ops)
	}

	tests := []struct {
		name, token, from string
		status            int
		code              string
	}{
		{"no token", "", ctlAddr, 401, "unauthorized"},
		{"unknown token", "bm9wZQ", ctlAddr, 401, "unauthorized"},
		{"another stack's address", ctl, readerAddr, 403, "wrong_source"},
		{"the host's address", ctl, "10.99.0.1", 403, "wrong_source"},
		{"the LAN", reader, "192.168.1.10", 403, "wrong_source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := call(stack, http.MethodGet, "/api/stacks", tt.token, tt.from)
			if status != tt.status || body["code"] != tt.code || body["type"] != "about:blank" {
				t.Errorf("got %d %v, want %d %s", status, body, tt.status, tt.code)
			}
		})
	}
	t.Run("not a bearer token", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/stacks", nil)
		r.RemoteAddr = ctlAddr + ":1"
		r.Header.Set("Authorization", "Basic "+ctl)
		w := httptest.NewRecorder()
		stack.ServeHTTP(w, r)
		if w.Code != 401 || w.Header().Get("Content-Type") != httpx.ContentTypeProblem {
			t.Errorf("got %d %s", w.Code, w.Header().Get("Content-Type"))
		}
	})
	t.Run("the handler sees the calling stack", func(t *testing.T) {
		if status, body := call(stack, http.MethodGet, "/api/whoami", reader, readerAddr); status != 200 || body["stack"] != "reader" {
			t.Errorf("got %d %v", status, body)
		}
		if status, body := call(op, http.MethodGet, "/api/whoami", "", "127.0.0.1"); status != 200 || body["stack"] != "" {
			t.Errorf("operator: got %d %v", status, body)
		}
	})
	t.Run("the operator API needs no token", func(t *testing.T) {
		if status, body := call(op, http.MethodPost, "/api/stacks/nope/fetch", "", "127.0.0.1"); status != 404 || body["code"] != "unknown_stack" {
			t.Errorf("got %d %v", status, body)
		}
	})
	t.Run("a revoked grant revokes the token", func(t *testing.T) {
		fs.set("bbbbbbb2", apiManifest("reader"))
		s, err := svc.Fetch(context.Background(), "reader")
		if err != nil || s.Approved.Rev != "bbbbbbb2" {
			t.Fatalf("dropping the grant was not approved at once: %v %+v", err, s.Approved)
		}
		if status, body := call(stack, http.MethodGet, "/api/stacks", reader, readerAddr); status != 401 {
			t.Errorf("got %d %v", status, body)
		}
	})
}

func TestStackAPIWithoutAServiceRefusesEverything(t *testing.T) {
	t.Parallel()
	_, stack, _ := testAPIs(nil)
	if status, body := call(stack, http.MethodGet, "/api/stacks", "x", "10.99.0.2"); status != 401 || body["code"] != "unauthorized" {
		t.Errorf("got %d %v", status, body)
	}
}

func TestStackAPITokenLifecycle(t *testing.T) {
	t.Parallel()
	svc, fs, h := newServiceOn(t)
	ctx := context.Background()
	var mu sync.Mutex
	var rules []string
	h.On("nft", "-f").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		b, _ := io.ReadAll(c.Stdin)
		mu.Lock()
		rules = append(rules, string(b))
		mu.Unlock()
		return ok("")
	})
	lastRules := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(rules) == 0 {
			return ""
		}
		return rules[len(rules)-1]
	}

	install(t, svc, fs, "aaaaaaa1", apiManifest("immich", APIRead, APIControl))
	env := svc.apiEnvFile("immich")
	fi, err := os.Stat(env)
	if err != nil {
		t.Fatalf("approve did not write the token: %v", err)
	}
	if fi.Mode().Perm() != 0o400 {
		t.Errorf("env file mode = %v", fi.Mode().Perm())
	}
	first := tokenOf(t, svc, "immich")
	b, _ := os.ReadFile(env)
	if want := "HOMELAB_API_URL=http://10.99.0.1:7781/api\nHOMELAB_API_TOKEN=" + first + "\n"; string(b) != want {
		t.Errorf("env file:\n%s", b)
	}
	r, _ := svc.row(ctx, "immich")
	if r.ApiTokenHash != hashToken(first) || strings.Contains(r.ApiTokenHash, first) {
		t.Errorf("stored %q, want only the token's hash", r.ApiTokenHash)
	}

	s := finished(t, svc, svc.Start)
	if s.Operation.Error != "" {
		t.Fatal(s.Operation.Error)
	}
	boot := argv(h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")[0])
	if want := "--bind-ro=" + filepath.Join(svc.stateDir("immich"), "homelab-api") + ":/var/lib/homelab-api:idmap"; !strings.Contains(boot, want) {
		t.Errorf("boot command lacks %q:\n%s", want, boot)
	}
	if !strings.Contains(lastRules(), "tcp dport 7781 accept") {
		t.Errorf("the firewall does not open the stack listener:\n%s", lastRules())
	}
	if tokenOf(t, svc, "immich") != first {
		t.Error("a start replaced a token whose file was in place")
	}

	// A file lost while the stack was stopped is written again, with a new
	// token: only the hash was kept.
	finished(t, svc, svc.Stop)
	if err := os.Remove(env); err != nil {
		t.Fatal(err)
	}
	finished(t, svc, svc.Start)
	second := tokenOf(t, svc, "immich")
	if second == first {
		t.Error("the token was not replaced")
	}
	if _, ok, _ := svc.caller(ctx, first); ok {
		t.Error("the lost token still works")
	}
	if c, ok, _ := svc.caller(ctx, second); !ok || c.name != "immich" || c.addr.String() != "10.99.0.2" {
		t.Errorf("the new token: %+v %v", c, ok)
	}

	// Dropping the grant is approved at once and takes the token away.
	fs.set("bbbbbbb2", apiManifest("immich"))
	if _, err := svc.Fetch(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	r, _ = svc.row(ctx, "immich")
	if _, err := os.Stat(env); err == nil || r.ApiTokenHash != "" {
		t.Errorf("dropping the grant left the file (%v) or the hash %q", err, r.ApiTokenHash)
	}
	if _, ok, _ := svc.caller(ctx, second); ok {
		t.Error("the token of a dropped grant still works")
	}

	// Asking again needs approval; approving issues a new token, and
	// removing the stack deletes it.
	fs.set("ccccccc3", apiManifest("immich", APIRead))
	if s, _ := svc.Fetch(ctx, "immich"); len(s.NewGrants) != 1 || s.NewGrants[0].Kind != StackGrantKindAPI {
		t.Fatalf("new grants = %+v", s.NewGrants)
	}
	if _, err := os.Stat(env); err == nil {
		t.Error("a token was issued before approval")
	}
	if _, err := svc.Approve(ctx, "immich", "ccccccc3"); err != nil {
		t.Fatal(err)
	}
	third := tokenOf(t, svc, "immich")
	finished(t, svc, svc.Stop)
	if err := svc.Remove(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env); err == nil {
		t.Error("removing the stack left its token file")
	}
	if _, ok, _ := svc.caller(ctx, third); ok {
		t.Error("the token of a removed stack still works")
	}
}

func TestStackWithoutAPIGetsNoListenerOrBind(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	finished(t, svc, svc.Start)
	boot := argv(h.CallsMatching("systemd-run", "--unit=homelab-stack-immich")[0])
	if strings.Contains(boot, "homelab-api") {
		t.Errorf("a stack without an api grant got the token directory:\n%s", boot)
	}
	if _, err := os.Stat(svc.apiEnvDir("immich")); err == nil {
		t.Error("a stack without an api grant got a token directory")
	}
}
