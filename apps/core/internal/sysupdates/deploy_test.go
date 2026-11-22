package sysupdates

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/power"
)

// newRun returns an update whose commands answer from f. The daemon answers
// as answers says.
func newRun(t *testing.T, f *cmdrunnertest.Fake, kind string, answers func() error) *deployRun {
	t.Helper()
	f.On("systemctl", "list-units").Reply("")
	return &deployRun{
		o: RunOptions{ID: 1, Kind: kind, Source: "github:o/r"},
		r: f, st: deployStore{path: filepath.Join(t.TempDir(), deployFile)},
		answers:       func(context.Context) error { return answers() },
		restarts:      func(context.Context) int { return -1 },
		healthTimeout: 40 * time.Millisecond, healthEvery: time.Millisecond, now: time.Now,
	}
}

func record(t *testing.T, x *deployRun) CoreDeploy {
	t.Helper()
	list := x.st.load()
	if len(list) != 1 {
		t.Fatalf("want one record, got %d", len(list))
	}
	return list[0]
}

// coreUpdateHost is an image host on core 1 whose sysupdate merges core 2.
func coreUpdateHost(t *testing.T) *cmdrunnertest.Fake {
	t.Helper()
	imageHost(t, "1")
	touch(t, filepath.Join(coreExtDir, "homelab-core_1.raw"))
	useCore(t, "1")
	f := cmdrunnertest.NewFake()
	sysupdateMoves(t, f, "2")
	f.On("systemctl", "restart").Reply("")
	f.On("systemd-sysext", "refresh").Reply("")
	return f
}

func caddyRuns(f *cmdrunnertest.Fake) {
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
}

// A daemon that dies at every start is not waited for: the core before goes
// back as soon as systemd has restarted it a few times.
func TestCoreUpdateRollsBackAtOnceWhenTheDaemonCrashLoops(t *testing.T) {
	f := coreUpdateHost(t)
	caddyRuns(f)
	x := newRun(t, f, KindCoreUpdate, func() error {
		if activeCore() == "2" {
			return errors.New("no socket")
		}
		return nil
	})
	var n atomic.Int32
	x.restarts = func(context.Context) int {
		if activeCore() == "2" {
			return int(n.Add(1))
		}
		return int(n.Load())
	}
	x.healthTimeout = time.Hour // only the crash loop can end the check in time
	done := make(chan struct{})
	go func() { _ = x.run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the health check waited out its timeout on a crash loop")
	}
	d := record(t, x)
	if d.Result != DeployRolledBack || !strings.Contains(d.Reason, "keeps crashing") {
		t.Fatalf("got %+v", d)
	}
}

func TestCoreUpdateFailsWhenTheCoreBeforeDoesNotComeBack(t *testing.T) {
	f := coreUpdateHost(t)
	caddyRuns(f)
	x := newRun(t, f, KindCoreUpdate, func() error { return errors.New("no socket") })
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployFailed || !strings.Contains(d.Reason, "did not come back either") {
		t.Fatalf("got %+v", d)
	}
}

func TestCoreUpdateRollsBackWhenCaddyIsDown(t *testing.T) {
	f := coreUpdateHost(t)
	// Caddy runs on the core before only.
	f.On("systemctl", "is-active", "caddy.service").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if activeCore() == "1" {
			return cmdrunner.Result{Stdout: "active\n"}, nil
		}
		return cmdrunner.Result{Stdout: "failed\n", ExitCode: 3}, errors.New("exit status 3")
	})
	x := newRun(t, f, KindCoreUpdate, func() error { return nil })
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployRolledBack || !strings.Contains(d.Reason, "Caddy") {
		t.Fatalf("got %+v", d)
	}
}

func TestNewFailedUnitsAreListedAndDoNotUndoTheUpdate(t *testing.T) {
	f := coreUpdateHost(t)
	caddyRuns(f)
	x := newRun(t, f, KindCoreUpdate, func() error { return nil })
	f.On("systemctl", "list-units", "--failed").Reply("old.service loaded failed failed Old\n").
		Reply("old.service loaded failed failed Old\nscrutiny.service loaded failed failed Scrutiny\n")
	_ = x.run(context.Background())
	d := record(t, x)
	if d.Result != DeployHealthy || len(d.FailedUnits) != 1 || d.FailedUnits[0] != "scrutiny.service" {
		t.Fatalf("got %+v", d)
	}
}

func TestStartRefusesWhileAnUpdateRuns(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	m := New(f, t.TempDir())
	m.SetImage(true)
	m.SetDeploy(DeployConfig{Exe: "/bin/daemon", Socket: "/s"})
	f.On("systemctl", "is-active", deployUnit).Reply("active\n")
	if _, err := m.StartImage(context.Background(), KindCoreUpdate, ""); !errors.Is(err, ErrDeployRunning) {
		t.Fatalf("running: %v", err)
	}
	if len(f.CallsMatching("systemd-run")) != 0 {
		t.Fatal("a refused update must not start a unit")
	}
}

func TestStartStartsTheUnitAndInterruptedIsClosed(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", deployUnit).FailCode(3, "inactive\n")
	f.On("systemd-run").Reply("")
	m := New(f, t.TempDir())
	m.SetImage(true)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.SetDeploy(DeployConfig{Source: "github:o/r", Exe: "/bin/daemon", Socket: "/s"})
	d, err := m.StartImage(context.Background(), KindCoreUpdate, "")
	if err != nil || d.Result != DeployRunning {
		t.Fatalf("%+v %v", d, err)
	}
	run := f.CallsMatching("systemd-run")
	if len(run) != 1 || !strings.Contains(strings.Join(run[0].Args, " "), "--unit="+deployUnit+" --collect --no-block") ||
		!strings.Contains(strings.Join(run[0].Args, " "), "-- /bin/daemon core-deploy --id") {
		t.Fatalf("systemd-run args: %+v", run)
	}
	// Still inside the grace period: running.
	if got := m.Deploys(context.Background()); got[0].Result != DeployRunning {
		t.Fatalf("got %+v", got[0])
	}
	// The unit is gone and wrote no result: interrupted.
	now = now.Add(time.Minute)
	if got := m.Deploys(context.Background()); got[0].Result != DeployInterrupted || got[0].FinishedAt == nil {
		t.Fatalf("got %+v", got[0])
	}
}

type fakePower struct{ asked []power.PowerAction }

func (f *fakePower) Request(_ context.Context, a power.PowerAction, force, _ bool) error {
	if !force {
		return errors.New("a reboot the operator asked for is forced")
	}
	f.asked = append(f.asked, a)
	return nil
}

func TestRebootGoesThroughPower(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	m := New(f, t.TempDir())
	p := &fakePower{}
	m.SetPower(p)
	if err := m.reboot(context.Background()); err != nil || len(p.asked) != 1 || p.asked[0] != power.Reboot {
		t.Fatalf("err=%v asked=%v", err, p.asked)
	}
}
