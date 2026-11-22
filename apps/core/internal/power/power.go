// Package power reboots and powers off the host. Before the host goes down
// the daemon stops the running stacks, then unmounts the pools and
// partitions, so nothing is cut off mid-write. It does so for every shutdown
// that goes through systemd-logind (this API, `shutdown -r`, `systemctl
// reboot`, the power button): it holds a delay lock, and logind waits for
// it. Nothing is recorded: the stacks still want to run and the pools still
// mount at boot, so the next boot brings back what ran.
package power

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

// Stacks is the stacks the sequence stops (*stacks.Service).
type Stacks interface {
	// BeginShutdown refuses new operations and boots; CancelShutdown allows them again.
	BeginShutdown()
	CancelShutdown()
	// Running names the stacks whose container is up.
	Running(ctx context.Context) ([]string, error)
	// Halt stops a container without recording a stop. A frozen one is
	// thawed first, so it stops cleanly.
	Halt(ctx context.Context, name string) error
	// Frozen names the frozen stacks: a shutdown loses what they hold.
	Frozen(ctx context.Context) []string
	// Kill ends a frozen stack's processes at once.
	Kill(ctx context.Context, name string) error
}

// Mounts is the file systems the sequence unmounts (*storage.Service).
type Mounts interface {
	// Mounted lists the daemon's mounts, the deepest first.
	Mounted() []string
	// Release unmounts one without recording it.
	Release(ctx context.Context, mountpoint string) error
	// Restore mounts again what is set to mount at boot.
	Restore(ctx context.Context)
}

// Meta keeps the time a shutdown started, to measure the next reboot
// (*store.Store).
type Meta interface {
	Meta(ctx context.Context, key string) (string, bool, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Meta keys.
const (
	metaLast       = "power_last"           // JSON lastShutdown, written as the host goes down
	metaRebootSecs = "power_reboot_seconds" // how long the last reboot took
)

// defaultReboot is the countdown before a reboot was ever measured.
const defaultReboot = 120 * time.Second

// lastShutdown is written as the host goes down; the next start measures
// the reboot from it.
type lastShutdown struct {
	Action PowerAction `json:"action"`
	At     time.Time   `json:"at"`
	BootID string      `json:"boot_id"`
}

// Service runs the power actions.
type Service struct {
	stacks Stacks
	mounts Mounts
	logind Logind
	meta   Meta
	r      cmdrunner.Runner
	busy   []func(context.Context) []string
	log    *slog.Logger
	now    func() time.Time
	bootID string

	mu       sync.Mutex
	st       PowerStatus   // Steps, Phase, Action, StartedAt, Error
	running  bool          // a sequence is under way or done
	seqDone  chan struct{} // closed when the sequence under way has finished
	release  func()        // the delay lock; nil when not held
	expected time.Duration
	ctx      context.Context // the daemon's lifetime, for the sequence
}

// New makes the service. busy lists the work a reboot would interrupt.
func New(stacks Stacks, mounts Mounts, logind Logind, meta Meta, r cmdrunner.Runner, log *slog.Logger, busy ...func(context.Context) []string) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		stacks: stacks, mounts: mounts, logind: logind, meta: meta, r: r, busy: busy, log: log,
		now: time.Now, bootID: readBootID(), st: PowerStatus{Phase: PhaseIdle, Steps: []PowerStep{}},
		expected: defaultReboot, ctx: context.Background(),
	}
}

func readBootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// Run measures the reboot that led here, takes the delay lock and answers
// logind's shutdowns until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	s.measure(ctx)
	s.inhibit(ctx)
	for ctx.Err() == nil {
		if err := s.logind.Prepare(ctx, func(start bool, a PowerAction) { s.prepare(ctx, start, a) }); err != nil {
			s.log.Warn("not told of shutdowns; a shutdown may cut the stacks off", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
	s.mu.Lock()
	if s.release != nil && !s.running {
		s.release()
		s.release = nil
	}
	s.mu.Unlock()
}

// measure saves how long the reboot that led to this boot took.
func (s *Service) measure(ctx context.Context) {
	v, ok, err := s.meta.Meta(ctx, metaLast)
	if err == nil && ok && v != "" {
		var last lastShutdown
		if json.Unmarshal([]byte(v), &last) == nil && last.Action == Reboot && last.BootID != s.bootID {
			if d := s.now().Sub(last.At); d > 0 && d < time.Hour {
				_ = s.meta.SetMeta(ctx, metaRebootSecs, fmt.Sprint(int(d.Seconds())))
			}
		}
		_ = s.meta.SetMeta(ctx, metaLast, "")
	}
	if v, ok, err := s.meta.Meta(ctx, metaRebootSecs); err == nil && ok {
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil && n > 0 {
			s.mu.Lock()
			s.expected = time.Duration(n) * time.Second
			s.mu.Unlock()
		}
	}
}

func (s *Service) inhibit(ctx context.Context) {
	rel, err := s.logind.Inhibit(ctx)
	if err != nil {
		s.log.Warn("no shutdown lock; only a reboot from the dashboard or CLI stops the stacks first", "error", err)
		return
	}
	s.mu.Lock()
	s.release = rel
	s.mu.Unlock()
}

// prepare answers logind: a shutdown starts (run the sequence, then let it
// go on) or was called off (undo it).
func (s *Service) prepare(ctx context.Context, start bool, a PowerAction) {
	if !start {
		s.callOff(ctx)
		return
	}
	s.mu.Lock()
	already, done := s.running, s.seqDone
	s.running = true
	s.mu.Unlock()
	if !already {
		s.log.Warn("the host is shutting down", "action", a)
		// Nobody can be asked, so frozen stacks are thawed and stop
		// cleanly.
		s.sequence(ctx, a, false)
	} else if done != nil {
		// One the API started: the lock goes once it has finished.
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	s.mu.Lock()
	rel := s.release
	s.release = nil
	s.mu.Unlock()
	if rel != nil {
		rel()
	}
}

// callOff undoes a sequence for a shutdown that will not happen: the pools
// mount again and the stacks may start.
func (s *Service) callOff(ctx context.Context) {
	s.mu.Lock()
	was := s.running
	s.running, s.seqDone = false, nil
	s.st = PowerStatus{Phase: PhaseIdle, Steps: []PowerStep{}}
	s.mu.Unlock()
	if was {
		s.log.Warn("the shutdown was called off; mounting the pools and starting the stacks again")
		s.mounts.Restore(ctx)
		s.stacks.CancelShutdown()
	}
	s.mu.Lock()
	held := s.release != nil
	s.mu.Unlock()
	if !held {
		s.inhibit(ctx)
	}
}

// Request starts a reboot or poweroff. Work it would interrupt refuses it
// unless force. A frozen stack refuses it unless thaw (it is thawed and
// stops cleanly) or force (it is killed, and what it held is lost). The
// answer comes at once; the sequence runs on.
func (s *Service) Request(ctx context.Context, a PowerAction, force, thaw bool) error {
	if a != Reboot && a != PowerOff {
		return httpx.BadRequest("invalid_action", "action must be reboot or poweroff")
	}
	if !force {
		if busy := s.Busy(ctx); len(busy) > 0 {
			return httpx.Conflict("busy", "%s would be interrupted; force to %s anyway", strings.Join(busy, ", "), a)
		}
	}
	if !force && !thaw {
		if frozen := s.stacks.Frozen(ctx); len(frozen) > 0 {
			return httpx.Conflict("stacks_frozen", "frozen stacks lose what they hold: %s; thaw them first, or force to %s without thawing", strings.Join(frozen, ", "), a)
		}
	}
	kill := force && !thaw
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return httpx.Conflict("shutting_down", "the host is already shutting down")
	}
	s.running = true
	done := make(chan struct{})
	s.seqDone = done
	bg := s.ctx
	s.mu.Unlock()
	s.log.Warn("shutdown requested", "action", a, "force", force, "thaw", thaw)
	go func() {
		// The sequence runs here, before logind is asked: it holds even
		// when the delay lock could not be taken. When logind then
		// signals the shutdown, the lock goes at once.
		ok := s.sequence(bg, a, kill)
		close(done)
		if !ok {
			return
		}
		if err := s.logind.Shutdown(bg, a); err != nil {
			s.log.Warn("logind did not take the shutdown; asking systemctl", "error", err)
			if _, err := s.r.Run(bg, cmdrunner.Cmd{Module: "power", Name: "systemctl", Args: []string{string(a)}}); err != nil {
				s.fail(fmt.Sprintf("the %s did not start: %v. The stacks are stopped and the pools unmounted; %s the host by hand", a, err, a))
			}
		}
	}()
	return nil
}

// Busy lists the work a shutdown would interrupt now.
func (s *Service) Busy(ctx context.Context) []string {
	out := []string{}
	for _, b := range s.busy {
		out = append(out, b(ctx)...)
	}
	return out
}

// sequence stops the stacks, then unmounts the pools and partitions, and
// records the time for the next boot's countdown. Frozen stacks are killed
// when kill, and otherwise thawed by their halt. It reports whether the host
// may go down: always, unless ctx ended.
func (s *Service) sequence(ctx context.Context, a PowerAction, kill bool) bool {
	s.stacks.BeginShutdown()
	now := s.now()
	s.mu.Lock()
	s.st = PowerStatus{Action: a, Phase: PhaseStacks, StartedAt: &now, Steps: []PowerStep{}}
	s.mu.Unlock()

	names, err := s.stacks.Running(ctx)
	if err != nil {
		s.log.Warn("cannot list the running stacks", "error", err)
	}
	for _, n := range names {
		s.add(PowerStep{Kind: StepStack, Name: n, State: StateWorking})
	}
	killed := map[string]bool{}
	if kill {
		for _, n := range s.stacks.Frozen(ctx) {
			if err := s.stacks.Kill(ctx, n); err != nil {
				s.log.Warn("frozen stack not killed; its halt thaws it", "stack", n, "error", err)
				continue
			}
			killed[n] = true
		}
	}
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.stacks.Halt(ctx, n)
			switch {
			case killed[n]:
				s.set(StepStack, n, StateFailed, "frozen; killed without thawing")
			case err != nil:
				s.log.Warn("stack did not stop cleanly", "stack", n, "error", err)
				s.set(StepStack, n, StateFailed, err.Error())
			default:
				s.set(StepStack, n, StateDone, "")
			}
		}()
	}
	wg.Wait()

	s.phase(PhaseMounts)
	syscall.Sync()
	mounts := s.mounts.Mounted()
	for _, m := range mounts {
		s.add(PowerStep{Kind: StepMount, Name: m, State: StateWaiting})
	}
	for _, m := range mounts {
		s.set(StepMount, m, StateWorking, "")
		if err := s.mounts.Release(ctx, m); err != nil {
			s.log.Warn("not unmounted; systemd unmounts it last", "mountpoint", m, "error", err)
			s.set(StepMount, m, StateFailed, "still in use; it is unmounted last, as the host goes down")
			continue
		}
		s.set(StepMount, m, StateDone, "")
	}
	syscall.Sync()

	if b, err := json.Marshal(lastShutdown{Action: a, At: s.now(), BootID: s.bootID}); err == nil {
		if err := s.meta.SetMeta(ctx, metaLast, string(b)); err != nil {
			s.log.Warn("the reboot will not be measured", "error", err)
		}
	}
	s.phase(PhaseDown)
	return ctx.Err() == nil
}

func (s *Service) add(st PowerStep) {
	s.mu.Lock()
	s.st.Steps = append(s.st.Steps, st)
	s.mu.Unlock()
}

func (s *Service) set(kind PowerStepKind, name string, state PowerStepState, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Steps {
		if s.st.Steps[i].Kind == kind && s.st.Steps[i].Name == name {
			s.st.Steps[i].State, s.st.Steps[i].Detail = state, detail
		}
	}
}

func (s *Service) phase(p PowerPhase) {
	s.mu.Lock()
	s.st.Phase = p
	s.mu.Unlock()
}

func (s *Service) fail(msg string) {
	s.log.Error("shutdown failed", "error", msg)
	s.mu.Lock()
	s.st.Phase, s.st.Error = PhaseFailed, msg
	s.mu.Unlock()
}

// Status is the shutdown under way, if any, with what the countdown needs.
func (s *Service) Status(ctx context.Context) PowerStatus {
	s.mu.Lock()
	st := s.st
	st.Steps = append([]PowerStep{}, s.st.Steps...)
	st.ExpectedSeconds = int(s.expected.Seconds())
	s.mu.Unlock()
	st.BootID = s.bootID
	if sch, err := s.logind.Scheduled(ctx); err == nil {
		st.Scheduled = sch
	}
	if st.Phase == PhaseIdle {
		st.Busy = s.Busy(ctx)
		st.Frozen = s.stacks.Frozen(ctx)
	}
	if st.Busy == nil {
		st.Busy = []string{}
	}
	if st.Frozen == nil {
		st.Frozen = []string{}
	}
	return st
}

// CancelScheduled calls off the shutdown `shutdown -r HH:MM` planned.
func (s *Service) CancelScheduled(ctx context.Context) error {
	sch, err := s.logind.Scheduled(ctx)
	if err != nil {
		return err
	}
	if sch == nil {
		return httpx.NotFound("not_scheduled", "no reboot or poweroff is scheduled")
	}
	return s.logind.CancelScheduled(ctx)
}
