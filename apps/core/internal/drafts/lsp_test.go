package drafts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeServer is a language server on pipes: it answers initialize, asks for
// its configuration once initialized, and reports what it was sent.
type fakeServer struct {
	got     chan map[string]any // every message it read
	stopped chan struct{}
}

func (f *fakeServer) start(_ context.Context, _ string) (*lspProcess, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	write := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(outW, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	go func() {
		rd := bufio.NewReader(inR)
		for {
			msg, err := readFrame(rd)
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(msg, &m)
			f.got <- m
			switch m["method"] {
			case "initialize":
				write(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"capabilities": map[string]any{}}})
			case "initialized":
				write(map[string]any{"jsonrpc": "2.0", "id": "cfg", "method": "workspace/configuration",
					"params": map[string]any{"items": []any{map[string]any{"section": "nixd"}}}})
			}
		}
	}()
	return &lspProcess{in: inW, out: outR, stop: func() {
		_ = inW.Close()
		_ = outW.Close()
		close(f.stopped)
	}}, nil
}

func lspServer(t *testing.T) (*Service, *httptest.Server, *fakeServer) {
	t.Helper()
	s, _ := newService(t)
	if _, err := s.Create(context.Background(), "site"); err != nil {
		t.Fatal(err)
	}
	// The template names nixos-unstable; its commit is known, so no git.
	s.revs["nixos-unstable"] = cachedRev{rev: "0123456789abcdef0123456789abcdef01234567", at: s.now()}
	f := &fakeServer{got: make(chan map[string]any, 16), stopped: make(chan struct{})}
	s.SetLSP(f.start)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/drafts/{name}/lsp", s.LSP)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv, f
}

func next(t *testing.T, f *fakeServer) map[string]any {
	t.Helper()
	select {
	case m := <-f.got:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("the server got nothing")
		return nil
	}
}

func TestLSPProxiesAndConfiguresNixd(t *testing.T) {
	_, srv, f := lspServer(t)
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/drafts/site/lsp", nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}

	// initialize reaches the server with the configuration and the
	// capability the daemon answers for.
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"capabilities": map[string]any{}}})
	init := next(t, f)
	params := init["params"].(map[string]any)
	ws := params["capabilities"].(map[string]any)["workspace"].(map[string]any)
	if ws["configuration"] != true || !strings.Contains(fmt.Sprint(params["initializationOptions"]), "github:NixOS/nixpkgs/0123456789abcdef") {
		t.Fatalf("initialize = %v", init)
	}
	// Its answer comes back as one frame of plain JSON.
	_, msg, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(msg), `"capabilities"`) || strings.Contains(string(msg), "Content-Length") {
		t.Fatalf("answer %q, %v", msg, err)
	}

	// The server's configuration question is answered by the daemon, never
	// reaching the editor.
	send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	if m := next(t, f); m["method"] != "initialized" {
		t.Fatalf("got %v", m)
	}
	// The settings note and the answer come from two goroutines, in either
	// order.
	var reply map[string]any
	noted := false
	for range 2 {
		switch m := next(t, f); {
		case m["method"] == "workspace/didChangeConfiguration":
			noted = true
		case m["id"] == "cfg":
			reply = m
		default:
			t.Fatalf("got %v", m)
		}
	}
	result, _ := reply["result"].([]any)
	if !noted || len(result) != 1 || !strings.Contains(fmt.Sprint(result[0]), "lib.nixosSystem") {
		t.Fatalf("noted %v, configuration reply = %v", noted, reply)
	}

	// Closing the editor stops the server.
	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-f.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the server was not stopped")
	}
}

func TestLSPRefusals(t *testing.T) {
	s, srv, _ := lspServer(t)
	ctx := context.Background()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	if _, res, err := websocket.Dial(ctx, url+"/api/drafts/nope/lsp", nil); err == nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown draft: %v", err)
	}
	// Another site's page may not open it.
	h := http.Header{"Origin": {"https://evil.example"}}
	if _, res, err := websocket.Dial(ctx, url+"/api/drafts/site/lsp", &websocket.DialOptions{HTTPHeader: h}); err == nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %v", err)
	}
	// Two sessions at most.
	for range lspSessions {
		s.lspSlots <- struct{}{}
	}
	_, res, err := websocket.Dial(ctx, url+"/api/drafts/site/lsp", nil)
	if err == nil || res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("busy: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "lsp_busy") {
		t.Fatalf("busy body %s", body)
	}
}

func TestNixdConfigPinsTheCommit(t *testing.T) {
	c := NixdConfig("abc")
	b, _ := json.Marshal(c)
	for _, want := range []string{`import (builtins.getFlake \"github:NixOS/nixpkgs/abc\") { system = \"x86_64-linux\"; }`, `.options`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config lacks %s: %s", want, b)
		}
	}
}
