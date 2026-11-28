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
	"homelab-ngx.com/core/internal/notifications"
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

// A build's unit caps its memory, so the page cache of what it writes is
// reclaimed first; the other kinds run without the cap.
func TestStartCapsABuildsMemory(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]bool{KindBuild: true, KindCoreUpdate: false} {
		f := cmdrunnertest.NewFake()
		f.On("systemctl", "is-active", deployUnit).FailCode(3, "inactive\n")
		f.On("systemd-run").Reply("")
		m := New(f, t.TempDir())
		m.SetImage(true)
		m.SetDeploy(DeployConfig{Source: "github:o/r", Exe: "/bin/daemon", Socket: "/s"})
		if _, err := m.StartImage(context.Background(), kind, ""); err != nil {
			t.Fatal(err)
		}
		run := f.CallsMatching("systemd-run")
		if len(run) != 1 {
			t.Fatalf("%s: systemd-run calls: %+v", kind, run)
		}
		args := strings.Join(run[0].Args, " ")
		if got := strings.Contains(args, "--property=MemoryHigh="+buildMemoryHigh+" "); got != want ||
			strings.Index(args, "MemoryHigh") > strings.Index(args, " -- ") {
			t.Errorf("%s: MemoryHigh %v, want %v: %s", kind, got, want, args)
		}
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

func TestDeployNotificationMessages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		deploy    CoreDeploy
		wantOK    bool
		wantSev   notifications.Severity
		wantEvent string
		wantTitle string
	}{
		{
			name:   "build already current skipped",
			deploy: CoreDeploy{Id: 1, Kind: KindBuild, Result: DeployHealthy, Phase: "Already on HEAD"},
		},
		{
			name:      "build healthy",
			deploy:    CoreDeploy{Id: 2, Kind: KindBuild, Version: "0.9.0", Result: DeployHealthy, Phase: "Built 0.9.0"},
			wantOK:    true,
			wantSev:   notifications.Info,
			wantEvent: "host.rebuild",
			wantTitle: "Built image 0.9.0",
		},
		{
			name:      "build failed",
			deploy:    CoreDeploy{Id: 3, Kind: KindBuild, Result: DeployBuildFailed, Reason: "oom"},
			wantOK:    true,
			wantSev:   notifications.Alert,
			wantEvent: "host.rebuild",
			wantTitle: "Host image build failed",
		},
		{
			name:      "core update rolled back",
			deploy:    CoreDeploy{Id: 4, Kind: KindCoreUpdate, Result: DeployRolledBack, Reason: "caddy down"},
			wantOK:    true,
			wantSev:   notifications.Alert,
			wantEvent: "core.update",
			wantTitle: "Core update rolled back",
		},
		{
			name:      "install needs reboot",
			deploy:    CoreDeploy{Id: 5, Kind: KindInstall, Version: "0.9.0", Result: DeployHealthy, Phase: "Installed 0.9.0; it boots at the next reboot"},
			wantOK:    true,
			wantSev:   notifications.Info,
			wantEvent: "core.reboot_needed",
			wantTitle: "Installed 0.9.0; reboot needed",
		},
		{
			name:      "image update rebooting",
			deploy:    CoreDeploy{Id: 6, Kind: KindImageUpdate, Version: "0.9.0", Result: DeployHealthy, Phase: "Installed 0.9.0; rebooting"},
			wantOK:    true,
			wantSev:   notifications.Info,
			wantEvent: "core.update",
			wantTitle: "Installed 0.9.0",
		},
		{
			name:   "download healthy skipped",
			deploy: CoreDeploy{Id: 7, Kind: KindDownload, Version: "0.9.0", Result: DeployHealthy, Phase: "Downloaded 0.9.0"},
		},
		{
			name:      "download failed",
			deploy:    CoreDeploy{Id: 8, Kind: KindDownload, Result: DeployFailed, Reason: "timeout"},
			wantOK:    true,
			wantSev:   notifications.Alert,
			wantEvent: "core.update",
			wantTitle: "Release download failed",
		},
	} {
		msg, ok := deployNotification(tc.deploy)
		if ok != tc.wantOK {
			t.Errorf("%s: ok=%v, want %v (%+v)", tc.name, ok, tc.wantOK, msg)
			continue
		}
		if !ok {
			continue
		}
		if msg.Severity != tc.wantSev || msg.Event != tc.wantEvent || msg.Title != tc.wantTitle || msg.Path != deployUpdatesPath {
			t.Errorf("%s: got %+v", tc.name, msg)
		}
	}
}

func TestStartBuildAndFinishedDeploysDeduplicate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", deployUnit).FailCode(3, "inactive\n")
	f.On("systemd-run").Reply("")
	dir := t.TempDir()
	m := New(f, dir)
	m.SetImage(true)
	m.now = func() time.Time { return now }
	m.SetDeploy(DeployConfig{Source: "github:o/r", Exe: "/bin/daemon", Socket: "/s"})
	n := &fakeNotifier{}
	m.SetNotifier(n)

	d, err := m.StartImage(context.Background(), KindBuild, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(n.sent) != 1 || n.sent[0].Event != "host.rebuild" || n.sent[0].Severity != notifications.Info {
		t.Fatalf("start build sent = %+v", n.sent)
	}

	// Pre-existing old deploy is skipped on bootstrap, recent finished deploy is emitted once.
	oldFin := now.Add(-2 * time.Hour)
	recentFin := now.Add(-2 * time.Minute)
	st := m.deploys()
	_ = st.put(CoreDeploy{Id: 10, Kind: KindCoreUpdate, Version: "0.8.0", Result: DeployHealthy, Phase: "Updated", FinishedAt: &oldFin})
	_ = st.put(CoreDeploy{Id: d.Id, Kind: KindBuild, Version: "0.9.0", Result: DeployHealthy, Phase: "Built 0.9.0", FinishedAt: &recentFin})

	m.notifyFinishedDeploys(context.Background())
	m.notifyFinishedDeploys(context.Background())
	if len(n.sent) != 2 || n.sent[1].Title != "Built image 0.9.0" {
		t.Fatalf("finished deploy sent = %+v", n.sent)
	}
}

func TestDeployRunNotifyTerminalFiresOnceOnRebootNow(t *testing.T) {
	t.Parallel()
	f := coreUpdateHost(t)
	caddyRuns(f)
	x := newRun(t, f, KindCoreUpdate, func() error { return nil })
	var calls int
	x.notifyFn = func(_ context.Context, d CoreDeploy) {
		calls++
		if d.Result != DeployHealthy {
			t.Errorf("result = %s", d.Result)
		}
	}
	_ = x.run(context.Background())
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	f2 := cmdrunnertest.NewFake()
	f2.On("systemctl", "reboot").Reply("")
	x2 := newRun(t, f2, KindInstall, func() error { return nil })
	x2.o.Reboot = "now"
	x2.d.Version = "1.0.0"
	var calls2 int
	x2.notifyFn = func(_ context.Context, d CoreDeploy) { calls2++ }
	res, why := x2.rebootChoice(context.Background())
	if res != DeployHealthy || why != "" || calls2 != 1 {
		t.Fatalf("res=%s why=%q calls2=%d", res, why, calls2)
	}
	x2.notifyTerminal(context.Background())
	if calls2 != 1 {
		t.Fatalf("calls2 after second trigger = %d, want 1", calls2)
	}
}
