package stacks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeTerm is a shell's terminal: the test writes the shell's output to out
// and reads what the client typed from in.
type fakeTerm struct {
	outR    *io.PipeReader
	out     *io.PipeWriter
	in      chan []byte
	resized chan [2]uint16
	closed  chan struct{}
}

func newFakeTerm() *fakeTerm {
	r, w := io.Pipe()
	return &fakeTerm{outR: r, out: w, in: make(chan []byte, 8), resized: make(chan [2]uint16, 8), closed: make(chan struct{})}
}

func (f *fakeTerm) Read(b []byte) (int, error) { return f.outR.Read(b) }
func (f *fakeTerm) Write(b []byte) (int, error) {
	f.in <- append([]byte(nil), b...)
	return len(b), nil
}
func (f *fakeTerm) Resize(cols, rows uint16) error { f.resized <- [2]uint16{cols, rows}; return nil }
func (f *fakeTerm) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
		_ = f.outR.Close()
	}
	return nil
}

func shellServer(t *testing.T) (*Service, string, chan *fakeTerm) {
	t.Helper()
	svc, _, _ := newRuntime(t)
	opened := make(chan *fakeTerm, 4)
	svc.SetShell(func(_ context.Context, machine string) (Terminal, error) {
		if machine != "immich" {
			return nil, errors.New("wrong machine " + machine)
		}
		f := newFakeTerm()
		opened <- f
		return f, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/{name}/shell", svc.Shell)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return svc, "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/stacks/", opened
}

func wait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", what)
		var zero T
		return zero
	}
}

func TestShellCarriesTheTerminalBothWays(t *testing.T) {
	t.Parallel()
	svc, url, opened := shellServer(t)
	finished(t, svc, svc.Start)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url+"immich/shell", nil)
	if err != nil {
		t.Fatal(err)
	}
	term := wait(t, opened, "terminal")

	go func() { _, _ = term.out.Write([]byte("root@immich:~# ")) }()
	typ, msg, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(msg) != "root@immich:~# " {
		t.Fatalf("output %v %q %v", typ, msg, err)
	}

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, term.in, "input"); string(got) != "ls\r" {
		t.Errorf("input %q", got)
	}

	// A resize is a text frame; anything else in text is ignored.
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`))
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	if got := wait(t, term.resized, "resize"); got != [2]uint16{120, 40} {
		t.Errorf("resized to %v", got)
	}

	// The shell exits: the client hears why, and the terminal is closed.
	_ = term.out.Close()
	_, _, err = conn.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusNormalClosure || ce.Reason != "The shell ended." {
		t.Fatalf("close %v", err)
	}
	wait(t, term.closed, "close of the terminal")
}

func TestClosingTheWindowHangsTheShellUp(t *testing.T) {
	t.Parallel()
	svc, url, opened := shellServer(t)
	finished(t, svc, svc.Start)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url+"immich/shell", nil)
	if err != nil {
		t.Fatal(err)
	}
	term := wait(t, opened, "terminal")
	_ = conn.Close(websocket.StatusNormalClosure, "")
	wait(t, term.closed, "close of the terminal")
	// The slot is free again.
	deadline := time.Now().Add(5 * time.Second)
	for len(svc.shellSlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the shell's slot was kept")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestShellRefusals(t *testing.T) {
	t.Parallel()
	svc, url, _ := shellServer(t)
	ctx := context.Background()

	refused := func(path string, h http.Header, status int, code string) {
		t.Helper()
		_, res, err := websocket.Dial(ctx, url+path, &websocket.DialOptions{HTTPHeader: h})
		if err == nil || res == nil || res.StatusCode != status {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		if code != "" && !strings.Contains(string(body), code) {
			t.Fatalf("%s: body %s", path, body)
		}
	}

	refused("nope/shell", nil, http.StatusNotFound, "unknown_stack")
	refused("immich/shell", nil, http.StatusConflict, "stack_stopped")

	finished(t, svc, svc.Start)
	// Another site's page may not open it.
	refused("immich/shell", http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden, "")

	if _, err := svc.Freeze(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	refused("immich/shell", nil, http.StatusConflict, "stack_frozen")
	if _, err := svc.Thaw(ctx, "immich"); err != nil {
		t.Fatal(err)
	}

	for range shellSessions {
		svc.shellSlots <- struct{}{}
	}
	refused("immich/shell", nil, http.StatusServiceUnavailable, "shell_busy")
}
