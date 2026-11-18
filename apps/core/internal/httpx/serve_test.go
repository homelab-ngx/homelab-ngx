package httpx

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}

func TestServeSocketModeStaleRemovalAndGracefulShutdown(t *testing.T) {
	// Relative to a temp cwd: an absolute t.TempDir() path can exceed the
	// 108-byte unix socket limit under nested nix shells.
	t.Chdir(t.TempDir())
	sock := "d.sock"
	// A stale file from a crashed run must not block startup.
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	inflight := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "pong") })
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(inflight)
		<-release
		_, _ = io.WriteString(w, "finished")
	})

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, sock, "", mux, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	c := unixClient(sock)
	var resp *http.Response
	var err error
	for i := 0; i < 100; i++ {
		if resp, err = c.Get("http://unix/ping"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	resp.Body.Close()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket mode = %o, want 660", perm)
	}

	// Start a slow request, then cancel: Serve must wait for it to finish.
	slow := make(chan string, 1)
	go func() {
		r, err := c.Get("http://unix/slow")
		if err != nil {
			slow <- "error: " + err.Error()
			return
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		slow <- string(b)
	}()
	<-inflight
	cancel()
	select {
	case err := <-served:
		t.Fatalf("Serve returned (%v) with a request still in flight", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if got := <-slow; got != "finished" {
		t.Errorf("in-flight request = %q", got)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after drain")
	}
}

func TestServeListenError(t *testing.T) {
	err := Serve(context.Background(), filepath.Join(t.TempDir(), "no", "such", "dir", "s.sock"), "", http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected a listen error")
	}
}
