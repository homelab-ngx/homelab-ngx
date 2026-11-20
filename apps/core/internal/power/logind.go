package power

import (
	"context"
	"errors"
	"fmt"
	"math"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// Logind is what the power service asks of systemd-logind: a delay lock on
// shutdowns, the signal that one is coming, and the shutdown itself.
type Logind interface {
	// Inhibit takes a delay lock: a shutdown waits until it is released,
	// for at most logind's InhibitDelayMaxSec.
	Inhibit(ctx context.Context) (release func(), err error)
	// Prepare calls fn when a shutdown is about to start (start true, with
	// the action) and when one was called off (start false), until ctx ends.
	Prepare(ctx context.Context, fn func(start bool, a PowerAction)) error
	// Shutdown asks logind to reboot or power off.
	Shutdown(ctx context.Context, a PowerAction) error
	// Scheduled is the shutdown planned with `shutdown -r HH:MM`; nil: none.
	Scheduled(ctx context.Context) (*PowerScheduled, error)
	// CancelScheduled calls the planned shutdown off.
	CancelScheduled(ctx context.Context) error
}

const (
	loginBus  = "org.freedesktop.login1"
	loginPath = dbus.ObjectPath("/org/freedesktop/login1")
	loginMgr  = "org.freedesktop.login1.Manager"
)

// SystemLogind is logind on the system bus.
type SystemLogind struct{}

func (SystemLogind) call(ctx context.Context, method string, args ...any) (*dbus.Call, func(), error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("system bus: %w", err)
	}
	c := conn.Object(loginBus, loginPath).CallWithContext(ctx, loginMgr+"."+method, 0, args...)
	if c.Err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("logind %s: %w", method, c.Err)
	}
	return c, func() { conn.Close() }, nil
}

func (l SystemLogind) Inhibit(ctx context.Context) (func(), error) {
	c, done, err := l.call(ctx, "Inhibit", "shutdown", "homelab-daemon", "Stopping the stacks and unmounting the pools", "delay")
	if err != nil {
		return nil, err
	}
	defer done()
	var fd dbus.UnixFD
	if err := c.Store(&fd); err != nil {
		return nil, fmt.Errorf("logind Inhibit: %w", err)
	}
	return func() { _ = syscall.Close(int(fd)) }, nil
}

func (SystemLogind) Prepare(ctx context.Context, fn func(bool, PowerAction)) error {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("system bus: %w", err)
	}
	defer conn.Close()
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(loginPath), dbus.WithMatchInterface(loginMgr),
		dbus.WithMatchMember("PrepareForShutdownWithMetadata"),
	); err != nil {
		return fmt.Errorf("logind PrepareForShutdownWithMetadata: %w", err)
	}
	ch := make(chan *dbus.Signal, 4)
	conn.Signal(ch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-ch:
			if !ok {
				return errors.New("the system bus closed")
			}
			if sig.Name != loginMgr+".PrepareForShutdownWithMetadata" || len(sig.Body) < 2 {
				continue
			}
			start, _ := sig.Body[0].(bool)
			meta, _ := sig.Body[1].(map[string]dbus.Variant)
			kind := ""
			if v, ok := meta["type"]; ok {
				kind, _ = v.Value().(string)
			}
			fn(start, actionOf(kind))
		}
	}
}

// actionOf reads logind's shutdown type ("reboot", "power-off", "halt",
// "kexec", "soft-reboot"; "poweroff" in ScheduledShutdown).
func actionOf(kind string) PowerAction {
	switch kind {
	case "power-off", "poweroff", "halt", "dry-poweroff", "dry-halt":
		return PowerOff
	}
	return Reboot
}

func (l SystemLogind) Shutdown(ctx context.Context, a PowerAction) error {
	method := "Reboot"
	if a == PowerOff {
		method = "PowerOff"
	}
	_, done, err := l.call(ctx, method, false)
	if err != nil {
		return err
	}
	done()
	return nil
}

func (SystemLogind) Scheduled(ctx context.Context) (*PowerScheduled, error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	defer conn.Close()
	v, err := conn.Object(loginBus, loginPath).GetProperty(loginMgr + ".ScheduledShutdown")
	if err != nil {
		return nil, fmt.Errorf("logind ScheduledShutdown: %w", err)
	}
	// (st): the type and the time in µs since the epoch; "" when none.
	var s struct {
		Kind string
		At   uint64
	}
	if err := dbus.Store([]any{v.Value()}, &s); err != nil {
		return nil, fmt.Errorf("logind ScheduledShutdown: %w", err)
	}
	if s.Kind == "" || s.At == 0 || s.At == math.MaxUint64 {
		return nil, nil
	}
	return &PowerScheduled{Action: actionOf(s.Kind), At: time.UnixMicro(int64(s.At))}, nil
}

func (l SystemLogind) CancelScheduled(ctx context.Context) error {
	_, done, err := l.call(ctx, "CancelScheduledShutdown")
	if err != nil {
		return err
	}
	done()
	return nil
}
