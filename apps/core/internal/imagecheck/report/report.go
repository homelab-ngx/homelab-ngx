// Package report prints the image check's progress: a heading per step
// with the progress line scripts/run reads, ✓ for what holds, ✗ for what
// does not, and notes.
package report

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Printer writes the run's lines. Several scenarios may print at once; with
// Prefix set each line names its scenario.
type Printer struct {
	W      io.Writer
	Total  int  // the last step's number
	Prefix bool // name the scenario on every line (scenarios run at once)

	mu    sync.Mutex
	done  map[int]bool
	start time.Time
}

// New is a printer for a run of total steps.
func New(w io.Writer, total int) *Printer {
	return &Printer{W: w, Total: total, done: map[int]bool{}, start: time.Now()}
}

// Took is a duration as the run prints them: 3 min 07 s.
func Took(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d min %02d s", s/60, s%60)
}

// Since is how long the run has taken.
func (p *Printer) Since() time.Duration { return time.Since(p.start) }

func (p *Printer) line(scenario, s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range strings.Split(s, "\n") {
		if p.Prefix && scenario != "" && l != "" {
			l = "[" + scenario + "] " + l
		}
		fmt.Fprintln(p.W, l)
	}
}

// Step prints step n's heading. The progress is the share of steps begun
// before it, over the whole run.
func (p *Printer) Step(scenario string, n int, title string) {
	p.mu.Lock()
	pct := len(p.done) * 100 / p.Total
	p.done[n] = true
	p.mu.Unlock()
	p.line(scenario, fmt.Sprintf("\nprogress: %d%%\n▸ %d. %s", pct, n, title))
}

// Took prints how long a step took.
func (p *Printer) Took(scenario string, n int, d time.Duration) {
	p.line(scenario, fmt.Sprintf("  step %d took %s", n, Took(d)))
}

// OK prints what holds.
func (p *Printer) OK(scenario, msg string) { p.line(scenario, "  ✓ "+msg) }

// Fail prints what does not.
func (p *Printer) Fail(scenario, msg string) { p.line(scenario, "  ✗ "+msg) }

// Note prints a line of its own.
func (p *Printer) Note(scenario, msg string) { p.line(scenario, "  "+msg) }

// Plain prints a line as it is.
func (p *Printer) Plain(msg string) { p.line("", msg) }
