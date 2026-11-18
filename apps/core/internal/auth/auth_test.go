package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

// rig is the operator API with the sign-in registered and one other
// operation, served as the CLI's socket (cli) and as the web socket (web).
type rig struct {
	t        *testing.T
	s        *Service
	cli, web http.Handler
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _ := storetest.Open(t)
	s := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	apis := httpx.APIs{Operator: httpx.NewAPI(mux, "test", "1")}
	NewHTTP(s).Register(apis)
	httpx.Register(apis, httpx.ScopeOperator, huma.Operation{
		OperationID: "approve", Method: http.MethodPost, Path: "/approve",
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	httpx.Register(apis, httpx.ScopeRead, huma.Operation{
		OperationID: "read", Method: http.MethodGet, Path: "/read",
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	return &rig{t: t, s: s, cli: mux, web: Gate(s)(mux)}
}

type req struct {
	method, path, body string
	cookie             *http.Cookie
	header             map[string]string
}

// do sends r to h as a same-origin browser request unless r's headers say
// otherwise.
func (g *rig) do(h http.Handler, r req) *http.Response {
	g.t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, "http://homelab.test"+r.path, body)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Forwarded-For", "192.0.2.10")
	if r.method != http.MethodGet {
		hr.Header.Set("Origin", "http://homelab.test")
	}
	for k, v := range r.header {
		if v == "" {
			hr.Header.Del(k)
		} else {
			hr.Header.Set(k, v)
		}
	}
	if r.cookie != nil {
		hr.AddCookie(r.cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, hr)
	return rec.Result()
}

func code(t *testing.T, resp *http.Response) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return p.Code
}

func want(t *testing.T, resp *http.Response, status int, problem string) {
	t.Helper()
	got := ""
	if resp.StatusCode >= 400 {
		got = code(t, resp)
	}
	if resp.StatusCode != status || got != problem {
		t.Fatalf("answer = %d %q, want %d %q", resp.StatusCode, got, status, problem)
	}
}

func session(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "homelab_session") && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", resp.Header.Values("Set-Cookie"))
	return nil
}

const pw = `{"password":"correct horse battery"}`

func TestFirstVisitorSetsThePassword(t *testing.T) {
	t.Parallel()
	g := newRig(t)

	want(t, g.do(g.web, req{method: "GET", path: "/api/read"}), 401, "setup_required")
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/login", body: pw}), 409, "no_password")
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: `{"password":"short"}`}), 400, "weak_password")

	resp := g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw})
	want(t, resp, 200, "")
	c := session(t, resp)
	if c.Name != cookiePlain || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure {
		t.Errorf("cookie = %+v", c)
	}
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c}), 204, "")
	want(t, g.do(g.web, req{method: "POST", path: "/api/approve", cookie: c}), 200, "")

	// The second visitor cannot take the box over.
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: `{"password":"another password"}`}), 409, "password_set")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read"}), 401, "login_required")
}

func TestLoginAndLimit(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw}), 200, "")

	resp := g.do(g.web, req{method: "POST", path: "/api/auth/login", body: pw})
	want(t, resp, 200, "")
	c := session(t, resp)
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c}), 204, "")

	for range clientFailures {
		want(t, g.do(g.web, req{method: "POST", path: "/api/auth/login", body: `{"password":"wrong"}`}), 401, "wrong_password")
	}
	// Locked out now, even with the right password.
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/login", body: pw}), 429, "too_many_attempts")
	// Another address is not.
	other := map[string]string{"X-Forwarded-For": "192.0.2.11"}
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/login", body: pw, header: other}), 200, "")
	// A live session is not affected.
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c}), 204, "")

	// Signing out ends the session.
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/logout", cookie: c}), 204, "")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c}), 401, "login_required")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: &http.Cookie{Name: cookiePlain, Value: "forged"}}), 401, "login_required")
}

func TestCrossOriginRefused(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	c := session(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw}))

	for name, h := range map[string]map[string]string{
		"a stack's site":       {"Sec-Fetch-Site": "same-site", "Origin": "https://photos.homelab.test"},
		"another site":         {"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"},
		"no fetch metadata":    {"Origin": "https://evil.example"},
		"no origin at all":     {"Origin": ""},
		"websocket, elsewhere": {"Origin": "https://evil.example", "Upgrade": "websocket"},
	} {
		method := http.MethodPost
		if h["Upgrade"] != "" {
			method = http.MethodGet
		}
		resp := g.do(g.web, req{method: method, path: "/api/approve", cookie: c, header: h})
		if resp.StatusCode != 403 || code(t, resp) != "cross_origin" {
			t.Errorf("%s: answer %d", name, resp.StatusCode)
		}
	}
	// The dashboard itself, with and without fetch metadata.
	want(t, g.do(g.web, req{method: "POST", path: "/api/approve", cookie: c, header: map[string]string{"Sec-Fetch-Site": "same-origin"}}), 200, "")
	want(t, g.do(g.web, req{method: "POST", path: "/api/approve", cookie: c}), 200, "")
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	a := session(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw}))
	b := session(t, g.do(g.web, req{method: "POST", path: "/api/auth/login", body: pw}))

	want(t, g.do(g.web, req{method: "PUT", path: "/api/auth/password", cookie: a,
		body: `{"current_password":"wrong","password":"a new password"}`}), 401, "wrong_password")
	resp := g.do(g.web, req{method: "PUT", path: "/api/auth/password", cookie: a,
		body: `{"current_password":"correct horse battery","password":"a new password"}`})
	want(t, resp, 200, "")
	a2 := session(t, resp)

	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: a}), 401, "login_required")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: b}), 401, "login_required")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: a2}), 204, "")
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/login", body: `{"password":"a new password"}`}), 200, "")
}

func TestTheCLISocketNeedsNoSession(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	cli := map[string]string{"Origin": "", "X-Forwarded-For": ""}
	c := session(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw}))

	want(t, g.do(g.cli, req{method: "POST", path: "/api/approve", header: cli}), 200, "")
	want(t, g.do(g.cli, req{method: "POST", path: "/api/auth/login", body: pw, header: cli}), 403, "web_only")
	want(t, g.do(g.web, req{method: "DELETE", path: "/api/auth/password", cookie: c}), 403, "cli_only")

	// The CLI sets a password without the old one, and it has no cookie.
	resp := g.do(g.cli, req{method: "PUT", path: "/api/auth/password", body: `{"password":"set by root"}`, header: cli})
	want(t, resp, 200, "")
	if v := resp.Header.Values("Set-Cookie"); len(v) != 0 {
		t.Errorf("the CLI got a cookie: %v", v)
	}
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c}), 401, "login_required")

	// Reset: the next visitor sets one again.
	want(t, g.do(g.cli, req{method: "DELETE", path: "/api/auth/password", header: cli}), 204, "")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read"}), 401, "setup_required")
	want(t, g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw}), 200, "")
}

func TestHTTPSCookieAndExpiry(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	https := map[string]string{"X-Forwarded-Proto": "https", "Origin": "https://homelab.test", "Sec-Fetch-Site": "same-origin"}
	resp := g.do(g.web, req{method: "POST", path: "/api/auth/setup", body: pw, header: https})
	want(t, resp, 200, "")
	c := session(t, resp)
	if c.Name != cookieSecure || !c.Secure {
		t.Fatalf("cookie over HTTPS = %+v", c)
	}
	// The plain cookie name is not read over HTTPS.
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: &http.Cookie{Name: cookiePlain, Value: c.Value}, header: https}), 401, "login_required")
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c, header: https}), 204, "")

	// A session unused for longer than its life is gone.
	g.s.now = func() time.Time { return time.Now().Add(sessionTTL + time.Minute) }
	want(t, g.do(g.web, req{method: "GET", path: "/api/read", cookie: c, header: https}), 401, "login_required")
}

func TestPasswordHash(t *testing.T) {
	t.Parallel()
	h, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=1$") {
		t.Errorf("hash = %q", h)
	}
	if ok, err := checkPassword(h, "correct horse battery"); !ok || err != nil {
		t.Errorf("right password: %v %v", ok, err)
	}
	if ok, _ := checkPassword(h, "correct horse batterY"); ok {
		t.Error("wrong password accepted")
	}
	if _, err := checkPassword("$argon2id$garbage", "x"); err == nil {
		t.Error("malformed hash accepted")
	}
}
