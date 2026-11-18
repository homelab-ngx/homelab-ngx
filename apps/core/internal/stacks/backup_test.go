package stacks

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// backupManifest is immich at version 7 with a path variable and a backup
// section.
func backupManifest(library, backup string) string {
	return `{
  "version": 7,
  "name": "immich",
  "nixpkgs": "nixos-unstable",
  "routes": [{"host": "immich.example.com", "port": 2283}],
  "variables": [{"name": "UPLOAD_DIR", "kind": "path", "description": "", "required": false, "mount": "/library"` + library + `}],
  "health": {"units": ["immich-server.service"]},
  "backup": ` + backup + `
}`
}

const backupSection = `{
  "prepare": "postgresqlBackup.service",
  "folders": [
    {"label": "Database dumps", "path": "/var/backup/postgresql"},
    {"label": "Library", "variable": "UPLOAD_DIR", "exclude": [{"pattern": "/thumbs/**", "reason": "regenerated"}]}
  ]
}`

func TestParseManifestReadsTheBackupSection(t *testing.T) {
	t.Parallel()
	m := mustParse(t, backupManifest("", backupSection))
	want := &Backup{Prepare: "postgresqlBackup.service", Folders: []BackupDir{
		{Label: "Database dumps", Path: "/var/backup/postgresql"},
		{Label: "Library", Variable: "UPLOAD_DIR", Exclude: []BackupIgnore{{Pattern: "/thumbs/**", Reason: "regenerated"}}},
	}}
	if !reflect.DeepEqual(m.Backup, want) {
		t.Fatalf("backup = %+v, want %+v", m.Backup, want)
	}
	if keys := []string{m.Backup.Folders[0].Key(), m.Backup.Folders[1].Key()}; !reflect.DeepEqual(keys, []string{"/var/backup/postgresql", "UPLOAD_DIR"}) {
		t.Errorf("keys = %v", keys)
	}
	// It survives the stored form, and the API's mirror of it.
	again, err := decode(encode(m))
	if err != nil || !reflect.DeepEqual(again, m) {
		t.Errorf("decode(encode) = %+v, %v", again, err)
	}
	if am := revision("r", "u", m, 0).Manifest; am.Backup == nil || am.Backup.Prepare != "postgresqlBackup.service" ||
		len(am.Backup.Folders) != 2 || am.Backup.Folders[1].Exclude[0].Reason != "regenerated" {
		t.Errorf("API manifest backup = %+v", am.Backup)
	}
	// An empty section is no section, and asks for nothing.
	if e := mustParse(t, backupManifest("", `{}`)); e.Backup != nil {
		t.Errorf("empty backup = %+v, want nil", e.Backup)
	}
	without := mustParse(t, backupManifest("", `{}`))
	if g := NewGrants(without, m); len(g) != 0 {
		t.Errorf("a backup section asked for grants: %+v", g)
	}
	if strings.Contains(encode(mustParse(t, immichManifest)), "backup") {
		t.Error("a manifest without backup encodes one")
	}
}

func TestParseManifestRefusesABadBackupSection(t *testing.T) {
	t.Parallel()
	folder := func(f string) string { return `{"folders": [` + f + `]}` }
	for name, tc := range map[string]struct{ manifest, want string }{
		"below version 6": {strings.Replace(backupManifest("", backupSection), "\"version\": 7,\n  \"name\": \"immich\",\n  \"nixpkgs\": \"nixos-unstable\",", `"version": 5, "name": "immich",`, 1), "backup needs version 6"},
		"prepare":         {backupManifest("", `{"prepare": "dump.timer"}`), "backup.prepare"},
		"no label":        {backupManifest("", folder(`{"path": "/var/lib/x"}`)), "label"},
		"blank label":     {backupManifest("", folder(`{"label": "  ", "path": "/var/lib/x"}`)), "label"},
		"both":            {backupManifest("", folder(`{"label": "a", "path": "/var/lib/x", "variable": "UPLOAD_DIR"}`)), "not both"},
		"neither":         {backupManifest("", folder(`{"label": "a"}`)), "needs a path"},
		"relative path":   {backupManifest("", folder(`{"label": "a", "path": "var/lib/x"}`)), "must be absolute"},
		"unclean path":    {backupManifest("", folder(`{"label": "a", "path": "/var/../etc"}`)), "plain path"},
		"whole stack":     {backupManifest("", folder(`{"label": "a", "path": "/"}`)), "whole stack"},
		"unknown var":     {backupManifest("", folder(`{"label": "a", "variable": "NOPE"}`)), "not a path variable"},
		"env var": {strings.Replace(backupManifest("", folder(`{"label": "a", "variable": "LEVEL"}`)), `"variables": [`,
			`"variables": [{"name": "LEVEL", "kind": "env", "description": "", "required": false},`, 1), "not a path variable"},
		"path twice": {backupManifest("", folder(`{"label": "a", "path": "/var/lib/x"}, {"label": "b", "path": "/var/lib/x"}`)), "listed twice"},
		"var twice":  {backupManifest("", folder(`{"label": "a", "variable": "UPLOAD_DIR"}, {"label": "b", "variable": "UPLOAD_DIR"}`)), "listed twice"},
		"no reason":  {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"pattern": "/cache/**"}]}`)), "reason"},
		"no pattern": {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"reason": "r"}]}`)), "pattern"},
		"unanchored": {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"pattern": "cache/**", "reason": "r"}]}`)), "must start with"},
		"class":      {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"pattern": "/[abc]", "reason": "r"}]}`)), "only plain characters"},
		"braces":     {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"pattern": "/{a,b}", "reason": "r"}]}`)), "only plain characters"},
		"backslash":  {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"pattern": "/a\\*", "reason": "r"}]}`)), "only plain characters"},
		"newline":    {backupManifest("", folder(`{"label": "a", "path": "/x", "exclude": [{"pattern": "/a\nb", "reason": "r"}]}`)), "only plain characters"},
		"unknown":    {backupManifest("", `{"folders": [], "restore": true}`), "unknown field"},
	} {
		if _, err := ParseManifest([]byte(tc.manifest)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestValidBackupPattern(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"/cache/**", "**/thumbs/**", "/a/*.tmp", "/logs/?.log", "**/*.part", "/My Files/x"} {
		if err := ValidBackupPattern(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	for _, p := range []string{"", "/", "**/", "cache", "*.tmp", "/a/***", "/a[b]", "/a{b}", `/a\b`, "/a\tb"} {
		if err := ValidBackupPattern(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

// installed adds and approves immich with manifest.
func installed(t *testing.T, svc *Service, fs *fakeSource, manifest string) {
	t.Helper()
	fs.set("aaaaaaa1", manifest)
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(context.Background(), "immich", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
}

func folderByKey(t *testing.T, fs []BackupFolder, key string) BackupFolder {
	t.Helper()
	for _, f := range fs {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no folder %q in %+v", key, fs)
	return BackupFolder{}
}

func TestBackupFoldersResolveToHostPaths(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	ctx := context.Background()
	// A host path a stack may be given cannot be under the test's temp dir;
	// one that does not exist resolves to itself.
	library := "/srv/homelab-test-absent/library"
	svc.statDir = func(string) error { return nil }
	installed(t, svc, fs, backupManifest("", `{
	  "prepare": "postgresqlBackup.service",
	  "folders": [
	    {"label": "Database dumps", "path": "/var/backup/postgresql"},
	    {"label": "Library", "variable": "UPLOAD_DIR", "exclude": [{"pattern": "/thumbs/**", "reason": "regenerated"}]},
	    {"label": "Uploads", "path": "/library/upload"},
	    {"label": "Not there yet", "path": "/var/lib/later/data"}
	  ]}`))

	// No value for the path yet: its two folders say so, the others resolve.
	got, err := svc.BackupFolders(ctx)
	if err != nil || len(got) != 4 {
		t.Fatalf("folders = %+v, %v", got, err)
	}
	root := filepath.Join(evalPath(t, svc.stateDir("immich")), "root")
	if f := folderByKey(t, got, "/var/backup/postgresql"); f.HostPath != filepath.Join(root, "var/backup/postgresql") || f.Error != "" ||
		f.Stack != "immich" || f.Running || f.Prepare != "postgresqlBackup.service" || f.Label != "Database dumps" {
		t.Errorf("root folder = %+v", f)
	}
	if f := folderByKey(t, got, "UPLOAD_DIR"); f.HostPath != "" || !strings.Contains(f.Error, "UPLOAD_DIR has no value") {
		t.Errorf("variable folder without a value = %+v", f)
	}
	if f := folderByKey(t, got, "/library/upload"); f.HostPath != "" || !strings.Contains(f.Error, "UPLOAD_DIR") {
		t.Errorf("folder under an unset bind = %+v (it must not fall back to the stack's root)", f)
	}

	if _, err := svc.SetVariable(ctx, "immich", "UPLOAD_DIR", StackVariableSet{Value: library}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.BackupFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f := folderByKey(t, got, "UPLOAD_DIR"); f.HostPath != library || f.Error != "" ||
		!reflect.DeepEqual(f.Excludes, []BackupExclude{{Pattern: "/thumbs/**", Reason: "regenerated"}}) {
		t.Errorf("variable folder = %+v", f)
	}
	if f := folderByKey(t, got, "/library/upload"); f.HostPath != filepath.Join(library, "upload") || f.Error != "" {
		t.Errorf("folder under a bind = %+v", f)
	}
	if f := folderByKey(t, got, "/var/lib/later/data"); f.HostPath != filepath.Join(root, "var/lib/later/data") || f.Error != "" {
		t.Errorf("folder that does not exist yet = %+v", f)
	}
}

func TestBackupFoldersRefuseALinkOutOfTheStack(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	ctx := context.Background()
	installed(t, svc, fs, backupManifest("", `{"folders": [
	  {"label": "Escapes", "path": "/var/lib/app"},
	  {"label": "Escapes below", "path": "/var/lib/link/deeper/data"},
	  {"label": "Stays", "path": "/var/lib/inside"}
	]}`))
	root := filepath.Join(svc.stateDir("immich"), "root")
	outside := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "var/lib/real"), 0o755))
	// The stack owns its root: it can leave a link to anywhere on the host.
	must(t, os.Symlink(outside, filepath.Join(root, "var/lib/app")))
	must(t, os.Symlink(outside, filepath.Join(root, "var/lib/link")))
	must(t, os.Symlink("real", filepath.Join(root, "var/lib/inside")))

	got, err := svc.BackupFolders(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("folders = %+v, %v", got, err)
	}
	for _, key := range []string{"/var/lib/app", "/var/lib/link/deeper/data"} {
		if f := folderByKey(t, got, key); f.HostPath != "" || !strings.Contains(f.Error, "outside") {
			t.Errorf("%s = %+v, want it refused", key, f)
		}
	}
	if f := folderByKey(t, got, "/var/lib/inside"); f.HostPath != filepath.Join(evalPath(t, root), "var/lib/real") || f.Error != "" {
		t.Errorf("a link that stays inside = %+v", f)
	}
}

func TestBackupFoldersMapAStorageBindAndSkipStacksWithoutASection(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	ctx := context.Background()
	installed(t, svc, fs, immichManifest)
	if got, err := svc.BackupFolders(ctx); err != nil || len(got) != 0 {
		t.Fatalf("folders of a stack without a backup section = %+v, %v", got, err)
	}
	// A version 4 storage bind maps like a path variable's.
	// The bind's host folder is the test's own: the host's real /pool/immich
	// may exist and be unreadable.
	pool := evalPath(t, t.TempDir())
	upload := filepath.Join(pool, "upload")
	must(t, os.MkdirAll(upload, 0o755))
	m := mustParse(t, immichManifest)
	for i := range m.Storage {
		m.Storage[i].HostPath = pool
	}
	for i := range m.Variables {
		if m.Variables[i].Kind == VarPath {
			m.Variables[i].Default = &pool
		}
	}
	p, err := svc.backupHostPath("immich", m, nil, BackupDir{Label: "l", Path: "/pool/immich/upload"})
	if err != nil || p != upload {
		t.Errorf("under a storage bind = %q, %v", p, err)
	}
}

func TestRunPrepareStartsTheUnitInsideTheStack(t *testing.T) {
	t.Parallel()
	svc, _, h := newServiceOn(t)
	ctx := context.Background()
	h.On("systemctl", "--machine=immich", "start").Reply("")
	if err := svc.RunPrepare(ctx, "immich", "postgresqlBackup.service"); err != nil {
		t.Fatal(err)
	}
	calls := h.CallsMatching("systemctl", "--machine=immich", "start")
	if len(calls) != 1 || argv(calls[0]) != "systemctl --machine=immich start -- postgresqlBackup.service" {
		t.Fatalf("calls = %+v", calls)
	}

	h.On("systemctl", "--machine=media", "start").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return fail(1, "Job for dump.service failed.")
	})
	err := svc.RunPrepare(ctx, "media", "dump.service")
	if err == nil || !strings.Contains(err.Error(), "dump.service") || !strings.Contains(err.Error(), "media") {
		t.Errorf("err = %v, want it to name the unit and the stack", err)
	}
	// Nothing that is not a unit name reaches systemctl.
	if err := svc.RunPrepare(ctx, "immich", "--now"); err == nil {
		t.Error("a unit name that is an option was accepted")
	}
}

// addedCalls records what OnBackupFoldersAdded hears.
type addedCalls struct {
	mu    sync.Mutex
	calls [][]string
}

func (a *addedCalls) listen(svc *Service) {
	svc.OnBackupFoldersAdded(func(_ context.Context, stack string, keys []string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.calls = append(a.calls, append([]string{stack}, keys...))
	})
}

func (a *addedCalls) got() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]string(nil), a.calls...)
}

func TestBackupFoldersAddedIsHeardOnceAfterAHealthyInstallAndUpdate(t *testing.T) {
	t.Parallel()
	svc, fs, h := newServiceOn(t)
	var heard addedCalls
	heard.listen(svc)
	one := `{"folders": [{"label": "Dumps", "path": "/var/backup/postgresql"}]}`
	installed(t, svc, fs, backupManifest("", one))
	if len(heard.got()) != 0 {
		t.Fatalf("heard before anything ran: %v", heard.got())
	}

	// The install: every folder is new.
	if s := finished(t, svc, svc.Start); s.Operation.Error != "" {
		t.Fatalf("install: %+v", s.Operation)
	}
	want := [][]string{{"immich", "/var/backup/postgresql"}}
	if !reflect.DeepEqual(heard.got(), want) {
		t.Fatalf("after the install heard %v, want %v", heard.got(), want)
	}

	// Stopping and starting what is already built is not an install.
	finished(t, svc, svc.Stop)
	finished(t, svc, svc.Start)
	if !reflect.DeepEqual(heard.got(), want) {
		t.Fatalf("a plain start was heard: %v", heard.got())
	}

	// An update that adds a folder: only the new one.
	two := `{"folders": [{"label": "Dumps", "path": "/var/backup/postgresql"}, {"label": "Config", "path": "/var/lib/app"}]}`
	release(t, svc, fs, "bbbbbbb2", backupManifest("", two))
	if s := finished(t, svc, svc.Update); s.Operation.Error != "" || s.Generation != 2 {
		t.Fatalf("update: %+v", s)
	}
	want = append(want, []string{"immich", "/var/lib/app"})
	if !reflect.DeepEqual(heard.got(), want) {
		t.Fatalf("after the update heard %v, want %v", heard.got(), want)
	}

	// An update that adds none is not heard.
	release(t, svc, fs, "ccccccc3", strings.Replace(backupManifest("", two), `"port": 2283`, `"port": 3001`, 1))
	if s := finished(t, svc, svc.Update); s.Operation.Error != "" || s.Generation != 3 {
		t.Fatalf("second update: %+v", s)
	}
	if !reflect.DeepEqual(heard.got(), want) {
		t.Fatalf("an update without new folders was heard: %v", heard.got())
	}

	// An update that is rolled back as unhealthy is not heard either.
	three := strings.Replace(two, `]}`, `, {"label": "More", "path": "/var/lib/more"}]}`, 1)
	h.mu.Lock()
	h.unhealthy["/nix/store/ddddddd4-system"] = true
	h.mu.Unlock()
	svc.healthTimeout = 0
	release(t, svc, fs, "ddddddd4", backupManifest("", three))
	if s := finished(t, svc, svc.Update); s.Operation.Error == "" {
		t.Fatalf("the unhealthy update did not fail: %+v", s)
	}
	if !reflect.DeepEqual(heard.got(), want) {
		t.Fatalf("a rolled-back update was heard: %v", heard.got())
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// evalPath is p with its symlinks resolved, so it compares equal to what the
// resolver returns.
func evalPath(t *testing.T, p string) string {
	t.Helper()
	real, err := resolveExisting(p)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
