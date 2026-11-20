package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestCheckAcceptsSuccess(t *testing.T) {
	for _, s := range []int{200, 202, 204} {
		if err := check(s, nil); err != nil {
			t.Errorf("status %d: %v", s, err)
		}
	}
}

func TestCheckPreservesProblemDetail(t *testing.T) {
	err := check(409, []byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"subvolume /pool/x is not empty","code":"subvolume_not_empty"}`))
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if ae.Status != 409 || ae.Code != "subvolume_not_empty" {
		t.Fatalf("unexpected %+v", ae)
	}
	for _, want := range []string{"409", "subvolume /pool/x is not empty", "subvolume_not_empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestCheckFallsBackToRawBody(t *testing.T) {
	err := check(502, []byte("bad gateway from proxy\n"))
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "bad gateway from proxy") {
		t.Fatalf("got %v", err)
	}
	if err := check(500, []byte("")); err == nil || !strings.Contains(err.Error(), "Internal Server Error") {
		t.Fatalf("empty body: %v", err)
	}
}

// serve starts handler on a unix socket and points the CLI at it.
func serve(t *testing.T, h http.Handler) {
	t.Helper()
	// Relative to a temp cwd: an absolute t.TempDir() path can exceed the
	// 108-byte unix socket limit under nested nix shells.
	t.Chdir(t.TempDir())
	sock := "d.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = l
	srv.Start()
	old := daemonSocket
	daemonSocket = sock
	t.Cleanup(func() { daemonSocket = old; srv.Close() })
}

func TestIsUnreachableWhenSocketMissing(t *testing.T) {
	old := daemonSocket
	t.Chdir(t.TempDir()) // short relative path, see serve
	daemonSocket = "absent.sock"
	defer func() { daemonSocket = old }()
	_, err := call[map[string]any](context.Background(), http.MethodGet, "/version", nil)
	if err == nil || !isUnreachable(err) {
		t.Fatalf("want unreachable, got %v", err)
	}
}

func TestDaemonAnswerIsNotUnreachable(t *testing.T) {
	serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Service Unavailable","status":503,"detail":"starting"}`))
	}))
	_, err := call[map[string]any](context.Background(), http.MethodGet, "/version", nil)
	if err == nil || isUnreachable(err) || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("got %v", err)
	}
}

func TestRenderErrorJSONWritesOnlyJSON(t *testing.T) {
	var out bytes.Buffer
	renderError(&out, true, io.EOF)
	var got struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Success || got.Error != "EOF" {
		t.Fatalf("unexpected JSON error: %q (%v)", out.String(), err)
	}
}

func TestRenderErrorJSONIncludesProblemFields(t *testing.T) {
	var out bytes.Buffer
	renderError(&out, true, &APIError{Status: 409, Code: "subvolume_not_empty", Detail: "subvolume /pool/x is not empty"})
	var got struct {
		Success bool   `json:"success"`
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Success || got.Status != 409 || got.Code != "subvolume_not_empty" || got.Detail == "" {
		t.Fatalf("unexpected JSON error: %q (%v)", out.String(), err)
	}
}

// runApp executes the real command tree against the fake daemon.
func runApp(t *testing.T, args ...string) error {
	t.Helper()
	app := &cli.App{Name: "homelab", Writer: io.Discard, ErrWriter: io.Discard,
		Flags:    []cli.Flag{&cli.BoolFlag{Name: "json"}},
		Commands: []*cli.Command{servicesCmd(), backupCmd(), stacksCmd(), systemCmd()}}
	return app.Run(append([]string{"homelab"}, args...))
}

func TestServiceStartMapsProblemToError(t *testing.T) {
	serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/immich/start" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"mount constraint missing","code":"constraint"}`))
	}))
	err := runApp(t, "services", "start", "immich")
	if err == nil || !strings.Contains(err.Error(), "mount constraint missing") || !strings.Contains(err.Error(), "409") {
		t.Fatalf("got %v", err)
	}
}
