package notifications

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

// newAPI serves the notifications operations on a real operator API, over a
// notifier pushing to hookURL ("" = no channel) and a database store.
func newAPI(t *testing.T, hookURL string) http.Handler {
	t.Helper()
	s, _ := storetest.Open(t)
	now := time.Now()
	mux := http.NewServeMux()
	NewHTTP(testNotifierStore(t, hookURL, &now, NewDBStore(s)), NewChannels(s, keyFile(t))).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	return mux
}

// do sends one request and decodes a 2xx body into out.
func do(t *testing.T, h http.Handler, method, path, body string, out any) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code < 300 && out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, w.Body)
		}
	}
	return w.Code, m
}

func TestHTTPApplyReportResolveSnapshot(t *testing.T) {
	t.Parallel()
	srv, _ := newHook(t, 200)
	h := newAPI(t, srv.URL)

	var r NotifyResponse
	if code, _ := do(t, h, "POST", "/api/notifications", `{"action":"report","key":"unit:x","title":"x failing"}`, &r); code != 200 ||
		len(r.Opened) != 1 || r.Reminded == nil || r.Resolved == nil {
		t.Fatalf("report: %d %+v", code, r)
	}
	var snap NotificationsSnapshot
	if do(t, h, "GET", "/api/notifications", "", &snap); snap.Open["unit:x"].Count != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if do(t, h, "POST", "/api/notifications", `{"action":"resolve","key":"unit:x"}`, &r); len(r.Resolved) != 1 {
		t.Fatalf("resolve = %+v", r)
	}
	var log []NotificationLogEntry
	do(t, h, "GET", "/api/notifications/log", "", &log)
	if len(log) != 2 || log[0].Kind != Resolved || log[1].Key == nil || *log[1].Key != "unit:x" {
		t.Fatalf("log = %+v", log)
	}
	if do(t, h, "GET", "/api/notifications/log?limit=1&before="+jsonInt(log[0].Id), "", &log); len(log) != 1 || log[0].Kind != Opened {
		t.Fatalf("second page = %+v", log)
	}
}

func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestHTTPErrors(t *testing.T) {
	t.Parallel()
	h := newAPI(t, "")
	for _, c := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"action":"report"}`, http.StatusBadRequest, "invalid_notification"},
		{`{"action":"bogus","title":"t"}`, http.StatusUnprocessableEntity, "bad_request"},
		{``, http.StatusBadRequest, "bad_request"},
		// No channel configured: delivery cannot succeed.
		{`{"action":"send","title":"t"}`, http.StatusBadGateway, "delivery_failed"},
	} {
		if code, m := do(t, h, "POST", "/api/notifications", c.body, nil); code != c.status || m["code"] != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.body, code, m, c.status, c.code)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(srv.Close)
	if code, _ := do(t, newAPI(t, srv.URL), "POST", "/api/notifications", `{"action":"send","title":"t"}`, nil); code != http.StatusBadGateway {
		t.Fatalf("failing hub: %d", code)
	}
}

func TestHTTPEmptyLog(t *testing.T) {
	t.Parallel()
	srv, _ := newHook(t, 200)
	var raw json.RawMessage
	if do(t, newAPI(t, srv.URL), "GET", "/api/notifications/log", "", &raw); string(raw) != "[]" {
		t.Fatalf("empty log = %s, want []", raw)
	}
}
