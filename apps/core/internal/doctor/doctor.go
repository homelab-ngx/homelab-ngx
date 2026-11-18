// Package doctor is the outside view of the host's health: the few things the
// daemon cannot report about itself (is it alive, is the proxy answering, is
// the disk full, did something it does not supervise fail, are the backups of
// its database fresh). Everything the daemon supervises -- services, restarts,
// backups, pools, remote access -- it already alerts on, so it is not checked
// here.
//
// A run is bounded: checks run concurrently, each gets a short timeout, and the
// whole run has a budget. A check that cannot reach a confident answer reports
// Unknown, never Fail.
package doctor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status is the tri-state outcome of one check.
type Status string

const (
	// OK means the check confirmed things are fine.
	OK Status = "ok"
	// Fail means the check confirmed a problem.
	Fail Status = "fail"
	// Unknown means the check could not tell (timeout, an unreachable
	// dependency). It never alerts and never resolves an alert.
	Unknown Status = "unknown"
)

const (
	// CheckTimeout bounds one check; exceeding it makes the result Unknown.
	CheckTimeout = 5 * time.Second
	// Budget bounds a whole run.
	Budget = 10 * time.Second
)

// Result is the outcome of a single health check.
type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the aggregate outcome of a run.
type Report struct {
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Unknown int      `json:"unknown"`
	Results []Result `json:"results"`
}

// Check is one health check. Run must honour ctx; the runner also enforces
// the timeout from outside in case it does not.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// Runner runs checks concurrently. Zero fields mean the package defaults.
type Runner struct {
	CheckTimeout time.Duration
	Budget       time.Duration
}

// Run executes every check and returns the report in the order of checks.
func (r Runner) Run(ctx context.Context, checks []Check) Report {
	ct, budget := r.CheckTimeout, r.Budget
	if ct <= 0 {
		ct = CheckTimeout
	}
	if budget <= 0 {
		budget = Budget
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runOne(bctx, c, ct)
		}()
	}
	wg.Wait()

	rep := Report{Results: results}
	for _, res := range results {
		switch res.Status {
		case OK:
			rep.Passed++
		case Fail:
			rep.Failed++
		default:
			rep.Unknown++
		}
	}
	return rep
}

func runOne(bctx context.Context, c Check, timeout time.Duration) Result {
	cctx, cancel := context.WithTimeout(bctx, timeout)
	defer cancel()

	done := make(chan Result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- Result{Status: Unknown, Detail: fmt.Sprintf("check panicked: %v", p)}
			}
		}()
		done <- c.Run(cctx)
	}()

	var res Result
	select {
	case res = <-done:
		// A "failure" reported only because the clock ran out is not confirmed.
		if res.Status == Fail && cctx.Err() != nil {
			res = Result{Status: Unknown, Detail: expiry(bctx, timeout)}
		}
	case <-cctx.Done():
		res = Result{Status: Unknown, Detail: expiry(bctx, timeout)}
	}
	if res.Status == "" {
		res.Status = Unknown
	}
	res.Name = c.Name()
	return res
}

func expiry(bctx context.Context, timeout time.Duration) string {
	if bctx.Err() != nil {
		return "run budget exceeded"
	}
	return fmt.Sprintf("timed out after %s", timeout)
}

// Names lists the check names in order.
func Names(checks []Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name()
	}
	return out
}

// Select returns the checks named by slugs (all of them when slugs is empty).
// An unknown slug is an error naming the valid ones.
func Select(checks []Check, slugs []string) ([]Check, error) {
	if len(slugs) == 0 {
		return checks, nil
	}
	byName := make(map[string]Check, len(checks))
	for _, c := range checks {
		byName[c.Name()] = c
	}
	var out []Check
	for _, s := range slugs {
		c, ok := byName[s]
		if !ok {
			return nil, fmt.Errorf("unknown check %q (valid: %s)", s, strings.Join(Names(checks), ", "))
		}
		out = append(out, c)
	}
	return out, nil
}

func mark(s Status) string {
	switch s {
	case OK:
		return "✔"
	case Fail:
		return "✗"
	default:
		return "?"
	}
}

// FormatReportText renders a Report compactly: a one-line verdict, failures
// and unknowns expanded, every passing check folded into one line.
func FormatReportText(r Report) string {
	var b strings.Builder
	switch {
	case r.Failed == 0 && r.Unknown == 0:
		fmt.Fprintf(&b, "🏥 Doctor · all %d checks passed\n", r.Passed)
	default:
		fmt.Fprintf(&b, "🏥 Doctor · %d passed · %d failed · %d unknown\n", r.Passed, r.Failed, r.Unknown)
	}
	var passing []string
	for _, res := range r.Results {
		if res.Status == OK {
			passing = append(passing, res.Name)
			continue
		}
		fmt.Fprintf(&b, "\n%s %s\n", mark(res.Status), res.Name)
		if res.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", res.Detail)
		}
		if res.Fix != "" && res.Status == Fail {
			fmt.Fprintf(&b, "  → %s\n", res.Fix)
		}
	}
	if len(passing) > 0 {
		fmt.Fprintf(&b, "\n✔ %s\n", strings.Join(passing, " · "))
	}
	return b.String()
}

// FormatReportVerbose renders every check with its detail (doctor --verbose).
func FormatReportVerbose(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🏥 Doctor · %d passed · %d failed · %d unknown\n", r.Passed, r.Failed, r.Unknown)
	for _, res := range r.Results {
		fmt.Fprintf(&b, "\n%s %s\n", mark(res.Status), res.Name)
		if res.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", res.Detail)
		}
		if res.Status == Fail && res.Fix != "" {
			fmt.Fprintf(&b, "  → %s\n", res.Fix)
		}
	}
	return b.String()
}

// Summary is the one-line verdict the daily audit writes to the journal.
func Summary(r Report) string {
	names := func(s Status) string {
		var n []string
		for _, res := range r.Results {
			if res.Status == s {
				n = append(n, res.Name)
			}
		}
		if len(n) == 0 {
			return ""
		}
		return " (" + strings.Join(n, ", ") + ")"
	}
	return fmt.Sprintf("%d ok · %d failed%s · %d unknown%s", r.Passed, r.Failed, names(Fail), r.Unknown, names(Unknown))
}
