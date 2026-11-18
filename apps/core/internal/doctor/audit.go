package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
)

// AlertAfter is how many consecutive failed runs open an alert.
const AlertAfter = 2

// ProblemScope is the notification key prefix of audit alerts
// ("audit:<check>").
const ProblemScope = "audit:"

// Streaks persists consecutive-failure counts per check between runs.
type Streaks interface {
	Load(ctx context.Context) (map[string]int, error)
	Save(ctx context.Context, streaks map[string]int) error
}

// memStreaks keeps streaks for one process only. With it, a check can never
// reach AlertAfter, so nothing alerts: the safe degradation when the runtime
// DB is unavailable.
type memStreaks struct{ m map[string]int }

// NewMemStreaks returns in-memory Streaks.
func NewMemStreaks() Streaks { return &memStreaks{m: map[string]int{}} }

func (s *memStreaks) Load(context.Context) (map[string]int, error) { return copyStreaks(s.m), nil }
func (s *memStreaks) Save(_ context.Context, m map[string]int) error {
	s.m = copyStreaks(m)
	return nil
}

type dbStreaks struct{ s *store.Store }

// NewDBStreaks keeps streaks in the doctor_streaks table.
func NewDBStreaks(s *store.Store) Streaks { return dbStreaks{s} }

func (d dbStreaks) Load(ctx context.Context) (map[string]int, error) {
	rows, err := d.s.Queries().ListDoctorStreaks(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.CheckName] = int(r.Fails)
	}
	return out, nil
}

func (d dbStreaks) Save(ctx context.Context, m map[string]int) error {
	now := time.Now().UnixMilli()
	return d.s.Tx(ctx, func(q *store.Queries) error {
		rows, err := q.ListDoctorStreaks(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if m[r.CheckName] == 0 {
				if err := q.DeleteDoctorStreak(ctx, r.CheckName); err != nil {
					return err
				}
			}
		}
		for name, n := range m {
			if n == 0 {
				continue
			}
			if err := q.UpsertDoctorStreak(ctx, store.UpsertDoctorStreakParams{CheckName: name, Fails: int64(n), UpdatedAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
}

func copyStreaks(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Advance applies one report to the previous streaks: a failure extends the
// check's streak, an ok resets it, an unknown leaves it untouched (it is no
// evidence either way). Checks absent from the report keep their streak.
func Advance(prev map[string]int, r Report) map[string]int {
	next := copyStreaks(prev)
	for _, res := range r.Results {
		switch res.Status {
		case Fail:
			next[res.Name]++
		case OK:
			delete(next, res.Name)
		}
	}
	return next
}

// Sink is the notifier as the audit uses it.
type Sink interface {
	OpenProblems(ctx context.Context) ([]notifications.ProblemState, error)
	Reconcile(ctx context.Context, scope string, current []notifications.Problem) (notifications.Result, error)
}

// Auditor turns reports into streak-based alerts.
type Auditor struct {
	Streaks Streaks
	// Sink is nil when no notifier could be built; streaks still advance.
	Sink Sink
	// Known are all check names; an open alert for a known check that this
	// run did not cover (a partial report) is left alone instead of resolved.
	Known []string
	Log   *slog.Logger
}

// Outcome is what Apply did. Delivery and Streak errors are informational:
// they are reported but never fail a run.
type Outcome struct {
	Opened   []string
	Resolved []string
	Delivery error
	Streak   error
}

// Apply advances the streaks and reconciles the audit:<check> problems:
// open after AlertAfter consecutive failures, resolve on ok, and leave
// unknown (or uncovered) checks as they are.
func (a Auditor) Apply(ctx context.Context, r Report) Outcome {
	var out Outcome
	prev, err := a.Streaks.Load(ctx)
	if err != nil {
		out.Streak = fmt.Errorf("loading streaks: %w", err)
		prev = map[string]int{}
	}
	next := Advance(prev, r)
	if err := a.Streaks.Save(ctx, next); err != nil {
		out.Streak = errors.Join(out.Streak, fmt.Errorf("saving streaks: %w", err))
	}
	if a.Sink == nil {
		out.Delivery = errors.New("no notifier available")
		return out
	}

	var current []notifications.Problem
	covered := map[string]bool{}
	var leaveOpen []string
	for _, res := range r.Results {
		covered[res.Name] = true
		switch {
		case res.Status == Fail && next[res.Name] >= AlertAfter:
			current = append(current, problemFor(res))
		case res.Status == Unknown:
			leaveOpen = append(leaveOpen, res.Name)
		}
	}
	for _, k := range a.Known {
		if !covered[k] {
			leaveOpen = append(leaveOpen, k)
		}
	}
	if len(leaveOpen) > 0 {
		open, err := a.Sink.OpenProblems(ctx)
		if err != nil {
			out.Delivery = fmt.Errorf("reading open problems: %w", err)
			return out
		}
		keep := map[string]bool{}
		for _, n := range leaveOpen {
			keep[ProblemScope+n] = true
		}
		for _, p := range open {
			if keep[p.Key] {
				current = append(current, notifications.Problem{Key: p.Key, Title: p.Title, Path: pathFor(strings.TrimPrefix(p.Key, ProblemScope))})
			}
		}
	}
	res, err := a.Sink.Reconcile(ctx, ProblemScope, current)
	out.Opened, out.Resolved = res.Opened, res.Resolved
	if err != nil {
		out.Delivery = err
		if a.Log != nil {
			a.Log.Warn("audit notification not delivered", "error", err.Error())
		}
	}
	return out
}

var checkPaths = map[string]string{
	"disk": "/storage", "smart": "/storage", "systemd-units": "/services", "daemon": "/services",
}

// checkLabels are the short, emoji-led names shown in notifications.
var checkLabels = map[string]string{
	"daemon": "🧠 Daemon", "caddy": "🌐 Caddy", "disk": "💽 Disk", "systemd-units": "⚙️ Units",
	"smart": "🩺 SMART", "cloudflare-tunnel": "☁️ Tunnel", "db-snapshot": "🗄️ DB snapshot",
}

func pathFor(check string) string {
	if p, ok := checkPaths[check]; ok {
		return p
	}
	return "/"
}

func problemFor(res Result) notifications.Problem {
	title, ok := checkLabels[res.Name]
	if !ok {
		title = "🔧 " + res.Name
	}
	return notifications.Problem{Key: ProblemScope + res.Name, Title: title, Detail: tidyDetail(res.Detail), Path: pathFor(res.Name)}
}

// tidyDetail trims a check detail for a phone screen: no ".service" noise,
// capped at 140 characters.
func tidyDetail(s string) string {
	s = strings.ReplaceAll(s, ".service", "")
	if r := []rune(s); len(r) > 140 {
		s = string(r[:139]) + "…"
	}
	return s
}

// DailyConfig wires one daily audit.
type DailyConfig struct {
	Checks  []Check
	Runner  Runner
	Auditor Auditor
	// Ping reports to the external healthcheck ("/start", "", "/fail").
	Ping func(context.Context, string) error
}

// DailyResult is everything a daily run produced. It has no error: a run that
// completed is a success regardless of failed checks or undeliverable alerts.
type DailyResult struct {
	Report   Report
	Outcome  Outcome
	PingErrs []error
}

// RunDaily pings start, runs the checks, applies the alerting rules and pings
// success. The healthcheck measures "did the audit run"; failed checks are
// alerts, not run failures.
func RunDaily(ctx context.Context, cfg DailyConfig) DailyResult {
	var res DailyResult
	ping := func(suffix string) {
		if cfg.Ping == nil {
			return
		}
		if err := cfg.Ping(ctx, suffix); err != nil {
			res.PingErrs = append(res.PingErrs, err)
		}
	}
	ping("/start")
	res.Report = cfg.Runner.Run(ctx, cfg.Checks)
	res.Outcome = cfg.Auditor.Apply(ctx, res.Report)
	ping("")
	return res
}

// DailySummary is the single journal line of a daily run. Delivery problems
// are reported as info here; the notifications store tracks failing channels.
func DailySummary(d DailyResult) string {
	s := "Daily health audit: " + Summary(d.Report)
	if len(d.Outcome.Opened)+len(d.Outcome.Resolved) > 0 {
		s += fmt.Sprintf(" · alerts: %d opened, %d resolved", len(d.Outcome.Opened), len(d.Outcome.Resolved))
	}
	if d.Outcome.Delivery != nil {
		s += " · info: alerts not delivered (" + oneLine(d.Outcome.Delivery) + ")"
	}
	if d.Outcome.Streak != nil {
		s += " · info: streak state unavailable (" + oneLine(d.Outcome.Streak) + ")"
	}
	if len(d.PingErrs) > 0 {
		s += " · info: healthcheck ping failed (" + oneLine(errors.Join(d.PingErrs...)) + ")"
	}
	return s
}

func oneLine(err error) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(err.Error(), "\n", "; ")), " ")
	if len(s) > 160 {
		s = s[:159] + "…"
	}
	return s
}

// NewHealthcheckPinger returns the Healthchecks.io ping function for uuid,
// or nil (no ping) when uuid is empty: Healthchecks.io is optional.
func NewHealthcheckPinger(client *http.Client, uuid string) func(context.Context, string) error {
	if uuid == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context, suffix string) error {
		if uuid == "" {
			return errors.New("healthcheck UUID is not configured")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://hc-ping.com/"+uuid+suffix, nil)
		if err != nil {
			return errors.New("create Healthchecks.io request")
		}
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("Healthchecks.io request failed")
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("Healthchecks.io returned HTTP %d", resp.StatusCode)
		}
		return nil
	}
}
