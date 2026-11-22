package drafts

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
)

// The stack editor's Nix language server: one nixd per open editor, on the
// host, behind a WebSocket. The browser speaks plain LSP, one JSON message
// per frame; the daemon frames it for nixd's stdio and answers nixd's
// workspace/configuration itself, pointing it at the draft's nixpkgs so it
// completes and documents NixOS options and packages. nixd runs as its own
// user in a transient unit and stops when the socket closes.

// lspSessions bounds the language servers running at once: each evaluates
// NixOS options, about a gigabyte.
const lspSessions = 2

// nixdPlatform is the system a stack is built for (the build wrapper's).
const nixdPlatform = "x86_64-linux"

// lspProcess is a running language server.
type lspProcess struct {
	in   io.WriteCloser // what the editor sends, framed
	out  io.Reader      // what the server answers, framed
	stop func()
	// last is the end of what the server printed on stderr and how it
	// exited, for the log when it ends by itself.
	last func() string
}

// tailBuffer keeps the last bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// lspStarter starts a language server; id names this session.
type lspStarter func(ctx context.Context, id string) (*lspProcess, error)

// lspCache is nixd's home and fetch cache, shared by its sessions; the
// NixOS module makes it, owned by homelab-nixd.
const lspCache = "/var/cache/homelab-nixd"

// systemdNixd starts nixd in the transient unit homelab-nixd-<id>, as the
// homelab-nixd user, sandboxed, with a shared fetch cache and a memory limit.
//
// The unit runs as root and setpriv drops to homelab-nixd: with User= on the
// unit, the pipes systemd-run --pipe hands over reach nixd as end of file.
// nixd's stderr goes to /dev/null and a shell stays its parent; nixd writing
// its log into the passed pipe, or exec'd in place of the shell, ends the
// session at once (both tried on the host, 2026-11-17).
func systemdNixd(_ context.Context, id string) (*lspProcess, error) {
	var paths [3]string
	for i, name := range []string{"nixd", "setpriv", "sh"} {
		p, err := exec.LookPath(name)
		if err != nil {
			return nil, fmt.Errorf("%s is not installed on the host", name)
		}
		paths[i] = p
	}
	nixd, setpriv, sh := paths[0], paths[1], paths[2]
	unit := "homelab-nixd-" + id
	cmd := exec.Command("systemd-run", "--pipe", "--quiet", "--collect", "--unit="+unit,
		"-p", "ReadWritePaths="+lspCache, "-E", "HOME="+lspCache, "-E", "XDG_CACHE_HOME="+lspCache,
		"-p", "MemoryMax=3G", "-p", "RuntimeMaxSec=12h",
		"-p", "NoNewPrivileges=yes", "-p", "ProtectSystem=strict", "-p", "ProtectHome=yes", "-p", "PrivateTmp=yes",
		"--", setpriv, "--reuid=homelab-nixd", "--regid=homelab-nixd", "--clear-groups",
		"--", sh, "-c", "'"+nixd+"' 2>/dev/null")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &tailBuffer{} // nixd logs every request there; the end is kept
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting nixd: %w", err)
	}
	var once sync.Once
	var waitErr error
	return &lspProcess{in: in, out: out, stop: func() {
		once.Do(func() {
			_ = in.Close()
			_ = exec.Command("systemctl", "stop", unit).Run()
			waitErr = cmd.Wait()
		})
	}, last: func() string {
		return fmt.Sprintf("exit: %v; stderr: %s", waitErr, strings.TrimSpace(stderr.String()))
	}}, nil
}

// SetLSP replaces how language servers are started; for tests.
func (s *Service) SetLSP(start lspStarter) { s.lspStart = start }

// LSP serves GET /api/drafts/{name}/lsp: a WebSocket to a language server
// for the draft's flake.nix. Huma has no WebSocket, so this is a plain
// handler on the operator API.
func (s *Service) LSP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.row(r.Context(), name); err != nil {
		var p *httpx.Problem
		if !errors.As(err, &p) {
			p = httpx.Internal("draft_unreadable", "%v", err)
		}
		httpx.Write(w, p)
		return
	}
	select {
	case s.lspSlots <- struct{}{}:
		defer func() { <-s.lspSlots }()
	default:
		httpx.Write(w, httpx.Unavailable("lsp_busy", "%d language servers already run; close another editor", lspSessions))
		return
	}
	// The default origin check: only the page served from this host.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered
	}
	conn.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	config := s.nixdConfig(ctx, name)
	idb := make([]byte, 6)
	_, _ = rand.Read(idb)
	id := hex.EncodeToString(idb)
	start := s.lspStart
	if start == nil {
		start = systemdNixd
	}
	proc, err := start(ctx, id)
	if err != nil {
		s.log.Warn("language server not started", "draft", name, "error", err)
		_ = conn.Close(websocket.StatusInternalError, err.Error())
		return
	}
	defer proc.stop()
	began := time.Now()
	s.log.Info("language server started", "draft", name, "session", id)
	defer func() {
		s.log.Info("language server stopped", "draft", name, "session", id, "seconds", int(time.Since(began).Seconds()))
	}()

	var inMu sync.Mutex
	send := func(msg []byte) error {
		inMu.Lock()
		defer inMu.Unlock()
		_, err := fmt.Fprintf(proc.in, "Content-Length: %d\r\n\r\n%s", len(msg), msg)
		return err
	}

	// Server to editor; nixd's questions about its configuration are answered here.
	serverEnded := make(chan error, 1)
	go func() {
		defer cancel()
		rd := bufio.NewReader(proc.out)
		for {
			msg, err := readFrame(rd)
			if err != nil {
				serverEnded <- err
				return
			}
			if reply, ok := configReply(msg, config); ok {
				if send(reply) != nil {
					return
				}
				continue
			}
			if conn.Write(ctx, websocket.MessageText, msg) != nil {
				return
			}
		}
	}()

	// Editor to server.
	var editorErr error
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			editorErr = err
			break
		}
		msg, method := withConfig(msg, config)
		if send(msg) != nil {
			break
		}
		if method == "initialized" {
			// Some servers read their settings only when told they changed.
			note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "workspace/didChangeConfiguration",
				"params": map[string]any{"settings": map[string]any{"nixd": config}}})
			if send(note) != nil {
				break
			}
		}
	}
	select {
	case err := <-serverEnded:
		proc.stop()
		why := ""
		if proc.last != nil {
			why = proc.last()
		}
		s.log.Warn("language server ended by itself", "draft", name, "session", id, "error", err, "last", why)
		_ = conn.Close(websocket.StatusInternalError, "The language server stopped.")
		return
	default:
	}
	s.log.Debug("editor left the language server", "draft", name, "session", id, "error", editorErr)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// readFrame reads one LSP message: headers, a blank line, and the body of
// Content-Length bytes.
func readFrame(rd *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			if length, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, fmt.Errorf("bad Content-Length %q", v)
			}
		}
	}
	if length < 0 || length > 64<<20 {
		return nil, fmt.Errorf("bad Content-Length %d", length)
	}
	body := make([]byte, length)
	_, err := io.ReadFull(rd, body)
	return body, err
}

// withConfig gives the server the configuration in the editor's initialize
// request, and says the client answers workspace/configuration (the daemon
// does). It returns the message and its method.
func withConfig(msg []byte, config map[string]any) ([]byte, string) {
	var m map[string]any
	if json.Unmarshal(msg, &m) != nil {
		return msg, ""
	}
	method, _ := m["method"].(string)
	if method != "initialize" {
		return msg, method
	}
	params, _ := m["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	caps, _ := params["capabilities"].(map[string]any)
	if caps == nil {
		caps = map[string]any{}
	}
	ws, _ := caps["workspace"].(map[string]any)
	if ws == nil {
		ws = map[string]any{}
	}
	ws["configuration"] = true
	caps["workspace"] = ws
	params["capabilities"] = caps
	params["initializationOptions"] = config
	m["params"] = params
	out, err := json.Marshal(m)
	if err != nil {
		return msg, method
	}
	return out, method
}

// configReply answers a workspace/configuration request from the server:
// the configuration for the nixd section, null for any other.
func configReply(msg []byte, config map[string]any) ([]byte, bool) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		} `json:"params"`
	}
	if json.Unmarshal(msg, &m) != nil || m.Method != "workspace/configuration" || len(m.ID) == 0 {
		return nil, false
	}
	result := make([]any, len(m.Params.Items))
	for i, it := range m.Params.Items {
		if it.Section == "nixd" || it.Section == "" {
			result[i] = config
		}
	}
	out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	return out, err == nil
}

// nixdConfig points nixd at the nixpkgs the draft's saved manifest names
// (nixos-unstable when it names none), pinned to a commit so the flake
// evaluates purely. Without a commit nixd still checks syntax.
func (s *Service) nixdConfig(ctx context.Context, name string) map[string]any {
	spec := "nixos-unstable"
	if files, err := s.read(name); err == nil {
		var m struct {
			Nixpkgs string `json:"nixpkgs"`
		}
		if json.Unmarshal([]byte(files.text(stacks.ManifestFile)), &m) == nil && stacks.ValidNixpkgs(m.Nixpkgs) {
			spec = m.Nixpkgs
		}
	}
	rev, err := s.nixpkgsRev(ctx, spec)
	if err != nil {
		s.log.Warn("no nixpkgs for the language server", "draft", name, "nixpkgs", spec, "error", err)
		return map[string]any{}
	}
	return NixdConfig(rev)
}

// NixdConfig is nixd's configuration for a nixpkgs commit: packages and the
// NixOS options of a plain system, which a stack's module extends.
func NixdConfig(rev string) map[string]any {
	flake := fmt.Sprintf("builtins.getFlake \"github:NixOS/nixpkgs/%s\"", rev)
	return map[string]any{
		"nixpkgs": map[string]any{"expr": fmt.Sprintf("import (%s) { system = %q; }", flake, nixdPlatform)},
		"options": map[string]any{"nixos": map[string]any{
			"expr": fmt.Sprintf("((%s).lib.nixosSystem { system = %q; modules = [ ]; }).options", flake, nixdPlatform),
		}},
	}
}

// nixpkgsRev resolves a nixpkgs spec to a commit, remembered for an hour so
// reopening the editor does not ask GitHub again.
func (s *Service) nixpkgsRev(ctx context.Context, spec string) (string, error) {
	s.mu.Lock()
	c, ok := s.revs[spec]
	s.mu.Unlock()
	if ok && s.now().Sub(c.at) < time.Hour {
		return c.rev, nil
	}
	rev, err := stacks.ResolveNixpkgs(ctx, s.r, spec)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.revs[spec] = cachedRev{rev: rev, at: s.now()}
	s.mu.Unlock()
	return rev, nil
}

type cachedRev struct {
	rev string
	at  time.Time
}
