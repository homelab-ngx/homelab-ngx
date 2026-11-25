package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"

	"homelab-ngx.com/core/internal/store"
)

func (s *Service) previewStatePath(backupID int64) string {
	return filepath.Join(s.cfg.StateDir, fmt.Sprintf("preview-%d.json", backupID))
}

func (s *Service) previewDir(backupID int64) string {
	return filepath.Join(s.cfg.RunDir, fmt.Sprintf("preview-%d", backupID))
}

// Cancel stops the backup's run or preview in flight. It returns once the
// unit has stopped and the outcome is recorded as cancelled. Files a
// run has already copied stay at the destination.
func (s *Service) Cancel(ctx context.Context, id int64) error {
	b, err := s.backupRow(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if _, busy := s.running[id]; !busy {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotRunning, b.Name)
	}
	s.cancelled[id] = true
	cancelStart := s.startCancel[id]
	done := s.runDone[id]
	s.mu.Unlock()
	s.log.Info("stopping a backup on request", "backup", b.Name)
	if cancelStart != nil {
		cancelStart() // still in a prepare step: there is no unit yet
	}
	// The stop is asked of systemd once; a caller that goes away does not
	// take it back.
	if err := s.stopUnit(context.WithoutCancel(ctx), unitName(id)); err != nil {
		return err
	}
	// The watcher records the outcome a moment after the unit is gone; wait
	// for it, so the caller reads the run as stopped.
	if done != nil {
		t := time.NewTimer(haltWatchWait)
		defer t.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-t.C:
		}
	}
	return nil
}

// previewPlan is the backup's plan as a preview runs it: nothing is prepared
// first, and the folders say so.
func (s *Service) previewPlan(ctx context.Context, b store.Backup) (*plan, error) {
	p, err := s.buildPlan(ctx, b)
	if err != nil {
		return nil, err
	}
	any := false
	for _, d := range p.spec.Destinations {
		any = any || d.Kind != runfile.KindRestic
	}
	if !any {
		return nil, fmt.Errorf("%w: %s copies to restic only", ErrNothingToPreview, b.Name)
	}
	p.spec.Preview = true
	p.write = nil // a preview writes nowhere, a local destination included
	for _, pr := range p.prepares {
		for i := range p.spec.Folders {
			if p.folderStack[i] == pr.stack {
				p.spec.Folders[i].Note = "a preview does not run the stack's prepare step, so these files may be older than a run's"
			}
		}
	}
	if p.core {
		for i := range p.spec.Folders {
			if p.spec.Folders[i].Path == s.cfg.DBBackupDir {
				p.spec.Folders[i].Note = "a preview does not take a new database snapshot"
			}
		}
	}
	p.prepares = nil
	return p, nil
}

// Preview starts a preview of the backup in the background: the runner
// compares every folder with its destinations and changes nothing there. It
// takes the backup's place in line like a run, so the two never overlap, and
// replaces the backup's earlier preview.
func (s *Service) Preview(ctx context.Context, id int64) error {
	b, err := s.backupRow(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.previewPlan(ctx, b); err != nil {
		return err
	}
	s.mu.Lock()
	if s.goingDown {
		s.mu.Unlock()
		return ErrShuttingDown
	}
	if _, busy := s.running[id]; busy {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, b.Name)
	}
	s.running[id] = 0
	s.previewing[id] = true
	s.runDone[id] = make(chan struct{})
	base := s.base
	s.mu.Unlock()
	started := s.now()
	if err := s.q.StartBackupPreview(base, store.StartBackupPreviewParams{BackupID: id, StartedAt: started.UnixMilli()}); err != nil {
		s.release(id)
		return fmt.Errorf("record backup preview: %w", err)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.release(id)
		if s.startPreview(base, b) {
			s.watchPreview(base, id, started)
		}
	}()
	return nil
}

// startPreview launches the runner for a preview. It reports whether there
// is a unit to watch; when there is not, the preview is already recorded.
func (s *Service) startPreview(ctx context.Context, b store.Backup) bool {
	stopped := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.goingDown || s.cancelled[b.ID]
	}
	p, err := s.previewPlan(ctx, b)
	if err != nil {
		s.finishPreview(ctx, b.ID, ResultFailure, err.Error(), nil)
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if stopped() {
		s.finishPreview(ctx, b.ID, ResultCancelled, "the preview was stopped", nil)
		return false
	}
	dir := s.previewDir(b.ID)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.finishPreview(ctx, b.ID, ResultFailure, "the preview could not be prepared: "+err.Error(), nil)
		return false
	}
	p.spec.WorkDir = dir
	specPath := filepath.Join(dir, "spec.json")
	if err := runfile.WriteSpec(specPath, p.spec); err != nil {
		s.finishPreview(ctx, b.ID, ResultFailure, "the preview could not be prepared: "+err.Error(), nil)
		return false
	}
	_ = os.Remove(s.previewStatePath(b.ID))
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launchTimeout)
	defer cancel()
	// --preview is what makes it a preview: a runner that does not know the
	// argument exits instead of copying.
	if _, err := s.launch(lctx, unitName(b.ID), "Homelab backup preview: "+b.Name, specPath, s.previewStatePath(b.ID), p.write, false, "--preview"); err != nil {
		_ = os.RemoveAll(dir)
		s.finishPreview(ctx, b.ID, ResultFailure, "the preview could not be started: "+err.Error(), nil)
		return false
	}
	if stopped() {
		_ = s.stopUnit(context.WithoutCancel(ctx), unitName(b.ID))
	}
	s.log.Info("backup preview started", "backup", b.Name, "unit", unitName(b.ID))
	return true
}

// watchPreview follows a launched preview to its end and records it. If ctx
// ends first the row stays running for the next daemon to adopt.
func (s *Service) watchPreview(ctx context.Context, backupID int64, started time.Time) {
	st, interrupted, done := s.await(ctx, unitName(backupID), s.previewStatePath(backupID), started)
	switch {
	case !done:
	case st == nil:
		s.finishPreview(ctx, backupID, ResultUnknown, errDaemonStopped.Error(), nil)
	default:
		result, msg, items := s.settle(backupID, st, interrupted)
		if result == ResultCancelled {
			msg = "the preview was stopped"
		}
		s.finishPreview(ctx, backupID, result, msg, items)
	}
}

// finishPreview records how the preview ended and removes its files. A
// preview opens no problem, sends nothing and pings nothing.
func (s *Service) finishPreview(ctx context.Context, backupID int64, result, msg string, items []runfile.Item) {
	ctx = context.WithoutCancel(ctx)
	if items == nil {
		items = []runfile.Item{}
	}
	doc, err := json.Marshal(items)
	if err == nil {
		fin := s.now().UnixMilli()
		err = s.q.FinishBackupPreview(ctx, store.FinishBackupPreviewParams{
			FinishedAt: &fin, Result: result, Error: msg, Items: string(doc), BackupID: backupID,
		})
	}
	if err != nil {
		s.log.Error("failed to record backup preview", "backup_id", backupID, "error", err)
	} else {
		_ = os.Remove(s.previewStatePath(backupID))
	}
	_ = os.RemoveAll(s.previewDir(backupID))
	s.log.Info("backup preview finished", "backup_id", backupID, "result", result, "error", msg)
}

// adoptPreviews takes over the previews a previous daemon left running and
// returns the names of their working directories.
func (s *Service) adoptPreviews(ctx context.Context) map[string]bool {
	live := map[string]bool{}
	rows, err := s.q.RunningBackupPreviews(ctx)
	if err != nil {
		s.log.Warn("read backup previews left running", "error", err)
		return live
	}
	for _, row := range rows {
		id := row.BackupID
		live[filepath.Base(s.previewDir(id))] = true
		s.mu.Lock()
		if _, busy := s.running[id]; busy {
			s.mu.Unlock()
			continue
		}
		s.running[id] = 0
		s.previewing[id] = true
		s.runDone[id] = make(chan struct{})
		s.mu.Unlock()
		s.log.Info("adopting a backup preview the previous daemon left running", "backup_id", id)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.release(id)
			s.watchPreview(ctx, id, time.UnixMilli(row.StartedAt))
		}()
	}
	return live
}

func previewItem(it runfile.Item) BackupPreviewItem {
	out := BackupPreviewItem{
		Kind: BackupItemKind(it.Kind), Destination: it.Destination, Label: it.Label, Path: it.Path,
		Result: BackupItemResult(it.Result), Error: it.Error, Note: it.Note,
		NewSample: []string{}, ChangedSample: []string{}, ExtraSample: []string{},
	}
	if p := it.Preview; p != nil {
		out.New, out.NewBytes, out.Changed, out.ChangedBytes = p.New, p.NewBytes, p.Changed, p.ChangedBytes
		out.Unchanged, out.Extra, out.Deletes, out.Errors = p.Unchanged, p.Extra, p.Deletes, p.Errors
		out.NewSample = append(out.NewSample, p.NewSample...)
		out.ChangedSample = append(out.ChangedSample, p.ChangedSample...)
		out.ExtraSample = append(out.ExtraSample, p.ExtraSample...)
	}
	return out
}

// PreviewDetail returns the backup's newest preview. While it is in flight
// its items and progress come from the runner's state file.
func (s *Service) PreviewDetail(ctx context.Context, id int64) (BackupPreview, error) {
	b, err := s.backupRow(ctx, id)
	if err != nil {
		return BackupPreview{}, err
	}
	row, err := s.q.GetBackupPreview(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupPreview{}, fmt.Errorf("%w: %s", ErrNoPreview, b.Name)
	}
	if err != nil {
		return BackupPreview{}, err
	}
	out := BackupPreview{
		BackupId: id, StartedAt: ms(row.StartedAt), FinishedAt: msPtr(row.FinishedAt),
		Result: BackupRunResult(row.Result), Error: row.Error, Items: []BackupPreviewItem{},
	}
	var items []runfile.Item
	if row.Result == ResultRunning {
		st, err := runfile.ReadState(s.previewStatePath(id))
		if err != nil {
			return out, nil // getting ready: no runner yet
		}
		items = st.Items
		if p := st.Progress; p != nil && !st.Final() {
			out.Progress = &BackupProgress{
				Destination: p.Destination, Folder: p.Folder, FilesDone: p.FilesDone, FilesTotal: p.FilesTotal,
			}
		}
	} else if err := json.Unmarshal([]byte(row.Items), &items); err != nil {
		return BackupPreview{}, fmt.Errorf("read the preview of %s: %w", b.Name, err)
	}
	for _, it := range items {
		out.Items = append(out.Items, previewItem(it))
	}
	return out, nil
}
