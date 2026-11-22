package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFreezeKeepsTheStackRunningAndThawLetsItGo(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx := context.Background()
	_, err := svc.Freeze(ctx, "immich")
	wantCode(t, err, http.StatusConflict, "stack_not_running")

	finished(t, svc, svc.Start)
	s, err := svc.Freeze(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Frozen || !s.Running {
		t.Fatalf("frozen=%v running=%v", s.Frozen, s.Running)
	}
	if n := len(h.CallsMatching("systemctl", "freeze", "--", "immich.scope")); n != 1 {
		t.Fatalf("freeze calls = %d; the scope is what freezes", n)
	}
	// The freeze held the slot for a moment only: the start stays the
	// stack's last operation.
	if s.Operation == nil || s.Operation.Kind != "start" {
		t.Errorf("operation = %+v", s.Operation)
	}

	s, err = svc.Thaw(ctx, "immich")
	if err != nil || s.Frozen || !s.Running {
		t.Fatalf("thaw: frozen=%v running=%v err=%v", s.Frozen, s.Running, err)
	}
	// A thaw of a stack that is not frozen does nothing.
	if _, err := svc.Thaw(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.CallsMatching("systemctl", "thaw")); n != 1 {
		t.Errorf("thaw calls = %d", n)
	}
}

func TestAFrozenStackRefusesWhatWouldCallIntoIt(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	if _, err := svc.Freeze(ctx, "immich"); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Update(ctx, "immich")
	wantCode(t, err, http.StatusConflict, "stack_frozen")
	_, err = svc.Rollback(ctx, "immich", 0)
	wantCode(t, err, http.StatusConflict, "stack_frozen")
	_, err = svc.Start(ctx, "immich")
	wantCode(t, err, http.StatusConflict, "stack_frozen")
	if q, _ := svc.UpdateAll(ctx); len(q) != 0 {
		t.Errorf("update all queued %v", q)
	}
	err = svc.UnitAction(ctx, "immich-server", "restart")
	wantCode(t, err, http.StatusConflict, "stack_frozen")
	if err := svc.RunPrepare(ctx, "immich", "immich-dump.service"); err == nil {
		t.Error("a prepare unit ran in a frozen stack")
	}

	// The units are listed without asking the container.
	before := len(h.CallsMatching("systemctl", "--machine=immich"))
	for _, u := range svc.Units(ctx) {
		if u.ActiveState != "active" || u.SubState != "frozen" {
			t.Errorf("unit %+v", u)
		}
	}
	if n := len(h.CallsMatching("systemctl", "--machine=immich")); n != before {
		t.Errorf("the units list called into the frozen stack %d times", n-before)
	}

	// The monitor neither probes nor reports it.
	fails := map[string]int{}
	for range unhealthyAfter + 1 {
		svc.monitorOnce(ctx, fails)
	}
	if _, ok := h.problems["stack:immich"]; ok || len(fails) != 0 {
		t.Errorf("problems=%v fails=%v", h.problems, fails)
	}
	if n := len(h.CallsMatching("systemctl", "--machine=immich")); n != before {
		t.Errorf("the monitor called into the frozen stack")
	}
}

func TestStopThawsAFrozenStackFirst(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	if _, err := svc.Freeze(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	s := finished(t, svc, svc.Stop)
	if s.Running || s.Frozen || s.Operation.Error != "" {
		t.Fatalf("running=%v frozen=%v op=%+v", s.Running, s.Frozen, s.Operation)
	}
	if len(h.CallsMatching("systemctl", "thaw")) != 1 {
		t.Error("the stop did not thaw the stack")
	}
}

func TestKillEndsAFrozenStack(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	if _, err := svc.Freeze(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if got := svc.Frozen(ctx); len(got) != 1 || got[0] != "immich" {
		t.Fatalf("frozen = %v", got)
	}
	if err := svc.Kill(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if len(h.CallsMatching("systemctl", "kill", "--signal=SIGKILL", "--", "immich.scope")) != 1 {
		t.Error("the scope was not killed")
	}
	if err := svc.Halt(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if len(h.CallsMatching("systemctl", "thaw")) != 0 {
		t.Error("a killed stack was thawed")
	}
}

func TestAutostartAppliesOncePerBoot(t *testing.T) {
	t.Parallel()
	svc, _, h := newRuntime(t)
	ctx := context.Background()
	boot := "boot-1"
	svc.bootID = func() string { return boot }

	// Installed stacks start at boot unless they are set not to.
	if s, _ := svc.Get(ctx, "immich"); !s.Autostart {
		t.Fatal("a new stack does not autostart")
	}
	svc.StartWanted(ctx)
	if !h.running {
		t.Fatal("a stack set to autostart did not start at boot")
	}

	// Stopped by the operator, it starts again at the next boot.
	finished(t, svc, svc.Stop)
	svc.StartWanted(ctx) // the daemon restarts within the same boot
	if h.running {
		t.Fatal("a daemon restart started a stack the operator stopped")
	}
	boot = "boot-2"
	svc.StartWanted(ctx)
	if !h.running {
		t.Fatal("a stack set to autostart did not start at the next boot")
	}

	// Off: it stays stopped at a boot, though it ran before.
	if _, err := svc.SetAutostart(ctx, "immich", false); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	boot = "boot-3"
	svc.StartWanted(ctx)
	if h.running {
		t.Fatal("a stack not set to autostart started at boot")
	}
	if s, _ := svc.Get(ctx, "immich"); s.Autostart || s.WantRunning {
		t.Errorf("autostart=%v want_running=%v", s.Autostart, s.WantRunning)
	}
}

func TestTheLockKeepsAutostart(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	ctx := context.Background()
	if _, err := svc.SetAutostart(ctx, "immich", false); err != nil {
		t.Fatal(err)
	}
	l, err := svc.CurrentLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a := l.Stacks[0].Autostart; a == nil || *a {
		t.Fatalf("lock autostart = %v", a)
	}
	b, _ := json.Marshal(l)
	var back Lock
	if err := json.Unmarshal(b, &back); err != nil || back.Stacks[0].Autostart == nil || *back.Stacks[0].Autostart {
		t.Fatalf("round trip: %s", b)
	}
}
