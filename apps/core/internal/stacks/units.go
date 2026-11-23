package stacks

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/services"
)

// Unit is one service inside a stack, as the host's service views show it.
type Unit struct {
	Stack string
	Unit  string
	// Name is what the host calls it: "<stack>-<unit>" (hostUnitName).
	Name        string
	ActiveState string
	SubState    string
	// StartedAt is zero unless the unit is active.
	StartedAt time.Time
	// Usage is its CPU and memory; nil when unknown.
	Usage *services.Usage
	// Masked: the operator disabled it (masks.go).
	Masked bool
	// DiskBytes is what its user owns in the stack's root at the last
	// measurement (disk.go); nil when unknown or when it runs as root.
	DiskBytes *int64
}

// unitsTTL bounds how often the service views, which poll, make the daemon
// ask every stack's systemd.
const unitsTTL = 5 * time.Second

// Units lists the service units of every stack that has been built: the
// units its manifest's health check names. They are the stack's own choice
// of what its services are. A stopped stack's units are inactive.
func (s *Service) Units(ctx context.Context) []Unit {
	s.mu.Lock()
	if s.unitsAt.Add(unitsTTL).After(time.Now()) {
		out := slices.Clone(s.units)
		s.mu.Unlock()
		return s.withUsage(out)
	}
	s.mu.Unlock()

	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		s.log.Warn("cannot list stacks for their units", "error", err)
		return nil
	}
	var out []Unit
	for _, r := range rows {
		g, err := s.generation(ctx, r)
		if err != nil || g == nil {
			continue
		}
		m, err := decode(g.Manifest)
		if err != nil || len(m.Health.Units) == 0 {
			continue
		}
		masks := liveMasks(r, m)
		state := map[string]Unit{}
		if s.isRunning(ctx, r.Name) {
			if s.frozen(ctx, r.Name) {
				for _, u := range m.Health.Units {
					state[u] = Unit{ActiveState: "active", SubState: "frozen"}
				}
			} else {
				state = s.unitStates(ctx, r.Name, m.Health.Units)
			}
		}
		for _, u := range m.Health.Units {
			st, ok := state[u]
			if !ok {
				st = Unit{ActiveState: "inactive", SubState: "dead"}
			}
			st.Stack, st.Unit, st.Masked = r.Name, u, slices.Contains(masks, u)
			out = append(out, st)
		}
	}
	qualify(out)
	s.mu.Lock()
	s.units, s.unitsAt = out, time.Now()
	s.mu.Unlock()
	return s.withUsage(slices.Clone(out))
}

// withUsage puts each unit's latest CPU and memory, and its last measured
// disk use, on it.
func (s *Service) withUsage(us []Unit) []Unit {
	for i := range us {
		us[i].Usage = s.usageOf(us[i].Stack + "/" + us[i].Unit)
		us[i].DiskBytes = s.diskOf(us[i].Stack, us[i].Unit)
	}
	return us
}

// qualify names every unit "<stack>-<unit>", so a name never depends on
// which other stacks are installed. A unit already named after its stack
// (immich-server in immich) keeps its name.
func qualify(units []Unit) {
	for i, u := range units {
		units[i].Name = hostUnitName(u.Stack, u.Unit)
	}
}

// hostUnitName is the host's name for unit inside stack.
func hostUnitName(stack, unit string) string {
	if strings.HasPrefix(unit, stack+"-") {
		return unit
	}
	return stack + "-" + unit
}

// unitStates reads the state of units inside a running stack in one call.
func (s *Service) unitStates(ctx context.Context, stack string, units []string) map[string]Unit {
	args := append([]string{"--machine=" + stack, "show",
		"--property=Id,ActiveState,SubState,ActiveEnterTimestampMonotonic,ActiveEnterTimestamp", "--timestamp=unix", "--"}, units...)
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "systemctl", Args: args})
	out := map[string]Unit{}
	if err != nil {
		return out
	}
	for _, block := range strings.Split(res.Stdout, "\n\n") {
		p := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				p[k] = v
			}
		}
		if p["Id"] == "" {
			continue
		}
		u := Unit{ActiveState: p["ActiveState"], SubState: p["SubState"]}
		if sec, err := strconv.ParseInt(strings.TrimPrefix(p["ActiveEnterTimestamp"], "@"), 10, 64); err == nil && sec > 0 && u.ActiveState == "active" {
			u.StartedAt = time.Unix(sec, 0)
		}
		out[p["Id"]] = u
	}
	return out
}

// UnitStack returns the stack a service unit belongs to and the unit's name
// inside it. name is the host's name for it (Unit.Name) and may leave out the
// ".service" suffix.
func (s *Service) UnitStack(ctx context.Context, name string) (stack, unit string, ok bool) {
	for _, u := range s.Units(ctx) {
		if u.Name == name || u.Name == name+".service" {
			return u.Stack, u.Unit, true
		}
	}
	return "", "", false
}

// UnitAction starts, stops or restarts one of a stack's service units. A
// disabled one cannot be started or restarted.
func (s *Service) UnitAction(ctx context.Context, name, action string) error {
	stack, unit, ok := s.UnitStack(ctx, name)
	if !ok {
		return httpx.NotFound("unknown_unit", "no stack has a service unit named %q", name)
	}
	if action != "start" && action != "stop" && action != "restart" {
		return httpx.BadRequest("invalid_action", "%q is not start, stop or restart", action)
	}
	if action != "stop" && s.unitMasked(ctx, stack, unit) {
		return maskedProblem(name)
	}
	if !s.isRunning(ctx, stack) {
		return httpx.Conflict("stack_stopped", "stack %q is not running; start the stack first", stack)
	}
	if s.frozen(ctx, stack) {
		return stackFrozen(stack)
	}
	if _, err := s.run(ctx, 5*time.Minute, "systemctl", "--machine="+stack, action, "--", unit); err != nil {
		return fmt.Errorf("%s %s in stack %s: %w", action, unit, stack, err)
	}
	s.mu.Lock()
	s.unitsAt = time.Time{}
	s.mu.Unlock()
	s.log.Info("stack unit "+action, "stack", stack, "unit", unit)
	return nil
}
