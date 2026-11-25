package backups

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"homelab-ngx.com/core/backup-runner/runfile"

	"homelab-ngx.com/core/internal/store"
)

// flight is the backup's unit as a test can stop it: a launch leaves the state
// the runner would have while it works, and `systemctl stop` leaves what
// stopped says and ends the unit.
type flight struct {
	mu        sync.Mutex
	statePath string
	spec      *runfile.Spec
	args      []string
	launched  chan struct{}
	stops     int
}

func (h *harness) flight(unit string, stopped func(*runfile.Spec) *runfile.State) *flight {
	f := &flight{launched: make(chan struct{}, 8)}
	h.setActive(true)
	h.fake.On("systemd-run", "--unit="+unit).Do(func(c cmdrunnerCmd) (cmdrunnerResult, error) {
		specPath := argAfter(c.Args, "--spec")
		spec, err := runfile.ReadSpec(specPath)
		if err != nil {
			return cmdrunnerResult{}, err
		}
		_ = os.Remove(specPath)
		f.mu.Lock()
		f.statePath, f.spec, f.args = argAfter(c.Args, "--state"), spec, c.Args
		f.mu.Unlock()
		st := finalState(spec, "", runfile.ResultRunning)
		st.FinishedAt = nil
		st.Progress = &runfile.Progress{Destination: "d", Folder: "f", FilesDone: 4, FilesTotal: 9}
		if err := runfile.WriteState(f.statePath, st); err != nil {
			return cmdrunnerResult{}, err
		}
		f.launched <- struct{}{}
		return cmdrunnerResult{}, nil
	})
	h.fake.On("systemctl", "stop", unit+".service").Do(func(cmdrunnerCmd) (cmdrunnerResult, error) {
		f.mu.Lock()
		f.stops++
		sp, spec := f.statePath, f.spec
		f.mu.Unlock()
		if sp != "" && stopped != nil {
			if st := stopped(spec); st != nil {
				_ = runfile.WriteState(sp, st)
			}
		}
		h.setActive(false)
		return cmdrunnerResult{}, nil
	})
	return f
}

func cancelledState(spec *runfile.Spec) *runfile.State {
	st := finalState(spec, runfile.ResultCancelled, runfile.ResultCancelled)
	st.Error = "the run was stopped"
	return st
}

func (h *harness) withHealthcheck(b Backup) {
	h.t.Helper()
	url := "https://ping.example/x"
	if _, err := h.svc.Update(context.Background(), b.Id, BackupPatch{HealthcheckUrl: &url}); err != nil {
		h.t.Fatal(err)
	}
}

func TestCancelStopsARun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	titles := h.withNotifier()
	b := h.backup("Nightly")
	h.withHealthcheck(b)

	// A failure first: its problem must survive the stopped run after it.
	h.setState(func(spec *runfile.Spec) *runfile.State {
		return finalState(spec, runfile.ResultFailure, runfile.ResultFailure)
	})
	if run := h.run(b.Id); run.Result != ResultFailure || !h.open(problemKey(b.Id)) {
		t.Fatalf("failed run = %+v", run)
	}

	if err := h.svc.Cancel(ctx, b.Id); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Cancel of an idle backup = %v, want ErrNotRunning", err)
	}
	f := h.flight("homelab-backup-1", cancelledState)
	runID, err := h.svc.Trigger(ctx, b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-f.launched
	if err := h.svc.Cancel(ctx, b.Id); err != nil {
		t.Fatalf("Cancel = %v", err)
	}
	// Cancel answers when the outcome is recorded.
	row := h.runRow(runID)
	if row.Result != ResultCancelled || row.Error != "the run was stopped" {
		t.Fatalf("run after Cancel = %+v", row)
	}
	for _, it := range h.items(runID) {
		if it.Kind == runfile.ItemFolder && it.Result != runfile.ResultCancelled {
			t.Errorf("folder item = %q, want cancelled", it.Result)
		}
	}
	if f.stops != 1 {
		t.Errorf("the unit was stopped %d times", f.stops)
	}
	// Not a failure: the old problem is neither reopened nor resolved, the
	// stop is told once as information, and the healthcheck hears nothing.
	if !h.open(problemKey(b.Id)) {
		t.Error("a stopped run resolved the backup's problem")
	}
	if got := titles(); !slices.Equal(got, []string{"Backup failed: Nightly", "Backup stopped: Nightly"}) {
		t.Errorf("notifications = %v", got)
	}
	want := []string{"https://ping.example/x/start", "https://ping.example/x/fail", "https://ping.example/x/start"}
	if got := h.hc.all(); !slices.Equal(got, want) {
		t.Errorf("pings = %v, want %v", got, want)
	}
	if err := h.svc.Cancel(ctx, b.Id); !errors.Is(err, ErrNotRunning) {
		t.Errorf("a second Cancel = %v, want ErrNotRunning", err)
	}
}

// A runner killed before it could write its last word: the run the operator
// stopped is still cancelled, not failed.
func TestCancelWithoutALastWord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	titles := h.withNotifier()
	b := h.backup("Nightly")
	f := h.flight("homelab-backup-1", nil)
	h.fake.On("journalctl").Do(func(cmdrunnerCmd) (cmdrunnerResult, error) { return cmdrunnerResult{}, nil })
	runID, err := h.svc.Trigger(ctx, b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-f.launched
	if err := h.svc.Cancel(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	if row := h.runRow(runID); row.Result != ResultCancelled || row.Error != "the run was stopped" {
		t.Fatalf("run = %+v", row)
	}
	for _, it := range h.items(runID) {
		if it.Kind == runfile.ItemFolder && it.Result != runfile.ResultCancelled {
			t.Errorf("folder item = %q, want cancelled", it.Result)
		}
	}
	if h.open(problemKey(b.Id)) {
		t.Error("a stopped run opened the backup's problem")
	}
	if got := titles(); !slices.Equal(got, []string{"Backup stopped: Nightly"}) {
		t.Errorf("notifications = %v", got)
	}
}

func TestCancelDuringAPrepareStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	bs := &blockingStacks{started: make(chan struct{})}
	bs.folders = []StackFolders{{
		Stack: "immich", Running: true, Prepare: "dump.service",
		Folders: []StackFolder{{Key: "/dump", Label: "Dump", HostPath: filepath.Join(h.dir, "stacks/immich/root/dump")}},
	}}
	h.svc.stacks = bs
	d := h.dest(s3Input("Bucket"))
	b, err := h.svc.Create(ctx, BackupInput{
		Name: "Photos", DestinationIds: []int64{d.Id},
		Sources: []SourceInput{{Kind: SourceStack, Stack: "immich", Folder: "/dump"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fake.On("systemctl", "stop", "homelab-backup-1.service").Do(func(cmdrunnerCmd) (cmdrunnerResult, error) {
		return cmdrunnerResult{}, nil
	})
	runID, err := h.svc.Trigger(ctx, b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-bs.started
	if err := h.svc.Cancel(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	if h.launchCount() != 0 {
		t.Fatalf("launched %d units, want 0", h.launchCount())
	}
	if row := h.runRow(runID); row.Result != ResultCancelled {
		t.Fatalf("run = %+v", row)
	}
}

// previewState is a preview's last word: counts on every folder.
func previewState(spec *runfile.Spec) *runfile.State {
	st := finalState(spec, runfile.ResultSuccess, runfile.ResultSuccess)
	st.Preview = true
	for i := range st.Items {
		it := &st.Items[i]
		it.Files, it.Bytes = 0, 0
		if it.Kind != runfile.ItemFolder {
			continue
		}
		it.Preview = &runfile.Preview{
			New: 7, NewBytes: 700, Changed: 2, ChangedBytes: 20, Unchanged: 40, Extra: 3, Deletes: true,
			NewSample: []string{"a/new.txt"}, ExtraSample: []string{"old.txt"},
		}
	}
	return st
}

func TestPreview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	titles := h.withNotifier()
	dump := filepath.Join(h.dir, "stacks/immich/root/dump")
	if err := os.MkdirAll(dump, 0o755); err != nil {
		t.Fatal(err)
	}
	h.stacks.folders = []StackFolders{{
		Stack: "immich", Running: true, Prepare: "dump.service",
		Folders: []StackFolder{{Key: "/dump", Label: "Dump", HostPath: dump}},
	}}
	d := h.dest(s3Input("Bucket"))
	local := h.dest(rcloneInput("Disk", "[disk]\ntype = local\n", filepath.Join(h.dir, "usb")))
	b, err := h.svc.Create(ctx, BackupInput{
		Name: "Photos", Schedule: "0 4 * * *", HealthcheckUrl: "https://ping.example/x",
		DestinationIds: []int64{d.Id, local.Id},
		Sources: []SourceInput{
			{Kind: SourceStack, Stack: "immich", Folder: "/dump"}, {Kind: SourceCore}, {Kind: SourcePath, Path: h.data},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.PreviewDetail(ctx, b.Id); !errors.Is(err, ErrNoPreview) {
		t.Fatalf("PreviewDetail before any preview = %v", err)
	}

	h.setState(previewState)
	if err := h.svc.Preview(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()

	spec := h.lastSpec()
	if !spec.Preview || len(spec.Folders) < 3 {
		t.Fatalf("spec: preview %v, %d folders", spec.Preview, len(spec.Folders))
	}
	h.mu.Lock()
	args := h.launches[len(h.launches)-1].Args
	h.mu.Unlock()
	if args[len(args)-1] != "--preview" {
		t.Errorf("the runner was not started with --preview: %v", args[len(args)-6:])
	}
	if !slices.Contains(args, "--unit=homelab-backup-1") {
		t.Errorf("a preview runs as the backup's own unit: %v", args)
	}
	// Nothing outside the comparison happens: no prepare step, no database
	// snapshot, no path made writable for a local destination.
	if len(h.stacks.prepared) != 0 || h.snaps != 0 {
		t.Errorf("prepare steps %v, snapshots %d", h.stacks.prepared, h.snaps)
	}
	for _, a := range args {
		if strings.Contains(a, "ReadWritePaths=-") {
			t.Errorf("a preview made a destination writable: %s", a)
		}
	}
	notes := 0
	for _, f := range spec.Folders {
		if strings.Contains(f.Note, "a preview does not") {
			notes++
		}
	}
	if notes != 2 {
		t.Errorf("%d folders say what the preview left out, want 2 (the stack's and the database)", notes)
	}

	p, err := h.svc.PreviewDetail(ctx, b.Id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Result != BackupRunResultSuccess || p.FinishedAt == nil || p.Progress != nil || len(p.Items) != 2*(len(spec.Folders)+1) {
		t.Fatalf("preview = %+v", p)
	}
	it := p.Items[1]
	if it.New != 7 || it.NewBytes != 700 || it.Changed != 2 || it.Unchanged != 40 || it.Extra != 3 || !it.Deletes ||
		!slices.Equal(it.NewSample, []string{"a/new.txt"}) || it.ChangedSample == nil {
		t.Errorf("folder item = %+v", it)
	}
	// A preview is not a run: no history, no notification, no ping, no files.
	if runs, _ := h.q.ListBackupRuns(ctx, store.ListBackupRunsParams{BackupID: &b.Id, Limit: 10}); len(runs) != 0 {
		t.Errorf("a preview left %d runs in the history", len(runs))
	}
	if len(titles()) != 0 || len(h.hc.all()) != 0 {
		t.Errorf("notifications %v, pings %v", titles(), h.hc.all())
	}
	for _, pat := range []string{"state/*", "run/*"} {
		if left, _ := filepath.Glob(filepath.Join(h.dir, pat)); len(left) != 0 {
			t.Errorf("left on disk: %v", left)
		}
	}
	if got, _ := h.svc.Get(ctx, b.Id); got.Previewing || got.Running || got.LastRun != nil {
		t.Errorf("backup after its preview = previewing %v, running %v, last run %v", got.Previewing, got.Running, got.LastRun)
	}

	// Over the API, and gone once the backup really runs.
	var body BackupPreview
	if code, _ := call(t, h, "GET", "/api/backups/1/preview", "", &body); code != 200 || body.Items[1].New != 7 {
		t.Errorf("GET preview = %d %+v", code, body)
	}
	h.setState(succeed)
	h.run(b.Id)
	if code, m := call(t, h, "GET", "/api/backups/1/preview", "", nil); code != 404 || m["code"] != "no_preview" {
		t.Errorf("GET preview after a run = %d %v", code, m)
	}
}

func TestPreviewLeavesOutRestic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	created, err := h.svc.CreateDestination(ctx, resticInput("Repo", "s3:https://s3.example.com/repo", resticPass))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ConfirmPassword(ctx, created.Destination.Id); err != nil {
		t.Fatal(err)
	}
	b, err := h.svc.Create(ctx, BackupInput{
		Name: "Docs", DestinationIds: []int64{created.Destination.Id},
		Sources: []SourceInput{{Kind: SourcePath, Path: h.data}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Preview(ctx, b.Id); !errors.Is(err, ErrNothingToPreview) {
		t.Fatalf("Preview of a restic-only backup = %v", err)
	}
	if code, m := call(t, h, "POST", "/api/backups/1/preview", "", nil); code != 409 || m["code"] != "nothing_to_preview" {
		t.Errorf("POST preview = %d %v", code, m)
	}
	if h.launchCount() != 0 {
		t.Errorf("launched %d units", h.launchCount())
	}
}

func TestPreviewTakesTheBackupsPlaceAndCanBeCancelled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")
	f := h.flight("homelab-backup-1", cancelledState)
	if err := h.svc.Preview(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	<-f.launched

	// The scheduled run that comes due is not started, as with a run in flight.
	if _, err := h.svc.Trigger(ctx, b.Id, TriggerCron); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Trigger during a preview = %v, want ErrAlreadyRunning", err)
	}
	if err := h.svc.Preview(ctx, b.Id); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a second Preview = %v, want ErrAlreadyRunning", err)
	}
	if got, _ := h.svc.Get(ctx, b.Id); !got.Previewing || got.Running {
		t.Errorf("backup = previewing %v, running %v", got.Previewing, got.Running)
	}
	if p, err := h.svc.PreviewDetail(ctx, b.Id); err != nil || p.Result != BackupRunResultRunning || p.Progress == nil || p.Progress.FilesDone != 4 {
		t.Errorf("preview in flight = %+v %v", p, err)
	}
	if running, _ := h.svc.Running(ctx); !slices.Equal(running, []string{"Nightly"}) {
		t.Errorf("Running = %v: a shutdown must see the preview", running)
	}

	if code, _ := call(t, h, "POST", "/api/backups/1/cancel", "", nil); code != 204 {
		t.Fatalf("POST cancel = %d", code)
	}
	p, err := h.svc.PreviewDetail(ctx, b.Id)
	if err != nil || p.Result != BackupRunResultCancelled || p.Error != "the preview was stopped" {
		t.Fatalf("preview after Cancel = %+v %v", p, err)
	}
	if got, _ := h.svc.Get(ctx, b.Id); got.Previewing {
		t.Error("still previewing after Cancel")
	}
	if code, m := call(t, h, "POST", "/api/backups/1/cancel", "", nil); code != 409 || m["code"] != "not_running" {
		t.Errorf("POST cancel when idle = %d %v", code, m)
	}

	// A shutdown refuses a new preview and stops one in flight.
	h.svc.BeginShutdown()
	if err := h.svc.Preview(ctx, b.Id); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Preview while shutting down = %v", err)
	}
	h.svc.CancelShutdown()
	h.setActive(true)
	if err := h.svc.Preview(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	<-f.launched
	if err := h.svc.Halt(ctx, "Nightly"); err != nil {
		t.Fatal(err)
	}
	if f.stops != 2 {
		t.Errorf("the unit was stopped %d times, want 2 (Cancel, then Halt)", f.stops)
	}
}

func TestAdoptAPreviewLeftRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")
	if err := h.q.StartBackupPreview(ctx, store.StartBackupPreviewParams{BackupID: b.Id, StartedAt: h.clock().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.svc.previewDir(b.Id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(h.svc.previewStatePath(b.Id)), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := &runfile.Spec{
		Destinations: []runfile.Destination{{ID: 1, Name: "Nightly bucket"}},
		Folders:      []runfile.Folder{{Label: "data", Path: h.data}},
	}
	if err := runfile.WriteState(h.svc.previewStatePath(b.Id), previewState(spec)); err != nil {
		t.Fatal(err)
	}
	h.svc.adoptRunning(ctx)
	h.svc.Wait()
	p, err := h.svc.PreviewDetail(ctx, b.Id)
	if err != nil || p.Result != BackupRunResultSuccess || len(p.Items) != 2 || p.Items[1].New != 7 {
		t.Fatalf("adopted preview = %+v %v", p, err)
	}
	if _, err := os.Stat(h.svc.previewDir(b.Id)); !os.IsNotExist(err) {
		t.Errorf("the preview's working directory is still there: %v", err)
	}
}
