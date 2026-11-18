package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"homelab-ngx.com/core/db"
)

// Options configures Open.
type Options struct {
	// Migrate runs pending goose migrations. Only the daemon sets this. When
	// false (the CLI), Open fails if the schema is not exactly at the version
	// this binary was built for, and never creates a missing database file.
	Migrate bool
	// BusyTimeout is how long a connection waits on a locked database.
	// Zero means 5s.
	BusyTimeout time.Duration
}

// ErrSchemaMismatch is returned by Open with Migrate=false when the database
// schema is behind or ahead of this binary's migrations.
var ErrSchemaMismatch = errors.New("store: database schema version does not match this binary")

// Store is the SQLite runtime store. It is safe for concurrent use; several
// processes (daemon, CLI) may each hold their own Store on the same file.
type Store struct {
	db  *sql.DB
	q   *Queries
	now func() time.Time
}

// Open opens (and optionally migrates) the database at path.
func Open(ctx context.Context, path string, o Options) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	bt := o.BusyTimeout
	if bt <= 0 {
		bt = 5 * time.Second
	}
	qv := url.Values{}
	qv.Add("_pragma", "journal_mode(WAL)")
	qv.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", bt.Milliseconds()))
	qv.Add("_pragma", "synchronous(NORMAL)")
	qv.Add("_pragma", "foreign_keys(ON)")
	// Zero freed space, so text an update replaced (a peer body scrubbed of a
	// secret value) leaves no copy in the page.
	qv.Add("_pragma", "secure_delete(ON)")
	qv.Set("_txlock", "immediate")
	if !o.Migrate {
		qv.Set("mode", "rw") // never create a file from a non-migrating handle
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: qv.Encode()}

	sdb, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("store: open %s: %w", abs, err)
	}
	if err := checkSchema(ctx, sdb, o.Migrate); err != nil {
		sdb.Close()
		return nil, err
	}
	return &Store{db: sdb, q: New(sdb), now: time.Now}, nil
}

func checkSchema(ctx context.Context, sdb *sql.DB, migrate bool) error {
	sub, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, sdb, sub)
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	if migrate {
		if _, err := p.Up(ctx); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
		return nil
	}
	srcs := p.ListSources()
	want := srcs[len(srcs)-1].Version
	have, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if have != want {
		return fmt.Errorf("%w (database at %d, binary expects %d)", ErrSchemaMismatch, have, want)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Queries returns the generated queries bound to the pool (autocommit).
func (s *Store) Queries() *Queries { return s.q }

// Tx runs fn in a write transaction (BEGIN IMMEDIATE), committing if fn
// returns nil and rolling back otherwise.
func (s *Store) Tx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// CheckpointTruncate copies the write-ahead log into the database and
// truncates it to zero bytes. It fails when a reader keeps the checkpoint
// from finishing.
func (s *Store) CheckpointTruncate(ctx context.Context) error {
	var busy, frames, done int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil {
		return fmt.Errorf("store: wal checkpoint: %w", err)
	}
	if busy != 0 {
		return errors.New("store: wal checkpoint blocked by a reader")
	}
	return nil
}

// MetaCleanShutdown holds the time of the last clean daemon exit (unix ms).
// It is blanked while the daemon runs, so a crash leaves it blank.
const MetaCleanShutdown = "clean_shutdown_at"

// Meta returns the meta value for key; ok is false if unset.
func (s *Store) Meta(ctx context.Context, key string) (string, bool, error) {
	v, err := s.q.GetMeta(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta upserts a meta value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.q.SetMeta(ctx, SetMetaParams{Key: key, Value: value})
}

func (s *Store) nowMS() int64 { return s.now().UnixMilli() }
