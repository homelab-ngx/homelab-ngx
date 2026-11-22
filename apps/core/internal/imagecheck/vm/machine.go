package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Boot is one start of the guest's operating system, as its systemd and
// QEMU reported it: a new one begins when QEMU starts and at every reset.
type Boot struct {
	N      int
	Ready  bool            // systemd sent READY=1: its start-up finished
	Units  map[string]bool // the units systemd reported active
	Status string          // systemd's last STATUS=
	Begun  time.Time
}

// Machine is a running QEMU and what it reported.
type Machine struct {
	Config Config
	// Log is the file QEMU's own output and the machine's events go to.
	Log string

	cmd    *exec.Cmd
	qmp    *qmp
	notify *notifyListener
	logf   *os.File

	mu      sync.Mutex
	changed chan struct{}
	boots   []*Boot
	resets  []time.Time
	downs   []string // SHUTDOWN reasons
	exited  bool
	exitErr error
	stderr  bytes.Buffer
}

// Start starts QEMU. With notify set it gives the machine a vsock device
// and listens for its systemd's messages: the CID and the listening port
// are picked at random and picked again when they are taken.
// detach leaves QEMU running when this process ends (KEEP); otherwise the
// kernel ends it with this process.
func Start(ctx context.Context, cfg Config, log string, notify, detach bool) (*Machine, error) {
	var lastErr error
	for try := 0; try < 8; try++ {
		m, err := start(ctx, cfg, log, notify, detach)
		if err == nil {
			return m, nil
		}
		lastErr = err
		if !errors.Is(err, errTaken) {
			return nil, err
		}
	}
	return nil, lastErr
}

var errTaken = errors.New("vsock address taken")

func start(ctx context.Context, cfg Config, log string, notify, detach bool) (*Machine, error) {
	creds := make(map[string][]byte, len(cfg.Credentials)+1)
	for k, v := range cfg.Credentials {
		creds[k] = v
	}
	cfg.Credentials = creds
	// Forwarded ports are picked here, free now: one that another process
	// takes before QEMU binds it is a start that is tried again.
	if cfg.SSHPort != 0 {
		cfg.SSHPort, cfg.HTTPPort = freePort(), freePort()
	}
	m := &Machine{Config: cfg, Log: log, changed: make(chan struct{})}
	if notify {
		cfg.CID = 3 + rand.Uint32N(1<<30)
		port := 10000 + rand.Uint32N(1<<20)
		l, err := listenNotify(port)
		if err != nil {
			if strings.Contains(err.Error(), "address already in use") {
				return nil, errTaken
			}
			return nil, err
		}
		m.notify = l
		cfg.Credentials["vmm.notify_socket"] = []byte(fmt.Sprintf("vsock:2:%d", port))
		m.Config = cfg
	}
	m.Config = cfg
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		m.closeNotify()
		return nil, err
	}
	m.logf = f
	_ = os.Remove(cfg.QMP)
	m.cmd = exec.Command("qemu-system-x86_64", cfg.Args()...)
	m.cmd.Stdout = f
	m.cmd.Stderr = &lockedWriter{w: f, also: &m.stderr, mu: &m.mu}
	m.cmd.SysProcAttr = procAttr(detach)
	m.event("start: qemu-system-x86_64 %s", strings.Join(cfg.Args(), " "))
	m.boots = []*Boot{{N: 1, Units: map[string]bool{}, Begun: time.Now()}}
	if err := m.cmd.Start(); err != nil {
		m.closeNotify()
		f.Close()
		return nil, fmt.Errorf("start QEMU: %w", err)
	}
	go func() {
		err := m.cmd.Wait()
		m.mu.Lock()
		m.exited, m.exitErr = true, err
		m.broadcast()
		m.mu.Unlock()
		m.event("exit: %v", err)
	}()
	if m.notify != nil {
		go m.notify.serve(m.onNotify)
	}
	q, err := dialQMP(cfg.QMP, 20*time.Second, m.onQMP)
	if err != nil {
		m.Kill()
		if m.taken() {
			return nil, errTaken
		}
		return nil, fmt.Errorf("%w (QEMU said: %s)", err, m.Stderr())
	}
	m.qmp = q
	return m, nil
}

// taken: QEMU ended because the CID was in use.
func (m *Machine) taken() bool {
	s := m.Stderr()
	return strings.Contains(s, "guest-cid") || strings.Contains(s, "vhost-vsock") && strings.Contains(s, "in use") ||
		strings.Contains(s, "host forwarding rule")
}

// freePort is a TCP port on 127.0.0.1 that is free now.
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 20000 + int(rand.Uint32N(20000))
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Stderr is what QEMU said on its error output.
func (m *Machine) Stderr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.TrimSpace(m.stderr.String())
}

type lockedWriter struct {
	w    *os.File
	also *bytes.Buffer
	mu   *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	if l.also.Len() < 64*1024 {
		l.also.Write(p)
	}
	l.mu.Unlock()
	return l.w.Write(p)
}

func (m *Machine) event(format string, args ...any) {
	if m.logf == nil {
		return
	}
	fmt.Fprintf(m.logf, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// broadcast wakes every waiter; m.mu is held.
func (m *Machine) broadcast() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *Machine) onNotify(cid uint32, msg []byte) {
	if cid != m.Config.CID {
		return
	}
	m.mu.Lock()
	b := m.boots[len(m.boots)-1]
	for _, line := range strings.Split(string(msg), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "READY":
			b.Ready = v == "1"
		case "X_SYSTEMD_UNIT_ACTIVE":
			b.Units[v] = true
		case "STATUS":
			b.Status = v
		}
	}
	m.broadcast()
	m.mu.Unlock()
	if !strings.HasPrefix(string(msg), "X_SYSTEMD_UNIT_ACTIVE=") {
		m.event("notify: %q", msg)
	}
}

func (m *Machine) onQMP(ev QMPEvent) {
	m.event("qmp: %s %s", ev.Event, ev.Data)
	m.mu.Lock()
	switch ev.Event {
	case "RESET":
		m.resets = append(m.resets, ev.At)
		m.boots = append(m.boots, &Boot{N: len(m.boots) + 1, Units: map[string]bool{}, Begun: ev.At})
	case "SHUTDOWN":
		m.downs = append(m.downs, string(ev.Data))
	}
	m.broadcast()
	m.mu.Unlock()
}

// Notifies says whether the machine's systemd reports its boots.
func (m *Machine) Notifies() bool { return m.notify != nil }

// CID is the guest's vsock address (0: none).
func (m *Machine) CID() uint32 { return m.Config.CID }

// Current is the number of the boot under way.
func (m *Machine) Current() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.boots)
}

// Resets is how many times the guest restarted.
func (m *Machine) Resets() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.resets)
}

// Alive says whether QEMU still runs.
func (m *Machine) Alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.exited
}

// Shutdowns are the reasons QEMU gave for each SHUTDOWN event.
func (m *Machine) Shutdowns() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.downs...)
}

// ErrExited is a wait that ended because QEMU did.
var ErrExited = errors.New("QEMU ended")

// waitFor waits until cond holds (called with m.mu held), QEMU ends, or
// the context ends.
func (m *Machine) waitFor(ctx context.Context, cond func() bool) error {
	for {
		m.mu.Lock()
		if cond() {
			m.mu.Unlock()
			return nil
		}
		if m.exited {
			m.mu.Unlock()
			return ErrExited
		}
		ch := m.changed
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// WaitReady waits for a boot after boot number after to finish starting
// (READY=1), and returns its number.
func (m *Machine) WaitReady(ctx context.Context, after int) (int, error) {
	n := 0
	err := m.waitFor(ctx, func() bool {
		for _, b := range m.boots {
			if b.N > after && b.Ready {
				n = b.N
				return true
			}
		}
		return false
	})
	return n, err
}

// ReadyAfter is the first boot after boot number after that finished
// starting, if one has.
func (m *Machine) ReadyAfter(after int) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.boots {
		if b.N > after && b.Ready {
			return b.N, true
		}
	}
	return 0, false
}

// Note writes a line to the machine's log.
func (m *Machine) Note(format string, args ...any) { m.event(format, args...) }

// WaitUnit waits for systemd to report unit active in boot n.
func (m *Machine) WaitUnit(ctx context.Context, n int, unit string) error {
	return m.waitFor(ctx, func() bool {
		return n >= 1 && n <= len(m.boots) && m.boots[n-1].Units[unit]
	})
}

// WaitReset waits for the guest to restart after boot number after began.
func (m *Machine) WaitReset(ctx context.Context, after int) error {
	return m.waitFor(ctx, func() bool { return len(m.boots) > after })
}

// WaitExit waits for QEMU to end.
func (m *Machine) WaitExit(ctx context.Context) error {
	err := m.waitFor(ctx, func() bool { return false })
	if errors.Is(err, ErrExited) {
		return nil
	}
	return err
}

// Status is what the boot under way last said, for a wait's message.
func (m *Machine) Status() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.boots[len(m.boots)-1]
	return fmt.Sprintf("boot %d, ready %v, status %q", b.N, b.Ready, b.Status)
}

// Powerdown presses the machine's power button.
func (m *Machine) Powerdown() error {
	if m.qmp == nil {
		return errors.New("no QMP")
	}
	_, err := m.qmp.Execute("system_powerdown", nil)
	return err
}

// Stop turns the machine off: the power button, then QEMU's end after
// grace, then a kill.
func (m *Machine) Stop(grace time.Duration) {
	if !m.Alive() {
		m.close()
		return
	}
	_ = m.Powerdown()
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if m.WaitExit(ctx) != nil {
		m.Kill()
	}
	m.close()
}

// Kill ends QEMU at once.
func (m *Machine) Kill() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.WaitExit(ctx)
	}
	m.close()
}

// Detach leaves QEMU running and lets go of it (KEEP).
func (m *Machine) Detach() {
	if m.qmp != nil {
		m.qmp.Close()
		m.qmp = nil
	}
}

// Pid is QEMU's process id.
func (m *Machine) Pid() int {
	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}

func (m *Machine) closeNotify() {
	if m.notify != nil {
		m.notify.Close()
		m.notify = nil
	}
}

func (m *Machine) close() {
	if m.qmp != nil {
		m.qmp.Close()
		m.qmp = nil
	}
	m.closeNotify()
}
