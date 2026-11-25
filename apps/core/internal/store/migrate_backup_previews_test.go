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

// TestMigrateBackupPreviews walks 00040 up and down over a run's items: they
// survive the rebuild, an item may be cancelled after it, and a cancelled
// item goes down as skipped.
func TestMigrateBackupPreviews(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 39); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := sdb.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	results := func() string {
		t.Helper()
		rows, err := sdb.QueryContext(ctx, `SELECT id || ':' || result || ':' || note FROM backup_run_items ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := ""
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out += s + " "
		}
		return out
	}
	exec(`INSERT INTO backups (id, name, created_at, updated_at) VALUES (1, 'Homelab', 1, 1)`)
	exec(`INSERT INTO backup_runs (id, backup_id, backup_name, trigger, started_at, result) VALUES (1, 1, 'Homelab', 'cron', 100, 'cancelled')`)
	exec(`INSERT INTO backup_run_items (id, run_id, kind, destination_name, position, result, note) VALUES
		(3, 1, 'destination', 'd', 0, 'success', 'n'), (7, 1, 'folder', 'd', 1, 'skipped', '')`)
	if _, err := sdb.ExecContext(ctx, `INSERT INTO backup_run_items (run_id, kind, destination_name, position, result) VALUES (1, 'folder', 'd', 2, 'cancelled')`); err == nil {
		t.Fatal("the old table took a cancelled item")
	}

	if _, err := p.UpTo(ctx, 40); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := results(); got != "3:success:n 7:skipped: " {
		t.Errorf("items after up: %q", got)
	}
	exec(`INSERT INTO backup_run_items (run_id, kind, destination_name, position, result) VALUES (1, 'folder', 'd', 2, 'cancelled')`)
	exec(`INSERT INTO backup_previews (backup_id, started_at, result) VALUES (1, 5, 'running')`)
	// The backup's preview goes with the backup; its items go with the run.
	exec(`DELETE FROM backups WHERE id = 1`)
	var n int
	if err := sdb.QueryRowContext(ctx, `SELECT count(*) FROM backup_previews`).Scan(&n); err != nil || n != 0 {
		t.Errorf("previews after the backup was deleted: %d %v", n, err)
	}

	if _, err := p.DownTo(ctx, 39); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got := results(); got != "3:success:n 7:skipped: 8:skipped: " {
		t.Errorf("items after down: %q", got)
	}
	if err := sdb.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'backup_previews'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("backup_previews after down: %d %v", n, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
