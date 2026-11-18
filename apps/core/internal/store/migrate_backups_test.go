package store_test

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"homelab-ngx.com/core/db"
	_ "homelab-ngx.com/core/internal/store" // registers the sqlite driver
)

// TestMigrateBackups walks 00019 up, down and up again over rows the old
// nix-declared job left behind.
func TestMigrateBackups(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sdb, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "m.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	sub, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, sdb, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 18); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := sdb.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO backup_runs (id, unit, trigger, started_at, finished_at, result, error) VALUES
		(1, 'b2-backup.service', 'cron', 100, 200, 'success', ''),
		(2, 'b2-backup.service', 'cron', 300, 400, 'failure', 'exit-code'),
		(3, 'b2-backup.service', 'cron', 500, 500, 'skipped', 'mount "/pool" not present'),
		(4, 'b2-backup.service', 'manual', 600, NULL, 'running', ''),
		(5, 'other', 'imported', 50, 60, 'unknown', 'x')`)
	exec(`INSERT INTO backup_overrides (unit, enabled, schedule, updated_at) VALUES ('b2-backup.service', 0, '0 5 * * *', 1)`)

	if _, err := p.UpTo(ctx, 19); err != nil {
		t.Fatalf("up: %v", err)
	}
	type run struct {
		backupID   sql.NullInt64
		name       string
		trigger    string
		started    int64
		finished   sql.NullInt64
		result     string
		errText    string
		files, byt int64
	}
	read := func() map[int64]run {
		t.Helper()
		rows, err := sdb.QueryContext(ctx, `SELECT id, backup_id, backup_name, trigger, started_at, finished_at, result, error, files, bytes FROM backup_runs`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int64]run{}
		for rows.Next() {
			var id int64
			var r run
			if err := rows.Scan(&id, &r.backupID, &r.name, &r.trigger, &r.started, &r.finished, &r.result, &r.errText, &r.files, &r.byt); err != nil {
				t.Fatal(err)
			}
			out[id] = r
		}
		return out
	}
	fin := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	want := map[int64]run{
		1: {name: "b2-backup", trigger: "cron", started: 100, finished: fin(200), result: "success"},
		2: {name: "b2-backup", trigger: "cron", started: 300, finished: fin(400), result: "failure", errText: "exit-code"},
		// A skipped run was never attempted; the new model calls that a failure.
		3: {name: "b2-backup", trigger: "cron", started: 500, finished: fin(500), result: "failure", errText: `mount "/pool" not present`},
		// A run still running belongs to a unit that no longer exists.
		4: {name: "b2-backup", trigger: "manual", started: 600, finished: fin(600), result: "unknown", errText: "superseded by the backup rework"},
		5: {name: "other", trigger: "imported", started: 50, finished: fin(60), result: "unknown", errText: "x"},
	}
	got := read()
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("run %d = %+v, want %+v", id, got[id], w)
		}
	}
	var n int
	if err := sdb.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'backup_overrides'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("backup_overrides still there: %d %v", n, err)
	}
	// New ids continue after the copied ones.
	exec(`INSERT INTO backups (id, name, created_at, updated_at) VALUES (1, 'Homelab', 1, 1)`)
	res, err := sdb.ExecContext(ctx, `INSERT INTO backup_runs (backup_id, backup_name, trigger, started_at, result) VALUES (1, 'Homelab', 'cron', 700, 'partial')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 6 {
		t.Errorf("new run id = %d, want 6", id)
	}
	exec(`INSERT INTO backup_runs (backup_id, backup_name, trigger, started_at, result) VALUES (1, 'Homelab', 'cron', 800, 'running')`)
	exec(`INSERT INTO backup_run_items (run_id, kind, destination_name, position, result) VALUES (6, 'folder', 'd', 0, 'partial')`)

	if _, err := p.DownTo(ctx, 18); err != nil {
		t.Fatalf("down: %v", err)
	}
	var units, running int
	_ = sdb.QueryRowContext(ctx, `SELECT count(*) FROM backup_runs`).Scan(&units)
	_ = sdb.QueryRowContext(ctx, `SELECT count(*) FROM backup_runs WHERE result NOT IN ('success', 'failure', 'skipped', 'unknown')`).Scan(&running)
	if units != 6 || running != 0 {
		t.Errorf("after down: %d rows (%d the old schema could not hold), want 6 and 0", units, running)
	}
	var res6 string
	_ = sdb.QueryRowContext(ctx, `SELECT result FROM backup_runs WHERE id = 6`).Scan(&res6)
	if res6 != "failure" {
		t.Errorf("a partial run went down as %q, want failure", res6)
	}
	if err := sdb.QueryRowContext(ctx, `SELECT count(*) FROM backup_overrides`).Scan(&n); err != nil || n != 0 {
		t.Errorf("backup_overrides after down: %d %v", n, err)
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := read(); len(got) != 6 || got[1] != want[1] {
		t.Errorf("after up again: %d rows, run 1 = %+v", len(got), got[1])
	}
}
