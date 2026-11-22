package power

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/httpx"
)

// journal is the order things happened in, across the fakes.
type journal struct {
	mu  sync.Mutex
	got []string
}

func (j *journal) add(s string) {
	j.mu.Lock()
	j.got = append(j.got, s)
	j.mu.Unlock()
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string{}, j.got...)
}

type fakeStacks struct {
	j       *journal
	running []string
	failing string
	gate    chan struct{} // when set, a halt waits for it
	frozen  []string
}

func (f *fakeStacks) BeginShutdown()  { f.j.add("freeze") }
func (f *fakeStacks) CancelShutdown() { f.j.add("thaw") }
func (f *fakeStacks) Running(context.Context) ([]string, error) {
	return f.running, nil
}
func (f *fakeStacks) Frozen(context.Context) []string { return f.frozen }
func (f *fakeStacks) Kill(_ context.Context, name string) error {
	f.j.add("kill " + name)
	return nil
}
func (f *fakeStacks) Halt(_ context.Context, name string) error {
	if f.gate != nil {
		<-f.gate
	}
	f.j.add("halt " + name)
	if name == f.failing {
		return errors.New("killed after 120s")
	}
	return nil
}

type fakeMounts struct {
	j    *journal
	mps  []string
	busy string
}

func (f *fakeMounts) Mounted() []string { return f.mps }
func (f *fakeMounts) Release(_ context.Context, mp string) error {
	f.j.add("unmount " + mp)
	if mp == f.busy {
		return errors.New("target is busy")
	}
	return nil
}
func (f *fakeMounts) Restore(context.Context) { f.j.add("restore") }

type fakeLogind struct {
	j        *journal
	down     chan PowerAction
	failDown bool
	sched    *PowerScheduled
}

func (f *fakeLogind) Inhibit(context.Context) (func(), error) {
	f.j.add("inhibit")
	return func() { f.j.add("release lock") }, nil
}
func (f *fakeLogind) Prepare(ctx context.Context, _ func(bool, PowerAction)) error {
	<-ctx.Done()
	return nil
}
func (f *fakeLogind) Shutdown(_ context.Context, a PowerAction) error {
	f.j.add("logind " + string(a))
	if f.failDown {
		return errors.New("no logind")
	}
	f.down <- a
	return nil
}
func (f *fakeLogind) Scheduled(context.Context) (*PowerScheduled, error) { return f.sched, nil }
func (f *fakeLogind) CancelScheduled(context.Context) error {
	f.j.add("cancel")
	f.sched = nil
	return nil
}

type memMeta struct {
	mu sync.Mutex
	m  map[string]string
}

func (m *memMeta) Meta(_ context.Context, k string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[k]
	return v, ok, nil
}
func (m *memMeta) SetMeta(_ context.Context, k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[k] = v
	return nil
}

type rig struct {
	j      *journal
	stacks *fakeStacks
	mounts *fakeMounts
	logind *fakeLogind
	meta   *memMeta
	run    *cmdrunnertest.Fake
	svc    *Service
	busy   []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	j := &journal{}
	r := &rig{
		j:      j,
		stacks: &fakeStacks{j: j, running: []string{"immich", "players"}},
		mounts: &fakeMounts{j: j, mps: []string{"/var/mnt/media/photos", "/var/mnt/media"}},
		logind: &fakeLogind{j: j, down: make(chan PowerAction, 1)},
		meta:   &memMeta{m: map[string]string{}},
		run:    cmdrunnertest.NewFake(),
	}
	r.svc = New(r.stacks, r.mounts, r.logind, r.meta, r.run, nil, func(context.Context) []string { return r.busy })
	r.svc.bootID = "boot-1"
	// Tests that read the status before a shutdown do not wait long for a
	// page to see it ready; the tests of that wait set their own.
	r.svc.lingerMax = 50 * time.Millisecond
	return r
}

// phase is the phase now, read without counting as a page's read.
func (r *rig) phase() PowerPhase {
	r.svc.mu.Lock()
	defer r.svc.mu.Unlock()
	return r.svc.st.Phase
}

func (r *rig) wait(t *testing.T) PowerAction {
	t.Helper()
	select {
	case a := <-r.logind.down:
		return a
	case <-time.After(5 * time.Second):
		t.Fatalf("no shutdown asked of logind; journal %v", r.j.list())
		return ""
	}
}

func TestRequestStopsStacksThenUnmountsThenShutsDown(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Request(context.Background(), Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	if a := r.wait(t); a != Reboot {
		t.Fatalf("asked logind for %s", a)
	}
	got := r.j.list()
	// The stacks halt in parallel: their order is free, but both come
	// before any unmount, and the deepest mount goes first.
	if got[0] != "freeze" || !slices.Contains(got[1:3], "halt immich") || !slices.Contains(got[1:3], "halt players") {
		t.Fatalf("journal %v", got)
	}
	want := []string{"unmount /var/mnt/media/photos", "unmount /var/mnt/media", "logind reboot"}
	if !slices.Equal(got[3:], want) {
		t.Fatalf("journal %v, want ... %v", got, want)
	}
	st := r.svc.Status(context.Background())
	if st.Phase != PhaseDown || st.Action != Reboot || len(st.Steps) != 4 {
		t.Fatalf("status %+v", st)
	}
	for _, s := range st.Steps {
		if s.State != StateDone {
			t.Errorf("step %+v", s)
		}
	}
	var last lastShutdown
	if err := json.Unmarshal([]byte(r.meta.m[metaLast]), &last); err != nil || last.Action != Reboot || last.BootID != "boot-1" {
		t.Fatalf("last shutdown %q: %v", r.meta.m[metaLast], err)
	}
}

func TestBusyWorkRefusesUnlessForced(t *testing.T) {
	r := newRig(t)
	r.busy = []string{"stack immich: update (building)"}
	err := r.svc.Request(context.Background(), PowerOff, false, false)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != 409 || p.Code != "busy" || !strings.Contains(p.Detail, "immich") {
		t.Fatalf("err = %v", err)
	}
	if got := r.j.list(); len(got) != 0 {
		t.Fatalf("a refused request did something: %v", got)
	}
	if st := r.svc.Status(context.Background()); st.Phase != PhaseIdle || len(st.Busy) != 1 {
		t.Fatalf("status %+v", st)
	}
	if err := r.svc.Request(context.Background(), PowerOff, true, false); err != nil {
		t.Fatal(err)
	}
	if a := r.wait(t); a != PowerOff {
		t.Fatalf("asked logind for %s", a)
	}
	// A second request while one runs is refused.
	if err := r.svc.Request(context.Background(), Reboot, true, false); !errors.As(err, &p) || p.Code != "shutting_down" {
		t.Fatalf("second request: %v", err)
	}
}

func TestFrozenStacksRefuseUnlessThawedOrForced(t *testing.T) {
	r := newRig(t)
	r.stacks.running = []string{"immich", "players"}
	r.stacks.frozen = []string{"immich"}
	err := r.svc.Request(context.Background(), Reboot, false, false)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != 409 || p.Code != "stacks_frozen" || !strings.Contains(p.Detail, "immich") {
		t.Fatalf("err = %v", err)
	}
	if got := r.j.list(); len(got) != 0 {
		t.Fatalf("a refused request did something: %v", got)
	}
	if st := r.svc.Status(context.Background()); len(st.Frozen) != 1 || st.Frozen[0] != "immich" {
		t.Fatalf("status frozen %v", st.Frozen)
	}
	// thaw: nothing is killed; the halt thaws the stack.
	if err := r.svc.Request(context.Background(), Reboot, false, true); err != nil {
		t.Fatal(err)
	}
	r.wait(t)
	for _, e := range r.j.list() {
		if strings.HasPrefix(e, "kill") {
			t.Fatalf("thaw killed a stack: %v", r.j.list())
		}
	}
	for _, st := range r.svc.Status(context.Background()).Steps {
		if st.Kind == StepStack && st.State != StateDone {
			t.Fatalf("step %+v", st)
		}
	}
}

func TestForceKillsFrozenStacks(t *testing.T) {
	r := newRig(t)
	r.stacks.running = []string{"immich", "players"}
	r.stacks.frozen = []string{"immich"}
	if err := r.svc.Request(context.Background(), PowerOff, true, false); err != nil {
		t.Fatal(err)
	}
	r.wait(t)
	got := strings.Join(r.j.list(), ",")
	if !strings.Contains(got, "kill immich") || strings.Contains(got, "kill players") ||
		strings.Index(got, "kill immich") > strings.Index(got, "halt immich") {
		t.Fatalf("journal %s", got)
	}
	states := map[string]PowerStepState{}
	for _, st := range r.svc.Status(context.Background()).Steps {
		states[st.Name] = st.State
	}
	if states["immich"] != StateFailed || states["players"] != StateDone {
		t.Fatalf("states %v", states)
	}
}

func TestFailuresAreShownAndTheShutdownGoesOn(t *testing.T) {
	r := newRig(t)
	r.stacks.failing = "players"
	r.mounts.busy = "/var/mnt/media"
	if err := r.svc.Request(context.Background(), Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	r.wait(t)
	st := r.svc.Status(context.Background())
	states := map[string]PowerStepState{}
	for _, s := range st.Steps {
		states[s.Name] = s.State
		if s.State == StateFailed && s.Detail == "" {
			t.Errorf("failed step without a reason: %+v", s)
		}
	}
	if states["immich"] != StateDone || states["players"] != StateFailed ||
		states["/var/mnt/media/photos"] != StateDone || states["/var/mnt/media"] != StateFailed {
		t.Fatalf("steps %+v", st.Steps)
	}
}

func TestSystemctlWhenLogindRefuses(t *testing.T) {
	r := newRig(t)
	r.logind.failDown = true
	done := make(chan struct{})
	r.run.On("systemctl", "reboot").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		close(done)
		return cmdrunner.Result{}, nil
	})
	if err := r.svc.Request(context.Background(), Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("systemctl reboot not run; journal %v", r.j.list())
	}
}

func TestLogindShutdownRunsTheSequenceBeforeTheLockGoes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.svc.inhibit(ctx)
	r.svc.prepare(ctx, true, PowerOff)
	got := r.j.list()
	if got[0] != "inhibit" || got[len(got)-1] != "release lock" || !slices.Contains(got, "unmount /var/mnt/media") {
		t.Fatalf("journal %v", got)
	}
	if slices.Index(got, "unmount /var/mnt/media") > slices.Index(got, "release lock") {
		t.Fatalf("the lock went before the unmount: %v", got)
	}
	// Called off: the pools mount again, the stacks may start, and the
	// lock is taken again for the next one.
	r.svc.prepare(ctx, false, PowerOff)
	tail := r.j.list()[len(got):]
	if !slices.Equal(tail, []string{"restore", "thaw", "inhibit"}) {
		t.Fatalf("after calling off: %v", tail)
	}
	if st := r.svc.Status(ctx); st.Phase != PhaseIdle {
		t.Fatalf("status %+v", st)
	}
}

func TestARequestedShutdownLetsTheLockGoAtOnce(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.svc.inhibit(ctx)
	if err := r.svc.Request(ctx, Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	r.wait(t)
	before := len(r.j.list())
	r.svc.prepare(ctx, true, Reboot)
	if tail := r.j.list()[before:]; !slices.Equal(tail, []string{"release lock"}) {
		t.Fatalf("logind's signal after a request ran %v", tail)
	}
}

func TestTheRebootIsMeasured(t *testing.T) {
	r := newRig(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	r.svc.now = func() time.Time { return now }
	b, _ := json.Marshal(lastShutdown{Action: Reboot, At: now.Add(-95 * time.Second), BootID: "boot-0"})
	r.meta.m[metaLast] = string(b)
	r.svc.measure(context.Background())
	if st := r.svc.Status(context.Background()); st.ExpectedSeconds != 95 {
		t.Fatalf("expected %d s", st.ExpectedSeconds)
	}
	if r.meta.m[metaLast] != "" {
		t.Fatal("the shutdown record was kept")
	}
	// A poweroff's time off is not a reboot's length.
	b, _ = json.Marshal(lastShutdown{Action: PowerOff, At: now.Add(-50 * time.Minute), BootID: "boot-0"})
	r.meta.m[metaLast] = string(b)
	r.svc.measure(context.Background())
	if st := r.svc.Status(context.Background()); st.ExpectedSeconds != 95 {
		t.Fatalf("expected %d s after a poweroff", st.ExpectedSeconds)
	}
}

func TestBeforeAnyRebootTheCountdownIsTheDefault(t *testing.T) {
	r := newRig(t)
	r.svc.measure(context.Background())
	if st := r.svc.Status(context.Background()); st.ExpectedSeconds != 120 || st.BootID != "boot-1" {
		t.Fatalf("status %+v", st)
	}
}

func TestCancelScheduled(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	var p *httpx.Problem
	if err := r.svc.CancelScheduled(ctx); !errors.As(err, &p) || p.Status != 404 {
		t.Fatalf("nothing scheduled: %v", err)
	}
	r.logind.sched = &PowerScheduled{Action: Reboot, At: time.Now().Add(time.Hour)}
	if st := r.svc.Status(ctx); st.Scheduled == nil || st.Scheduled.Action != Reboot {
		t.Fatalf("status %+v", st)
	}
	if err := r.svc.CancelScheduled(ctx); err != nil {
		t.Fatal(err)
	}
	if st := r.svc.Status(ctx); st.Scheduled != nil {
		t.Fatalf("still scheduled: %+v", st.Scheduled)
	}
}

func TestActionOfLogindTypes(t *testing.T) {
	for in, want := range map[string]PowerAction{
		"reboot": Reboot, "soft-reboot": Reboot, "kexec": Reboot,
		"power-off": PowerOff, "poweroff": PowerOff, "halt": PowerOff,
	} {
		if got := actionOf(in); got != want {
			t.Errorf("actionOf(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestTheLockWaitsForASequenceTheAPIStarted(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.svc.inhibit(ctx)
	gate := make(chan struct{})
	r.stacks.gate = gate
	if err := r.svc.Request(ctx, Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		// The power button, while the API's sequence still stops the stacks.
		r.svc.prepare(ctx, true, Reboot)
		close(released)
	}()
	select {
	case <-released:
		t.Fatal("the lock went while the stacks were still stopping")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	r.wait(t)
	<-released
	got := r.j.list()
	if slices.Index(got, "release lock") < slices.Index(got, "unmount /var/mnt/media") {
		t.Fatalf("journal %v", got)
	}
}

func TestTheHostWaitsUntilAWatchingPageSeesItReady(t *testing.T) {
	r := newRig(t)
	r.svc.lingerMax = time.Minute
	ctx := context.Background()
	r.svc.Status(ctx) // a page follows the shutdown
	if err := r.svc.Request(ctx, Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.phase() != PhaseDown {
		if time.Now().After(deadline) {
			t.Fatalf("never ready; journal %v", r.j.list())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-r.logind.down:
		t.Fatal("handed to logind before a page saw the host ready")
	case <-time.After(100 * time.Millisecond):
	}
	if st := r.svc.Status(ctx); st.Phase != PhaseDown {
		t.Fatalf("phase %s", st.Phase)
	}
	r.wait(t)
}

func TestNoPageNoWait(t *testing.T) {
	r := newRig(t)
	r.svc.lingerMax = time.Minute
	start := time.Now()
	if err := r.svc.Request(context.Background(), Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	r.wait(t)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %s with no page reading", d)
	}
}

func TestTheWaitForAPageHasALimit(t *testing.T) {
	r := newRig(t)
	r.svc.lingerMax = 100 * time.Millisecond
	ctx := context.Background()
	r.svc.Status(ctx) // a page reads once, then is closed
	if err := r.svc.Request(ctx, Reboot, false, false); err != nil {
		t.Fatal(err)
	}
	r.wait(t)
}
