package backups

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
)

// errDaemonStopped is a run's error when the daemon that started it stopped
// before the runner was launched, or the runner left nothing to read.
var errDaemonStopped = errors.New("the daemon stopped before the run finished")

// unitReadAttempts is how many failed reads of the run's unit a watcher
// allows before it reads the outcome from what the runner left.
const unitReadAttempts = 3

// launchTimeout bounds starting the unit, not the run.
const launchTimeout = 30 * time.Second

func unitName(backupID int64) string { return fmt.Sprintf("homelab-backup-%d.service", backupID) }

func (s *Service) statePath(runID int64) string {
	return filepath.Join(s.cfg.StateDir, strconv.FormatInt(runID, 10)+".json")
}

func (s *Service) runDir(runID int64) string {
	return filepath.Join(s.cfg.RunDir, "run-"+strconv.FormatInt(runID, 10))
}

// prepare is a stack whose prepare unit a run starts before copying.
type prepare struct {
	stack, unit string
	running     bool
}

// plan is a run as the runner will be told, plus what the daemon does first.
type plan struct {
	spec     *runfile.Spec
	write    []string  // local directories the run may write (local destinations)
	prepares []prepare // one per stack with a prepare unit
	// folderStack is the stack each spec folder belongs to; "" for the rest.
	folderStack []string
	core        bool
}

// buildPlan resolves a backup into a plan. ErrNotReady means it cannot run
// as configured.
func (s *Service) buildPlan(ctx context.Context, b store.Backup) (*plan, error) {
	targets, err := s.q.ListBackupTargetsOf(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: %s has no destination", ErrNotReady, b.Name)
	}
	sources, err := s.q.ListBackupSourcesOf(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: %s has nothing to back up", ErrNotReady, b.Name)
	}
	p := &plan{spec: &runfile.Spec{
		Version: runfile.Version, Backup: b.Name, Bwlimit: b.Bwlimit, Transfers: int(b.Transfers),
		CacheDir: s.cfg.CacheDir,
	}}
	if s.cfg.PoolMounts != nil {
		p.spec.PoolMounts = s.cfg.PoolMounts()
	}
	for _, t := range targets {
		row, err := s.destination(ctx, t.DestinationID)
		if err != nil {
			return nil, err
		}
		if !destinationReady(row) {
			return nil, fmt.Errorf("%w: confirm that the password of %s is saved first", ErrNotReady, row.Name)
		}
		d, write, err := s.destinationSpec(row)
		if err != nil {
			return nil, err
		}
		p.spec.Destinations = append(p.spec.Destinations, d)
		p.write = append(p.write, write...)
	}
	folders := s.stackFolders(ctx)
	seenPrepare := map[string]bool{}
	add := func(f runfile.Folder, stack string) {
		p.spec.Folders = append(p.spec.Folders, f)
		p.folderStack = append(p.folderStack, stack)
	}
	for _, src := range sources {
		view := s.sourceView(src, folders)
		remote := func(host string) string {
			if src.RemotePath != nil {
				return *src.RemotePath
			}
			return strings.TrimPrefix(host, "/")
		}
		var extra []string
		for _, e := range view.Excludes {
			extra = append(extra, e.Pattern)
		}
		switch view.Kind {
		case SourceCore:
			p.core = true
			base := func(host, sub string) string {
				if src.RemotePath != nil {
					return path.Join(*src.RemotePath, sub)
				}
				return strings.TrimPrefix(host, "/")
			}
			add(runfile.Folder{
				Label: "Server settings: database", Path: s.cfg.DBBackupDir,
				RemotePath: base(s.cfg.DBBackupDir, "daemon"), Excludes: []string{"/*.tmp"},
			}, "")
			add(runfile.Folder{
				Label: "Server settings: stack variables and list", Path: s.cfg.StacksDir,
				RemotePath: base(s.cfg.StacksDir, "stacks"),
				Files:      []string{"/stacks.lock.json", "/*/variables.age"},
			}, "")
			if s.cfg.DraftsDir != "" {
				if _, err := os.Stat(s.cfg.DraftsDir); err == nil {
					add(runfile.Folder{
						Label: "Server settings: stacks written in the dashboard", Path: s.cfg.DraftsDir,
						RemotePath: base(s.cfg.DraftsDir, "drafts"),
					}, "")
				}
			}
		case SourcePath:
			add(runfile.Folder{
				Label: view.Label, Path: view.Path, RemotePath: remote(view.Path), Excludes: extra, SizeOnly: view.SizeOnly,
			}, "")
		case SourceStack:
			f := runfile.Folder{Label: view.Label, Path: view.HostPath, Excludes: extra, SizeOnly: view.SizeOnly}
			for _, e := range view.DeclaredExcludes {
				f.Excludes = append(f.Excludes, e.Pattern)
			}
			if view.Problem != "" {
				f.Fail = view.Problem
			} else {
				f.RemotePath = remote(view.HostPath)
			}
			if sf, _, ok := findFolder(folders, src.Stack, src.Folder); ok && sf.Prepare != "" && !seenPrepare[sf.Stack] {
				seenPrepare[sf.Stack] = true
				p.prepares = append(p.prepares, prepare{stack: sf.Stack, unit: sf.Prepare, running: sf.Running})
			}
			add(f, src.Stack)
		}
	}
	return p, nil
}

// launch starts the runner in a transient unit that sees the whole
// filesystem read-only and may write only its own files and write. It
// returns when the unit has started, or with wait when it has ended.
func (s *Service) launch(ctx context.Context, unit, desc, specPath, statePath string, write []string, wait bool, runnerArgs ...string) (cmdrunner.Result, error) {
	workDir := filepath.Dir(specPath)
	for _, d := range []string{filepath.Dir(statePath), s.cfg.CacheDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return cmdrunner.Result{}, err
		}
	}
	args := []string{"--unit=" + strings.TrimSuffix(unit, ".service"), "--collect", "--quiet", "--description=" + desc}
	// Without --wait systemd-run returns once the unit has started, so a
	// watcher that asks about the unit right after never sees it "inactive"
	// because its start job has not run yet. (--no-block would return before.)
	if wait {
		args = append(args, "--wait")
	}
	args = append(args,
		"-p", "ProtectSystem=strict",
		"-p", "CapabilityBoundingSet=CAP_DAC_READ_SEARCH",
		"-p", "NoNewPrivileges=yes",
		"-p", "PrivateTmp=yes",
		"-p", "Nice=10",
		"-p", "IOSchedulingClass=idle",
		"-p", "RuntimeMaxSec=72h",
		"-p", "TimeoutStopSec=2min",
		"-p", "ReadWritePaths="+workDir,
		"-p", "ReadWritePaths="+filepath.Dir(statePath),
		"-p", "ReadWritePaths="+s.cfg.CacheDir,
	)
	for _, w := range write {
		// "-": a local destination that is not there fails in the tool, with
		// its own message, instead of failing the unit's start.
		args = append(args, "-p", "ReadWritePaths=-"+w)
	}
	if s.cfg.PoolMounts != nil {
		// At a shutdown systemd stops units in the reverse of their start order:
		// a pool mount is unmounted only after the backup unit has stopped.
		seen := map[string]bool{}
		for _, mp := range s.cfg.PoolMounts() {
			if mp == "" || seen[mp] {
				continue
			}
			seen[mp] = true
			args = append(args, "-p", "WantsMountsFor="+mp)
		}
	}
	args = append(args,
		"--setenv=PATH="+os.Getenv("PATH"), "--setenv=HOME="+workDir, "--setenv=XDG_CACHE_HOME="+s.cfg.CacheDir,
		"--", s.cfg.Runner, "--spec", specPath, "--state", statePath,
	)
	args = append(args, runnerArgs...)
	return s.r.Run(ctx, cmdrunner.Cmd{Module: "backups", Name: "systemd-run", Args: args})
}

func (s *Service) runScheduled(id int64) {
	if _, err := s.Trigger(context.Background(), id, TriggerCron); err != nil {
		s.log.Warn("scheduled backup not started", "backup_id", id, "error", err)
		if errors.Is(err, ErrNotReady) {
			// A scheduled backup that cannot run must not stay silent.
			if b, berr := s.q.GetBackup(context.Background(), id); berr == nil {
				s.notifyResult(context.Background(), b.ID, 0, b.Name, err.Error())
			}
		}
	}
}

// Trigger records a run and starts it in the background. It returns the run
// id as soon as the run is recorded.
func (s *Service) Trigger(ctx context.Context, id int64, trigger string) (int64, error) {
	b, err := s.backupRow(ctx, id)
	if err != nil {
		return 0, err
	}
	// Fail now, where the caller can read why, not in the background.
	if _, err := s.buildPlan(ctx, b); err != nil {
		return 0, err
	}
	s.mu.Lock()
	if s.goingDown {
		s.mu.Unlock()
		return 0, ErrShuttingDown
	}
	if _, busy := s.running[id]; busy {
		s.mu.Unlock()
		return 0, fmt.Errorf("%w: %s", ErrAlreadyRunning, b.Name)
	}
	s.running[id] = 0
	s.runDone[id] = make(chan struct{})
	base := s.base
	s.mu.Unlock()
	run := store.BackupRun{BackupID: &id, BackupName: b.Name, Trigger: trigger, StartedAt: s.now().UnixMilli(), Result: ResultRunning}
	run.ID, err = s.q.InsertBackupRun(base, store.InsertBackupRunParams{
		BackupID: &id, BackupName: b.Name, Trigger: trigger, StartedAt: run.StartedAt,
	})
	if err != nil {
		s.release(id)
		return 0, fmt.Errorf("record backup run: %w", err)
	}
	s.mu.Lock()
	s.running[id] = run.ID
	s.mu.Unlock()
	// A preview said what this run would do; after it, that is no longer true.
	if err := s.q.DeleteBackupPreview(base, id); err != nil {
		s.log.Warn("remove the backup's preview", "backup", b.Name, "error", err)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.release(id)
		if s.start(base, run, b) {
			s.watch(base, run)
		}
	}()
	return run.ID, nil
}

func (s *Service) release(id int64) {
	s.mu.Lock()
	delete(s.running, id)
	delete(s.startCancel, id)
	delete(s.previewing, id)
	delete(s.cancelled, id)
	if ch, ok := s.runDone[id]; ok {
		close(ch)
		delete(s.runDone, id)
	}
	s.mu.Unlock()
}

// start does the daemon's part of a run (healthcheck ping, prepare units,
// database snapshot) and launches the runner. It reports whether there is a
// unit to watch; when there is not, the run is already recorded.
func (s *Service) start(ctx context.Context, run store.BackupRun, b store.Backup) bool {
	log := s.log.With("backup", b.Name, "run_id", run.ID)
	startCtx, cancelStart := context.WithCancel(ctx)
	s.mu.Lock()
	if s.goingDown {
		s.mu.Unlock()
		cancelStart()
		s.finish(ctx, run, ResultCancelled, "the run was stopped", nil)
		return false
	}
	s.startCancel[b.ID] = cancelStart
	s.mu.Unlock()
	defer func() {
		cancelStart()
		s.mu.Lock()
		delete(s.startCancel, b.ID)
		s.mu.Unlock()
	}()
	if b.HealthcheckUrl != "" && s.hc != nil {
		s.hc.Ping(context.WithoutCancel(ctx), b.HealthcheckUrl+"/start")
	}
	p, err := s.buildPlan(startCtx, b)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if startCtx.Err() != nil {
			s.finish(ctx, run, ResultCancelled, "the run was stopped", nil)
			return false
		}
		s.finish(ctx, run, ResultFailure, err.Error(), nil)
		return false
	}
	p.spec.RunID = run.ID
	for _, pr := range p.prepares {
		var fail, note string
		switch {
		case !pr.running:
			note = "the stack was stopped, so its prepare step did not run"
		case s.stacks == nil:
		default:
			log.Info("running the stack's prepare unit", "stack", pr.stack, "unit", pr.unit)
			if err := s.stacks.RunPrepare(startCtx, pr.stack, pr.unit); err != nil {
				if ctx.Err() != nil {
					return false // the next daemon closes the run
				}
				if startCtx.Err() != nil {
					s.finish(ctx, run, ResultCancelled, "the run was stopped", nil)
					return false
				}
				log.Warn("prepare unit failed", "stack", pr.stack, "unit", pr.unit, "error", err)
				fail = fmt.Sprintf("the prepare step %s failed: %s", pr.unit, err)
			}
		}
		for i := range p.spec.Folders {
			if p.folderStack[i] != pr.stack {
				continue
			}
			if fail != "" && p.spec.Folders[i].Fail == "" {
				p.spec.Folders[i].Fail = fail
			}
			if note != "" {
				p.spec.Folders[i].Note = note
			}
		}
	}
	if p.core && s.cfg.Snapshot != nil {
		if err := s.cfg.Snapshot(startCtx); err != nil {
			log.Warn("database snapshot failed", "error", err)
			for i := range p.spec.Folders {
				if p.spec.Folders[i].Path == s.cfg.DBBackupDir {
					p.spec.Folders[i].Fail = "the database snapshot failed: " + err.Error()
				}
			}
		}
	}
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	stopped := s.goingDown || startCtx.Err() != nil
	s.mu.Unlock()
	if stopped {
		s.finish(ctx, run, ResultCancelled, "the run was stopped", nil)
		return false
	}
	dir := s.runDir(run.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.finish(ctx, run, ResultFailure, "the run could not be prepared: "+err.Error(), nil)
		return false
	}
	p.spec.WorkDir = dir
	specPath := filepath.Join(dir, "spec.json")
	if err := runfile.WriteSpec(specPath, p.spec); err != nil {
		s.finish(ctx, run, ResultFailure, "the run could not be prepared: "+err.Error(), nil)
		return false
	}
	_ = os.Remove(s.statePath(run.ID))
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launchTimeout)
	defer cancel()
	if _, err := s.launch(lctx, unitName(b.ID), "Homelab backup: "+b.Name, specPath, s.statePath(run.ID), p.write, false); err != nil {
		_ = os.RemoveAll(dir)
		s.finish(ctx, run, ResultFailure, "the run could not be started: "+err.Error(), nil)
		return false
	}
	// A stop asked for while the unit was starting found no unit to stop.
	s.mu.Lock()
	down := s.goingDown || s.cancelled[b.ID]
	s.mu.Unlock()
	if down {
		_ = s.stopUnit(context.WithoutCancel(ctx), unitName(b.ID))
	}
	log.Info("backup run started", "unit", unitName(b.ID))
	return true
}

// unitRunning reports whether an ActiveState means the unit is in flight.
func unitRunning(state string) bool {
	switch state {
	case "activating", "active", "deactivating", "reloading":
		return true
	}
	return false
}

// watch follows a launched run to its end and records the outcome. If ctx
// ends first the row stays running for the next daemon to adopt.
func (s *Service) watch(ctx context.Context, run store.BackupRun) {
	if run.BackupID == nil {
		return
	}
	st, interrupted, done := s.await(ctx, unitName(*run.BackupID), s.statePath(run.ID), time.UnixMilli(run.StartedAt))
	switch {
	case !done:
	case st == nil:
		s.finish(ctx, run, ResultUnknown, errDaemonStopped.Error(), nil)
	default:
		s.finishFromState(ctx, run, st, interrupted)
	}
}

// await follows a launched unit until its runner has said how it ended, or
// the unit is gone. It returns what the runner left (nil when nothing can be
// read) and, when the runner did not finish, why. done is false when ctx
// ended first.
func (s *Service) await(ctx context.Context, unit, statePath string, started time.Time) (st *runfile.State, interrupted string, done bool) {
	failedReads := 0
	for {
		st, err := runfile.ReadState(statePath)
		if err == nil && st.Final() {
			return st, "", true
		}
		p := s.sd.PropsUnix(ctx, []string{unit}, "Id", "ActiveState")[unit]
		if ctx.Err() != nil {
			return nil, "", false
		}
		wait := s.pollEvery
		switch {
		case p == nil:
			// A failed read (systemd busy mid-switch) is not an outcome: ask
			// again, slowly, before concluding anything.
			failedReads++
			wait = s.retryEvery
		case unitRunning(p["ActiveState"]):
			failedReads = 0
		default:
			failedReads = unitReadAttempts
		}
		if failedReads >= unitReadAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, "", false
		case <-time.After(wait):
		}
	}
	// The unit is gone. The runner may have written its last word between the
	// two reads above.
	st, err := runfile.ReadState(statePath)
	switch {
	case err == nil && st.Final():
		return st, "", true
	case err == nil:
		reason := "the runner stopped before it finished"
		if outcome, found, oerr := s.sd.LastRunOutcome(ctx, unit, started); oerr == nil && found && outcome != "success" {
			reason += " (unit result " + outcome + ")"
		}
		return st, reason, true
	}
	return nil, "", true
}

// settle turns what the runner left into a result, a message and items.
// interrupted, when set, is why the runner did not finish: the run is then a
// failure, or cancelled when the operator stopped it, and its unfinished
// items are closed.
func (s *Service) settle(backupID int64, st *runfile.State, interrupted string) (result, msg string, items []runfile.Item) {
	result, msg = st.Result, st.Error
	items = make([]runfile.Item, len(st.Items))
	copy(items, st.Items)
	if interrupted != "" {
		s.mu.Lock()
		stopped := s.cancelled[backupID]
		s.mu.Unlock()
		result, msg = ResultFailure, interrupted
		open := runfile.ResultSkipped
		if stopped {
			result, msg, open = ResultCancelled, "the run was stopped", runfile.ResultCancelled
		}
		for i := range items {
			if items[i].Result == runfile.ResultPending || items[i].Result == runfile.ResultRunning {
				items[i].Result = open
			}
		}
	}
	switch result {
	case ResultSuccess, ResultPartial, ResultFailure, ResultCancelled:
	default:
		result = ResultFailure
	}
	if msg == "" && result != ResultSuccess {
		msg = summarize(items)
	}
	return result, msg, items
}

// finishFromState records what the runner left as the run's outcome.
func (s *Service) finishFromState(ctx context.Context, run store.BackupRun, st *runfile.State, interrupted string) {
	var id int64
	if run.BackupID != nil {
		id = *run.BackupID
	}
	result, msg, items := s.settle(id, st, interrupted)
	s.finish(ctx, run, result, msg, items)
}

// summarize names what went wrong in a run that gave no error of its own.
func summarize(items []runfile.Item) string {
	var failed []string
	for _, it := range items {
		if it.Result != runfile.ResultFailure && it.Result != runfile.ResultPartial {
			continue
		}
		name := it.Label
		if it.Kind == runfile.ItemDestination {
			name = it.Destination
		} else {
			name += " to " + it.Destination
		}
		failed = append(failed, name)
	}
	switch len(failed) {
	case 0:
		return "the run did not finish"
	case 1:
		return failed[0] + " failed"
	case 2, 3:
		return strings.Join(failed, ", ") + " failed"
	}
	return fmt.Sprintf("%s and %d more failed", strings.Join(failed[:2], ", "), len(failed)-2)
}

// finish writes the run's result and items, opens or resolves the backup's
// problem, sends the healthcheck ping and removes the run's files. The poll
// is over, so a daemon stopping now must not cancel any of it.
func (s *Service) finish(ctx context.Context, run store.BackupRun, result, msg string, items []runfile.Item) {
	ctx = context.WithoutCancel(ctx)
	var files, bytes int64
	for _, it := range items {
		if it.Kind == runfile.ItemFolder {
			files += it.Files
			bytes += it.Bytes
		}
	}
	fin := s.now().UnixMilli()
	err := s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteBackupRunItems(ctx, run.ID); err != nil {
			return err
		}
		for i, it := range items {
			destID := it.DestinationID
			if err := q.InsertBackupRunItem(ctx, store.InsertBackupRunItemParams{
				RunID: run.ID, Kind: it.Kind, DestinationID: &destID, DestinationName: it.Destination,
				Label: it.Label, Path: it.Path, Position: int64(i), Result: it.Result, Files: it.Files, Bytes: it.Bytes,
				StartedAt: unixMs(it.StartedAt), FinishedAt: unixMs(it.FinishedAt), Error: it.Error, Note: it.Note,
			}); err != nil {
				return err
			}
		}
		return q.FinishBackupRun(ctx, store.FinishBackupRunParams{
			FinishedAt: &fin, Result: result, Error: msg, Files: files, Bytes: bytes, ID: run.ID,
		})
	})
	if err != nil {
		s.log.Error("failed to record backup result", "backup", run.BackupName, "run_id", run.ID, "error", err)
	} else {
		_ = os.Remove(s.statePath(run.ID))
	}
	_ = os.RemoveAll(s.runDir(run.ID))
	s.log.Info("backup run finished", "backup", run.BackupName, "run_id", run.ID, "result", result, "error", msg)
	if run.BackupID == nil || result == ResultUnknown {
		return // an unknown outcome leaves the problem as it is
	}
	if result == ResultCancelled {
		// Somebody stopped it: that is not a failed backup. The problem stays
		// as it was, the healthcheck gets no ping (it alerts by itself when no
		// later run succeeds in time), and the stop is told once.
		s.notifyStopped(ctx, *run.BackupID, run.ID, run.BackupName)
		return
	}
	problem := ""
	if result != ResultSuccess {
		problem = msg
	}
	s.notifyResult(ctx, *run.BackupID, run.ID, run.BackupName, problem)
	if b, err := s.q.GetBackup(ctx, *run.BackupID); err == nil && b.HealthcheckUrl != "" && s.hc != nil {
		url := b.HealthcheckUrl
		if result != ResultSuccess {
			url += "/fail"
		}
		s.hc.Ping(ctx, url)
	}
}

func unixMs(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.UnixMilli()
	return &v
}

// notifyResult opens the backup's problem with detail, or resolves it when
// detail is empty. The problem links to the run when there is one (runID 0:
// the backup's page).
func (s *Service) notifyResult(ctx context.Context, id, runID int64, name, detail string) {
	if s.notify == nil {
		return
	}
	var err error
	if detail != "" {
		_, err = s.notify.Report(ctx, notifications.Problem{
			Key: problemKey(id), Title: "Backup failed: " + name, Detail: detail, Path: backupPath(id, runID), Event: "backup.failed",
		})
	} else {
		_, err = s.notify.Resolve(ctx, problemKey(id))
	}
	if err != nil {
		s.log.Warn("failed to send backup notification", "backup", name, "error", err)
	}
}

// notifyStopped tells, as information, that a run was stopped.
func (s *Service) notifyStopped(ctx context.Context, id, runID int64, name string) {
	if s.notify == nil {
		return
	}
	if _, err := s.notify.Send(ctx, notifications.Message{
		Severity: notifications.Info, Title: "Backup stopped: " + name,
		Body: "The run was stopped before it finished. Files already copied stay at the destination.",
		Path: backupPath(id, runID), Event: "backup.stopped", Key: fmt.Sprintf("backup-stopped:%d", id),
	}); err != nil && !errors.Is(err, notifications.ErrNoChannels) {
		s.log.Warn("failed to send backup notification", "backup", name, "error", err)
	}
}

// backupPath is the dashboard page of a backup, opened on one of its runs.
func backupPath(id, runID int64) string {
	if runID == 0 {
		return fmt.Sprintf("/backups/%d", id)
	}
	return fmt.Sprintf("/backups/%d?run=%d", id, runID)
}

func notificationFor(stack, body string) notifications.Message {
	return notifications.Message{
		Severity: notifications.Info, Title: "Not backed up: " + stack, Body: body,
		Path: "/backups", Event: "backup.coverage", Key: "backup-coverage:" + stack,
	}
}

// adoptRunning takes over the runs a previous daemon left running: each
// backup's newest one is watched to its end, the rest are unknown. Then it
// removes the working directories no run owns any more.
func (s *Service) adoptRunning(ctx context.Context) {
	rows, err := s.q.RunningBackupRuns(ctx)
	if err != nil {
		s.log.Warn("read backup runs left running", "error", err)
		return
	}
	live := s.adoptPreviews(ctx)
	for i, run := range rows {
		if run.BackupID == nil || (i > 0 && rows[i-1].BackupID != nil && *rows[i-1].BackupID == *run.BackupID) {
			s.finish(ctx, run, ResultUnknown, errDaemonStopped.Error(), nil)
			continue
		}
		id := *run.BackupID
		s.mu.Lock()
		if _, busy := s.running[id]; busy {
			s.mu.Unlock()
			live[filepath.Base(s.runDir(run.ID))] = true
			continue
		}
		s.running[id] = run.ID
		s.runDone[id] = make(chan struct{})
		s.mu.Unlock()
		live[filepath.Base(s.runDir(run.ID))] = true
		s.log.Info("adopting a backup run the previous daemon left running", "backup", run.BackupName, "run_id", run.ID)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.release(id)
			s.watch(ctx, run)
		}()
	}
	ents, err := os.ReadDir(s.cfg.RunDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !live[e.Name()] {
			_ = os.RemoveAll(filepath.Join(s.cfg.RunDir, e.Name()))
		}
	}
}

func itemView(it runfile.Item) BackupRunItem {
	return BackupRunItem{
		Kind: BackupItemKind(it.Kind), Destination: it.Destination, Label: it.Label, Path: it.Path,
		Result: BackupItemResult(it.Result), Files: it.Files, Bytes: it.Bytes,
		StartedAt: it.StartedAt, FinishedAt: it.FinishedAt, Error: it.Error, Note: it.Note,
	}
}

// Run returns one run with its items. While it is in flight they, and the
// progress, come from the runner's state file.
func (s *Service) RunDetail(ctx context.Context, runID int64) (BackupRunDetail, error) {
	row, err := s.q.GetBackupRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRunDetail{}, fmt.Errorf("%w: %d", ErrUnknownRun, runID)
	}
	if err != nil {
		return BackupRunDetail{}, err
	}
	out := BackupRunDetail{BackupRun: runView(row), Items: []BackupRunItem{}}
	if row.Result == ResultRunning {
		st, err := runfile.ReadState(s.statePath(runID))
		if err != nil {
			return out, nil // preparing: no runner yet
		}
		for _, it := range st.Items {
			out.Items = append(out.Items, itemView(it))
			if it.Kind == runfile.ItemFolder {
				out.Files += it.Files
				out.Bytes += it.Bytes
			}
		}
		if p := st.Progress; p != nil && !st.Final() {
			out.Progress = &BackupProgress{
				Destination: p.Destination, Folder: p.Folder, BytesDone: p.BytesDone, BytesTotal: p.BytesTotal,
				FilesDone: p.FilesDone, FilesTotal: p.FilesTotal, Speed: p.Speed,
			}
		}
		return out, nil
	}
	items, err := s.q.ListBackupRunItems(ctx, runID)
	if err != nil {
		return BackupRunDetail{}, err
	}
	for _, it := range items {
		out.Items = append(out.Items, BackupRunItem{
			Kind: BackupItemKind(it.Kind), Destination: it.DestinationName, Label: it.Label, Path: it.Path,
			Result: BackupItemResult(it.Result), Files: it.Files, Bytes: it.Bytes,
			StartedAt: msPtr(it.StartedAt), FinishedAt: msPtr(it.FinishedAt), Error: it.Error, Note: it.Note,
		})
	}
	return out, nil
}

// Busy names the backups running now, for the operator to see before a
// reboot interrupts them.
func (s *Service) Busy(ctx context.Context) []string {
	rows, err := s.q.RunningBackupRuns(ctx)
	if err != nil {
		s.log.Warn("read running backup runs", "error", err)
		return nil
	}
	var out []string
	for _, r := range rows {
		out = append(out, "backup "+r.BackupName)
	}
	return out
}
