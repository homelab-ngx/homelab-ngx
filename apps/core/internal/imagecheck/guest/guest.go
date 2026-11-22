// Package guest reaches a machine of the image check from the host: SSH
// (over vsock or a forwarded TCP port), commands, files, the daemon's API
// through its socket, and HTTPS through Caddy, all over one SSH connection
// that is made again after the machine restarts.
package guest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Dialer opens a stream to the guest's SSH server.
type Dialer func(ctx context.Context) (net.Conn, error)

// Guest is one machine's SSH connection, made when first needed.
type Guest struct {
	dial   Dialer
	signer ssh.Signer
	// Timeout bounds a command or request that is given no deadline of its
	// own: one that hangs fails then, not the workflow at its limit.
	Timeout time.Duration

	mu     sync.Mutex
	client *ssh.Client
}

// New is a guest reached through dial, as root with the key.
func New(dial Dialer, key ssh.Signer) *Guest {
	return &Guest{dial: dial, signer: key, Timeout: 10 * time.Minute}
}

// Client is the SSH connection, made if there is none.
func (g *Guest) Client(ctx context.Context) (*ssh.Client, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.client != nil {
		return g.client, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := g.dial(ctx)
	if err != nil {
		return nil, err
	}
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(g.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // every install makes its own
		Timeout:         20 * time.Second,
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, "guest", cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	g.client = client
	go g.keepalive(client)
	return client, nil
}

// keepalive ends a connection the guest no longer answers on (a restart
// cut it), so that the next command makes a new one instead of waiting.
func (g *Guest) keepalive(c *ssh.Client) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		done := make(chan error, 1)
		go func() {
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				continue
			}
		case <-time.After(15 * time.Second):
		}
		g.drop(c)
		return
	}
}

// drop forgets the connection c, if it is still the one in use.
func (g *Guest) drop(c *ssh.Client) {
	g.mu.Lock()
	if g.client == c {
		g.client = nil
	}
	g.mu.Unlock()
	c.Close()
}

// Close ends the connection.
func (g *Guest) Close() {
	g.mu.Lock()
	c := g.client
	g.client = nil
	g.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Result is what a command did.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// ErrTransport is a command that did not run to an end: the connection
// could not be made or broke.
var ErrTransport = errors.New("no answer over SSH")

// Run runs cmd in the guest's shell with stdin. A command that ran has a
// nil error and its exit code; anything else is ErrTransport.
func (g *Guest) Run(ctx context.Context, cmd string, stdin io.Reader) (Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}
	c, err := g.Client(ctx)
	if err != nil {
		return Result{Code: -1}, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	s, err := c.NewSession()
	if err != nil {
		g.drop(c)
		return Result{Code: -1}, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	defer s.Close()
	var out, errb bytes.Buffer
	s.Stdout, s.Stderr = &out, &errb
	if stdin != nil {
		s.Stdin = stdin
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(cmd) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		s.Close()
		return Result{Stdout: out.String(), Stderr: errb.String(), Code: -1}, fmt.Errorf("%w: %s: %v", ErrTransport, short(cmd), ctx.Err())
	}
	r := Result{Stdout: out.String(), Stderr: errb.String()}
	var exit *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		r.Code = exit.ExitStatus()
	default:
		// A connection cut while the command ran (a reboot), or a command
		// ended by a signal without a status.
		g.drop(c)
		r.Code = -1
		return r, fmt.Errorf("%w: %s: %v", ErrTransport, short(cmd), err)
	}
	return r, nil
}

func short(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if len(cmd) > 80 {
		return cmd[:77] + "..."
	}
	return cmd
}

// Out runs cmd and returns its output without the last newline. A command
// that fails is an error with what it said.
func (g *Guest) Out(ctx context.Context, cmd string) (string, error) {
	r, err := g.Run(ctx, cmd, nil)
	if err != nil {
		return "", err
	}
	if r.Code != 0 {
		return strings.TrimRight(r.Stdout, "\n"), fmt.Errorf("%s: exit %d: %s", short(cmd), r.Code, strings.TrimSpace(r.Stderr))
	}
	return strings.TrimRight(r.Stdout, "\n"), nil
}

// OK says whether cmd ran and exited 0.
func (g *Guest) OK(ctx context.Context, cmd string) bool {
	r, err := g.Run(ctx, cmd, nil)
	return err == nil && r.Code == 0
}

// Probe answers yes (exit 0) or no (exit 1). Anything else, a connection
// that broke or another exit code, is not an answer, and is an error.
func (g *Guest) Probe(ctx context.Context, cmd string) (bool, error) {
	r, err := g.Run(ctx, cmd, nil)
	if err != nil {
		return false, err
	}
	switch r.Code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fmt.Errorf("%s: exit %d: %s", short(cmd), r.Code, strings.TrimSpace(r.Stderr))
}

// Put writes data to path in the guest.
func (g *Guest) Put(ctx context.Context, path string, data io.Reader) error {
	r, err := g.Run(ctx, "cat > '"+strings.ReplaceAll(path, "'", `'\''`)+"'", data)
	if err != nil {
		return err
	}
	if r.Code != 0 {
		return fmt.Errorf("write %s: exit %d: %s", path, r.Code, strings.TrimSpace(r.Stderr))
	}
	return nil
}

// Dial opens a connection from the guest's side: tcp to its own address,
// or unix to one of its sockets.
func (g *Guest) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := g.Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	conn, err := c.DialContext(ctx, network, addr)
	if err != nil {
		// A dead connection fails every channel: start again next time.
		if _, _, perr := c.SendRequest("keepalive@openssh.com", true, nil); perr != nil {
			g.drop(c)
		}
		return nil, fmt.Errorf("%w: dial %s %s: %v", ErrTransport, network, addr, err)
	}
	return conn, nil
}
