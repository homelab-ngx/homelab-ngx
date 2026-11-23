package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"homelab-ngx.com/core/internal/httpx"
)

// A stack shell is a root login shell in a running stack's container, for
// the operator: `homelab stacks shell` on the host, and the dashboard's
// shell window over a WebSocket. Root in the container is an unprivileged
// uid range on the host (--private-users=pick), but the endpoint runs in the
// root daemon, so it is on the operator API only and never on the stack
// listener.
//
// The terminal comes from systemd-machined (OpenMachineShell), as it does
// for `machinectl shell`: machined starts the shell as a unit inside the
// container and hands back the pseudo-terminal's master end. The daemon
// copies bytes between it and the socket and needs no PTY of its own.

// shellSessions bounds the shells open at once from the API.
const shellSessions = 4

// Terminal is an open shell's terminal: what is read is the shell's output,
// what is written its input. Closing it hangs the shell up.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// ShellOpener opens a login shell for root in the named machine.
type ShellOpener func(ctx context.Context, machine string) (Terminal, error)

// SetShell replaces how a shell is opened; a seam for tests.
func (s *Service) SetShell(open ShellOpener) { s.openShell = open }

// shellFrame is a text frame from the client. The only one is a resize.
type shellFrame struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// ShellReady checks that a shell can open in the stack: it exists, it runs,
// and it is not frozen (a call into a frozen container waits until it times
// out). The CLI asks before it runs machinectl.
func (s *Service) ShellReady(ctx context.Context, name string) error {
	if _, err := s.row(ctx, name); err != nil {
		return err
	}
	if !s.isRunning(ctx, name) {
		return httpx.Conflict("stack_stopped", "stack %q is not running; start the stack first", name)
	}
	if s.frozen(ctx, name) {
		return stackFrozen(name)
	}
	return nil
}

// Shell serves GET /api/stacks/{name}/shell: a WebSocket to a root shell in
// the stack. Binary frames carry the terminal's bytes both ways; a text
// frame from the client is a shellFrame. Huma has no WebSocket, so this is
// a plain handler on the operator API.
func (s *Service) Shell(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.ShellReady(r.Context(), name); err != nil {
		var p *httpx.Problem
		if !errors.As(err, &p) {
			p = httpx.Internal("stack_unreadable", "%v", err)
		}
		httpx.Write(w, p)
		return
	}
	select {
	case s.shellSlots <- struct{}{}:
		defer func() { <-s.shellSlots }()
	default:
		httpx.Write(w, httpx.Unavailable("shell_busy", "%d shells are already open; close another", shellSessions))
		return
	}
	// The default origin check: only the page served from this host.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	open := s.openShell
	if open == nil {
		open = machinedShell
	}
	term, err := open(ctx, name)
	if err != nil {
		s.log.Warn("stack shell not opened", "stack", name, "error", err)
		_ = conn.Close(websocket.StatusInternalError, "The shell did not open.")
		return
	}
	defer term.Close()
	began := time.Now()
	s.log.Info("stack shell opened", "stack", name)
	defer func() {
		s.log.Info("stack shell closed", "stack", name, "seconds", int(time.Since(began).Seconds()))
	}()

	// Terminal to client. A read error is the shell's end: it exited, or
	// the container stopped. The close frame ends the read loop below.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "The shell ended.")
				return
			}
		}
	}()

	// Client to terminal, until either side closes.
	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ == websocket.MessageBinary {
			if _, err := term.Write(msg); err != nil {
				break
			}
			continue
		}
		var f shellFrame
		if json.Unmarshal(msg, &f) != nil || f.Type != "resize" || f.Cols == 0 || f.Rows == 0 {
			continue
		}
		if err := term.Resize(f.Cols, f.Rows); err != nil {
			s.log.Debug("stack shell not resized", "stack", name, "error", err)
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

const (
	machineBus  = "org.freedesktop.machine1"
	machinePath = dbus.ObjectPath("/org/freedesktop/machine1")
	machineMgr  = "org.freedesktop.machine1.Manager"
)

// machinedShell asks systemd-machined for root's login shell in the
// machine. An empty path makes machined pick the user's own shell, as
// `machinectl shell` does.
func machinedShell(ctx context.Context, machine string) (Terminal, error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	defer conn.Close()
	var (
		fd   dbus.UnixFD
		path string
	)
	err = conn.Object(machineBus, machinePath).CallWithContext(ctx, machineMgr+".OpenMachineShell", 0,
		machine, "root", "", []string{}, []string{"TERM=xterm-256color"}).Store(&fd, &path)
	if err != nil {
		return nil, fmt.Errorf("machined OpenMachineShell: %w", err)
	}
	// Non-blocking, so Close ends a Read waiting in another goroutine.
	if err := unix.SetNonblock(int(fd), true); err != nil {
		_ = unix.Close(int(fd))
		return nil, fmt.Errorf("pty: %w", err)
	}
	return ptyMaster{os.NewFile(uintptr(fd), path)}, nil
}

// ptyMaster is the master end of a pseudo-terminal.
type ptyMaster struct{ *os.File }

func (p ptyMaster) Read(b []byte) (int, error) {
	n, err := p.File.Read(b)
	// Linux answers EIO once the terminal's last user has gone.
	if errors.Is(err, unix.EIO) {
		err = io.EOF
	}
	return n, err
}

func (p ptyMaster) Resize(cols, rows uint16) error {
	rc, err := p.SyscallConn()
	if err != nil {
		return err
	}
	var ioErr error
	err = rc.Control(func(fd uintptr) {
		ioErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	})
	if err != nil {
		return err
	}
	return ioErr
}
