package doctor

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type stub struct {
	name string
	run  func(ctx context.Context) Result
}

func (s stub) Name() string                   { return s.name }
func (s stub) Run(ctx context.Context) Result { return s.run(ctx) }

func fixed(name string, st Status) Check {
	return stub{name, func(context.Context) Result { return Result{Status: st, Detail: "d"} }}
}

func TestRunnerCountsTriState(t *testing.T) {
	t.Parallel()
	rep := Runner{}.Run(context.Background(), []Check{fixed("a", OK), fixed("b", Fail), fixed("c", Unknown), fixed("d", OK)})
	if rep.Passed != 2 || rep.Failed != 1 || rep.Unknown != 1 {
		t.Fatalf("report = %+v", rep)
	}
	for i, want := range []string{"a", "b", "c", "d"} {
		if rep.Results[i].Name != want {
			t.Fatalf("order: %+v", rep.Results)
		}
	}
}

func TestRunnerTimeoutIsUnknownEvenWhenCheckIgnoresContext(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		defer close(block)
		hang := stub{"hang", func(context.Context) Result { <-block; return Result{Status: OK} }}
		start := time.Now()
		rep := Runner{CheckTimeout: 50 * time.Millisecond, Budget: 5 * time.Second}.Run(context.Background(), []Check{hang, fixed("ok", OK)})
		if time.Since(start) > 2*time.Second {
			t.Fatalf("runner waited for the hung check: %s", time.Since(start))
		}
		if rep.Results[0].Status != Unknown || !strings.Contains(rep.Results[0].Detail, "timed out") {
			t.Fatalf("hung check = %+v", rep.Results[0])
		}
		if rep.Results[1].Status != OK {
			t.Fatalf("other check disturbed: %+v", rep.Results[1])
		}
	})
}

func TestRunnerBudgetExpiryIsUnknown(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		slow := stub{"slow", func(ctx context.Context) Result { <-ctx.Done(); return Result{Status: OK} }}
		rep := Runner{CheckTimeout: 5 * time.Second, Budget: 50 * time.Millisecond}.Run(context.Background(), []Check{slow})
		if rep.Results[0].Status != Unknown || !strings.Contains(rep.Results[0].Detail, "budget") {
			t.Fatalf("result = %+v", rep.Results[0])
		}
	})
}

func TestRunnerDoesNotTrustAFailureReportedAfterExpiry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		late := stub{"late", func(ctx context.Context) Result {
			<-ctx.Done()
			return Result{Status: Fail, Detail: "context deadline exceeded"}
		}}
		rep := Runner{CheckTimeout: 30 * time.Millisecond}.Run(context.Background(), []Check{late})
		if rep.Results[0].Status != Unknown || rep.Failed != 0 {
			t.Fatalf("report = %+v", rep)
		}
	})
}

func TestRunnerRunsChecksConcurrently(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		nap := func(name string) Check {
			return stub{name, func(context.Context) Result { time.Sleep(150 * time.Millisecond); return Result{Status: OK} }}
		}
		start := time.Now()
		rep := Runner{}.Run(context.Background(), []Check{nap("a"), nap("b"), nap("c"), nap("d")})
		if d := time.Since(start); d > 450*time.Millisecond || rep.Passed != 4 {
			t.Fatalf("took %s (sequential would be 600ms), report = %+v", d, rep)
		}
	})
}

func TestRunnerTurnsPanicIntoUnknown(t *testing.T) {
	t.Parallel()
	boom := stub{"boom", func(context.Context) Result { panic("kaboom") }}
	rep := Runner{}.Run(context.Background(), []Check{boom})
	if rep.Results[0].Status != Unknown || !strings.Contains(rep.Results[0].Detail, "kaboom") {
		t.Fatalf("result = %+v", rep.Results[0])
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()
	cs := []Check{fixed("a", OK), fixed("b", OK), fixed("c", OK)}
	got, err := Select(cs, []string{"c", "a"})
	if err != nil || len(got) != 2 || got[0].Name() != "c" {
		t.Fatalf("got %v %v", Names(got), err)
	}
	if all, _ := Select(cs, nil); len(all) != 3 {
		t.Fatal("empty selection should keep every check")
	}
	if _, err := Select(cs, []string{"zzz"}); err == nil || !strings.Contains(err.Error(), "a, b, c") {
		t.Fatalf("err = %v", err)
	}
}

func TestFormatReport(t *testing.T) {
	t.Parallel()
	rep := Report{Passed: 1, Failed: 1, Unknown: 1, Results: []Result{
		{Name: "caddy", Status: OK, Detail: "HTTP 200"},
		{Name: "daemon", Status: Fail, Detail: "inactive", Fix: "sudo systemctl start homelab-daemon.service"},
		{Name: "smart", Status: Unknown, Detail: "scrutiny unreachable", Fix: "ignored for unknown"},
	}}
	body := FormatReportText(rep)
	for _, want := range []string{"✔ caddy", "✗ daemon", "→ sudo systemctl", "? smart", "scrutiny unreachable"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "HTTP 200") || strings.Contains(body, "ignored for unknown") {
		t.Errorf("passing detail / unknown fix leaked:\n%s", body)
	}
	if v := FormatReportVerbose(rep); !strings.Contains(v, "HTTP 200") {
		t.Errorf("verbose lost passing detail:\n%s", v)
	}
}

func TestSummaryIsOneLine(t *testing.T) {
	t.Parallel()
	s := Summary(Report{Passed: 5, Failed: 1, Unknown: 1, Results: []Result{
		{Name: "disk", Status: Fail}, {Name: "smart", Status: Unknown},
	}})
	if strings.Contains(s, "\n") || s != "5 ok · 1 failed (disk) · 1 unknown (smart)" {
		t.Fatalf("summary = %q", s)
	}
}
