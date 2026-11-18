package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return m
}

func TestWriteRendersProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	p := Conflict("dependent_services_running", "cannot unmount %s", "/pool").With("active_deps", []string{"a.service", "b.service"})
	Write(rec, p)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	m := decode(t, rec)
	want := map[string]any{
		"type":   "about:blank",
		"title":  "Conflict",
		"status": float64(409),
		"detail": "cannot unmount /pool",
		"code":   "dependent_services_running",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	deps, _ := m["active_deps"].([]any)
	if len(deps) != 2 || deps[0] != "a.service" {
		t.Errorf("active_deps = %v", m["active_deps"])
	}
}

func TestWriteExtCannotOverrideStandardMembers(t *testing.T) {
	rec := httptest.NewRecorder()
	p := NotFound("unknown_unit", "no unit %q", "x").With("status", 200).With("title", "forged")
	Write(rec, p)
	m := decode(t, rec)
	if m["status"] != float64(404) || m["title"] != "Not Found" {
		t.Errorf("standard members overridden: %v", m)
	}
}

func TestHelpersStatusAndTitle(t *testing.T) {
	cases := []struct {
		p      *Problem
		status int
	}{
		{BadRequest("c", ""), 400},
		{NotFound("c", ""), 404},
		{Conflict("c", ""), 409},
		{Internal("c", ""), 500},
		{BadGateway("c", ""), 502},
		{Unavailable("c", ""), 503},
	}
	for _, c := range cases {
		if c.p.Status != c.status || c.p.Title != http.StatusText(c.status) {
			t.Errorf("%+v: want status %d title %q", c.p, c.status, http.StatusText(c.status))
		}
		if c.p.Detail != "" {
			t.Errorf("empty format should give empty detail, got %q", c.p.Detail)
		}
	}
}

func TestNotFoundHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	NotFoundHandler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d", rec.Code)
	}
	if m := decode(t, rec); m["code"] != "not_found" {
		t.Errorf("body = %v", m)
	}
}
