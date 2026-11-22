package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

// serveAPI registers the services operations on a real operator API and
// answers one request. A non-empty stack marks it as made by that stack.
func serveAPI(t *testing.T, h *HTTP, stack, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	r := httptest.NewRequest(method, path, nil)
	if stack != "" {
		r = r.WithContext(httpx.WithCallerStack(r.Context(), stack))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func TestHTTPStartStopRestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"sshd.service"}, StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service"})
	api := NewHTTP(h.svc)

	if w, m := serveAPI(t, api, "", "POST", "/api/services/nope/start"); w.Code != 404 || m["code"] != "unknown_unit" {
		t.Fatalf("unknown unit: %d %s", w.Code, w.Body)
	}
	for _, action := range []string{"start", "stop", "restart"} {
		if w, _ := serveAPI(t, api, "", "POST", "/api/services/media-plex/"+action); w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body)
		}
	}
	if got := h.stacks.calls; len(got) != 3 || got[0] != "start media-plex" || got[2] != "restart media-plex" {
		t.Fatalf("stack calls = %v", got)
	}
	h.stacks.err = &cmdrunner.Error{Err: errors.New("unit masked")}
	if w, m := serveAPI(t, api, "", "POST", "/api/services/media-plex/start"); w.Code != 500 || m["code"] != "systemd_failed" {
		t.Fatalf("systemd failure: %d %s", w.Code, w.Body)
	}
}

func TestHTTPStackCallerControlsStackUnitsOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"sshd.service"}, StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service"})
	api := NewHTTP(h.svc)
	for _, action := range []string{"start", "stop", "restart"} {
		for _, unit := range []string{"sshd", "sshd.service", "nope"} {
			w, m := serveAPI(t, api, "ha", "POST", "/api/services/"+unit+"/"+action)
			if w.Code != http.StatusForbidden || m["code"] != "core_unit" {
				t.Fatalf("%s %s by a stack: %d %s", action, unit, w.Code, w.Body)
			}
		}
		if w, _ := serveAPI(t, api, "ha", "POST", "/api/services/media-plex/"+action); w.Code != http.StatusNoContent {
			t.Fatalf("%s of a stack unit by a stack: %d %s", action, w.Code, w.Body)
		}
	}
	if len(h.stacks.calls) != 3 {
		t.Fatalf("stack calls = %v", h.stacks.calls)
	}
}

func TestHTTPLists(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"sshd.service"}, StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service"})
	api := NewHTTP(h.svc)
	h.fake.On("systemctl", "show").Reply("Id=sshd.service\nActiveState=active\nSubState=running\n")

	for path, want := range map[string]string{"/api/services": "media-plex", "/api/core-services": "sshd"} {
		w, _ := serveAPI(t, api, "", "GET", path)
		var got []ServiceInfo
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || len(got) != 1 || got[0].Name != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestHTTPMaskIsForTheOperatorOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"sshd.service"}, StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service"})
	api := NewHTTP(h.svc)

	if w, _ := serveAPI(t, api, "", "PUT", "/api/services/media-plex/mask"); w.Code != http.StatusNoContent {
		t.Fatalf("mask: %d %s", w.Code, w.Body)
	}
	if w, _ := serveAPI(t, api, "", "DELETE", "/api/services/media-plex.service/mask"); w.Code != http.StatusNoContent {
		t.Fatalf("unmask: %d %s", w.Code, w.Body)
	}
	if got := h.stacks.calls; len(got) != 2 || got[0] != "mask media-plex" || got[1] != "unmask media-plex.service" {
		t.Fatalf("stack calls = %v", got)
	}
	for _, unit := range []string{"sshd", "nope"} {
		if w, m := serveAPI(t, api, "", "PUT", "/api/services/"+unit+"/mask"); w.Code != 404 || m["code"] != "unknown_unit" {
			t.Fatalf("mask %s: %d %s", unit, w.Code, w.Body)
		}
	}
	h.stacks.err = httpx.Conflict("last_unit", "media-plex is the last enabled service")
	if w, m := serveAPI(t, api, "", "PUT", "/api/services/media-plex/mask"); w.Code != 409 || m["code"] != "last_unit" {
		t.Fatalf("last unit: %d %s", w.Code, w.Body)
	}

	h.stacks.err = nil

	// The stack API has the unit actions but not the mask: a stack with
	// the control grant cannot disable a service.
	opMux, stackMux := http.NewServeMux(), http.NewServeMux()
	api.Register(httpx.APIs{Operator: httpx.NewAPI(opMux, "t", "1"), Stack: httpx.NewAPI(stackMux, "t", "1")})
	for _, req := range []*http.Request{
		httptest.NewRequest("PUT", "/api/services/media-plex/mask", nil),
		httptest.NewRequest("DELETE", "/api/services/media-plex/mask", nil),
	} {
		w := httptest.NewRecorder()
		stackMux.ServeHTTP(w, req.WithContext(httpx.WithCallerStack(req.Context(), "ha")))
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s on the stack API: %d", req.Method, req.URL.Path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	stackMux.ServeHTTP(w, httptest.NewRequest("POST", "/api/services/media-plex/stop", nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("stop on the stack API: %d %s", w.Code, w.Body)
	}
}

func TestHTTPListShowsADisabledUnit(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil,
		StackUnit{Stack: "media", Unit: "plex.service", Name: "media-plex.service", ActiveState: "active"},
		StackUnit{Stack: "media", Unit: "sonarr.service", Name: "media-sonarr.service", Masked: true})
	w, _ := serveAPI(t, NewHTTP(h.svc), "", "GET", "/api/services")
	var got []ServiceInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got[0].Masked || !got[1].Masked || got[1].ActiveState != "inactive" {
		t.Errorf("list = %+v", got)
	}
}
