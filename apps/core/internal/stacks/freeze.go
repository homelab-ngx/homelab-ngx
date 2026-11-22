package stacks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

// A frozen stack's processes are stopped where they are by the kernel's
// cgroup freezer: they hold their memory and open files and use no CPU, and
// a thaw carries on from the same instruction. The freezer acts on the
// container's scope (machine.slice/<name>.scope, beside the stack's unit),
// so nspawn and the host's side of the stack (routes, firewall, interface)
// stay as they are. Nothing is stored: frozen is read from systemd, so a
// reboot or a container that ended leaves no stale flag.
//
// A call into a frozen container (systemctl --machine=) waits on its bus
// until it times out, so each such call checks first.

// scopeName is the container's scope, where its processes run. The metrics
// sampler reads the same cgroup.
func scopeName(name string) string { return name + ".scope" }

// frozen reports whether the stack's container is frozen, or freezing.
func (s *Service) frozen(ctx context.Context, name string) bool {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "systemctl", Args: []string{"show", "--property=FreezerState", "--value", "--", scopeName(name)},
	})
	if err != nil {
		return false
	}
	st := strings.TrimSpace(res.Stdout)
	return st == "frozen" || st == "freezing"
}

// Frozen names the frozen stacks, for a shutdown to warn of.
func (s *Service) Frozen(ctx context.Context) []string {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, r := range rows {
		if s.isRunning(ctx, r.Name) && s.frozen(ctx, r.Name) {
			out = append(out, r.Name)
		}
	}
	return out
}

func stackFrozen(name string) *httpx.Problem {
	return httpx.Conflict("stack_frozen", "stack %q is frozen; thaw it first", name)
}

// hold takes the stack's operation slot for a moment, so no start, stop,
// update or health check runs under a freeze or thaw. A health check holds
// the slot for a few seconds, so it is waited for; an operation refuses.
// release gives the slot back with the operator's last operation in it.
func (s *Service) hold(ctx context.Context, name string) (release func(), err error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		s.mu.Lock()
		last := s.ops[name]
		checking := last != nil && last.finished.IsZero() && last.check
		s.mu.Unlock()
		if !checking || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.healthEvery):
		}
	}
	s.mu.Lock()
	last := s.ops[name]
	s.mu.Unlock()
	o, err := s.begin(name, opStart)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	o.check = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if last != nil {
			s.ops[name] = last
		} else {
			delete(s.ops, name)
		}
		s.mu.Unlock()
		close(o.done)
	}, nil
}

// Freeze stops every process in the running stack where it is, keeping its
// memory. Its addresses stop answering until it is thawed. A reboot loses
// what it held.
func (s *Service) Freeze(ctx context.Context, name string) (Stack, error) {
	return s.freezer(ctx, name, "freeze")
}

// Thaw lets a frozen stack carry on where it stopped. A stack that is not
// frozen is left as it is.
func (s *Service) Thaw(ctx context.Context, name string) (Stack, error) {
	return s.freezer(ctx, name, "thaw")
}

func (s *Service) freezer(ctx context.Context, name, action string) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	release, err := s.hold(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	err = func() error {
		defer release()
		if !s.isRunning(ctx, name) {
			return httpx.Conflict("stack_not_running", "stack %q is not running", name)
		}
		if action == "thaw" && !s.frozen(ctx, name) {
			return nil
		}
		if _, err := s.run(ctx, time.Minute, "systemctl", action, "--", scopeName(name)); err != nil {
			return fmt.Errorf("%s stack %s: %w", action, name, err)
		}
		return nil
	}()
	if err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	s.unitsAt = time.Time{}
	s.mu.Unlock()
	s.log.Info("stack "+action, "stack", name)
	return s.view(ctx, r)
}

// thaw lets a frozen stack go before it is stopped, so it can shut down
// cleanly: frozen, it cannot answer the signal to stop, and nspawn would
// kill it after TimeoutStopSec.
func (s *Service) thaw(ctx context.Context, name string) {
	if !s.frozen(ctx, name) {
		return
	}
	if _, err := s.run(ctx, time.Minute, "systemctl", "thaw", "--", scopeName(name)); err != nil {
		s.log.Warn("cannot thaw the stack before stopping it", "stack", name, "error", err)
	}
}

// Kill ends a frozen stack's processes at once, for a shutdown that is not
// to wait for a thaw: what it held in memory is lost. A fatal signal ends a
// frozen process.
func (s *Service) Kill(ctx context.Context, name string) error {
	if _, err := s.run(ctx, time.Minute, "systemctl", "kill", "--signal=SIGKILL", "--", scopeName(name)); err != nil {
		return fmt.Errorf("kill stack %s: %w", name, err)
	}
	s.log.Warn("frozen stack killed for a shutdown", "stack", name)
	return nil
}
