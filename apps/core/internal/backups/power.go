package backups

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// haltTimeout exceeds the backup unit's TimeoutStopSec=2min so systemctl stop
// waits for systemd's full stop grace period instead of timing out at 30s.
const haltTimeout = 2*time.Minute + 15*time.Second

// haltWatchWait caps how long Halt waits for the watcher goroutine to record
// the cancelled state after the unit has stopped.
const haltWatchWait = 5 * time.Second

// BeginShutdown refuses new backup triggers while a host shutdown runs.
func (s *Service) BeginShutdown() {
	s.mu.Lock()
	s.goingDown = true
	s.mu.Unlock()
}

// CancelShutdown allows backup triggers again after a called-off shutdown.
func (s *Service) CancelShutdown() {
	s.mu.Lock()
	s.goingDown = false
	s.mu.Unlock()
}

// Running returns the names of backups that currently have a run in flight.
func (s *Service) Running(ctx context.Context) ([]string, error) {
	rows, err := s.q.RunningBackupRuns(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rows))
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if seen[r.BackupName] {
			continue
		}
		seen[r.BackupName] = true
		out = append(out, r.BackupName)
	}
	sort.Strings(out)
	return out, nil
}

// stopUnit stops a transient backup service unit and treats exit code 5 (unit
// already unloaded) as stopped.
func (s *Service) stopUnit(ctx context.Context, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, haltTimeout)
	defer cancel()
	_, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "backups", Name: "systemctl", Args: []string{"stop", unit}, ExpectExit: []int{5},
	})
	var ce *cmdrunner.Error
	if err != nil && !(errors.As(err, &ce) && ce.Result.ExitCode == 5) {
		return fmt.Errorf("stopping the backup unit: %w", err)
	}
	return nil
}

// Halt stops the named running backup before pool mounts are released.
func (s *Service) Halt(ctx context.Context, name string) error {
	rows, err := s.q.RunningBackupRuns(ctx)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, r := range rows {
		if r.BackupName != name || r.BackupID == nil || seen[*r.BackupID] {
			continue
		}
		seen[*r.BackupID] = true
		ids = append(ids, *r.BackupID)
	}
	var firstErr error
	for _, id := range ids {
		s.mu.Lock()
		cancel := s.startCancel[id]
		done := s.runDone[id]
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if err := s.stopUnit(ctx, unitName(id)); err != nil && firstErr == nil {
			firstErr = err
		}
		if done != nil {
			t := time.NewTimer(haltWatchWait)
			select {
			case <-done:
			case <-ctx.Done():
			case <-t.C:
			}
			t.Stop()
		}
	}
	return firstErr
}
