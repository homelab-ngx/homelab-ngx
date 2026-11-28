package store_test

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"homelab-ngx.com/core/db"
	_ "homelab-ngx.com/core/internal/store"
)

func TestMigrateNotificationsV2(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 42); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := sdb.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO notifications (at, kind, severity, title, body) VALUES (100, 'sent', 'info', 'Old', '')`)
	exec(`INSERT INTO notifications (at, kind, severity, title, body) VALUES (200, 'sent', 'info', 'Newer', '')`)

	if _, err := p.UpTo(ctx, 43); err != nil {
		t.Fatalf("up: %v", err)
	}
	var readUpTo string
	if err := sdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'notifications.read_up_to'`).Scan(&readUpTo); err != nil {
		t.Fatal(err)
	}
	if readUpTo != "2" {
		t.Fatalf("read_up_to = %q, want 2", readUpTo)
	}
	exec(`INSERT INTO notifications (at, kind, event, path, severity, title, body) VALUES (300, 'sent', 'core.update', '/settings/updates', 'info', 'Updated', '')`)
	exec(`INSERT INTO notification_deliveries (notification_id, at, channel, ok, filtered, error) VALUES (3, 300, 'Phone', FALSE, TRUE, '')`)

	if _, err := p.DownTo(ctx, 42); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := sdb.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key = 'notifications.read_up_to'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("meta after down: %d %v", n, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
