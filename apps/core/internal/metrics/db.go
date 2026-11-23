package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// Retention is how long metrics.db keeps a minute.
const Retention = 30 * 24 * time.Hour

// DB is metrics.db: a row per scope per minute. It is throwaway telemetry,
// so it lives outside homelab.db: no snapshots, no off-site backup, no
// goose migrations. The one table is created here; a change to it can drop
// and recreate it.
type DB struct{ db *sql.DB }

// columns are the figure columns, in the order of the fig constants.
var columns = []string{
	"cpu", "memory", "pressure_cpu", "pressure_memory", "pressure_io",
	"disk_read", "disk_write", "net_rx", "net_tx", "temp_cpu", "temp_disk",
	"cpu_max", "memory_max", "power_cpu",
}

const schema = `CREATE TABLE IF NOT EXISTS metrics_minute (
  scope TEXT NOT NULL,
  ts INTEGER NOT NULL,
  cpu REAL, memory REAL, pressure_cpu REAL, pressure_memory REAL, pressure_io REAL,
  disk_read REAL, disk_write REAL, net_rx REAL, net_tx REAL, temp_cpu REAL, temp_disk REAL,
  cpu_max REAL, memory_max REAL, power_cpu REAL,
  PRIMARY KEY (ts, scope)
) WITHOUT ROWID`

// OpenDB opens or creates metrics.db at path.
func OpenDB(ctx context.Context, path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	qv := url.Values{}
	qv.Add("_pragma", "journal_mode(WAL)")
	qv.Add("_pragma", "busy_timeout(5000)")
	qv.Add("_pragma", "synchronous(NORMAL)")
	u := url.URL{Scheme: "file", Path: abs, RawQuery: qv.Encode()}
	sdb, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if _, err := sdb.ExecContext(ctx, schema); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("metrics: open %s: %w", abs, err)
	}
	// A table from before power_cpu gains it; its old rows read as no
	// reading.
	var has int
	if err := sdb.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('metrics_minute') WHERE name = 'power_cpu'").Scan(&has); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("metrics: open %s: %w", abs, err)
	}
	if has == 0 {
		if _, err := sdb.ExecContext(ctx, "ALTER TABLE metrics_minute ADD COLUMN power_cpu REAL"); err != nil {
			sdb.Close()
			return nil, fmt.Errorf("metrics: open %s: %w", abs, err)
		}
	}
	return &DB{db: sdb}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// write stores one minute's rows, one per scope, in one transaction.
func (d *DB) write(ctx context.Context, minute time.Time, rows map[string]point) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := "INSERT OR REPLACE INTO metrics_minute (scope, ts, " + strings.Join(columns, ", ") +
		") VALUES (?, ?" + strings.Repeat(", ?", nFig) + ")"
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for scope, p := range rows {
		args := []any{scope, minute.Unix()}
		for _, v := range p {
			args = append(args, nullable(v))
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullable(v float64) any {
	if v != v { // NaN
		return nil
	}
	return v
}

// prune deletes the minutes before cut.
func (d *DB) prune(ctx context.Context, cut time.Time) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM metrics_minute WHERE ts < ?", cut.Unix())
	return err
}

// samples reads the minutes since since, averaged into buckets of bucket
// seconds (max for the max columns), as one sample per bucket.
func (d *DB) samples(ctx context.Context, since time.Time, bucket int) ([]sample, error) {
	sel := make([]string, len(columns))
	for i, c := range columns {
		agg := "avg"
		if strings.HasSuffix(c, "_max") {
			agg = "max"
		}
		sel[i] = agg + "(" + c + ")"
	}
	q := fmt.Sprintf("SELECT scope, (ts / %[1]d) * %[1]d AS t, %s FROM metrics_minute WHERE ts >= ? GROUP BY t, scope ORDER BY t",
		bucket, strings.Join(sel, ", "))
	rows, err := d.db.QueryContext(ctx, q, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sample
	for rows.Next() {
		var scope string
		var t int64
		vals := make([]sql.NullFloat64, nFig)
		dest := []any{&scope, &t}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].at.Unix() != t {
			out = append(out, sample{at: time.Unix(t, 0), host: emptyPoint(), parts: emptyParts(), stacks: map[string]point{}})
		}
		p := emptyPoint()
		for i, v := range vals {
			if v.Valid {
				p[i] = v.Float64
			}
		}
		s := &out[len(out)-1]
		if scope == hostScope {
			s.host = p
		} else if i := slices.Index(partScopes[:], scope); i >= 0 {
			s.parts[i] = p
		} else if !strings.HasPrefix(scope, "@") { // an older layout's part
			s.stacks[scope] = p
		}
	}
	return out, rows.Err()
}

// bucketOf is the resolution a range is read at: about 1500 points or fewer.
func bucketOf(r MetricsRange) (time.Duration, int, bool) {
	switch r {
	case Range24h:
		return 24 * time.Hour, 60, true
	case Range7d:
		return 7 * 24 * time.Hour, 300, true
	case Range30d:
		return 30 * 24 * time.Hour, 1800, true
	}
	return 0, 0, false
}

// History reads a range of stored minutes.
func (m *Metrics) History(ctx context.Context, r MetricsRange) (MetricFrames, error) {
	span, bucket, ok := bucketOf(r)
	if !ok {
		return MetricFrames{}, fmt.Errorf("unknown range %q", r)
	}
	if m.db == nil {
		return framesOf(nil, bucket, true), nil
	}
	ss, err := m.db.samples(ctx, m.now().Add(-span), bucket)
	if err != nil {
		return MetricFrames{}, err
	}
	return framesOf(ss, bucket, true), nil
}
