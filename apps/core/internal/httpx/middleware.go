package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/logging"
)

// Recorder receives one observation per finished request (Prometheus
// counters in the daemon). It may be nil.
type Recorder func(method string, status int, d time.Duration)

// Middleware propagates X-Request-Id (generating one when absent), echoes it
// on the response, emits one structured log line per request and reports it
// to rec.
//
// Auth posture: the daemon listens on a unix socket with mode 0660 and group
// homelab-daemon; socket permissions are the only access control.
func Middleware(rec Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-Id")
			if reqID == "" {
				reqID = genRequestID()
			}
			w.Header().Set("X-Request-Id", reqID)
			ctx := context.WithValue(r.Context(), logging.CtxKeyReqID, reqID)
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(sr, r.WithContext(ctx))
			dur := time.Since(start)

			msg := fmt.Sprintf("%s %s %d %dms", r.Method, r.URL.Path, sr.status, dur.Milliseconds())
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", sr.status,
				"duration_ms", dur.Milliseconds(),
				"req_id", reqID,
			}
			code := sr.problemCode()
			if code != "" {
				attrs = append(attrs, "code", code)
			}
			log.Log(r.Context(), logLevel(r.Method, sr.status, code, dur), msg, attrs...)
			if rec != nil {
				rec(r.Method, sr.status, dur)
			}
		})
	}
}

// StatusClientClosedRequest is the status logged and counted for a request
// whose client disconnected before the response was written (nginx's 499).
// It is never sent; there is nobody to send it to.
const StatusClientClosedRequest = 499

const slowRequest = time.Second

// logLevel picks the severity of a finished request. A client that hung
// up is DEBUG, and so is a fast 2xx or 3xx to a GET or HEAD: the dashboard
// polls those every few seconds. 4xx are the client's doing and 503 is "not
// available right now" (a store that is down): both WARN. Only other 5xx are
// the daemon's own errors.
func logLevel(method string, status int, code string, d time.Duration) slog.Level {
	switch {
	case status == StatusClientClosedRequest:
		return slog.LevelDebug
	case status == http.StatusServiceUnavailable, status >= 400 && status < 500:
		return slog.LevelWarn
	case status >= 500:
		return slog.LevelError
	case (method == http.MethodGet || method == http.MethodHead) && d <= slowRequest:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// genRequestID produces a 16-hex-char random id when X-Request-Id is absent.
func genRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusRecorder captures the response status. It implements Flush and
// Unwrap so http.ResponseController reaches the real writer through it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   []byte // first bytes of a 5xx problem body, to read its code
}

const maxProblemSniff = 1024

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status >= 500 && len(s.body) < maxProblemSniff &&
		strings.HasPrefix(s.Header().Get("Content-Type"), ContentTypeProblem) {
		s.body = append(s.body, b[:min(len(b), maxProblemSniff-len(s.body))]...)
	}
	return s.ResponseWriter.Write(b)
}

// problemCode returns the machine code of a 5xx problem response, if any.
func (s *statusRecorder) problemCode() string {
	if len(s.body) == 0 {
		return ""
	}
	var p struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(s.body, &p) != nil {
		return ""
	}
	return p.Code
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.NewResponseController find the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
