package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMiddlewareRequestIDAndRecorder(t *testing.T) {
	var gotMethod string
	var gotStatus int
	h := Middleware(func(method string, status int, d time.Duration) {
		gotMethod, gotStatus = method, status
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	// Inbound id is propagated.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/version", nil)
	req.Header.Set("X-Request-Id", "abc123")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") != "abc123" {
		t.Errorf("id = %q", rec.Header().Get("X-Request-Id"))
	}
	if gotMethod != "GET" || gotStatus != http.StatusTeapot {
		t.Errorf("recorder got %s %d", gotMethod, gotStatus)
	}

	// Missing id is generated.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/version", nil))
	if len(rec.Header().Get("X-Request-Id")) != 16 {
		t.Errorf("generated id = %q", rec.Header().Get("X-Request-Id"))
	}
}

func TestMiddlewareNilRecorderAndImplicit200(t *testing.T) {
	h := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLogLevel(t *testing.T) {
	fast, slow := 5*time.Millisecond, 1500*time.Millisecond
	for _, tc := range []struct {
		method string
		status int
		code   string
		dur    time.Duration
		want   slog.Level
	}{
		{"GET", 200, "", fast, slog.LevelDebug},
		{"HEAD", 200, "", fast, slog.LevelDebug},
		{"GET", 304, "", fast, slog.LevelDebug},
		{"GET", 200, "", slow, slog.LevelInfo},
		{"POST", 200, "", fast, slog.LevelInfo},
		{"PUT", 204, "", fast, slog.LevelInfo},
		{"GET", StatusClientClosedRequest, "", fast, slog.LevelDebug},
		{"POST", StatusClientClosedRequest, "", fast, slog.LevelDebug},
		{"POST", 400, "bad_request", fast, slog.LevelWarn},
		{"GET", 404, "", fast, slog.LevelWarn},
		{"POST", 409, "dependent_services_running", fast, slog.LevelWarn},
		{"GET", 503, "", fast, slog.LevelWarn},
		{"GET", 503, "store_unavailable", fast, slog.LevelWarn},
		{"GET", 500, "internal", fast, slog.LevelError},
		{"GET", 502, "", fast, slog.LevelError},
	} {
		if got := logLevel(tc.method, tc.status, tc.code, tc.dur); got != tc.want {
			t.Errorf("logLevel(%s, %d, %q, %v) = %v, want %v", tc.method, tc.status, tc.code, tc.dur, got, tc.want)
		}
	}
}

func TestMiddlewareLogsASuccessfulGETAtDebug(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/services", nil))
	if out := buf.String(); !strings.Contains(out, `"level":"DEBUG"`) || !strings.Contains(out, `"path":"/api/services"`) {
		t.Fatalf("want the request line at DEBUG:\n%s", out)
	}
}

func TestStatusRecorderReadsTheCodeOfA5xxProblem(t *testing.T) {
	var sr *statusRecorder
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sr = &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		Write(sr, Unavailable("store_unavailable", "the store is down"))
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/storage", nil))
	if got := sr.problemCode(); got != "store_unavailable" {
		t.Fatalf("code = %q", got)
	}
	if sr.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", sr.status)
	}
}

func TestStatusRecorderIgnoresBodiesOfSuccessfulResponses(t *testing.T) {
	sr := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	sr.Header().Set("Content-Type", ContentTypeProblem)
	_, _ = sr.Write([]byte(`{"code":"x"}`))
	if sr.problemCode() != "" {
		t.Fatal("sniffed a non-5xx body")
	}
}
