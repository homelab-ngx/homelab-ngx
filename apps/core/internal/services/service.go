// Package services is the service views: the units inside stacks, whose
// start, stop and restart it hands to the stacks package, the core units on
// the host (read-only), and the minute watch pass that reports units that
// keep restarting and resolves stale unit alerts.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/systemd"
)

// ErrUnknownUnit is returned for a name that is not a stack's unit.
var ErrUnknownUnit = errors.New("unknown unit")

// Service is the service views over systemd, the configuration and the stacks.
type Service struct {
	sd     *systemd.Client
	cfg    *config.Manager
	notify *notifications.Notifier
	log    *slog.Logger

	churn  churnTracker
	stacks StackUnits

	// Seams overridden by tests.
	now   func() time.Time
	every time.Duration // watch pass interval
}

// New wires a Service. notify may be nil (no unit alerts).
func New(sd *systemd.Client, cfg *config.Manager, notify *notifications.Notifier, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{sd: sd, cfg: cfg, notify: notify, log: log, now: time.Now, every: time.Minute}
}

// StackUnits is the stacks package's side of the service views: the service
// units that run inside stacks.
type StackUnits interface {
	Units(ctx context.Context) []StackUnit
	UnitStack(ctx context.Context, name string) (stack, unit string, ok bool)
	UnitAction(ctx context.Context, name, action string) error
	MaskUnit(ctx context.Context, name string, mask bool) error
}

// StackUnit is one service unit inside a stack.
type StackUnit struct {
	// Name is the host's name for the unit; Unit is its name in the stack.
	Stack, Unit, Name     string
	ActiveState, SubState string
	StartedAt             time.Time // zero unless active
	Usage                 *Usage    // nil unless running
	Masked                bool      // the operator disabled it
	DiskBytes             *int64    // what its user owns in the stack's root; nil when unknown
}

// SetStacks adds the units inside stacks to the service list and its
// actions. Without it only the core units are known.
func (s *Service) SetStacks(st StackUnits) { s.stacks = st }

// inStack reports whether name is a stack's unit and not also a core unit:
// a core unit of the same name keeps the name.
func (s *Service) inStack(ctx context.Context, name string) bool {
	if s.stacks == nil {
		return false
	}
	if _, known := s.resolveCore(name); known {
		return false
	}
	_, _, ok := s.stacks.UnitStack(ctx, name)
	return ok
}

// Start starts a stack's unit. Errors: ErrUnknownUnit, or the stack's
// failure.
func (s *Service) Start(ctx context.Context, name string) error { return s.act(ctx, name, "start") }

// Stop stops a stack's unit.
func (s *Service) Stop(ctx context.Context, name string) error { return s.act(ctx, name, "stop") }

// Restart restarts a stack's unit.
func (s *Service) Restart(ctx context.Context, name string) error {
	return s.act(ctx, name, "restart")
}

// Mask disables a stack's unit (mask) or enables it again.
func (s *Service) Mask(ctx context.Context, name string, mask bool) error {
	if !s.inStack(ctx, name) {
		return fmt.Errorf("%w: %q", ErrUnknownUnit, name)
	}
	return s.stacks.MaskUnit(ctx, name, mask)
}

func (s *Service) act(ctx context.Context, name, action string) error {
	if !s.inStack(ctx, name) {
		return fmt.Errorf("%w: %q", ErrUnknownUnit, name)
	}
	return s.stacks.UnitAction(ctx, name, action)
}
