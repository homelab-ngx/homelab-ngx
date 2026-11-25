package backups

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"

	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
)

func sptr(s string) *string { return &s }

func TestBackupCreateAndUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	d := h.dest(s3Input("Bucket"))
	b, err := h.svc.Create(ctx, BackupInput{
		Name: " Nightly ", Schedule: " 0 4 * * * ", Bwlimit: "4m", Transfers: 2,
		HealthcheckUrl: "0F9B4A6C-1111-2222-3333-444455556666", DestinationIds: []int64{d.Id},
		Sources: []SourceInput{
			{Kind: SourceCore},
			{Kind: SourcePath, Path: h.data + "/", RemotePath: sptr("/photos/"),
				Excludes: []Exclude{{Pattern: " /cache/** ", Reason: " rebuilt "}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Nightly" || b.Schedule != "0 4 * * *" || b.Bwlimit != "4M" || b.Transfers != 2 || !b.Enabled ||
		b.HealthcheckUrl != "https://hc-ping.com/0f9b4a6c-1111-2222-3333-444455556666" {
		t.Errorf("backup = %+v", b)
	}
	if len(b.Sources) != 2 || b.Sources[0].Kind != SourceCore || b.Sources[0].Label != "Core" {
		t.Fatalf("sources = %+v", b.Sources)
	}
	src := b.Sources[1]
	if src.Path != h.data || src.HostPath != h.data || *src.RemotePath != "photos" || src.Problem != "" ||
		!reflect.DeepEqual(src.Excludes, []Exclude{{Pattern: "/cache/**", Reason: "rebuilt"}}) {
		t.Errorf("path source = %+v", src)
	}
	if len(b.Destinations) != 1 || b.Destinations[0] != (DestinationRef{Id: d.Id, Name: "Bucket", Kind: KindS3, Ready: true}) {
		t.Errorf("destinations = %+v", b.Destinations)
	}
	if b.NextRun == nil || !b.NextRun.Equal(time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)) || b.LastRun != nil || b.Overdue || b.Running {
		t.Errorf("run state = next %v last %v overdue %v", b.NextRun, b.LastRun, b.Overdue)
	}
	if got := h.svc.scheduled(); len(got) != 1 {
		t.Fatalf("scheduled after create = %v", got)
	}

	// An absent member stays; a present list replaces the whole list.
	off := false
	b, err = h.svc.Update(ctx, b.Id, BackupPatch{Enabled: &off})
	if err != nil || b.Enabled || b.Schedule != "0 4 * * *" || len(b.Sources) != 2 || len(b.Destinations) != 1 || b.NextRun != nil {
		t.Fatalf("after disable = %+v %v", b, err)
	}
	if got := h.svc.scheduled(); len(got) != 0 {
		t.Fatalf("scheduled after disable = %v", got)
	}
	on := true
	b, err = h.svc.Update(ctx, b.Id, BackupPatch{Enabled: &on, Schedule: sptr("30 2 * * 0"),
		Sources: &[]SourceInput{{Kind: SourcePath, Path: h.data}}, HealthcheckUrl: sptr("https://ping.example/x/"), Bwlimit: sptr("")})
	if err != nil || len(b.Sources) != 1 || b.Sources[0].RemotePath != nil || b.HealthcheckUrl != "https://ping.example/x" || b.Bwlimit != "" {
		t.Fatalf("after update = %+v %v", b, err)
	}
	if got := h.svc.scheduled(); len(got) != 1 {
		t.Fatalf("scheduled after re-enable = %v", got)
	}
	// An emptied schedule takes it off the cron without disabling it.
	if b, err = h.svc.Update(ctx, b.Id, BackupPatch{Schedule: sptr("")}); err != nil || !b.Enabled || b.NextRun != nil {
		t.Fatalf("after clearing the schedule = %+v %v", b, err)
	}
	if got := h.svc.scheduled(); len(got) != 0 {
		t.Fatalf("scheduled with an empty schedule = %v", got)
	}

	if _, err := h.svc.Create(ctx, BackupInput{Name: "Nightly"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name = %v", err)
	}
	other, _ := h.svc.Create(ctx, BackupInput{Name: "Other", Schedule: "0 1 * * *"})
	if _, err := h.svc.Update(ctx, other.Id, BackupPatch{Name: sptr("Nightly")}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename onto a taken name = %v", err)
	}
	if _, err := h.svc.Update(ctx, 999, BackupPatch{}); !errors.Is(err, ErrUnknownBackup) {
		t.Errorf("update unknown = %v", err)
	}
	if _, err := h.svc.Get(ctx, 999); !errors.Is(err, ErrUnknownBackup) {
		t.Errorf("get unknown = %v", err)
	}
	if err := h.svc.Delete(ctx, other.Id); err != nil {
		t.Fatal(err)
	}
	if got := h.svc.scheduled(); len(got) != 0 {
		t.Fatalf("scheduled after delete = %v", got)
	}
	if err := h.svc.Delete(ctx, other.Id); !errors.Is(err, ErrUnknownBackup) {
		t.Errorf("delete twice = %v", err)
	}
}

func TestBackupValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	d := h.dest(s3Input("Bucket"))
	file := filepath.Join(h.dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := func(p string) []SourceInput { return []SourceInput{{Kind: SourcePath, Path: p}} }
	cases := []struct {
		name string
		in   BackupInput
		code string
	}{
		{"empty name", BackupInput{Name: " "}, "invalid_name"},
		{"control in name", BackupInput{Name: "a\nb"}, "invalid_name"},
		{"six-field cron", BackupInput{Name: "x", Schedule: "0 0 4 * * *"}, "invalid_schedule"},
		{"descriptor", BackupInput{Name: "x", Schedule: "@daily"}, "invalid_schedule"},
		{"onCalendar", BackupInput{Name: "x", Schedule: "*-*-* 03:30:00"}, "invalid_schedule"},
		{"bad cron field", BackupInput{Name: "x", Schedule: "61 4 * * *"}, "invalid_schedule"},
		{"bwlimit timetable", BackupInput{Name: "x", Bwlimit: "08:00,512k"}, "invalid_bwlimit"},
		{"bwlimit words", BackupInput{Name: "x", Bwlimit: "fast"}, "invalid_bwlimit"},
		{"healthcheck not a url", BackupInput{Name: "x", HealthcheckUrl: "ping me"}, "invalid_healthcheck"},
		{"relative path", BackupInput{Name: "x", Sources: path("data")}, "invalid_path"},
		{"root", BackupInput{Name: "x", Sources: path("/")}, "invalid_path"},
		{"kernel filesystem", BackupInput{Name: "x", Sources: path("/proc/sys")}, "invalid_path"},
		{"nix store", BackupInput{Name: "x", Sources: path("/nix")}, "invalid_path"},
		{"missing folder", BackupInput{Name: "x", Sources: path(filepath.Join(h.dir, "nope"))}, "invalid_path"},
		{"a file", BackupInput{Name: "x", Sources: path(file)}, "invalid_path"},
		{"same path twice", BackupInput{Name: "x", Sources: []SourceInput{
			{Kind: SourcePath, Path: h.data}, {Kind: SourcePath, Path: h.data + "/."}}}, "invalid_source"},
		{"core twice", BackupInput{Name: "x", Sources: []SourceInput{{Kind: SourceCore}, {Kind: SourceCore}}}, "invalid_source"},
		{"stack without folder", BackupInput{Name: "x", Sources: []SourceInput{{Kind: SourceStack, Stack: "immich"}}}, "invalid_source"},
		{"unknown kind", BackupInput{Name: "x", Sources: []SourceInput{{Kind: "disk"}}}, "invalid_source"},
		{"unanchored pattern", BackupInput{Name: "x", Sources: []SourceInput{
			{Kind: SourcePath, Path: h.data, Excludes: []Exclude{{Pattern: "cache/**"}}}}}, "invalid_pattern"},
		{"pattern with a class", BackupInput{Name: "x", Sources: []SourceInput{
			{Kind: SourcePath, Path: h.data, Excludes: []Exclude{{Pattern: "/[ab]"}}}}}, "invalid_pattern"},
		{"multi-line reason", BackupInput{Name: "x", Sources: []SourceInput{
			{Kind: SourcePath, Path: h.data, Excludes: []Exclude{{Pattern: "/a", Reason: "a\nb"}}}}}, "invalid_pattern"},
		{"remote path escaping", BackupInput{Name: "x", Sources: []SourceInput{
			{Kind: SourcePath, Path: h.data, RemotePath: sptr("a/../../b")}}}, "invalid_remote_path"},
		{"unknown destination", BackupInput{Name: "x", DestinationIds: []int64{999}}, "invalid_destination"},
		{"destination twice", BackupInput{Name: "x", DestinationIds: []int64{d.Id, d.Id}}, "invalid_destination"},
	}
	for _, c := range cases {
		if _, err := h.svc.Create(ctx, c.in); invalidCode(err) != c.code {
			t.Errorf("create %s: err = %v, want %s", c.name, err, c.code)
		}
	}
	if list, _ := h.svc.List(ctx); len(list) != 0 {
		t.Fatalf("a refused backup was stored: %+v", list)
	}
	// The same rules guard an update, and a refused one changes nothing.
	b := h.backup("Nightly")
	for _, c := range cases[2:] {
		p := BackupPatch{}
		switch {
		case c.in.Schedule != "":
			p.Schedule = &c.in.Schedule
		case c.in.Bwlimit != "":
			p.Bwlimit = &c.in.Bwlimit
		case c.in.HealthcheckUrl != "":
			p.HealthcheckUrl = &c.in.HealthcheckUrl
		case c.in.Sources != nil:
			p.Sources = &c.in.Sources
		default:
			p.DestinationIds = &c.in.DestinationIds
		}
		if _, err := h.svc.Update(ctx, b.Id, p); invalidCode(err) != c.code {
			t.Errorf("update %s: err = %v, want %s", c.name, err, c.code)
		}
	}
	after, _ := h.svc.Get(ctx, b.Id)
	after.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(after, b) {
		t.Errorf("refused updates changed the backup:\n got %+v\nwant %+v", after, b)
	}
}

// /tmp is private to a run's unit, so a folder under it would be copied
// empty; the harness lifts that rule for its own folders.
func TestPathSourceRules(t *testing.T) {
	saved := pathDenied
	pathDenied = []string{"/proc", "/sys", "/dev", "/run", "/nix", "/tmp"}
	t.Cleanup(func() { pathDenied = saved })
	for _, p := range []string{"/tmp", "/tmp/x", "/run/secrets", "/dev", "/sys/fs"} {
		if _, err := cleanSources([]SourceInput{{Kind: SourcePath, Path: p}}); invalidCode(err) != "invalid_path" {
			t.Errorf("%s: err = %v", p, err)
		}
	}
}

func stackFixture(h *harness) {
	h.stacks.mu.Lock()
	defer h.stacks.mu.Unlock()
	h.stacks.folders = []StackFolders{
		{Stack: "immich", Running: true, Prepare: "postgresqlBackup.service", Folders: []StackFolder{
			{Key: "/var/backup/postgresql", Label: "Database dumps", HostPath: filepath.Join(h.dir, "stacks/immich/root/var/backup/postgresql")},
			{Key: "UPLOAD_DIR", Label: "Library", HostPath: "/pool/immich",
				Excludes: []Exclude{{Pattern: "/thumbs/**", Reason: "regenerated"}}},
		}},
		{Stack: "media", Running: false, Prepare: "dump.service", Folders: []StackFolder{
			{Key: "/config", Label: "Config", HostPath: filepath.Join(h.dir, "stacks/media/root/config")},
			{Key: "BROKEN", Label: "Broken", Problem: "the variable BROKEN has no value"},
		}},
		{Stack: "plain", Running: true, Folders: []StackFolder{{Key: "/data", Label: "Data", HostPath: "/cache/plain"}}},
	}
}

func stackSrc(stack, folder string) SourceInput {
	return SourceInput{Kind: SourceStack, Stack: stack, Folder: folder}
}

func TestBuildPlan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	stackFixture(h)
	s3 := h.dest(s3Input("Bucket"))
	disk := h.dest(rcloneInput("Disk", "[disk]\ntype = local\n", "/mnt/mirror"))
	repo := h.dest(resticInput("Repo", "/mnt/usb/repo", resticPass))
	lib := stackSrc("immich", "UPLOAD_DIR")
	lib.RemotePath = sptr("")
	lib.Excludes = []Exclude{{Pattern: "**/*.tmp", Reason: "scratch"}}
	lib.SizeOnly = true
	b, err := h.svc.Create(ctx, BackupInput{
		Name: "Everything", Bwlimit: "2M", Transfers: 4, DestinationIds: []int64{repo.Id, s3.Id, disk.Id},
		Sources: []SourceInput{
			{Kind: SourceCore},
			{Kind: SourcePath, Path: h.data},
			lib,
			stackSrc("immich", "/var/backup/postgresql"),
			stackSrc("media", "BROKEN"),
			stackSrc("media", "/gone"),
			stackSrc("removed", "/x"),
			stackSrc("plain", "/data"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// What the operator sees for each source.
	probs := map[string]string{}
	for _, s := range b.Sources {
		probs[s.Label] = s.Problem
	}
	wantProbs := map[string]string{
		"Core": "", h.data: "", "immich: Library": "", "immich: Database dumps": "",
		"media: Broken": "the variable BROKEN has no value", "media: /gone": "the stack no longer recommends this folder",
		"removed: /x": "the stack removed is not installed", "plain: Data": "",
	}
	if !reflect.DeepEqual(probs, wantProbs) {
		t.Errorf("source problems = %v", probs)
	}
	if !b.Sources[2].SizeOnly || b.Sources[1].SizeOnly {
		t.Errorf("size_only = %v on the library, %v on the path source", b.Sources[2].SizeOnly, b.Sources[1].SizeOnly)
	}

	row, _ := h.svc.backupRow(ctx, b.Id)
	p, err := h.svc.buildPlan(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	sp := p.spec
	if sp.Version != runfile.Version || sp.Backup != "Everything" || sp.Bwlimit != "2M" || sp.Transfers != 4 ||
		sp.CacheDir != filepath.Join(h.dir, "cache") || !reflect.DeepEqual(sp.PoolMounts, []string{"/pool"}) {
		t.Errorf("spec head = %+v", sp)
	}
	// Destinations keep the operator's order; local ones may be written.
	var names []string
	for _, d := range sp.Destinations {
		names = append(names, d.Name+"/"+d.Kind)
	}
	if !reflect.DeepEqual(names, []string{"Repo/restic", "Bucket/rclone", "Disk/rclone"}) {
		t.Errorf("destinations = %v", names)
	}
	if !reflect.DeepEqual(p.write, []string{"/mnt/usb/repo", "/mnt/mirror"}) {
		t.Errorf("write paths = %v", p.write)
	}
	dbDir, stacksDir := filepath.Join(h.dir, "dbsnap"), filepath.Join(h.dir, "stacks")
	want := []runfile.Folder{
		{Label: "Server settings: database", Path: dbDir, RemotePath: strings.TrimPrefix(dbDir, "/"), Excludes: []string{"/*.tmp"}},
		{Label: "Server settings: stack variables and list", Path: stacksDir, RemotePath: strings.TrimPrefix(stacksDir, "/"),
			Files: []string{"/stacks.lock.json", "/*/variables.age"}},
		{Label: h.data, Path: h.data, RemotePath: strings.TrimPrefix(h.data, "/")},
		{Label: "immich: Library", Path: "/pool/immich", RemotePath: "", Excludes: []string{"**/*.tmp", "/thumbs/**"}, SizeOnly: true},
		{Label: "immich: Database dumps", Path: filepath.Join(stacksDir, "immich/root/var/backup/postgresql"),
			RemotePath: strings.TrimPrefix(filepath.Join(stacksDir, "immich/root/var/backup/postgresql"), "/")},
		{Label: "media: Broken", Fail: "the variable BROKEN has no value"},
		{Label: "media: /gone", Fail: "the stack no longer recommends this folder"},
		{Label: "removed: /x", Fail: "the stack removed is not installed"},
		{Label: "plain: Data", Path: "/cache/plain", RemotePath: "cache/plain"},
	}
	if !reflect.DeepEqual(sp.Folders, want) {
		for i := range want {
			if i >= len(sp.Folders) || !reflect.DeepEqual(sp.Folders[i], want[i]) {
				t.Errorf("folder %d:\n got %+v\nwant %+v", i, at(sp.Folders, i), want[i])
			}
		}
		if len(sp.Folders) != len(want) {
			t.Errorf("%d folders, want %d", len(sp.Folders), len(want))
		}
	}
	if !reflect.DeepEqual(p.folderStack, []string{"", "", "", "immich", "immich", "media", "media", "removed", "plain"}) {
		t.Errorf("folderStack = %v", p.folderStack)
	}
	// One prepare per stack that has one, however many of its folders are in.
	if !reflect.DeepEqual(p.prepares, []prepare{
		{stack: "immich", unit: "postgresqlBackup.service", running: true},
		{stack: "media", unit: "dump.service", running: false},
	}) {
		t.Errorf("prepares = %+v", p.prepares)
	}
	if !p.core {
		t.Error("core not flagged")
	}

	// Core with a folder of its own at the destination.
	core, err := h.svc.Create(ctx, BackupInput{Name: "Core only", DestinationIds: []int64{s3.Id},
		Sources: []SourceInput{{Kind: SourceCore, RemotePath: sptr("core")}}})
	if err != nil {
		t.Fatal(err)
	}
	row, _ = h.svc.backupRow(ctx, core.Id)
	if p, _ = h.svc.buildPlan(ctx, row); p.spec.Folders[0].RemotePath != "core/daemon" || p.spec.Folders[1].RemotePath != "core/stacks" {
		t.Errorf("core remote paths = %q %q", p.spec.Folders[0].RemotePath, p.spec.Folders[1].RemotePath)
	}
}

func at(f []runfile.Folder, i int) any {
	if i < len(f) {
		return f[i]
	}
	return "(missing)"
}

func TestNotReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	d := h.dest(s3Input("Bucket"))
	noDest, _ := h.svc.Create(ctx, BackupInput{Name: "No destination", Sources: []SourceInput{{Kind: SourceCore}}})
	noSrc, _ := h.svc.Create(ctx, BackupInput{Name: "No source", DestinationIds: []int64{d.Id}})
	for _, b := range []Backup{noDest, noSrc} {
		if _, err := h.svc.Trigger(ctx, b.Id, TriggerManual); !errors.Is(err, ErrNotReady) {
			t.Errorf("%s: trigger = %v", b.Name, err)
		}
	}
	if _, err := h.svc.Trigger(ctx, 999, TriggerManual); !errors.Is(err, ErrUnknownBackup) {
		t.Errorf("unknown backup = %v", err)
	}
	if runs, _ := h.q.RunningBackupRuns(ctx); len(runs) != 0 || h.launchCount() != 0 {
		t.Error("a backup that is not ready left a run")
	}
	// On the schedule it must not stay silent.
	h.withNotifier()
	h.svc.runScheduled(noDest.Id)
	if !h.open(problemKey(noDest.Id)) {
		t.Error("a scheduled backup that cannot run opened no problem")
	}
}

func TestRunEndToEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.withNotifier()
	b := h.backup("Nightly")
	if _, err := h.svc.Update(ctx, b.Id, BackupPatch{HealthcheckUrl: sptr("https://ping.example/x")}); err != nil {
		t.Fatal(err)
	}
	key := problemKey(b.Id)

	// A partial run: one folder failed.
	h.setState(func(spec *runfile.Spec) *runfile.State {
		return finalState(spec, runfile.ResultPartial, runfile.ResultPartial)
	})
	run := h.run(b.Id)
	if run.Result != ResultPartial || run.FinishedAt == nil || run.Files != 3 || run.Bytes != 1000 ||
		run.Error != h.data+" to Nightly bucket failed" || run.Trigger != TriggerManual || run.BackupName != "Nightly" {
		t.Fatalf("partial run = %+v", run)
	}
	items := h.items(run.ID)
	if len(items) != 2 || items[0].Kind != "destination" || items[0].DestinationName != "Nightly bucket" || items[0].Result != "success" ||
		items[1].Kind != "folder" || items[1].Label != h.data || items[1].Path != h.data || items[1].Result != "partial" ||
		items[1].Files != 3 || items[1].Bytes != 1000 || items[1].Error != "copy failed" || items[1].StartedAt == nil ||
		*items[1].DestinationID != b.Destinations[0].Id || items[1].Position != 1 {
		t.Fatalf("items = %+v", items)
	}
	if !h.open(key) {
		t.Error("a partial run opened no problem")
	}

	// The spec the runner got, and how it was launched.
	spec := h.lastSpec()
	if spec.RunID != run.ID || spec.WorkDir != filepath.Join(h.dir, "run", "run-1") || len(spec.Folders) != 1 || spec.CheckOnly {
		t.Errorf("spec = %+v", spec)
	}
	h.mu.Lock()
	launch := h.launches[0]
	h.mu.Unlock()
	args := strings.Join(launch.Args, "\x00")
	for _, want := range []string{
		"--unit=homelab-backup-1", "--collect", "ProtectSystem=strict", "CapabilityBoundingSet=CAP_DAC_READ_SEARCH",
		"NoNewPrivileges=yes", "PrivateTmp=yes", "Nice=10", "IOSchedulingClass=idle", "RuntimeMaxSec=72h",
		"ReadWritePaths=" + spec.WorkDir, "ReadWritePaths=" + filepath.Join(h.dir, "state"), "ReadWritePaths=" + filepath.Join(h.dir, "cache"),
		"--setenv=HOME=" + spec.WorkDir,
		"--\x00/nix/store/x-runner/bin/homelab-backup-runner\x00--spec\x00" + filepath.Join(spec.WorkDir, "spec.json") +
			"\x00--state\x00" + filepath.Join(h.dir, "state", "1.json"),
	} {
		if !strings.Contains(args, want) {
			t.Errorf("launch lacks %q: %v", strings.ReplaceAll(want, "\x00", " "), launch.Args)
		}
	}
	for _, a := range launch.Args {
		if a == "--no-block" || a == "--wait" {
			t.Errorf("a run is launched with %s", a)
		}
	}
	if launch.Name != "systemd-run" || launch.Module != "backups" {
		t.Errorf("launch = %s (%s)", launch.Name, launch.Module)
	}

	// A failed run keeps the problem open.
	h.setState(func(spec *runfile.Spec) *runfile.State {
		st := finalState(spec, runfile.ResultFailure, runfile.ResultSkipped)
		st.Items[0].Result, st.Items[0].Error = runfile.ResultFailure, "access denied"
		st.Items[1].Files, st.Items[1].Bytes, st.Items[1].Error = 0, 0, ""
		return st
	})
	if run = h.run(b.Id); run.Result != ResultFailure || run.Error != "Nightly bucket failed" || run.Files != 0 {
		t.Fatalf("failed run = %+v", run)
	}
	if !h.open(key) {
		t.Error("the problem closed on a failed run")
	}

	// A success resolves it.
	h.setState(succeed)
	if run = h.run(b.Id); run.Result != ResultSuccess || run.Error != "" || run.Files != 3 {
		t.Fatalf("successful run = %+v", run)
	}
	if h.open(key) {
		t.Error("a successful run left the problem open")
	}
	want := []string{
		"https://ping.example/x/start", "https://ping.example/x/fail",
		"https://ping.example/x/start", "https://ping.example/x/fail",
		"https://ping.example/x/start", "https://ping.example/x",
	}
	if got := h.hc.all(); !reflect.DeepEqual(got, want) {
		t.Errorf("pings = %v", got)
	}
	// Nothing of the runs is left on disk.
	for _, pat := range []string{"state/*", "run/*"} {
		if left, _ := filepath.Glob(filepath.Join(h.dir, pat)); len(left) != 0 {
			t.Errorf("left behind: %v", left)
		}
	}
	// What the list shows.
	got, _ := h.svc.Get(ctx, b.Id)
	if got.LastRun == nil || got.LastRun.Id != run.ID || got.LastRun.Result != BackupRunResultSuccess || got.Running ||
		got.LastSuccess == nil || !got.LastSuccess.Equal(h.clock()) {
		t.Errorf("backup after runs = %+v last %+v", got, got.LastRun)
	}
	runs, err := h.svc.Runs(ctx, b.Id, 2)
	if err != nil || len(runs) != 2 || runs[0].Id != run.ID || runs[1].Result != BackupRunResultFailure {
		t.Errorf("runs = %+v %v", runs, err)
	}
	if _, err := h.svc.Runs(ctx, 999, 0); !errors.Is(err, ErrUnknownBackup) {
		t.Errorf("runs of unknown = %v", err)
	}
}

// No secret of any destination reaches the unit's command line or
// environment: `systemctl show` gives both to any local user.
func TestLaunchCarriesNoSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.setState(succeed)
	ids := []int64{
		h.dest(s3Input("Bucket")).Id,
		h.dest(rcloneInput("B2", rcloneSectionText, "bucket")).Id,
		h.dest(resticInput("Repo", "/mnt/usb/repo", resticPass)).Id,
	}
	b, err := h.svc.Create(ctx, BackupInput{Name: "All", DestinationIds: ids, Sources: []SourceInput{{Kind: SourcePath, Path: h.data}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := h.run(b.Id); r.Result != ResultSuccess {
		t.Fatalf("run = %+v", r)
	}
	// The spec did carry them, so the check below means something.
	spec := h.lastSpec()
	if !strings.Contains(spec.Destinations[0].Config, s3Secret) || !strings.Contains(spec.Destinations[1].Config, rcloneTok) ||
		spec.Destinations[2].Password != resticPass || spec.Destinations[2].Env["AWS_SECRET_ACCESS_KEY"] != resticEnv {
		t.Fatalf("the spec lacks the secrets: %+v", spec.Destinations)
	}
	for _, c := range h.fake.Calls() {
		all := c.Name + " " + strings.Join(c.Args, " ") + " " + strings.Join(c.Env, " ")
		for _, s := range []string{s3Secret, rcloneTok, resticPass, resticEnv} {
			if strings.Contains(all, s) {
				t.Errorf("%s carries the secret %q", c.Name, s)
			}
		}
	}
	h.mu.Lock()
	args := strings.Join(h.launches[0].Args, " ")
	h.mu.Unlock()
	if !strings.Contains(args, "ReadWritePaths=-/mnt/usb/repo") {
		t.Errorf("the local repository is not writable to the run: %s", args)
	}
}

func TestSpecFileIsPrivate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.backup("Nightly")
	var mode, dirMode os.FileMode
	h.fake.On("systemd-run", "--unit=homelab-backup-1").Do(func(c cmdrunnerCmd) (cmdrunnerResult, error) {
		p := argAfter(c.Args, "--spec")
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
		if fi, err := os.Stat(filepath.Dir(p)); err == nil {
			dirMode = fi.Mode().Perm()
		}
		return cmdrunnerResult{}, errors.New("no systemd here")
	})
	run := h.run(b.Id)
	if mode != 0o600 || dirMode != 0o700 {
		t.Errorf("spec mode %o in a %o directory, want 600 in 700", mode, dirMode)
	}
	// A launch that fails is a failed run, and the secrets do not stay on disk.
	if run.Result != ResultFailure || !strings.HasPrefix(run.Error, "the run could not be started: ") {
		t.Errorf("run = %+v", run)
	}
	if left, _ := filepath.Glob(filepath.Join(h.dir, "run", "*")); len(left) != 0 {
		t.Errorf("left behind after a failed launch: %v", left)
	}
}

func TestPrepareAndSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	stackFixture(h)
	h.setState(succeed)
	d := h.dest(s3Input("Bucket"))
	b, err := h.svc.Create(ctx, BackupInput{Name: "Stacks", DestinationIds: []int64{d.Id}, Sources: []SourceInput{
		{Kind: SourceCore},
		stackSrc("immich", "UPLOAD_DIR"), stackSrc("immich", "/var/backup/postgresql"),
		stackSrc("media", "/config"), stackSrc("plain", "/data"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	folders := func() map[string]runfile.Folder {
		out := map[string]runfile.Folder{}
		for _, f := range h.lastSpec().Folders {
			out[f.Label] = f
		}
		return out
	}

	// All well: the running stack's prepare unit ran once, the stopped
	// stack's did not, and its folders say so.
	if r := h.run(b.Id); r.Result != ResultSuccess {
		t.Fatalf("run = %+v", r)
	}
	if !reflect.DeepEqual(h.stacks.prepared, []string{"immich/postgresqlBackup.service"}) {
		t.Errorf("prepared = %v", h.stacks.prepared)
	}
	f := folders()
	for label, fo := range f {
		wantNote := ""
		if label == "media: Config" {
			wantNote = "the stack was stopped, so its prepare step did not run"
		}
		if fo.Fail != "" || fo.Note != wantNote {
			t.Errorf("%s: fail %q note %q", label, fo.Fail, fo.Note)
		}
	}
	if h.snaps != 1 {
		t.Errorf("snapshots = %d, want 1", h.snaps)
	}
	if got := h.items(1); len(got) != 7 || got[5].Label != "media: Config" || got[5].Note == "" {
		t.Errorf("the note did not reach the run's items: %+v", got)
	}

	// A failing prepare fails that stack's folders, and only those.
	h.stacks.mu.Lock()
	h.stacks.prepErr = map[string]error{"immich": errors.New("exit status 1")}
	h.stacks.mu.Unlock()
	h.setState(func(spec *runfile.Spec) *runfile.State {
		return finalState(spec, runfile.ResultPartial, runfile.ResultSuccess)
	})
	r := h.run(b.Id)
	f = folders()
	wantFail := "the prepare step postgresqlBackup.service failed: exit status 1"
	for label, fo := range f {
		want := ""
		if strings.HasPrefix(label, "immich: ") {
			want = wantFail
		}
		if fo.Fail != want {
			t.Errorf("%s: fail = %q, want %q", label, fo.Fail, want)
		}
	}
	if r.Result != ResultPartial || !strings.Contains(r.Error, "immich: Library to Bucket") {
		t.Errorf("run = %+v", r)
	}
	h.stacks.mu.Lock()
	h.stacks.prepErr = nil
	h.stacks.mu.Unlock()

	// A failing snapshot fails the database folder alone.
	h.mu.Lock()
	h.snapErr = errors.New("disk full")
	h.mu.Unlock()
	h.run(b.Id)
	for label, fo := range folders() {
		want := ""
		if label == "Server settings: database" {
			want = "the database snapshot failed: disk full"
		}
		if fo.Fail != want {
			t.Errorf("%s: fail = %q, want %q", label, fo.Fail, want)
		}
	}

	// Without Core there is no snapshot.
	h.mu.Lock()
	h.snapErr, h.snaps = nil, 0
	h.mu.Unlock()
	plain := h.backup("Plain")
	h.run(plain.Id)
	if h.snaps != 0 {
		t.Errorf("a backup without Core took %d snapshots", h.snaps)
	}
}

func TestWatchUnitGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.withNotifier()
	b := h.backup("Nightly")
	second := filepath.Join(h.dir, "second")
	_ = os.Mkdir(second, 0o755)
	if _, err := h.svc.Update(ctx, b.Id, BackupPatch{Sources: &[]SourceInput{
		{Kind: SourcePath, Path: h.data}, {Kind: SourcePath, Path: second}}}); err != nil {
		t.Fatal(err)
	}

	// The runner died mid-copy: what finished stays, the rest is skipped.
	h.fake.On("journalctl", "--no-pager").Reply(`{"MESSAGE_ID":"d9b373ed55a64feb8242e02dbe79a49c","UNIT_RESULT":"oom-kill"}`)
	h.setState(func(spec *runfile.Spec) *runfile.State {
		st := finalState(spec, "", runfile.ResultSuccess)
		st.FinishedAt = nil
		st.Items[2].Result, st.Items[2].Files, st.Items[2].Bytes = runfile.ResultRunning, 1, 10
		st.Progress = &runfile.Progress{Destination: "Nightly bucket", Folder: second, BytesDone: 10, BytesTotal: 100}
		return st
	})
	run := h.run(b.Id)
	if run.Result != ResultFailure || run.Error != "the runner stopped before it finished (unit result oom-kill)" || run.Files != 4 {
		t.Fatalf("run = %+v", run)
	}
	items := h.items(run.ID)
	if len(items) != 3 || items[1].Result != "success" || items[2].Result != "skipped" {
		t.Fatalf("items = %+v", items)
	}
	if !h.open(problemKey(b.Id)) {
		t.Error("no problem for a runner that died")
	}

	// The runner left nothing at all: the outcome is unknown, and an unknown
	// outcome leaves the problem as it is.
	h.setState(nil)
	run = h.run(b.Id)
	if run.Result != ResultUnknown || run.Error != errDaemonStopped.Error() || len(h.items(run.ID)) != 0 {
		t.Fatalf("run without a state = %+v", run)
	}
	if !h.open(problemKey(b.Id)) {
		t.Error("an unknown outcome resolved the problem")
	}
	// A cancelled run (the unit was stopped) is recorded as such.
	h.setState(func(spec *runfile.Spec) *runfile.State {
		return finalState(spec, runfile.ResultCancelled, runfile.ResultSkipped)
	})
	if run = h.run(b.Id); run.Result != ResultCancelled || run.Error == "" {
		t.Fatalf("cancelled run = %+v", run)
	}
}

// systemd not answering is not an outcome: the watcher asks again before it
// reads what the runner left.
func TestWatchToleratesFailedReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.backup("Nightly")
	var statePath string
	reads := 0
	h.fake.On("systemd-run", "--unit=homelab-backup-1").Do(func(c cmdrunnerCmd) (cmdrunnerResult, error) {
		statePath = argAfter(c.Args, "--state")
		spec, _ := runfile.ReadSpec(argAfter(c.Args, "--spec"))
		h.mu.Lock()
		h.specs = append(h.specs, spec)
		h.mu.Unlock()
		return cmdrunnerResult{}, nil
	})
	h.fake.On("systemctl", "show", "--timestamp=unix").Do(func(cmdrunnerCmd) (cmdrunnerResult, error) {
		reads++
		if reads == 2 {
			// The runner finishes while systemd is busy.
			if err := runfile.WriteState(statePath, succeed(h.lastSpec())); err != nil {
				t.Error(err)
			}
		}
		return cmdrunnerResult{}, errors.New("systemd is busy")
	})
	if run := h.run(b.Id); run.Result != ResultSuccess {
		t.Fatalf("run = %+v after %d reads", run, reads)
	}
	if reads != 2 {
		t.Errorf("unit reads = %d, want 2 (the final state ends the watch)", reads)
	}
}

func TestRunDetailLiveThenStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")
	h.setActive(true)
	h.setState(func(spec *runfile.Spec) *runfile.State {
		st := finalState(spec, "", runfile.ResultRunning)
		st.FinishedAt = nil
		st.Items[1].Files, st.Items[1].Bytes, st.Items[1].Error, st.Items[1].FinishedAt = 2, 500, "", nil
		st.Progress = &runfile.Progress{Destination: "Nightly bucket", Folder: h.data, BytesDone: 500, BytesTotal: 2000, FilesDone: 2, FilesTotal: 8, Speed: 250}
		return st
	})
	runID, err := h.svc.Trigger(ctx, b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	// Already running: a second start is refused, and so is a delete.
	if _, err := h.svc.Trigger(ctx, b.Id, TriggerCron); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second trigger = %v", err)
	}
	if err := h.svc.Delete(ctx, b.Id); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("delete while running = %v", err)
	}
	var d BackupRunDetail
	deadline := time.Now().Add(5 * time.Second)
	for {
		if d, err = h.svc.RunDetail(ctx, runID); err != nil {
			t.Fatal(err)
		}
		if d.Progress != nil || time.Now().After(deadline) {
			break
		}
		// Preparing: the run exists, with nothing to show yet.
		if d.Result != BackupRunResultRunning || len(d.Items) != 0 {
			t.Fatalf("detail while preparing = %+v", d)
		}
		time.Sleep(time.Millisecond)
	}
	if d.Progress == nil || *d.Progress != (BackupProgress{Destination: "Nightly bucket", Folder: h.data, BytesDone: 500, BytesTotal: 2000, FilesDone: 2, FilesTotal: 8, Speed: 250}) {
		t.Fatalf("progress = %+v", d.Progress)
	}
	if d.Result != BackupRunResultRunning || len(d.Items) != 2 || d.Items[1].Result != "running" || d.Files != 2 || d.Bytes != 500 || d.FinishedAt != nil {
		t.Fatalf("live detail = %+v", d)
	}
	if got, _ := h.svc.Get(ctx, b.Id); !got.Running || got.LastRun == nil || got.LastRun.Id != runID {
		t.Errorf("backup while running = %+v", got)
	}
	if rows := h.items(runID); len(rows) != 0 {
		t.Errorf("items were stored before the run ended: %+v", rows)
	}

	// The runner finishes; the watcher imports its state.
	if err := runfile.WriteState(filepath.Join(h.dir, "state", "1.json"), succeed(h.lastSpec())); err != nil {
		t.Fatal(err)
	}
	h.svc.Wait()
	d, err = h.svc.RunDetail(ctx, runID)
	if err != nil || d.Result != BackupRunResultSuccess || d.Progress != nil || len(d.Items) != 2 ||
		d.Items[1].Result != "success" || d.Items[1].Destination != "Nightly bucket" || d.Files != 3 || d.Bytes != 1000 || d.FinishedAt == nil {
		t.Fatalf("stored detail = %+v %v", d, err)
	}
	if _, err := h.svc.RunDetail(ctx, 999); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("unknown run = %v", err)
	}
	// Now it can be deleted; its runs stay under its name.
	if err := h.svc.Delete(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	if d, err = h.svc.RunDetail(ctx, runID); err != nil || d.BackupId != nil || d.BackupName != "Nightly" || len(d.Items) != 2 {
		t.Errorf("run after its backup was deleted = %+v %v", d, err)
	}
}

func TestAdoptRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")
	other := h.backup("Other")
	insert := func(id int64, at int64) int64 {
		t.Helper()
		rid, err := h.q.InsertBackupRun(ctx, store.InsertBackupRunParams{BackupID: &id, BackupName: "x", Trigger: "cron", StartedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return rid
	}
	// The previous daemon left three rows running: two of one backup (only
	// the newest can be in flight) and one whose runner never started.
	old := insert(b.Id, 1000)
	newest := insert(b.Id, 2000)
	lost := insert(other.Id, 3000)
	spec := &runfile.Spec{RunID: newest, Destinations: []runfile.Destination{{ID: 1, Name: "Nightly bucket"}},
		Folders: []runfile.Folder{{Label: h.data, Path: h.data}}}
	if err := os.MkdirAll(filepath.Join(h.dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runfile.WriteState(h.svc.statePath(newest), succeed(spec)); err != nil {
		t.Fatal(err)
	}
	// Working directories: the adopted run's, a dead run's, a dead test's.
	for _, d := range []string{"run-2", "run-77", "test-1-5"} {
		if err := os.MkdirAll(filepath.Join(h.dir, "run", d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.adoptRunning(ctx)
	h.svc.Wait()

	if r := h.runRow(old); r.Result != ResultUnknown || r.Error != errDaemonStopped.Error() || r.FinishedAt == nil {
		t.Errorf("older row = %+v", r)
	}
	if r := h.runRow(newest); r.Result != ResultSuccess || r.Files != 3 || len(h.items(newest)) != 2 {
		t.Errorf("adopted row = %+v", r)
	}
	if r := h.runRow(lost); r.Result != ResultUnknown {
		t.Errorf("row without a runner = %+v", r)
	}
	if left, _ := filepath.Glob(filepath.Join(h.dir, "run", "*")); len(left) != 0 {
		t.Errorf("working directories left: %v", left)
	}
	if h.launchCount() != 0 {
		t.Error("adopting launched a runner")
	}
	// Nothing is claimed any more.
	h.setState(succeed)
	if r := h.run(b.Id); r.Result != ResultSuccess {
		t.Errorf("run after adoption = %+v", r)
	}

	// A run whose backup was deleted meanwhile cannot be watched.
	orphan := insert(other.Id, 4000)
	if err := h.q.DeleteBackup(ctx, other.Id); err != nil {
		t.Fatal(err)
	}
	h.svc.adoptRunning(ctx)
	h.svc.Wait()
	if r := h.runRow(orphan); r.Result != ResultUnknown || r.BackupID != nil {
		t.Errorf("orphan row = %+v", r)
	}
}

// A daemon stopping mid-run leaves the row running for the next one.
func TestDaemonStopLeavesRunForAdoption(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.backup("Nightly")
	h.setActive(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.svc.Run(ctx); close(done) }()
	waitChan(t, h.svc.runReady, "Run did not become ready")
	runID, err := h.svc.Trigger(context.Background(), b.Id, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.launchCount() == 1 })
	cancel()
	waitChan(t, done, "Run did not return after cancel")
	if r := h.runRow(runID); r.Result != ResultRunning || r.FinishedAt != nil {
		t.Fatalf("row after the daemon stopped = %+v", r)
	}
	if got := h.svc.scheduled(); got != nil {
		t.Errorf("schedule after stop = %v", got)
	}
	// The next daemon: the unit has ended meanwhile.
	h.setActive(false)
	if err := runfile.WriteState(h.svc.statePath(runID), succeed(h.lastSpec())); err != nil {
		t.Fatal(err)
	}
	h.svc.adoptRunning(context.Background())
	waited := make(chan struct{})
	go func() { h.svc.Wait(); close(waited) }()
	waitChan(t, waited, "adopted backup watcher did not finish")
	if r := h.runRow(runID); r.Result != ResultSuccess {
		t.Fatalf("row after adoption = %+v", r)
	}
}

func waitChan(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStaleAndOverdue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.withNotifier()
	h.setState(succeed)
	b := h.backup("Nightly") // 0 4 * * *
	manual, _ := h.svc.Create(ctx, BackupInput{Name: "By hand"})
	key := staleKey(b.Id)
	overdue := func() bool {
		t.Helper()
		got, err := h.svc.Get(ctx, b.Id)
		if err != nil {
			t.Fatal(err)
		}
		return got.Overdue
	}
	// Never ran: measured from the daemon start.
	h.advance(47 * time.Hour)
	if h.svc.checkStale(ctx); overdue() || h.open(key) {
		t.Fatal("stale within two intervals of the daemon start")
	}
	h.advance(2 * time.Hour)
	if h.svc.checkStale(ctx); !overdue() || !h.open(key) {
		t.Fatal("not stale after two intervals without a run")
	}
	if h.open(staleKey(manual.Id)) {
		t.Error("an unscheduled backup is stale")
	}
	// A partial run is not a success.
	h.setState(func(spec *runfile.Spec) *runfile.State {
		return finalState(spec, runfile.ResultPartial, runfile.ResultPartial)
	})
	h.run(b.Id)
	if h.svc.checkStale(ctx); !overdue() || !h.open(key) {
		t.Fatal("a partial run cleared the stale problem")
	}
	h.setState(succeed)
	h.run(b.Id)
	if h.svc.checkStale(ctx); overdue() || h.open(key) {
		t.Fatal("still stale after a successful run")
	}
	// Stale again, then taken off the schedule: nothing would check it again.
	h.advance(49 * time.Hour)
	if h.svc.checkStale(ctx); !h.open(key) {
		t.Fatal("not stale 49h after the last success")
	}
	off := false
	if _, err := h.svc.Update(ctx, b.Id, BackupPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if h.open(key) || overdue() {
		t.Error("a disabled backup is still reported stale")
	}
}

func TestResolveOrphans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.withNotifier()
	b := h.backup("Nightly")
	for _, k := range []string{
		"backup:b2-backup.service", "backup-stale:b2-backup.service", // the nix job this replaced
		"backup:999", "backup-coverage-like:1", "service:caddy.service",
		problemKey(b.Id), staleKey(b.Id),
	} {
		if _, err := h.notify.Report(ctx, notifications.Problem{Key: k, Title: k}); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.resolveOrphans(ctx)
	for k, want := range map[string]bool{
		"backup:b2-backup.service": false, "backup-stale:b2-backup.service": false, "backup:999": false,
		"backup-coverage-like:1": true, "service:caddy.service": true, problemKey(b.Id): true, staleKey(b.Id): true,
	} {
		if h.open(k) != want {
			t.Errorf("%s open = %v, want %v", k, !want, want)
		}
	}
	// Deleting a backup closes its own.
	if err := h.svc.Delete(ctx, b.Id); err != nil {
		t.Fatal(err)
	}
	if h.open(problemKey(b.Id)) || h.open(staleKey(b.Id)) {
		t.Error("a deleted backup's problems stayed open")
	}
}

func TestCoverageAndFoldersAdded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	titles := h.withNotifier()
	stackFixture(h)
	d := h.dest(s3Input("Bucket"))
	mk := func(name string, enabled bool, dests []int64, src ...SourceInput) Backup {
		t.Helper()
		b, err := h.svc.Create(ctx, BackupInput{Name: name, Enabled: &enabled, DestinationIds: dests, Sources: src})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	mk("Photos", true, []int64{d.Id}, stackSrc("immich", "UPLOAD_DIR"))
	mk("Also photos", true, []int64{d.Id}, stackSrc("immich", "UPLOAD_DIR"))
	mk("Disabled", false, []int64{d.Id}, stackSrc("immich", "/var/backup/postgresql"))
	mk("Nowhere", true, nil, stackSrc("media", "/config"))

	cov, err := h.svc.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, st := range cov.Stacks {
		for _, f := range st.Folders {
			got[st.Stack+"/"+f.Key] = f.CoveredBy
			if f.Excludes == nil {
				t.Errorf("%s/%s: excludes is null", st.Stack, f.Key)
			}
		}
	}
	want := map[string][]string{
		"immich/UPLOAD_DIR": {"Photos", "Also photos"}, "immich//var/backup/postgresql": {},
		"media//config": {}, "media/BROKEN": {}, "plain//data": {},
	}
	if !reflect.DeepEqual(got, want) || cov.Uncovered != 4 {
		t.Errorf("coverage = %v, uncovered %d", got, cov.Uncovered)
	}
	if cov.Stacks[0].Prepare != "postgresqlBackup.service" || cov.Stacks[1].Folders[1].Problem == "" {
		t.Errorf("coverage detail = %+v", cov.Stacks)
	}

	// One message when a new folder is not backed up; none when all are.
	h.svc.FoldersAdded(ctx, "immich", []string{"UPLOAD_DIR"})
	h.svc.FoldersAdded(ctx, "immich", nil)
	if got := titles(); len(got) != 0 {
		t.Fatalf("notified for covered folders: %v", got)
	}
	h.svc.FoldersAdded(ctx, "immich", []string{"UPLOAD_DIR", "/var/backup/postgresql", "NEW"})
	if got := titles(); !reflect.DeepEqual(got, []string{"Not backed up: immich"}) {
		t.Fatalf("titles = %v", got)
	}
	// It is a message, not a problem: nothing reminds about it.
	if ps, _ := h.notify.OpenProblems(ctx); len(ps) != 0 {
		t.Errorf("open problems = %+v", ps)
	}
}

// The stacks' folders are read through a short cache; an install or update
// drops it.
func TestStackFoldersCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	stackFixture(h)
	for range 3 {
		if _, err := h.svc.Coverage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if h.stacks.reads != 1 {
		t.Errorf("reads = %d, want 1", h.stacks.reads)
	}
	h.svc.FoldersAdded(ctx, "immich", nil)
	_, _ = h.svc.Coverage(ctx)
	h.advance(foldersTTL + time.Second)
	_, _ = h.svc.Coverage(ctx)
	if h.stacks.reads != 3 {
		t.Errorf("reads = %d, want 3", h.stacks.reads)
	}
	// A failed read keeps the last answer.
	h.stacks.mu.Lock()
	h.stacks.err = errors.New("systemd is busy")
	h.stacks.mu.Unlock()
	h.advance(foldersTTL + time.Second)
	if cov, _ := h.svc.Coverage(ctx); len(cov.Stacks) != 3 {
		t.Errorf("coverage after a failed read = %+v", cov)
	}
}

func TestLogsAndLogUnits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")
	h.fake.On("journalctl", "-u", "homelab-backup-1.service").Reply("line one\nline two\n")
	if logs, err := h.svc.Logs(ctx, b.Id); err != nil || logs != "line one\nline two\n" {
		t.Errorf("logs = %q %v", logs, err)
	}
	if _, err := h.svc.Logs(ctx, 999); !errors.Is(err, ErrUnknownBackup) {
		t.Errorf("logs of unknown = %v", err)
	}
	if got := h.svc.LogUnits(ctx); !reflect.DeepEqual(got, []LogUnit{{Name: "Nightly", Unit: "homelab-backup-1.service"}}) {
		t.Errorf("log units = %+v", got)
	}
}

func TestScheduledRunFires(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.setState(succeed)
	b := h.backup("Nightly")
	h.svc.runScheduled(b.Id)
	h.svc.Wait()
	runs, _ := h.svc.Runs(context.Background(), b.Id, 10)
	if len(runs) != 1 || runs[0].Trigger != BackupRunTriggerCron || runs[0].Result != BackupRunResultSuccess {
		t.Fatalf("runs = %+v", runs)
	}
	// A backup deleted between the tick and the run is not an error to act on.
	h.svc.runScheduled(999)
}
