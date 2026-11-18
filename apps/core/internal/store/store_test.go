package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

func TestMetaAndTx(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := storetest.Open(t)
	if _, ok, err := s.Meta(ctx, "k"); err != nil || ok {
		t.Fatalf("unset meta: ok=%v err=%v", ok, err)
	}
	if err := s.SetMeta(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Meta(ctx, "k"); !ok || v != "v2" {
		t.Fatalf("got %q %v", v, ok)
	}
	// Rollback on error.
	_ = s.Tx(ctx, func(q *store.Queries) error {
		_ = q.SetMeta(ctx, store.SetMetaParams{Key: "rb", Value: "x"})
		return fmt.Errorf("boom")
	})
	if _, ok, _ := s.Meta(ctx, "rb"); ok {
		t.Fatal("tx not rolled back")
	}
}

func TestMigrateFalse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "x.db")
	if _, err := store.Open(ctx, p, store.Options{}); err == nil {
		t.Fatal("expected error opening missing db without Migrate")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("non-migrating open created the file")
	}
	s, err := store.Open(ctx, p, store.Options{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := store.Open(ctx, p, store.Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.Close()
}

func TestPoolsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := storetest.Open(t)

	p := store.PoolRecord{UUID: "u1", Mountpoint: "/pool", AutoMount: true, Name: "pool", Options: "ro"}
	if err := s.UpsertPool(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.AutoMount = false
	_ = s.UpsertPool(ctx, p)
	pools, _ := s.Pools(ctx)
	if len(pools) != 1 || pools[0] != p {
		t.Fatalf("pools: %+v", pools)
	}
}

func TestOtherQueriesRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := storetest.Open(t)
	q := s.Queries()

	bid, err := q.InsertBackup(ctx, store.InsertBackupParams{Name: "b", Enabled: true, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	id, err := q.InsertBackupRun(ctx, store.InsertBackupRunParams{BackupID: &bid, BackupName: "b", Trigger: "cron", StartedAt: 10})
	if err != nil {
		t.Fatal(err)
	}
	fin := int64(20)
	if err := q.FinishBackupRun(ctx, store.FinishBackupRunParams{ID: id, FinishedAt: &fin, Result: "success", Files: 3, Bytes: 4}); err != nil {
		t.Fatal(err)
	}
	rs, err := q.LastBackupRuns(ctx)
	if err != nil || len(rs) != 1 || rs[0].Result != "success" || rs[0].FinishedAt == nil || rs[0].Files != 3 {
		t.Fatalf("%+v %v", rs, err)
	}
	if _, err := q.InsertBackupRun(ctx, store.InsertBackupRunParams{BackupID: &bid, BackupName: "b", Trigger: "bogus", StartedAt: 1}); err == nil {
		t.Fatal("CHECK constraint on trigger not enforced")
	}
	// Deleting the backup keeps its runs, under its name.
	if err := q.DeleteBackup(ctx, bid); err != nil {
		t.Fatal(err)
	}
	if r, err := q.GetBackupRun(ctx, id); err != nil || r.BackupID != nil || r.BackupName != "b" {
		t.Fatalf("run after backup delete: %+v %v", r, err)
	}

	_ = q.UpsertNotificationProblem(ctx, store.UpsertNotificationProblemParams{Key: "k", Title: "t", OpenedAt: 1, LastSentAt: 1, Count: 1})
	_ = q.UpsertNotificationProblem(ctx, store.UpsertNotificationProblemParams{Key: "k", Title: "t", OpenedAt: 1, LastSentAt: 2, Count: 2})
	if ps, _ := q.ListNotificationProblems(ctx); len(ps) != 1 || ps[0].Count != 2 {
		t.Fatalf("problems %+v", ps)
	}
	nid, err := q.InsertNotification(ctx, store.InsertNotificationParams{At: 1, Kind: "sent", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_ = q.InsertNotificationDelivery(ctx, store.InsertNotificationDeliveryParams{NotificationID: nid, At: 1, Channel: "ntfy", Ok: true})
	if d, _ := q.ListNotificationDeliveries(ctx, []int64{nid}); len(d) != 1 || !d[0].Ok {
		t.Fatal("deliveries")
	}

	_ = q.SetSecretRotation(ctx, store.SetSecretRotationParams{Name: "a/B", RotatedAt: 3})
	if r, _ := q.ListSecretRotations(ctx); len(r) != 1 || r[0].RotatedAt != 3 {
		t.Fatal("secret rotations")
	}
}

func TestSnapshotKeep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := storetest.Open(t)
	_ = s.SetMeta(ctx, "x", "y")
	dir := filepath.Join(t.TempDir(), "snaps")
	base := time.Date(2026, 1, 1, 3, 15, 0, 0, time.UTC)
	var last string
	for i := 0; i < 5; i++ {
		d := base.AddDate(0, 0, i)
		store.SetNow(s, func() time.Time { return d })
		p, err := s.Snapshot(ctx, dir, 3)
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 {
		t.Fatalf("want 3 files, got %d", len(ents))
	}
	if filepath.Base(last) != "homelab-2026-01-05.db" {
		t.Fatalf("last %s", last)
	}
	if _, err := os.Stat(filepath.Join(dir, "homelab-2026-01-03.db")); err != nil {
		t.Fatalf("expected 01-03 (oldest kept): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "homelab-2026-01-02.db")); err == nil {
		t.Fatal("01-02 should be pruned")
	}
	// Same-day rerun overwrites, and the snapshot is a usable DB.
	if _, err := s.Snapshot(ctx, dir, 3); err != nil {
		t.Fatalf("same-day rerun: %v", err)
	}
	snap, err := store.Open(ctx, last, store.Options{})
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	if v, ok, _ := snap.Meta(ctx, "x"); !ok || v != "y" {
		t.Fatal("snapshot missing data")
	}
	if _, ok, _ := s.Meta(ctx, store.MetaLastSnapshotAt); !ok {
		t.Fatal("last_snapshot_at not set")
	}
}

func TestPruneBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := storetest.Open(t)
	q := s.Queries()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	retain := 90 * 24 * time.Hour
	cutoff := now.Add(-retain).UnixMilli()

	for _, at := range []int64{cutoff - 1, cutoff, cutoff + 1} {
		id, _ := q.InsertNotification(ctx, store.InsertNotificationParams{At: at, Kind: "sent", Title: "t"})
		_ = q.InsertNotificationDelivery(ctx, store.InsertNotificationDeliveryParams{NotificationID: id, At: at, Channel: "c", Ok: true})
	}
	year := now.Add(-365 * 24 * time.Hour).UnixMilli()
	bid, _ := q.InsertBackup(ctx, store.InsertBackupParams{Name: "b", Enabled: true, CreatedAt: 1, UpdatedAt: 1})
	var runIDs []int64
	for _, at := range []int64{year - 1, year, year + 1} {
		rid, _ := q.InsertBackupRun(ctx, store.InsertBackupRunParams{BackupID: &bid, BackupName: "b", Trigger: "cron", StartedAt: at})
		fin := at + 1
		_ = q.FinishBackupRun(ctx, store.FinishBackupRunParams{ID: rid, FinishedAt: &fin, Result: "success"})
		_ = q.InsertBackupRunItem(ctx, store.InsertBackupRunItemParams{RunID: rid, Kind: "folder", DestinationName: "d", Result: "success"})
		runIDs = append(runIDs, rid)
	}
	// A run still in flight is never pruned, however old.
	_, _ = q.InsertBackupRun(ctx, store.InsertBackupRunParams{BackupID: &bid, BackupName: "b", Trigger: "cron", StartedAt: 1})

	n, err := s.Prune(ctx, now, retain)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d notifications, want 1", n) // strictly older than cutoff is removed
	}
	var deliveries int
	_ = store.DB(s).QueryRow("SELECT count(*) FROM notification_deliveries").Scan(&deliveries)
	if deliveries != 2 {
		t.Fatalf("deliveries should cascade, have %d", deliveries)
	}
	if runs, _ := q.ListBackupRuns(ctx, store.ListBackupRunsParams{BackupID: &bid, Limit: 10}); len(runs) != 3 {
		t.Fatalf("runs after prune = %d, want 3 (a year old or newer, and the running one)", len(runs))
	}
	if items, _ := q.ListBackupRunItems(ctx, runIDs[0]); len(items) != 0 {
		t.Fatal("a pruned run's items should cascade")
	}
	if items, _ := q.ListBackupRunItems(ctx, runIDs[1]); len(items) != 1 {
		t.Fatal("a kept run lost its items")
	}
}

func TestConcurrentTwoHandles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s1, p := storetest.Open(t)
	s2, err := store.Open(ctx, p, store.Options{}) // CLI-style handle
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	const n = 25
	var wg sync.WaitGroup
	for h, st := range []*store.Store{s1, s2} {
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < n; i++ {
					unit := fmt.Sprintf("h%dw%di%d", h, w, i)
					if err := st.Tx(ctx, func(q *store.Queries) error {
						if err := q.SetMeta(ctx, store.SetMetaParams{Key: unit, Value: "1"}); err != nil {
							return err
						}
						_, err := q.InsertNotification(ctx, store.InsertNotificationParams{At: 1, Kind: "sent", Title: unit})
						return err
					}); err != nil {
						t.Errorf("write %s: %v", unit, err)
						return
					}
					if _, _, err := st.Meta(ctx, unit); err != nil {
						t.Errorf("read: %v", err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	var c int
	_ = store.DB(s1).QueryRow("SELECT count(*) FROM notifications").Scan(&c)
	if c != 2*4*n {
		t.Fatalf("rows %d want %d", c, 2*4*n)
	}
}
