package stacks

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

// shuttingDown refuses an operation or a boot while the host reboots or
// powers off. A new problem each time: httpx fills in the request's path.
func shuttingDown() error { return httpx.Conflict("shutting_down", "the host is shutting down") }

// BeginShutdown stops the stacks from changing while the host shuts down: no
// operation begins, no container boots and the monitor restarts nothing.
// An operation already running goes on, but cannot boot a container.
func (s *Service) BeginShutdown() {
	s.mu.Lock()
	s.goingDown = true
	s.mu.Unlock()
}

// CancelShutdown undoes BeginShutdown: a shutdown that was cancelled. The monitor then starts
// the stacks the operator left running.
func (s *Service) CancelShutdown() {
	s.mu.Lock()
	s.goingDown = false
	s.mu.Unlock()
}

// Busy names the stack operations running now, for the operator to see
// before a reboot interrupts them. A health check is not one.
func (s *Service) Busy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for name, o := range s.ops {
		if o.finished.IsZero() && !o.check {
			out = append(out, fmt.Sprintf("stack %s: %s (%s)", name, o.kind, o.phase))
		}
	}
	sort.Strings(out)
	return out
}

// Running names the stacks whose container is up, or on its way up or down.
func (s *Service) Running(ctx context.Context) ([]string, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if !s.isStopped(ctx, r.Name) {
			out = append(out, r.Name)
		}
	}
	return out, nil
}

// Halt stops a stack's container for a shutdown. Unlike a stop it records
// nothing: the stack still wants to run, so the next boot starts it again.
// Its routes and firewall rules go with the reboot.
func (s *Service) Halt(ctx context.Context, name string) error {
	return s.halt(ctx, name)
}

// Holdup names what a halting stack still waits for and when systemd kills
// it. The unit is the one inside the stack whose stop would end last, or the
// stack's own unit once nothing inside answers. Two deadlines count: systemd
// in the stack kills a unit after its TimeoutStopSec, and the host kills the
// whole container after haltTimeout, whichever comes first. ok is false when
// the stack is not stopping.
func (s *Service) Holdup(ctx context.Context, name string) (unit, description string, killAt time.Time, ok bool) {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "systemctl", Args: append([]string{"show"}, stoppingProps(unitName(name))...)})
	if err != nil {
		return "", "", time.Time{}, false
	}
	host := stoppingUnits(res.Stdout)
	if len(host) == 0 || host[0].killAt.IsZero() {
		return "", "", time.Time{}, false
	}
	unit, killAt = host[0].id, host[0].killAt
	res, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "systemctl", Args: []string{
		"--machine=" + name, "list-units", "--state=deactivating", "--plain", "--no-legend", "--full",
	}})
	if err != nil {
		return unit, "", killAt, true
	}
	var names []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	if len(names) == 0 {
		return unit, "", killAt, true
	}
	res, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "systemctl", Args: append([]string{"--machine=" + name, "show"}, stoppingProps(names...)...)})
	if err != nil {
		return unit, "", killAt, true
	}
	var last *stoppingUnit
	for _, u := range stoppingUnits(res.Stdout) {
		if u.killAt.IsZero() || u.killAt.After(killAt) {
			u.killAt = killAt // no limit of its own, or past the container's
		}
		if last == nil || u.killAt.After(last.killAt) {
			last = &u
		}
	}
	if last == nil {
		return unit, "", killAt, true
	}
	return last.id, last.desc, last.killAt, true
}

// stoppingUnit is a unit on its way down, and when systemd kills it; zero
// when its stop has no time limit.
type stoppingUnit struct {
	id, desc string
	killAt   time.Time
}

// stoppingProps is `systemctl show`'s arguments for stoppingUnits.
func stoppingProps(units ...string) []string {
	return append([]string{"--property=Id,Description,ActiveState,StateChangeTimestamp,TimeoutStopUSec", "--timestamp=unix", "--"}, units...)
}

// stoppingUnits reads the units in `systemctl show` output that are
// deactivating. systemd restarts a stop's timer at each step of it (stop,
// SIGTERM, the final SIGTERM), and each step changes the state, so the kill
// is due TimeoutStopSec after the last change.
func stoppingUnits(stdout string) []stoppingUnit {
	var out []stoppingUnit
	for _, block := range strings.Split(stdout, "\n\n") {
		p := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				p[k] = v
			}
		}
		if p["Id"] == "" || p["ActiveState"] != "deactivating" {
			continue
		}
		u := stoppingUnit{id: p["Id"], desc: p["Description"]}
		sec, err := strconv.ParseInt(strings.TrimPrefix(p["StateChangeTimestamp"], "@"), 10, 64)
		if d, ok := timespan(p["TimeoutStopUSec"]); err == nil && sec > 0 && ok && d > 0 {
			u.killAt = time.Unix(sec, 0).Add(d)
		}
		out = append(out, u)
	}
	return out
}

// timespanUnits are the units systemd writes a time span in.
var timespanUnits = map[string]time.Duration{
	"us": time.Microsecond, "ms": time.Millisecond, "s": time.Second, "min": time.Minute,
	"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

// timespan reads a time span as systemd writes it: "1min 30s", "500ms".
// ok is false for "infinity" and anything else it cannot read.
func timespan(v string) (time.Duration, bool) {
	f := strings.Fields(v)
	if len(f) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, part := range f {
		i := strings.IndexFunc(part, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if i <= 0 {
			return 0, false
		}
		n, err := strconv.ParseFloat(part[:i], 64)
		unit, ok := timespanUnits[part[i:]]
		if err != nil || !ok {
			return 0, false
		}
		total += time.Duration(n * float64(unit))
	}
	return total, true
}
