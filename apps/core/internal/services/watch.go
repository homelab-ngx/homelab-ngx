package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/notifications"
)

// Unit watching, the minute watch pass (Watch):
//
//   - churn:<unit> opens when a unit started at least churnThreshold times
//     within the last hour, and resolves after a full quiet hour. It replaces
//     the doctor's journal-scanning unit-churn check; both signals are
//     property reads (one `systemctl show`), not journal scans.
//     A "start" is the larger of two per-sample signals: growth of the
//     NRestarts counter (Restart= loops), and
//     a changed InvocationID, which also catches oneshot units re-triggered by
//     a .path unit (NRestarts never moves for those; the original port-sync
//     incident). Taking the max keeps a Restart= restart from counting twice.
//     InvocationID changes are NOT counted for units with a .timer or .socket
//     in TriggeredBy: those start on a schedule or on demand by design, so a
//     5-minute timer must not alert. (Limitation: a path-triggered loop on a
//     unit that also has a timer is only caught through NRestarts.) The
//     sampling is once a minute, so a loop faster than that counts as one
//     start per sample, which still crosses the threshold in five minutes.
//   - unit:<unit> problems (opened by the OnFailure= notifier) resolve once
//     the unit is active, finished successfully, or no longer exists, so a
//     transient failure never leaves a stale alert behind.
const (
	churnWindow    = time.Hour
	churnThreshold = 5
)

const (
	churnPrefix = "churn:"
	unitPrefix  = "unit:"
)

type restartSample struct {
	at time.Time
	n  int
}

// churnTracker turns periodic NRestarts samples into "restarts in the last
// hour". NRestarts resets to zero when a unit is started by hand, so a drop
// counts the new value as restarts since the reset.
type churnTracker struct {
	samples map[string][]restartSample
	prev    map[string]*unitPrev
}

// observe records a sample and returns the restarts seen in the trailing
// churnWindow and how long this unit has been observed (capped by the window
// a caller cares about).
func (t *churnTracker) observe(unit string, n int, at time.Time) (restarts int, observed time.Duration) {
	if t.samples == nil {
		t.samples = map[string][]restartSample{}
	}
	s := t.samples[unit]
	switch {
	case len(s) >= 2 && s[len(s)-1].n == n && s[len(s)-2].n == n:
		s[len(s)-1].at = at // collapse a run of equal values
	default:
		s = append(s, restartSample{at, n})
	}
	// Drop samples that fall before the window, keeping the newest of them as
	// the baseline for the first in-window increment.
	cut := at.Add(-churnWindow)
	first := 0
	for first+1 < len(s) && !s[first+1].at.After(cut) {
		first++
	}
	s = s[first:]
	t.samples[unit] = s

	for i := 1; i < len(s); i++ {
		if s[i].n >= s[i-1].n {
			restarts += s[i].n - s[i-1].n
		} else {
			restarts += s[i].n // counter reset by a manual start
		}
	}
	return restarts, at.Sub(s[0].at)
}

func (t *churnTracker) forgetExcept(live map[string]bool) {
	for u := range t.samples {
		if !live[u] {
			delete(t.samples, u)
			delete(t.prev, u)
		}
	}
}

// unitPrev is the previous raw sample of a unit plus a synthetic monotone
// "starts" counter built from it.
type unitPrev struct {
	n      int
	inv    string
	starts int
}

// sample folds one raw reading (NRestarts, InvocationID) into the unit's
// synthetic starts counter and returns observe's result for it. countInv
// says whether InvocationID changes count (false for timer/socket units).
// The first sample of a unit is only a baseline.
func (t *churnTracker) sample(unit string, n int, inv string, countInv bool, at time.Time) (int, time.Duration) {
	if t.prev == nil {
		t.prev = map[string]*unitPrev{}
	}
	p, seen := t.prev[unit]
	if !seen {
		p = &unitPrev{n: n, inv: inv}
		t.prev[unit] = p
	} else {
		inc := n - p.n
		if inc < 0 {
			inc = n // counter reset by a manual start
		}
		if countInv && inc == 0 && inv != "" && inv != p.inv {
			inc = 1
		}
		p.starts += inc
		p.n, p.inv = n, inv
	}
	return t.observe(unit, p.starts, at)
}

// Watch runs the watch pass every minute until ctx ends.
func (s *Service) Watch(ctx context.Context) {
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.housekeep(ctx, s.cfg.Current())
		}
	}
}

// housekeep runs one watch pass.
func (s *Service) housekeep(ctx context.Context, snap *config.Snapshot) {
	if s.notify == nil || !s.notify.Enabled() {
		return
	}
	open, err := s.notify.OpenProblems(ctx)
	if err != nil {
		s.log.Debug("cannot read open problems", "error", err)
		return
	}
	s.watchChurn(ctx, open)
	s.resolveUnitProblems(ctx, open)
}

func (s *Service) watchChurn(ctx context.Context, open []notifications.ProblemState) {
	units, err := s.sd.ServiceUnits(ctx)
	if err != nil {
		s.log.Debug("cannot list service units", "error", err)
		return
	}
	openChurn := map[string]bool{}
	for _, p := range open {
		if u, ok := strings.CutPrefix(p.Key, churnPrefix); ok {
			openChurn[u] = true
		}
	}
	live := make(map[string]bool, len(units))
	for _, u := range units {
		live[u] = true
	}
	s.churn.forgetExcept(live)

	now := s.now()
	props := s.sd.Props(ctx, units, "Id", "NRestarts", "InvocationID", "TriggeredBy")
	for _, u := range units {
		raw, ok := props[u]["NRestarts"]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		restarts, observed := s.churn.sample(u, n, props[u]["InvocationID"], !scheduledUnit(props[u]["TriggeredBy"]), now)
		key := churnPrefix + u
		switch {
		case restarts >= churnThreshold:
			if _, err := s.notify.Report(ctx, notifications.Problem{
				Key:    key,
				Title:  unitLabel(u) + " keeps restarting",
				Detail: fmt.Sprintf("%d starts in the last hour", restarts),
				Path:   "/settings/logs?unit=" + strings.TrimSuffix(u, ".service"),
			}); err != nil {
				s.log.Warn("failed to report unit churn", "unit", u, "error", err)
			}
		case restarts == 0 && observed >= churnWindow && openChurn[u]:
			if _, err := s.notify.Resolve(ctx, key); err != nil {
				s.log.Warn("failed to resolve unit churn", "unit", u, "error", err)
			}
		}
	}
	// A unit that no longer exists cannot be churning.
	for u := range openChurn {
		if !live[u] {
			_, _ = s.notify.Resolve(ctx, churnPrefix+u)
		}
	}
}

func (s *Service) resolveUnitProblems(ctx context.Context, open []notifications.ProblemState) {
	var units []string
	for _, p := range open {
		if u, ok := strings.CutPrefix(p.Key, unitPrefix); ok {
			units = append(units, u)
		}
	}
	if len(units) == 0 {
		return
	}
	props := s.sd.Props(ctx, units, "Id", "ActiveState", "LoadState", "Result")
	for _, u := range units {
		p, ok := props[u]
		if !ok {
			continue // systemctl failed; try again next pass
		}
		if unitSettled(p) {
			if _, err := s.notify.Resolve(ctx, unitPrefix+u); err != nil {
				s.log.Warn("failed to resolve unit problem", "unit", u, "error", err)
			}
		}
	}
}

// unitSettled reports whether a unit no longer warrants a "unit failed"
// alert: it is running, it finished with exit 0, or it is gone.
func unitSettled(p map[string]string) bool {
	switch {
	case p["LoadState"] == "not-found":
		return true
	case p["ActiveState"] == "active":
		return true
	case p["ActiveState"] == "inactive" && p["Result"] == "success":
		return true
	}
	return false
}

// scheduledUnit reports whether a unit is started by a timer or socket
// (TriggeredBy lists the activating units), i.e. repeated starts are by design.
func scheduledUnit(triggeredBy string) bool {
	for _, t := range strings.Fields(triggeredBy) {
		if strings.HasSuffix(t, ".timer") || strings.HasSuffix(t, ".socket") {
			return true
		}
	}
	return false
}

func unitLabel(unit string) string {
	return strings.TrimSuffix(unit, ".service")
}
