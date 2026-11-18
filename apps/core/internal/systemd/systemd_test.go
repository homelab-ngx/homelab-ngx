package systemd_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/systemd"
)

func TestProps(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "show").Reply("Id=a.service\nActiveState=active\nSubState=running\n\nId=b.service\nActiveState=failed\n")
	got := systemd.New(f).Props(context.Background(), []string{"a.service", "b.service"}, "Id", "ActiveState", "SubState")
	if got["a.service"]["SubState"] != "running" || got["b.service"]["ActiveState"] != "failed" {
		t.Fatalf("props = %v", got)
	}
	call := f.Calls()[0]
	want := []string{"show", "a.service", "b.service", "--property=Id,ActiveState,SubState"}
	if len(call.Args) != len(want) {
		t.Fatalf("args = %v", call.Args)
	}
	if systemd.New(cmdrunnertest.NewFake()).Props(context.Background(), []string{"a"}, "Id") != nil {
		t.Error("failed call must yield nil")
	}
	if systemd.New(f).Props(context.Background(), nil) != nil {
		t.Error("no units must yield nil")
	}
}

func TestActiveMulti(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", "a", "b").FailCode(3, "active\ninactive\n")
	got := systemd.New(f).ActiveMulti(context.Background(), []string{"a", "b"})
	if !got["a"] || got["b"] {
		t.Fatalf("got %v", got)
	}

	// Mismatched output falls back to per-unit checks.
	g := cmdrunnertest.NewFake()
	g.On("systemctl", "is-active", "--quiet", "x").Reply("")
	g.On("systemctl", "is-active", "--quiet", "y").Fail("")
	g.On("systemctl", "is-active", "x", "y").Reply("garbage")
	got = systemd.New(g).ActiveMulti(context.Background(), []string{"x", "y"})
	if !got["x"] || got["y"] {
		t.Fatalf("fallback got %v", got)
	}
}

func TestStopResetsSpuriousFailed(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "stop", "u").Reply("")
	f.On("systemctl", "show", "u", "--property=ActiveState").Reply("failed\n")
	f.On("systemctl", "reset-failed", "u").Reply("")
	if err := systemd.New(f).Stop(context.Background(), "u"); err != nil {
		t.Fatal(err)
	}
	if len(f.CallsMatching("systemctl", "reset-failed", "u")) != 1 {
		t.Fatalf("calls: %v", f.Calls())
	}

	g := cmdrunnertest.NewFake()
	g.On("systemctl", "stop", "u").Reply("")
	g.On("systemctl", "show", "u").Reply("inactive\n")
	if err := systemd.New(g).Stop(context.Background(), "u"); err != nil {
		t.Fatal(err)
	}
	if len(g.CallsMatching("systemctl", "reset-failed")) != 0 {
		t.Fatal("reset-failed must only follow a failed state")
	}
}

func TestStartErrorCarriesOutput(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "start", "u").Fail("Job failed")
	err := systemd.New(f).Start(context.Background(), "u")
	if err == nil || !strings.Contains(err.Error(), "Job failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestPerUnitOpsAreSerialised(t *testing.T) {
	f := cmdrunnertest.NewFake()
	var cur, peak atomic.Int32
	var mu sync.Mutex
	f.On("systemctl", "start").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		n := cur.Add(1)
		mu.Lock()
		if n > peak.Load() {
			peak.Store(n)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return cmdrunner.Result{}, nil
	})
	f.On("systemctl", "restart").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		n := cur.Add(1)
		mu.Lock()
		if n > peak.Load() {
			peak.Store(n)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return cmdrunner.Result{}, nil
	})
	c := systemd.New(f)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.Start(context.Background(), "same") }()
		go func() { defer wg.Done(); _ = c.Restart(context.Background(), "same") }()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("same-unit ops overlapped (peak %d)", peak.Load())
	}
}

func TestEveryCallHasDeadline(t *testing.T) {
	f := cmdrunnertest.NewFake()
	var withDeadline atomic.Int32
	rule := f.On("systemctl")
	rule.Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) { return cmdrunner.Result{Stdout: "active\n"}, nil })
	// Wrap: the Fake does not expose ctx, so use a Runner shim.
	shim := runnerFunc(func(ctx context.Context, c cmdrunner.Cmd) (cmdrunner.Result, error) {
		if d, ok := ctx.Deadline(); ok && time.Until(d) <= systemd.CallTimeout {
			withDeadline.Add(1)
		}
		return f.Run(ctx, c)
	})
	c := systemd.New(shim)
	ctx := context.Background()
	c.IsActive(ctx, "u")
	c.ActiveState(ctx, "u")
	c.ExitedWithFailure(ctx, "u")
	_ = c.Start(ctx, "u")
	_ = c.Restart(ctx, "u")
	_ = c.Stop(ctx, "u")
	c.Props(ctx, []string{"u"}, "Id")
	if got := withDeadline.Load(); got < 7 {
		t.Fatalf("only %d calls carried a <=30s deadline", got)
	}
}

type runnerFunc func(context.Context, cmdrunner.Cmd) (cmdrunner.Result, error)

func (f runnerFunc) Run(ctx context.Context, c cmdrunner.Cmd) (cmdrunner.Result, error) {
	return f(ctx, c)
}

func TestWaitActive(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", "--quiet", "u").Fail("").Fail("").Reply("")
	c := systemd.New(f, systemd.WithPollInterval(time.Millisecond))
	if err := c.WaitActive(context.Background(), "u", time.Second); err != nil {
		t.Fatal(err)
	}
	g := cmdrunnertest.NewFake()
	g.On("systemctl", "is-active").Fail("")
	err := systemd.New(g, systemd.WithPollInterval(time.Millisecond)).WaitActive(context.Background(), "u", 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := systemd.New(g).WaitActive(ctx, "u", time.Hour); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestExitedWithFailureAndJournal(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "show", "ok").Reply("ExecMainStatus=0\n")
	f.On("systemctl", "show", "bad").Reply("ExecMainStatus=1\n")
	f.On("journalctl").Fail("line1\nline2\n")
	c := systemd.New(f)
	if c.ExitedWithFailure(context.Background(), "ok") || !c.ExitedWithFailure(context.Background(), "bad") {
		t.Fatal("ExitedWithFailure wrong")
	}
	if got := c.JournalTail(context.Background(), "u", 10); got != "line1\nline2\n" {
		t.Fatalf("journal = %q", got)
	}
	if a := f.CallsMatching("journalctl")[0].Args; a[0] != "-u" || a[3] != "10" {
		t.Fatalf("args %v", a)
	}
}

func TestLastRunOutcome(t *testing.T) {
	cases := []struct {
		name, entry, want string
		found             bool
	}{
		{"success", `{"MESSAGE_ID":"7ad2d189f7e94e70a38c781354912448"}`, "success", true},
		{"failure result", `{"MESSAGE_ID":"d9b373ed55a64feb8242e02dbe79a49c","UNIT_RESULT":"signal"}`, "signal", true},
		{"no run ended", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cmdrunnertest.NewFake()
			f.On("journalctl").Reply(tc.entry)
			got, found, err := systemd.New(f).LastRunOutcome(context.Background(), "b.service", time.Unix(1700000000, 0))
			if err != nil || got != tc.want || found != tc.found {
				t.Fatalf("got %q, %v, %v", got, found, err)
			}
			args := strings.Join(f.CallsMatching("journalctl")[0].Args, " ")
			if !strings.Contains(args, "--since @1700000000") || !strings.Contains(args, "UNIT=b.service") {
				t.Fatalf("args %s", args)
			}
		})
	}
}

func TestServiceUnits(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "list-units", "--type=service").Reply("a.service loaded active running A\nb.service loaded failed failed B\n\n")
	got, err := systemd.New(f).ServiceUnits(context.Background())
	if err != nil || len(got) != 2 || got[0] != "a.service" {
		t.Fatalf("units = %v, err = %v", got, err)
	}
	if _, err := systemd.New(cmdrunnertest.NewFake()).ServiceUnits(context.Background()); err == nil {
		t.Fatal("a failing systemctl must be an error")
	}
}
