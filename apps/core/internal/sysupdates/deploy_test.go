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
)

const (
	testRev  = "187ef4de4e3f1bc8e017a7976045e8e3bd53f5b1"
	testMeta = `{"revision":"` + testRev + `","url":"github:o/r/` + testRev + `?narHash=x"}`
)

// newRun returns a deploy whose commands answer from f. The daemon reports
// commit; the system profile reads as profile().
func newRun(t *testing.T, f *cmdrunnertest.Fake, kind string, commit func() (string, error), profile func() string) *deployRun {
	t.Helper()
	f.On("systemctl", "list-units").Reply("")
	return &deployRun{
		o: RunOptions{ID: 1, Kind: kind, Source: "github:o/r", Host: "homelab"},
		r: f, st: deployStore{path: filepath.Join(t.TempDir(), deployFile)},
		version:       func(context.Context) (string, error) { return commit() },
		profile:       profile,
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

// switching moves the profile when nixos-rebuild switch runs.
func switching(f *cmdrunnertest.Fake) func() string {
	gen := "system-1-link"
	f.On("nixos-rebuild", "switch", "--flake").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		gen = "system-2-link"
		return cmdrunner.Result{}, nil
	})
	f.On("nixos-rebuild", "switch", "--rollback").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		gen = "system-1-link"
		return cmdrunner.Result{}, nil
	})
	return func() string { return gen }
}

func TestDeployHealthy(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "187ef4d", nil }, switching(f))
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := record(t, x)
	if d.Result != DeployHealthy || d.Rev != testRev || d.FinishedAt == nil {
		t.Fatalf("got %+v", d)
	}
	if len(f.CallsMatching("nixos-rebuild", "switch", "--rollback")) != 0 {
		t.Fatal("a healthy deploy must not roll back")
	}
	sw := f.CallsMatching("nixos-rebuild", "switch", "--flake")
	if len(sw) != 1 || sw[0].Args[2] != "github:o/r/"+testRev+"?narHash=x#homelab" {
		t.Fatalf("switch args: %+v", sw)
	}
}

func TestDeployRollsBackWhenDaemonStaysOnOldCommit(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "0ldc0de", nil }, switching(f))
	if err := x.run(context.Background()); err == nil {
		t.Fatal("want an error for the unit's exit status")
	}
	d := record(t, x)
	if d.Result != DeployRolledBack || !strings.Contains(d.Reason, "0ldc0de") {
		t.Fatalf("got %+v", d)
	}
	if len(f.CallsMatching("nixos-rebuild", "switch", "--rollback")) != 1 {
		t.Fatal("want one rollback")
	}
}

func TestDeployRollsBackAtOnceWhenTheDaemonCrashLoops(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	profile := switching(f)
	rolledBack := func() bool { return len(f.CallsMatching("nixos-rebuild", "switch", "--rollback")) > 0 }
	x := newRun(t, f, KindDeploy, func() (string, error) {
		if rolledBack() {
			return "0ldc0de", nil
		}
		return "", errors.New("no socket")
	}, profile)
	// The new daemon dies at every start: systemd's count climbs until the
	// rollback, then stays.
	var n atomic.Int32
	x.restarts = func(context.Context) int {
		if rolledBack() {
			return int(n.Load())
		}
		return int(n.Add(1))
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

func TestDeployFailsWhenRollbackDoesNotBringTheDaemonBack(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "", errors.New("no socket") }, switching(f))
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployFailed || !strings.Contains(d.Reason, "after the rollback") {
		t.Fatalf("got %+v", d)
	}
}

func TestDeployCaddyDownRollsBack(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	profile := switching(f)
	// Caddy runs on the old generation only.
	f.On("systemctl", "is-active", "caddy.service").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if profile() == "system-1-link" {
			return cmdrunner.Result{Stdout: "active\n"}, nil
		}
		return cmdrunner.Result{Stdout: "failed\n", ExitCode: 3}, errors.New("exit status 3")
	})
	x := newRun(t, f, KindDeploy, func() (string, error) { return "187ef4d", nil }, profile)
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployRolledBack || !strings.Contains(d.Reason, "Caddy") {
		t.Fatalf("got %+v", d)
	}
}

func TestDeployBuildFailureKeepsNixErrorBlock(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix", "--extra-experimental-features", "nix-command flakes", "flake").Reply(testMeta)
	f.On("nix", "--extra-experimental-features", "nix-command flakes", "build").
		Fail("building foo\nerror: attribute 'nosuchattr' missing\n       at /tmp/bad.nix:1:33:\n            1| pkgs.nosuchattr\nnext line\n")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "187ef4d", nil }, func() string { return "system-1-link" })
	_ = x.run(context.Background())
	d := record(t, x)
	if d.Result != DeployBuildFailed || !strings.Contains(d.Reason, "at /tmp/bad.nix:1:33") || strings.Contains(d.Reason, "next line") {
		t.Fatalf("got %+v", d)
	}
	if len(f.CallsMatching("nixos-rebuild")) != 0 {
		t.Fatal("a failed build must not switch")
	}
}

func TestDeploySwitchThatNeverStartedDoesNotRollBack(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("nixos-rebuild", "switch", "--flake").Fail("boom")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "187ef4d", nil }, func() string { return "system-1-link" })
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployFailed {
		t.Fatalf("got %+v", d)
	}
	if len(f.CallsMatching("nixos-rebuild", "switch", "--rollback")) != 0 {
		t.Fatal("nothing was activated, so nothing to roll back")
	}
}

func TestDeployNewFailedUnitsAreListedAndDoNotUndoIt(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "187ef4d", nil }, switching(f))
	f.On("systemctl", "list-units", "--failed").Reply("old.service loaded failed failed Old\n").
		Reply("old.service loaded failed failed Old\nscrutiny.service loaded failed failed Scrutiny\n")
	_ = x.run(context.Background())
	d := record(t, x)
	if d.Result != DeployHealthy || len(d.FailedUnits) != 1 || d.FailedUnits[0] != "scrutiny.service" {
		t.Fatalf("got %+v", d)
	}
}

func TestStartDeployRefusals(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	m := New(f, t.TempDir(), "")
	if _, err := m.StartDeploy(context.Background(), KindDeploy); !errors.Is(err, ErrDeployNotSet) {
		t.Fatalf("no source: %v", err)
	}
	m.SetDeploy(DeployConfig{Source: "github:o/r", Host: "h", Exe: "/bin/daemon", Socket: "/s",
		Unpushed: func(context.Context) (bool, error) { return true, nil }})
	f.On("systemctl", "is-active", deployUnit).Reply("active\n").FailCode(3, "inactive\n")
	if _, err := m.StartDeploy(context.Background(), KindDeploy); !errors.Is(err, ErrDeployRunning) {
		t.Fatalf("running: %v", err)
	}
	if _, err := m.StartDeploy(context.Background(), KindDeploy); !errors.Is(err, ErrSecretsNotPushed) {
		t.Fatalf("unpushed: %v", err)
	}
	if len(f.CallsMatching("systemd-run")) != 0 {
		t.Fatal("a refused deploy must not start a unit")
	}
}

func TestStartDeployStartsTheUnitAndInterruptedIsClosed(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", deployUnit).FailCode(3, "inactive\n")
	f.On("systemd-run").Reply("")
	m := New(f, t.TempDir(), "")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.SetDeploy(DeployConfig{Source: "github:o/r", Host: "h", Exe: "/bin/daemon", Socket: "/s"})
	d, err := m.StartDeploy(context.Background(), KindDeploy)
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

func TestBehind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		running, main string
		want          bool
	}{
		{"187ef4d", testRev, false},
		{"dd3d609", testRev, true},
		{"187ef4d-dirty", testRev, true},
		{"", testRev, false},
		{"187ef4d", "", false},
	} {
		if got := behind(c.running, c.main); got != c.want {
			t.Errorf("behind(%q,%q) = %v", c.running, c.main, got)
		}
	}
}

func TestCoreStatusUsesTheDeployedCommitOverAnOlderCheck(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", deployUnit).FailCode(3, "inactive\n")
	f.On("nix").Reply(testMeta)
	m := New(f, t.TempDir(), "")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.SetDeploy(DeployConfig{Source: "github:o/r", Host: "h"})
	m.ds.mainRev, m.ds.checked = "0000000aaaa", now.Add(-time.Minute)
	fin := now.Add(-time.Second)
	if err := m.deploys().put(CoreDeploy{Id: 1, Kind: KindDeploy, Rev: testRev, Result: DeployHealthy, FinishedAt: &fin}); err != nil {
		t.Fatal(err)
	}
	if st := m.CoreStatus(context.Background(), false); st.MainRev != testRev {
		t.Fatalf("main_rev = %q, want the deployed commit", st.MainRev)
	}
}

func TestRollbackThatDoesNotMoveTheProfileFails(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nixos-rebuild", "switch", "--rollback").Fail("error: file 'nixpkgs/nixos' was not found")
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x := newRun(t, f, KindRollback, func() (string, error) { return "187ef4d", nil }, func() string { return "system-2-link" })
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployFailed || !strings.Contains(d.Reason, "did not happen") {
		t.Fatalf("got %+v", d)
	}
}

func TestDeployWhoseRollbackDoesNotHappenFails(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(testMeta)
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	gen := "system-1-link"
	f.On("nixos-rebuild", "switch", "--flake").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		gen = "system-2-link"
		return cmdrunner.Result{}, nil
	})
	f.On("nixos-rebuild", "switch", "--rollback").Fail("boom")
	x := newRun(t, f, KindDeploy, func() (string, error) { return "0ldc0de", nil }, func() string { return gen })
	_ = x.run(context.Background())
	if d := record(t, x); d.Result != DeployFailed || !strings.Contains(d.Reason, "did not happen") {
		t.Fatalf("got %+v", d)
	}
}

func TestRollbackHealthy(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	profile := switching(f)
	x := newRun(t, f, KindRollback, func() (string, error) { return "0ldc0de", nil }, func() string { return "system-2-link" })
	x.profile = func() string {
		if len(f.CallsMatching("nixos-rebuild", "switch", "--rollback", "--no-reexec")) > 0 {
			return "system-1-link"
		}
		return "system-2-link"
	}
	_ = profile
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := record(t, x); d.Result != DeployHealthy {
		t.Fatalf("got %+v", d)
	}
}
