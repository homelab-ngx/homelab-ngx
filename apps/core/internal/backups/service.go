package backups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/systemd"
)

// Run triggers stored in backup_runs.trigger.
const (
	TriggerCron   = "cron"
	TriggerManual = "manual"
)

// Run results stored in backup_runs.result.
const (
	ResultRunning   = "running"
	ResultSuccess   = "success"
	ResultPartial   = "partial"
	ResultFailure   = "failure"
	ResultCancelled = "cancelled"
	ResultUnknown   = "unknown"
)

var (
	// ErrUnknownBackup means no backup has that id.
	ErrUnknownBackup = errors.New("unknown backup")
	// ErrUnknownDestination means no destination has that id.
	ErrUnknownDestination = errors.New("unknown destination")
	// ErrUnknownRun means no run has that id.
	ErrUnknownRun = errors.New("unknown run")
	// ErrAlreadyRunning means a run of that backup is still in progress.
	ErrAlreadyRunning = errors.New("backup already running")
	// ErrNameTaken means another backup or destination has the name.
	ErrNameTaken = errors.New("name already in use")
	// ErrDestinationInUse means a backup still copies to the destination.
	ErrDestinationInUse = errors.New("destination is used by a backup")
	// ErrNotReady means the backup cannot run as it is configured.
	ErrNotReady = errors.New("backup is not ready to run")
	// ErrConfirm means a reveal was not confirmed with the destination's name.
	ErrConfirm = errors.New("confirmation does not match the destination's name")
)

// InvalidError is a request the service refuses, with the field at fault.
type InvalidError struct {
	Code string // problem code, e.g. invalid_schedule
	Msg  string
}

func (e *InvalidError) Error() string { return e.Msg }

func invalid(code, format string, args ...any) error {
	return &InvalidError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// HealthPinger pings a Healthchecks.io-style URL. Failures are the pinger's to
// log; they never fail a backup.
type HealthPinger interface {
	Ping(ctx context.Context, url string)
}

// NewHealthchecks returns the production HealthPinger. The 10s timeout keeps
// an unreachable ping host from stalling a backup run.
func NewHealthchecks() HealthPinger {
	return &httpPinger{c: &http.Client{Timeout: 10 * time.Second}, log: logging.Logger("backups")}
}

type httpPinger struct {
	c   *http.Client
	log *slog.Logger
}

func (p *httpPinger) Ping(ctx context.Context, url string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		p.log.Warn("healthcheck ping failed", "error", err)
		return
	}
	resp, err := p.c.Do(req)
	if err != nil {
		p.log.Warn("healthcheck ping failed", "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// StackFolder is one folder a stack recommends backing up.
type StackFolder struct {
	Key      string
	Label    string
	HostPath string
	Excludes []Exclude
	// Problem says why the folder could not be resolved; empty when it could.
	Problem string
}

// StackFolders is one stack's recommended folders.
type StackFolders struct {
	Stack   string
	Running bool
	// Prepare is the unit to run inside the stack before its folders are
	// copied; empty for none.
	Prepare string
	Folders []StackFolder
}

// Stacks is what backups needs from the stacks: the recommended folders and
// a way to run a stack's prepare unit.
type Stacks interface {
	BackupFolders(ctx context.Context) ([]StackFolders, error)
	RunPrepare(ctx context.Context, stack, unit string) error
}

// Config is where a Service finds its files and helpers.
type Config struct {
	// Runner is the homelab-backup-runner binary.
	Runner string
	// KeyFile holds the host's age identities; secrets are sealed to them.
	KeyFile string
	// StateDir holds one state file per run in flight.
	StateDir string
	// RunDir is a tmpfs directory; a run's spec and working files live in a
	// subdirectory of it. It is not under the daemon's own runtime directory,
	// which a daemon restart wipes.
	RunDir string
	// CacheDir is restic's cache.
	CacheDir string
	// DBBackupDir is where database snapshots are written; StacksDir holds
	// every stack's state; DraftsDir holds the stacks written in the
	// dashboard. Together they are the Core source.
	DBBackupDir string
	StacksDir   string
	DraftsDir   string
	// Snapshot writes a fresh database snapshot into DBBackupDir.
	Snapshot func(ctx context.Context) error
	// PoolMounts returns the pools' mountpoints.
	PoolMounts func() []string
}

// DefaultConfig is the layout on the host; the caller fills Runner, KeyFile,
// DBBackupDir, StacksDir, Snapshot and PoolMounts.
func DefaultConfig() Config {
	return Config{
		StateDir: "/var/lib/homelab-daemon/backup-runs",
		RunDir:   "/run/homelab-backup",
		CacheDir: "/var/cache/homelab-backup",
	}
}

// Service owns the backups, their destinations and their runs.
type Service struct {
	cfg    Config
	sd     *systemd.Client
	r      cmdrunner.Runner
	st     *store.Store
	q      *store.Queries
	stacks Stacks
	notify *notifications.Notifier
	hc     HealthPinger
	log    *slog.Logger

	mu      sync.Mutex
	base    context.Context // set by Run; parent of every run's context
	running map[int64]int64 // backup id -> run id in flight
	cron    *cron.Cron
	sig     string // signature of what cron was built from
	// started is when the Service was built (the daemon start): a backup that
	// never succeeded is measured from here.
	started time.Time
	wg      sync.WaitGroup
	folders *folderCache

	// Seams overridden by tests.
	now        func() time.Time
	pollEvery  time.Duration
	retryEvery time.Duration // between reads of a unit systemd could not answer for
}

// New wires a Service. stacks, notify and hc may be nil.
func New(cfg Config, sd *systemd.Client, r cmdrunner.Runner, st *store.Store, stacks Stacks,
	notify *notifications.Notifier, hc HealthPinger, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		cfg: cfg, sd: sd, r: r, st: st, q: st.Queries(), stacks: stacks, notify: notify, hc: hc, log: log,
		base: context.Background(), running: map[int64]int64{}, started: time.Now(),
		now: time.Now, pollEvery: 2 * time.Second, retryEvery: 30 * time.Second,
	}
}

// staleCheckEvery is how often scheduled backups are checked for staleness.
const staleCheckEvery = time.Hour

// staleIntervals is how many schedule intervals may pass without a successful
// run before the backup is reported stale.
const staleIntervals = 2

// Run adopts the runs a previous daemon left running, schedules the backups
// and checks them for staleness every hour. It blocks until ctx is done, then
// stops the scheduler and waits for the watchers; the runs themselves go on
// in their own units.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	s.adoptRunning(ctx)
	s.resolveOrphans(ctx)
	s.reschedule(ctx)
	tick := time.NewTicker(staleCheckEvery)
	defer tick.Stop()
	s.checkStale(ctx)
	for {
		select {
		case <-tick.C:
			s.checkStale(ctx)
		case <-ctx.Done():
			s.mu.Lock()
			c := s.cron
			s.cron, s.sig = nil, ""
			s.mu.Unlock()
			if c != nil {
				<-c.Stop().Done()
			}
			s.wg.Wait()
			return
		}
	}
}

// Wait blocks until every watcher has finished.
func (s *Service) Wait() { s.wg.Wait() }

// reschedule rebuilds the cron schedule from the stored backups if it
// changed. Every write to a backup calls it.
func (s *Service) reschedule(ctx context.Context) {
	rows, err := s.q.ListBackups(ctx)
	if err != nil {
		s.log.Error("read backups for the schedule", "error", err)
		return
	}
	var b strings.Builder
	for _, bk := range rows {
		if bk.Enabled && bk.Schedule != "" {
			fmt.Fprintf(&b, "%d|%s\n", bk.ID, bk.Schedule)
		}
	}
	sig := b.String()
	s.mu.Lock()
	if s.cron != nil && sig == s.sig {
		s.mu.Unlock()
		return
	}
	old := s.cron
	c := cron.New()
	for _, bk := range rows {
		if !bk.Enabled || bk.Schedule == "" {
			continue
		}
		id := bk.ID
		if _, err := c.AddFunc(bk.Schedule, func() { s.runScheduled(id) }); err != nil {
			s.log.Error("failed to schedule backup", "backup", bk.Name, "schedule", bk.Schedule, "error", err)
			continue
		}
		s.log.Info("scheduled backup", "backup", bk.Name, "schedule", bk.Schedule)
	}
	c.Start()
	s.cron, s.sig = c, sig
	s.mu.Unlock()
	if old != nil {
		old.Stop() // runs in flight go on in their units
	}
}

// scheduled returns the ids on the cron schedule (sorted); for tests.
func (s *Service) scheduled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(s.sig), "\n") {
		if u, _, ok := strings.Cut(line, "|"); ok {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// staleness sorts the scheduled backups by whether their last success (or, if
// there has never been one, the daemon start) is older than staleIntervals
// schedule intervals. It catches what a failed-run notification cannot: a host
// that was off, a daemon that was down, or a schedule that stopped firing.
func (s *Service) staleness(ctx context.Context, rows []store.Backup) (stale, ok []store.Backup) {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	now := s.now()
	runs, err := s.q.LastSuccessfulBackupRuns(ctx)
	if err != nil {
		s.log.Warn("stale check: read last successes", "error", err)
		return nil, nil
	}
	lastSuccess := make(map[int64]int64, len(runs))
	for _, r := range runs {
		if r.FinishedAt != nil && r.BackupID != nil {
			lastSuccess[*r.BackupID] = *r.FinishedAt
		}
	}
	for _, b := range rows {
		if !b.Enabled || b.Schedule == "" {
			continue
		}
		sched, err := cron.ParseStandard(b.Schedule)
		if err != nil {
			continue
		}
		first := sched.Next(now)
		interval := sched.Next(first).Sub(first)
		ref := started
		if at, found := lastSuccess[b.ID]; found {
			ref = time.UnixMilli(at)
		}
		if now.Sub(ref) > staleIntervals*interval {
			stale = append(stale, b)
		} else {
			ok = append(ok, b)
		}
	}
	return stale, ok
}

// checkStale opens a problem for every stale backup and resolves the ones that
// have recovered. Report and Resolve are idempotent per key.
func (s *Service) checkStale(ctx context.Context) {
	rows, err := s.q.ListBackups(ctx)
	if err != nil {
		s.log.Warn("stale check: read backups", "error", err)
		return
	}
	stale, ok := s.staleness(ctx, rows)
	for _, b := range stale {
		s.log.Warn("backup is stale", "backup", b.Name)
	}
	if s.notify == nil || !s.notify.Enabled() {
		return
	}
	for _, b := range stale {
		if _, err := s.notify.Report(ctx, notifications.Problem{
			Key: staleKey(b.ID), Title: "Backup stale: " + b.Name,
			Detail: "no successful run within two schedule intervals", Path: backupPath(b.ID, 0),
		}); err != nil {
			s.log.Warn("failed to send stale-backup notification", "backup", b.Name, "error", err)
		}
	}
	for _, b := range ok {
		if _, err := s.notify.Resolve(ctx, staleKey(b.ID)); err != nil {
			s.log.Warn("failed to resolve stale-backup notification", "backup", b.Name, "error", err)
		}
	}
}

func problemKey(id int64) string { return fmt.Sprintf("backup:%d", id) }
func staleKey(id int64) string   { return fmt.Sprintf("backup-stale:%d", id) }

// resolveOrphans closes the open backup problems whose backup is gone: a
// deleted backup, or the unit-named problems of the nix job this replaced.
// Nothing else would ever resolve them, and an open problem reminds daily.
func (s *Service) resolveOrphans(ctx context.Context) {
	if s.notify == nil || !s.notify.Enabled() {
		return
	}
	open, err := s.notify.OpenProblems(ctx)
	if err != nil {
		s.log.Warn("read open problems", "error", err)
		return
	}
	rows, err := s.q.ListBackups(ctx)
	if err != nil {
		s.log.Warn("read backups", "error", err)
		return
	}
	live := make(map[string]bool, 2*len(rows))
	for _, b := range rows {
		live[problemKey(b.ID)], live[staleKey(b.ID)] = true, true
	}
	for _, p := range open {
		if !strings.HasPrefix(p.Key, "backup:") && !strings.HasPrefix(p.Key, "backup-stale:") {
			continue
		}
		if live[p.Key] {
			continue
		}
		if _, err := s.notify.Resolve(ctx, p.Key); err != nil {
			s.log.Warn("failed to resolve an orphaned backup problem", "key", p.Key, "error", err)
		}
	}
}

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func msPtr(v *int64) *time.Time {
	if v == nil {
		return nil
	}
	t := ms(*v)
	return &t
}
