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

// TestMigrateHostSettingsDDNS walks 00033 up, down and up again over the
// settings a host saved before it, sealed secrets included.
func TestMigrateHostSettingsDDNS(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 32); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := sdb.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO host_settings (section, value, secret, updated_at) VALUES
		('host', '{"hostname":"h"}', NULL, 1),
		('wireguard', '{"enabled":true}', X'00FF10', 2),
		('tunnel', '{"enabled":true}', X'ABCD', 3)`)
	type row struct {
		value   string
		secret  []byte
		updated int64
	}
	read := func() map[string]row {
		t.Helper()
		rs, err := sdb.QueryContext(ctx, `SELECT section, value, secret, updated_at FROM host_settings`)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		m := map[string]row{}
		for rs.Next() {
			var s string
			var r row
			if err := rs.Scan(&s, &r.value, &r.secret, &r.updated); err != nil {
				t.Fatal(err)
			}
			m[s] = r
		}
		return m
	}
	same := func(when string, got map[string]row) {
		t.Helper()
		if len(got) != 3 || got["host"].value != `{"hostname":"h"}` || string(got["wireguard"].secret) != "\x00\xff\x10" ||
			string(got["tunnel"].secret) != "\xab\xcd" || got["tunnel"].updated != 3 || got["host"].secret != nil {
			t.Errorf("%s: %+v", when, got)
		}
	}

	if _, err := p.UpTo(ctx, 33); err != nil {
		t.Fatalf("up: %v", err)
	}
	same("after up", read())
	exec(`INSERT INTO host_settings (section, value, updated_at) VALUES ('ddns', '{"enabled":true}', 4)`)
	if _, err := sdb.ExecContext(ctx, `INSERT INTO host_settings (section, value, updated_at) VALUES ('other', '{}', 5)`); err == nil {
		t.Error("a section outside the list was stored")
	}

	if _, err := p.DownTo(ctx, 32); err != nil {
		t.Fatalf("down: %v", err)
	}
	same("after down", read())
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	same("after up again", read())
}
