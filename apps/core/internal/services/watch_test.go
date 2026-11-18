package services

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/notifications/notifytest"
)

func TestChurnTrackerCountsRestartsInTheTrailingHour(t *testing.T) {
	t.Parallel()
	var tr churnTracker
	t0 := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	if n, _ := tr.observe("a.service", 0, t0); n != 0 {
		t.Fatalf("first sample is a baseline, got %d", n)
	}
	if n, _ := tr.observe("a.service", 2, t0.Add(10*time.Minute)); n != 2 {
		t.Fatalf("n = %d", n)
	}
	if n, _ := tr.observe("a.service", 6, t0.Add(30*time.Minute)); n != 6 {
		t.Fatalf("n = %d", n)
	}
	// 70 minutes in: the first 2 restarts (t0..t0+10m) fell out of the hour.
	n, obs := tr.observe("a.service", 6, t0.Add(70*time.Minute))
	if n != 4 || obs < time.Hour {
		t.Fatalf("n = %d observed = %s", n, obs)
	}
	// Two hours in with no growth: a quiet hour.
	n, _ = tr.observe("a.service", 6, t0.Add(2*time.Hour+time.Minute))
	if n != 0 {
		t.Fatalf("quiet hour n = %d", n)
	}
}

func TestChurnTrackerTreatsACounterDropAsAReset(t *testing.T) {
	t.Parallel()
	var tr churnTracker
	t0 := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	tr.observe("a.service", 10, t0)
	n, _ := tr.observe("a.service", 3, t0.Add(time.Minute)) // started by hand, then 3 restarts
	if n != 3 {
		t.Fatalf("n = %d, want 3 (not -7)", n)
	}
}

func TestChurnTrackerCollapsesFlatRuns(t *testing.T) {
	t.Parallel()
	var tr churnTracker
	t0 := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	for i := range 59 {
		tr.observe("a.service", 0, t0.Add(time.Duration(i)*time.Minute))
	}
	if got := len(tr.samples["a.service"]); got > 3 {
		t.Fatalf("kept %d samples for a flat counter", got)
	}
}

// watchHarness gives the service a real notifier over its DB and a recording
// webhook.
func watchHarness(t *testing.T) (*harness, *[]string) {
	t.Helper()
	h := newHarness(t, nil)
	var titles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		titles = append(titles, notifytest.Title(r))
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	h.svc.notify = notifications.New(notifytest.Config(t, srv.URL),
		notifications.NewDBStore(h.st), func() time.Time { return h.now })
	return h, &titles
}

func openKeys(t *testing.T, h *harness) map[string]bool {
	t.Helper()
	ps, err := h.svc.notify.OpenProblems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, p := range ps {
		m[p.Key] = true
	}
	return m
}

func TestHousekeepOpensChurnAtFiveRestartsPerHourAndResolvesAfterAQuietHour(t *testing.T) {
	t.Parallel()
	h, titles := watchHarness(t)
	ctx := context.Background()
	snap := h.cfg.Current()
	unit := "flappy.service"

	restarts := 0
	h.fake.On("systemctl", "list-units").Reply(unit + " loaded active running d\n")
	h.fake.On("systemctl", "show", unit, "--property=Id,NRestarts,InvocationID,TriggeredBy").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		out := fmt.Sprintf("Id=%s\nNRestarts=%d\n\n", unit, restarts)
		return cmdrunner.Result{Stdout: out, Output: out}, nil
	})

	step := func(d time.Duration, n int) {
		h.now = h.now.Add(d)
		restarts = n
		h.svc.housekeep(ctx, snap)
	}
	step(0, 0)
	step(time.Minute, 3)
	if openKeys(t, h)["churn:"+unit] {
		t.Fatal("3 restarts must not open churn")
	}
	step(time.Minute, 5)
	if !openKeys(t, h)["churn:"+unit] {
		t.Fatal("5 restarts in an hour must open churn:<unit>")
	}
	if len(*titles) != 1 || !strings.Contains((*titles)[0], "flappy keeps restarting") {
		t.Fatalf("notifications = %v", *titles)
	}
	// The unit calms down; 30 minutes later it is still open ...
	step(30*time.Minute, 5)
	if !openKeys(t, h)["churn:"+unit] {
		t.Fatal("resolved before a quiet hour")
	}
	// ... and after a full quiet hour it resolves.
	step(61*time.Minute, 5)
	step(time.Minute, 5)
	if openKeys(t, h)["churn:"+unit] {
		t.Fatalf("not resolved after a quiet hour: %v", openKeys(t, h))
	}
}

func TestHousekeepResolvesChurnForUnitsThatNoLongerExist(t *testing.T) {
	t.Parallel()
	h, _ := watchHarness(t)
	ctx := context.Background()
	if _, err := h.svc.notify.Report(ctx, notifications.Problem{Key: "churn:gone.service", Title: "gone keeps restarting"}); err != nil {
		t.Fatal(err)
	}
	h.fake.On("systemctl", "list-units").Reply("other.service loaded active running d\n")
	h.fake.On("systemctl", "show", "other.service").Reply("Id=other.service\nNRestarts=0\n")
	h.svc.housekeep(ctx, h.cfg.Current())
	if openKeys(t, h)["churn:gone.service"] {
		t.Fatal("churn for a vanished unit stayed open")
	}
}

func TestHousekeepResolvesUnitProblemsWhenTheUnitRecovered(t *testing.T) {
	t.Parallel()
	h, _ := watchHarness(t)
	ctx := context.Background()
	for _, u := range []string{"active.service", "done.service", "gone.service", "broken.service", "stopped.service"} {
		if _, err := h.svc.notify.Report(ctx, notifications.Problem{Key: "unit:" + u, Title: u + " failed"}); err != nil {
			t.Fatal(err)
		}
	}
	h.fake.On("systemctl", "list-units").Reply("")
	show := func(id, active, load, result string) string {
		return fmt.Sprintf("Id=%s\nActiveState=%s\nLoadState=%s\nResult=%s\n\n", id, active, load, result)
	}
	out := show("active.service", "active", "loaded", "success") +
		show("done.service", "inactive", "loaded", "success") +
		show("gone.service", "inactive", "not-found", "success") +
		show("broken.service", "failed", "loaded", "exit-code") +
		show("stopped.service", "inactive", "loaded", "exit-code")
	h.fake.On("systemctl", "show").Reply(out)

	h.svc.housekeep(ctx, h.cfg.Current())
	open := openKeys(t, h)
	for _, resolved := range []string{"unit:active.service", "unit:done.service", "unit:gone.service"} {
		if open[resolved] {
			t.Errorf("%s should have resolved", resolved)
		}
	}
	for _, still := range []string{"unit:broken.service", "unit:stopped.service"} {
		if !open[still] {
			t.Errorf("%s must stay open", still)
		}
	}
}

func TestWatchRunsAPassEveryIntervalUntilCancelled(t *testing.T) {
	t.Parallel()
	h, _ := watchHarness(t)
	h.fake.On("systemctl", "list-units").Reply("")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { h.svc.Watch(ctx); close(done) }()
		time.Sleep(2*h.svc.every + time.Second)
		synctest.Wait()
		if n := len(h.fake.CallsMatching("systemctl", "list-units")); n != 2 {
			t.Fatalf("%d watch passes in two intervals, want 2", n)
		}
		cancel()
		<-done
	})
}

// churnProbe drives housekeep for one unit with scripted systemd properties.
type churnProbe struct {
	h             *harness
	unit          string
	restarts, inv int
	triggeredBy   string
}

func newChurnProbe(t *testing.T, unit, triggeredBy string) *churnProbe {
	h, _ := watchHarness(t)
	p := &churnProbe{h: h, unit: unit, triggeredBy: triggeredBy}
	h.fake.On("systemctl", "list-units").Reply(unit + " loaded inactive dead d\n")
	h.fake.On("systemctl", "show", unit, "--property=Id,NRestarts,InvocationID,TriggeredBy").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		out := fmt.Sprintf("Id=%s\nNRestarts=%d\nInvocationID=inv%d\nTriggeredBy=%s\n\n", unit, p.restarts, p.inv, p.triggeredBy)
		return cmdrunner.Result{Stdout: out, Output: out}, nil
	})
	return p
}

// minute advances one minute and runs a watch pass.
func (p *churnProbe) minute(restartsDelta, newInvocations int) {
	p.h.now = p.h.now.Add(time.Minute)
	p.restarts += restartsDelta
	p.inv += newInvocations
	p.h.svc.housekeep(context.Background(), p.h.cfg.Current())
}

func TestChurnCountsPathTriggeredOneshotLoops(t *testing.T) {
	t.Parallel()
	p := newChurnProbe(t, "port-sync.service", "port-sync.path")
	p.minute(0, 0) // baseline
	for range 4 {
		p.minute(0, 1) // re-triggered every minute; NRestarts never moves
	}
	if openKeys(t, p.h)["churn:port-sync.service"] {
		t.Fatal("4 starts must not open churn")
	}
	p.minute(0, 1)
	if !openKeys(t, p.h)["churn:port-sync.service"] {
		t.Fatal("5 path-triggered invocations in an hour must open churn")
	}
}

func TestChurnIgnoresTimerActivatedUnits(t *testing.T) {
	t.Parallel()
	p := newChurnProbe(t, "sync.service", "sync.timer")
	p.minute(0, 0)
	for i := range 60 {
		if i%5 == 0 {
			p.minute(0, 1) // every-5-minute timer: 12 invocations an hour
		} else {
			p.minute(0, 0)
		}
	}
	if openKeys(t, p.h)["churn:sync.service"] {
		t.Fatal("a timer cadence must not alert")
	}
}

func TestChurnStillCountsNRestartsOnTimerUnitsAndDoesNotDoubleCount(t *testing.T) {
	t.Parallel()
	// Restart= loop: NRestarts and InvocationID move together;
	// each restart counts once, so 4 do not alert and 5 do.
	p := newChurnProbe(t, "loop.service", "")
	p.minute(0, 0)
	for range 4 {
		p.minute(1, 1)
	}
	if openKeys(t, p.h)["churn:loop.service"] {
		t.Fatal("4 restarts counted as more (double count)")
	}
	p.minute(1, 1)
	if !openKeys(t, p.h)["churn:loop.service"] {
		t.Fatal("5 restarts must open churn")
	}
	// A timer-activated unit that crash-loops via Restart= is still caught.
	q := newChurnProbe(t, "timed.service", "timed.timer")
	q.minute(0, 0)
	q.minute(5, 5)
	if !openKeys(t, q.h)["churn:timed.service"] {
		t.Fatal("NRestarts growth on a timer unit must still alert")
	}
}
