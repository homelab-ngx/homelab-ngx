package stacks

import (
	"context"
	"fmt"
	"sort"

	"homelab-ngx.com/core/internal/httpx"
)

// errShuttingDown refuses an operation or a boot while the host reboots or
// powers off.
var errShuttingDown = httpx.Conflict("shutting_down", "the host is shutting down")

// Freeze stops the stacks from changing while the host shuts down: no
// operation begins, no container boots and the monitor restarts nothing.
// An operation already running goes on, but cannot boot a container.
func (s *Service) Freeze() {
	s.mu.Lock()
	s.frozen = true
	s.mu.Unlock()
}

// Thaw undoes Freeze: a shutdown that was cancelled. The monitor then starts
// the stacks the operator left running.
func (s *Service) Thaw() {
	s.mu.Lock()
	s.frozen = false
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
