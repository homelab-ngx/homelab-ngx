package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"homelab-ngx.com/core/internal/logging"
)

const (
	snapshotPrefix = "homelab-"
	snapshotSuffix = ".db"

	// MetaLastSnapshotAt is the meta key holding the last snapshot time (unix ms).
	MetaLastSnapshotAt = "last_snapshot_at"
)

// Snapshot writes a consistent copy of the database to dir/homelab-<date>.db
// via VACUUM INTO, then keeps only the newest keep snapshots. The copy is
// built under a .tmp name (excluded from off-site backups) and renamed once
// fsynced. It returns the final path.
func (s *Store) Snapshot(ctx context.Context, dir string, keep int) (string, error) {
	if keep < 1 {
		keep = 1
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	name := snapshotPrefix + s.now().Format("2006-01-02") + snapshotSuffix
	final := filepath.Join(dir, name)
	tmp := filepath.Join(dir, "."+name+".tmp")
	// VACUUM INTO refuses to overwrite; clear leftovers of a crashed run.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("store: vacuum into: %w", err)
	}
	if err := fsyncFile(tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	if err := pruneSnapshots(dir, keep); err != nil {
		return final, err
	}
	if err := s.SetMeta(ctx, MetaLastSnapshotAt, strconv.FormatInt(s.nowMS(), 10)); err != nil {
		return final, err
	}
	return final, nil
}

func fsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func pruneSnapshots(dir string, keep int) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, snapshotPrefix) && strings.HasSuffix(n, snapshotSuffix) {
			names = append(names, n)
		}
	}
	sort.Strings(names) // ISO dates sort chronologically
	if len(names) <= keep {
		return nil
	}
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// runRetention is how long a finished backup run and its items are kept.
const runRetention = 365 * 24 * time.Hour

// Prune deletes notifications (deliveries cascade) older than retain
// relative to now and returns how many went. Backup runs older than a year
// go with their items.
func (s *Store) Prune(ctx context.Context, now time.Time, retain time.Duration) (int64, error) {
	if _, err := s.q.PruneBackupRuns(ctx, now.Add(-runRetention).UnixMilli()); err != nil {
		return 0, err
	}
	return s.q.PruneNotifications(ctx, now.Add(-retain).UnixMilli())
}

// RunMaintenance runs the snapshot and prune cron schedules (5-field cron)
// until ctx is cancelled. Failures are logged, never fatal. It blocks.
func (s *Store) RunMaintenance(ctx context.Context, snapshotSpec, pruneSpec, dir string, keep int, retain time.Duration) {
	log := logging.Logger("store")
	c := cron.New()
	if _, err := c.AddFunc(snapshotSpec, func() {
		p, err := s.Snapshot(ctx, dir, keep)
		if err != nil {
			log.Error("db snapshot failed", "dir", dir, "err", err)
			return
		}
		log.Info("db snapshot written", "path", p)
	}); err != nil {
		log.Error("invalid snapshot schedule; maintenance not started", "spec", snapshotSpec, "err", err)
		return
	}
	if _, err := c.AddFunc(pruneSpec, func() {
		n, err := s.Prune(ctx, s.now(), retain)
		if err != nil {
			log.Error("db prune failed", "err", err)
			return
		}
		log.Info("db pruned", "notifications", n)
	}); err != nil {
		log.Error("invalid prune schedule; maintenance not started", "spec", pruneSpec, "err", err)
		return
	}
	c.Start()
	<-ctx.Done()
	<-c.Stop().Done()
}
