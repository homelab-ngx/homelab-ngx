package doctor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

// fakeSink models the notifier's audit: scope: state.
type fakeSink struct {
	open       map[string]string // key -> title
	reconciles [][]string
	err        error
}

func (f *fakeSink) OpenProblems(context.Context) ([]notifications.ProblemState, error) {
	var out []notifications.ProblemState
	for k, v := range f.open {
		out = append(out, notifications.ProblemState{Key: k, Title: v})
	}
	return out, nil
}

func (f *fakeSink) Reconcile(_ context.Context, scope string, current []notifications.Problem) (notifications.Result, error) {
	var res notifications.Result
	var keys []string
	seen := map[string]bool{}
	for _, p := range current {
		keys = append(keys, p.Key)
		seen[p.Key] = true
		if _, ok := f.open[p.Key]; !ok {
			res.Opened = append(res.Opened, p.Key)
			if f.err == nil {
				f.open[p.Key] = p.Title
			}
		}
	}
	for k := range f.open {
		if strings.HasPrefix(k, scope) && !seen[k] {
			res.Resolved = append(res.Resolved, k)
			if f.err == nil {
				delete(f.open, k)
			}
		}
	}
	f.reconciles = append(f.reconciles, keys)
	return res, f.err
}

func report(rs ...Result) Report {
	r := Report{Results: rs}
	for _, x := range rs {
		switch x.Status {
		case OK:
			r.Passed++
		case Fail:
			r.Failed++
		default:
			r.Unknown++
		}
	}
	return r
}

func res(name string, st Status) Result {
	return Result{Name: name, Status: st, Detail: name + " detail"}
}

func newAuditor(sink Sink, known ...string) Auditor {
	return Auditor{Streaks: NewMemStreaks(), Sink: sink, Known: known}
}

func TestAdvance(t *testing.T) {
	t.Parallel()
	prev := map[string]int{"disk": 1, "smart": 3, "caddy": 2, "other": 4}
	got := Advance(prev, report(res("disk", Fail), res("smart", Unknown), res("caddy", OK)))
	want := map[string]int{"disk": 2, "smart": 3, "other": 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if prev["disk"] != 1 {
		t.Fatal("Advance mutated its input")
	}
}

func TestAlertOpensOnlyAfterTwoConsecutiveFailures(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{open: map[string]string{}}
	a := newAuditor(sink, "disk")
	ctx := context.Background()

	if out := a.Apply(ctx, report(res("disk", Fail))); len(out.Opened) != 0 {
		t.Fatalf("a single failure must not alert: %+v", out)
	}
	out := a.Apply(ctx, report(res("disk", Fail)))
	if !reflect.DeepEqual(out.Opened, []string{"audit:disk"}) {
		t.Fatalf("second failure should open audit:disk: %+v", out)
	}
	if sink.open["audit:disk"] != "💽 Disk" {
		t.Fatalf("title = %q", sink.open["audit:disk"])
	}
}

func TestAnOKRunResolvesAndResetsTheStreak(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{open: map[string]string{}}
	a := newAuditor(sink, "disk")
	ctx := context.Background()
	a.Apply(ctx, report(res("disk", Fail)))
	a.Apply(ctx, report(res("disk", Fail)))
	out := a.Apply(ctx, report(res("disk", OK)))
	if !reflect.DeepEqual(out.Resolved, []string{"audit:disk"}) {
		t.Fatalf("ok should resolve: %+v", out)
	}
	// The streak restarted: one new failure is not enough again.
	if out := a.Apply(ctx, report(res("disk", Fail))); len(out.Opened) != 0 {
		t.Fatalf("streak survived an ok run: %+v", out)
	}
}

func TestUnknownNeverOpensResolvesOrResetsAnything(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{open: map[string]string{}}
	a := newAuditor(sink, "disk", "smart")
	ctx := context.Background()

	// unknown does not open ...
	for range 3 {
		if out := a.Apply(ctx, report(res("smart", Unknown), res("disk", OK))); len(out.Opened) != 0 {
			t.Fatalf("unknown opened an alert: %+v", out)
		}
	}
	// ... and does not break a failing streak.
	a.Apply(ctx, report(res("disk", Fail)))
	a.Apply(ctx, report(res("disk", Unknown)))
	out := a.Apply(ctx, report(res("disk", Fail)))
	if !reflect.DeepEqual(out.Opened, []string{"audit:disk"}) {
		t.Fatalf("unknown reset the streak: %+v", out)
	}
	// An open alert stays open through an unknown run.
	out = a.Apply(ctx, report(res("disk", Unknown)))
	if len(out.Resolved) != 0 || sink.open["audit:disk"] == "" {
		t.Fatalf("unknown resolved an open alert: %+v open=%v", out, sink.open)
	}
}

func TestPartialReportLeavesOtherChecksAlertsAndResolvesObsoleteOnes(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{open: map[string]string{
		"audit:disk":    "💽 Disk",
		"audit:backups": "💾 Backups", // a check the old doctor had
	}}
	a := newAuditor(sink, "disk", "smart")
	out := a.Apply(context.Background(), report(res("smart", OK)))
	if !reflect.DeepEqual(out.Resolved, []string{"audit:backups"}) {
		t.Fatalf("resolved = %v", out.Resolved)
	}
	if sink.open["audit:disk"] == "" {
		t.Fatal("a check this partial run did not cover lost its alert")
	}
}

func TestDeliveryFailureIsReportedNotFatal(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{open: map[string]string{}, err: errors.New("ntfy: HTTP 500; email: refused")}
	a := newAuditor(sink, "disk")
	ctx := context.Background()
	a.Apply(ctx, report(res("disk", Fail)))
	out := a.Apply(ctx, report(res("disk", Fail)))
	if out.Delivery == nil {
		t.Fatal("delivery error was swallowed")
	}
	// No notifier at all is the same: informational.
	out = Auditor{Streaks: NewMemStreaks(), Known: []string{"disk"}}.Apply(ctx, report(res("disk", Fail)))
	if out.Delivery == nil {
		t.Fatal("missing sink not reported")
	}
}

func TestDBStreaksPersistAcrossHandles(t *testing.T) {
	t.Parallel()
	path := storetest.Path(t)
	open := func() *store.Store {
		s, err := store.Open(context.Background(), path, store.Options{Migrate: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ctx := context.Background()
	s1 := open()
	if err := NewDBStreaks(s1).Save(ctx, map[string]int{"disk": 1, "smart": 2}); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	s2 := open()
	defer s2.Close()
	got, err := NewDBStreaks(s2).Load(ctx)
	if err != nil || !reflect.DeepEqual(got, map[string]int{"disk": 1, "smart": 2}) {
		t.Fatalf("got %v %v", got, err)
	}
	// Saving a map without a key (or with zero) deletes the row.
	if err := NewDBStreaks(s2).Save(ctx, map[string]int{"disk": 2}); err != nil {
		t.Fatal(err)
	}
	got, _ = NewDBStreaks(s2).Load(ctx)
	if !reflect.DeepEqual(got, map[string]int{"disk": 2}) {
		t.Fatalf("got %v", got)
	}
}

func TestRunDailyNeverFailsAndPingsStartAndSuccess(t *testing.T) {
	t.Parallel()
	var pings []string
	sink := &fakeSink{open: map[string]string{}, err: errors.New("down")}
	r := RunDaily(context.Background(), DailyConfig{
		Checks:  []Check{fixed("disk", Fail), fixed("smart", Unknown)},
		Auditor: newAuditor(sink, "disk", "smart"),
		Ping:    func(_ context.Context, s string) error { pings = append(pings, s); return errors.New("hc down") },
	})
	if strings.Join(pings, ",") != "/start," {
		t.Fatalf("pings = %v", pings)
	}
	if r.Report.Failed != 1 || r.Report.Unknown != 1 || len(r.PingErrs) != 2 {
		t.Fatalf("result = %+v", r)
	}
	line := DailySummary(r)
	if strings.Contains(line, "\n") || !strings.Contains(line, "1 failed (disk)") || !strings.Contains(line, "info: healthcheck ping failed") {
		t.Fatalf("summary = %q", line)
	}
}
