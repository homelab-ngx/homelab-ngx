package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"
)

// The test binary stands in for three programs. Linked as "rclone" or
// "restic" it is a fake tool driven by FAKE_* variables; with RUNNER_MAIN set
// it is the runner itself, for the tests that need to signal a real process.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "rclone":
		os.Exit(fakeRclone(os.Args[1:]))
	case "restic":
		os.Exit(fakeRestic(os.Args[1:]))
	}
	if os.Getenv("RUNNER_MAIN") == "1" {
		os.Exit(realMain(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}

func fakeLog(tool string, args []string) {
	if p := os.Getenv("FAKE_LOG"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			b, _ := json.Marshal(map[string]any{"tool": tool, "args": args})
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

func fakeRclone(args []string) int {
	fakeLog("rclone", args)
	if args[0] == "lsd" {
		if strings.Contains(args[1], "unreachable") {
			fmt.Fprintln(os.Stderr, `{"level":"error","msg":"couldn't connect: access denied"}`)
			return 1
		}
		if strings.Contains(args[1], "fresh") || strings.Contains(args[1], "nobucket") {
			fmt.Fprintln(os.Stderr, `{"level":"error","msg":"error listing: directory not found"}`)
			return 3
		}
		fmt.Println("        4096 2026-11-16 19:21:45        -1 out")
		return 0
	}
	stats := func(b, tb, n, tn int) {
		fmt.Fprintf(os.Stderr, `{"time":"t","level":"notice","msg":"\nTransferred: ...","stats":{"bytes":%d,"totalBytes":%d,"transfers":%d,"totalTransfers":%d,"speed":1000.5,"errors":0,"transferring":[{"name":"a"}]},"source":"accounting/stats.go:549"}`+"\n", b, tb, n, tn)
	}
	for i, a := range args {
		if a != "--combined" {
			continue
		}
		// A dry run: report the files to the pipe, copy nothing.
		dry := false
		for _, b := range args {
			dry = dry || b == "--dry-run"
		}
		if !dry {
			fmt.Fprintln(os.Stderr, `{"level":"error","msg":"--combined without --dry-run"}`)
			return 1
		}
		f, err := os.OpenFile(args[i+1], os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, `{"level":"notice","msg":"Skipped copy as --dry-run is set (size 1)","skipped":"copy","size":1,"object":"file.txt"}`+"\n")
		fmt.Fprintf(os.Stderr, `{"level":"notice","msg":"stats","stats":{"checks":2,"totalChecks":4,"transfers":1,"totalTransfers":1}}`+"\n")
		fmt.Fprint(f, "+ file.txt\n* sub/changed.bin\n= same.txt\n= same2.txt\n- only/there.txt\n")
		for n := 0; n < 150; n++ {
			fmt.Fprintf(f, "+ many/%03d\n", n)
		}
		f.Close()
		if os.Getenv("FAKE_RCLONE") == "hang" {
			// Stats keep coming, as from a comparison that goes on: the
			// runner counts the report's lines into its progress on each.
			for {
				time.Sleep(50 * time.Millisecond)
				fmt.Fprintf(os.Stderr, `{"level":"notice","msg":"stats","stats":{"checks":2,"totalChecks":4,"transfers":1,"totalTransfers":1}}`+"\n")
			}
		}
		return 0
	}
	stats(100, 300, 1, 3)
	switch os.Getenv("FAKE_RCLONE") {
	case "noise":
		fmt.Fprintln(os.Stderr, strings.Repeat("x", 1<<20))
		fmt.Fprintln(os.Stderr, `{"somethingNew":{"a":1},"n":[1,2]}`)
		fmt.Fprintln(os.Stderr, `{"level":"notice","msg":"`+strings.Repeat("y", 1<<20)+`"}`)
		fmt.Fprintln(os.Stderr, "not json at all")
		stats(300, 300, 3, 3)
		fmt.Fprint(os.Stderr, "{broken json without newline")
		return 0
	case "hang":
		// A helper in the same process group, to prove the group is signalled.
		time.Sleep(time.Hour)
	case "fail":
		if strings.Contains(args[1], "bad") {
			fmt.Fprintln(os.Stderr, `{"level":"error","msg":"Failed to copy: quota exceeded"}`)
			return 1
		}
	}
	stats(300, 300, 3, 3)
	return 0
}

func fakeRestic(args []string) int {
	fakeLog("restic", args)
	repo := os.Getenv("RESTIC_REPOSITORY")
	pw, _ := os.ReadFile(os.Getenv("RESTIC_PASSWORD_FILE"))
	if os.Getenv("B2_ACCOUNT_KEY") != "backend-secret" {
		fmt.Println(`{"message_type":"exit_error","code":1,"message":"Fatal: backend credentials missing from the environment"}`)
		return 1
	}
	_, statErr := os.Stat(filepath.Join(repo, "config"))
	switch args[0] {
	case "cat":
		if statErr != nil {
			fmt.Println(`{"message_type":"exit_error","code":10,"message":"Fatal: repository does not exist"}`)
			return 10
		}
		if string(pw) != "restic-secret" {
			fmt.Println(`{"message_type":"exit_error","code":12,"message":"Fatal: wrong password or no key found"}`)
			return 12
		}
		fmt.Println(`{"version":2,"id":"abc","chunker_polynomial":"x"}`)
		return 0
	case "init":
		os.MkdirAll(repo, 0o700)
		os.WriteFile(filepath.Join(repo, "config"), nil, 0o600)
		fmt.Println(`{"message_type":"initialized","id":"abc","repository":"r"}`)
		return 0
	case "backup":
		fmt.Println(`{"message_type":"status","percent_done":0.5,"total_files":4,"files_done":2,"total_bytes":4000,"bytes_done":2000}`)
		if os.Getenv("FAKE_RESTIC") == "partial" {
			fmt.Fprintln(os.Stderr, `{"message_type":"error","error":{"message":"open /x: permission denied"},"during":"archival","item":"/x"}`)
		}
		fmt.Println(`{"message_type":"summary","files_new":2,"files_changed":1,"files_unmodified":1,"data_added":1234,"total_files_processed":4,"total_bytes_processed":4000,"snapshot_id":"s"}`)
		if os.Getenv("FAKE_RESTIC") == "partial" {
			fmt.Println(`{"message_type":"exit_error","code":3,"message":"Warning: at least one source file could not be read"}`)
			return 3
		}
		return 0
	case "forget":
		if os.Getenv("FAKE_RESTIC") == "prunefail" {
			fmt.Fprintln(os.Stderr, "Fatal: unable to create lock in backend")
			return 11
		}
		fmt.Println("Applying Policy: keep 7 daily snapshots")
		return 0
	}
	return 2
}

// env is one test's world: fake tools, a work directory, two source folders.
type env struct {
	t     *testing.T
	dir   string
	spec  *runfile.Spec
	state string
	log   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"rclone", "restic"} {
		if err := os.Symlink(self, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, dir: dir, state: filepath.Join(dir, "state.json"), log: filepath.Join(dir, "argv.log")}
	t.Setenv("RCLONE_BIN", filepath.Join(dir, "rclone"))
	t.Setenv("RESTIC_BIN", filepath.Join(dir, "restic"))
	t.Setenv("FAKE_LOG", e.log)
	// A race-built binary sleeps a second at exit; the fakes are this binary.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	mounts := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(mounts, []byte("36 35 98:0 / / rw - ext4 /dev/root rw\n40 36 0:30 / /mnt/with\\040space rw - ext4 /dev/x rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := mountinfoPath
	mountinfoPath = mounts
	t.Cleanup(func() { mountinfoPath = old })

	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o700)
	e.spec = &runfile.Spec{Version: runfile.Version, RunID: 7, Backup: "Homelab", WorkDir: work, CacheDir: filepath.Join(dir, "cache")}
	for _, name := range []string{"good", "bad"} {
		p := filepath.Join(dir, "src", name)
		os.MkdirAll(p, 0o700)
		os.WriteFile(filepath.Join(p, "file.txt"), []byte("x"), 0o600)
		e.spec.Folders = append(e.spec.Folders, runfile.Folder{Label: name, Path: p, RemotePath: "data/" + name})
	}
	return e
}

func (e *env) rclone(id int64, name, remote string) runfile.Destination {
	return runfile.Destination{
		ID: id, Name: name, Kind: runfile.KindRclone, Remote: remote,
		Config: "[r]\ntype = s3\nsecret_access_key = rclone-secret\n",
	}
}

func (e *env) restic(id int64, name string, init bool) runfile.Destination {
	return runfile.Destination{
		ID: id, Name: name, Kind: runfile.KindRestic, Repository: filepath.Join(e.dir, "repo-"+name),
		Password: "restic-secret", Env: map[string]string{"B2_ACCOUNT_KEY": "backend-secret"}, Init: init,
		Keep: runfile.Keep{Daily: 7, Weekly: 4},
	}
}

func (e *env) writeSpec() string {
	e.t.Helper()
	p := filepath.Join(e.dir, "spec.json")
	if err := runfile.WriteSpec(p, e.spec); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// run runs the runner in this process and returns its exit code and state.
func (e *env) run() (int, *runfile.State) {
	e.t.Helper()
	spec := e.writeSpec()
	var stderr bytes.Buffer
	args := []string{"--spec", spec, "--state", e.state}
	if e.spec.Preview {
		args = append(args, "--preview")
	}
	code := realMain(args, &stderr)
	if _, err := os.Stat(spec); !os.IsNotExist(err) {
		e.t.Errorf("spec file was not deleted")
	}
	e.t.Logf("runner stderr:\n%s", truncate(stderr.String(), 3000))
	return code, e.readState()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (e *env) readState() *runfile.State {
	e.t.Helper()
	st, err := runfile.ReadState(e.state)
	if err != nil {
		e.t.Fatalf("read state: %v", err)
	}
	return st
}

type call struct {
	Tool string   `json:"tool"`
	Args []string `json:"args"`
}

func (e *env) calls() []call {
	e.t.Helper()
	b, _ := os.ReadFile(e.log)
	var out []call
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var c call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func (e *env) verbs(tool string) []string {
	var out []string
	for _, c := range e.calls() {
		if c.Tool == tool {
			out = append(out, c.Args[0])
		}
	}
	return out
}

func results(st *runfile.State) string {
	var parts []string
	for _, it := range st.Items {
		name := it.Destination
		if it.Kind == runfile.ItemFolder {
			name += "/" + it.Label
		}
		parts = append(parts, name+"="+it.Result)
	}
	return strings.Join(parts, " ")
}

func want(t *testing.T, st *runfile.State, result, items string) {
	t.Helper()
	if st.Result != result {
		t.Errorf("run result = %q (error %q), want %q", st.Result, st.Error, result)
	}
	if got := results(st); got != items {
		t.Errorf("items:\n got  %s\n want %s", got, items)
	}
	if st.FinishedAt == nil {
		t.Errorf("final state has no finished_at")
	}
	if st.Progress != nil {
		t.Errorf("final state still carries progress")
	}
}

func TestHappyPath(t *testing.T) {
	e := newEnv(t)
	e.spec.Bwlimit, e.spec.Transfers = "4M", 3
	e.spec.Folders[0].Excludes = []string{"/cache/**", "**/thumbs/**"}
	e.spec.Folders[0].SizeOnly = true
	d := e.rclone(1, "bucket", "r:bucket/prefix")
	d.DeleteRemoved = true
	e.spec.Destinations = []runfile.Destination{d, e.restic(2, "repo", true)}

	code, st := e.run()
	if code != exitOK {
		t.Errorf("exit code %d, want 0", code)
	}
	want(t, st, runfile.ResultSuccess,
		"bucket=success bucket/good=success bucket/bad=success repo=success repo/good=success repo/bad=success")
	if it := st.Items[1]; it.Files != 3 || it.Bytes != 300 || it.StartedAt == nil || it.FinishedAt == nil {
		t.Errorf("rclone folder item = %+v, want 3 files, 300 bytes, both times", it)
	}
	if it := st.Items[0]; it.Files != 6 || it.Bytes != 600 {
		t.Errorf("rclone destination totals = %d files, %d bytes, want 6 and 600", it.Files, it.Bytes)
	}
	if it := st.Items[4]; it.Files != 3 || it.Bytes != 1234 {
		t.Errorf("restic folder item = %d files, %d bytes, want 3 and 1234", it.Files, it.Bytes)
	}

	calls := e.calls()
	sync := strings.Join(calls[1].Args, " ")
	for _, s := range []string{
		"sync " + e.spec.Folders[0].Path + " r:bucket/prefix/data/good", "--one-file-system", "--use-json-log",
		"--stats 2s", "--delete-excluded", "--size-only", "--bwlimit 4M", "--transfers 3", "--filter - /cache/** --filter - **/thumbs/**",
	} {
		if !strings.Contains(sync, s) {
			t.Errorf("rclone argv %q lacks %q", sync, s)
		}
	}
	if other := strings.Join(calls[2].Args, " "); strings.Contains(other, "--size-only") {
		t.Errorf("--size-only reached a folder that did not ask for it: %q", other)
	}
	if got := strings.Join(e.verbs("restic"), " "); got != "cat init backup backup forget" {
		t.Errorf("restic calls = %q", got)
	}
	var backup, forget string
	for _, c := range calls {
		if c.Tool == "restic" && c.Args[0] == "backup" && backup == "" {
			backup = strings.Join(c.Args, " ")
		}
		if c.Args[0] == "forget" {
			forget = strings.Join(c.Args, " ")
		}
	}
	for _, s := range []string{
		"--json", "--tag backup=Homelab", "--limit-upload 4096",
		"--exclude " + e.spec.Folders[0].Path + "/cache/**", "--exclude **/thumbs/**",
	} {
		if !strings.Contains(backup, s) {
			t.Errorf("restic argv %q lacks %q", backup, s)
		}
	}
	if forget != "forget --keep-daily 7 --keep-weekly 4 --group-by host,paths --tag backup=Homelab --prune --retry-lock 5m" {
		t.Errorf("forget argv = %q", forget)
	}
	// Nothing the destinations wrote stays behind.
	if left, _ := os.ReadDir(e.spec.WorkDir); len(left) != 0 {
		t.Errorf("work dir still holds %d files", len(left))
	}
}

func TestNoSecretInArgv(t *testing.T) {
	e := newEnv(t)
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket"), e.restic(2, "repo", true)}
	if _, st := e.run(); st.Result != runfile.ResultSuccess {
		t.Fatalf("run did not succeed: %s", st.Error)
	}
	b, _ := os.ReadFile(e.log)
	for _, secret := range []string{"rclone-secret", "restic-secret", "backend-secret"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("%q reached a tool's argv", secret)
		}
	}
	if len(e.calls()) < 6 {
		t.Fatalf("only %d tool calls were recorded", len(e.calls()))
	}
}

func TestFailedDestinationSkipsOnlyItsFolders(t *testing.T) {
	e := newEnv(t)
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "down", "r:unreachable"), e.rclone(2, "up", "r:fresh")}
	code, st := e.run()
	if code != exitFailed {
		t.Errorf("exit code %d, want 1", code)
	}
	want(t, st, runfile.ResultPartial, "down=failure down/good=skipped down/bad=skipped up=success up/good=success up/bad=success")
	if !strings.Contains(st.Items[0].Error, "access denied") {
		t.Errorf("destination error = %q, want rclone's own message", st.Items[0].Error)
	}
	if got := strings.Join(e.verbs("rclone"), " "); got != "lsd lsd copy copy" {
		t.Errorf("rclone calls = %q", got)
	}
}

func TestMissingBucketFailsTheDestination(t *testing.T) {
	e := newEnv(t)
	gone, fresh := e.rclone(1, "gone", "r:nobucket/host"), e.rclone(2, "fresh", "r:bucket/fresh")
	gone.Bucket, fresh.Bucket = "r:nobucket", "r:bucket"
	e.spec.Destinations = []runfile.Destination{gone, fresh}
	code, st := e.run()
	if code != exitFailed {
		t.Errorf("exit code %d, want 1", code)
	}
	want(t, st, runfile.ResultPartial, "gone=failure gone/good=skipped gone/bad=skipped fresh=success fresh/good=success fresh/bad=success")
	if !strings.Contains(st.Items[0].Error, `bucket "nobucket" does not exist`) {
		t.Errorf("destination error = %q, want the missing bucket", st.Items[0].Error)
	}
	if got := strings.Join(e.verbs("rclone"), " "); got != "lsd lsd lsd lsd copy copy" {
		t.Errorf("rclone calls = %q", got)
	}
}

func TestOneFolderFailureDoesNotStopTheNext(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_RCLONE", "fail")
	e.spec.Folders[0], e.spec.Folders[1] = e.spec.Folders[1], e.spec.Folders[0]
	e.spec.Folders = append(e.spec.Folders, runfile.Folder{Label: "prepared", Path: "/nowhere", Fail: "prepare unit dump.service failed"})
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
	_, st := e.run()
	want(t, st, runfile.ResultPartial, "bucket=success bucket/bad=failure bucket/good=success bucket/prepared=failure")
	if st.Items[1].Error != "Failed to copy: quota exceeded" {
		t.Errorf("folder error = %q", st.Items[1].Error)
	}
	if st.Items[3].Error != "prepare unit dump.service failed" {
		t.Errorf("failed-prepare folder error = %q", st.Items[3].Error)
	}
	if !strings.Contains(st.Error, "2 of 4") {
		t.Errorf("run error = %q", st.Error)
	}
}

func TestUnmountedPool(t *testing.T) {
	e := newEnv(t)
	e.spec.PoolMounts = []string{filepath.Dir(e.spec.Folders[0].Path), "/mnt/with space"}
	e.spec.Folders = e.spec.Folders[:1]
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
	_, st := e.run()
	want(t, st, runfile.ResultFailure, "bucket=success bucket/good=failure")
	if !strings.Contains(st.Items[1].Error, "is not mounted") {
		t.Errorf("error = %q", st.Items[1].Error)
	}
	if got := strings.Join(e.verbs("rclone"), " "); got != "lsd" {
		t.Errorf("rclone calls = %q, the folder must not be copied", got)
	}
	if ok, err := isMountpoint("/mnt/with space"); err != nil || !ok {
		t.Errorf("escaped mountpoint not recognised: %v %v", ok, err)
	}
}

func TestEmptySourceWithDelete(t *testing.T) {
	e := newEnv(t)
	empty := filepath.Join(e.dir, "src", "empty")
	os.MkdirAll(empty, 0o700)
	e.spec.Folders = []runfile.Folder{{Label: "empty", Path: empty, RemotePath: "empty"}}
	sync := e.rclone(1, "sync", "r:a")
	sync.DeleteRemoved = true
	e.spec.Destinations = []runfile.Destination{sync, e.rclone(2, "copy", "r:b")}
	_, st := e.run()
	want(t, st, runfile.ResultPartial, "sync=success sync/empty=failure copy=success copy/empty=success")
	if !strings.Contains(st.Items[1].Error, "is empty") {
		t.Errorf("error = %q", st.Items[1].Error)
	}
}

func TestResticInit(t *testing.T) {
	t.Run("absent without init is a failure", func(t *testing.T) {
		e := newEnv(t)
		e.spec.Destinations = []runfile.Destination{e.restic(1, "repo", false)}
		_, st := e.run()
		want(t, st, runfile.ResultFailure, "repo=failure repo/good=skipped repo/bad=skipped")
		if st.Items[0].Error != "the repository does not exist" {
			t.Errorf("error = %q", st.Items[0].Error)
		}
		if got := strings.Join(e.verbs("restic"), " "); got != "cat" {
			t.Errorf("restic calls = %q", got)
		}
	})
	t.Run("existing repository is not initialised again", func(t *testing.T) {
		e := newEnv(t)
		d := e.restic(1, "repo", true)
		os.MkdirAll(d.Repository, 0o700)
		os.WriteFile(filepath.Join(d.Repository, "config"), nil, 0o600)
		d.Keep = runfile.Keep{}
		e.spec.Destinations = []runfile.Destination{d}
		_, st := e.run()
		want(t, st, runfile.ResultSuccess, "repo=success repo/good=success repo/bad=success")
		if got := strings.Join(e.verbs("restic"), " "); got != "cat backup backup" {
			t.Errorf("restic calls = %q (no init, and no forget without a keep rule)", got)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		e := newEnv(t)
		d := e.restic(1, "repo", true)
		os.MkdirAll(d.Repository, 0o700)
		os.WriteFile(filepath.Join(d.Repository, "config"), nil, 0o600)
		d.Password = "nope"
		e.spec.Destinations = []runfile.Destination{d}
		_, st := e.run()
		want(t, st, runfile.ResultFailure, "repo=failure repo/good=skipped repo/bad=skipped")
		if st.Items[0].Error != "wrong password for this repository" {
			t.Errorf("error = %q", st.Items[0].Error)
		}
	})
}

func TestResticPartialAndPruneFailure(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_RESTIC", "partial")
	e.spec.Folders = e.spec.Folders[:1]
	e.spec.Destinations = []runfile.Destination{e.restic(1, "repo", true)}
	code, st := e.run()
	if code != exitFailed {
		t.Errorf("exit code %d, want 1", code)
	}
	want(t, st, runfile.ResultPartial, "repo=success repo/good=partial")
	if it := st.Items[1]; it.Files != 3 || !strings.Contains(it.Error, "could not be read") || !strings.Contains(it.Error, "permission denied") {
		t.Errorf("partial item = %+v", it)
	}

	e = newEnv(t)
	t.Setenv("FAKE_RESTIC", "prunefail")
	e.spec.Destinations = []runfile.Destination{e.restic(1, "repo", true)}
	_, st = e.run()
	want(t, st, runfile.ResultPartial, "repo=failure repo/good=success repo/bad=success")
	if !strings.Contains(st.Items[0].Error, "unable to create lock") {
		t.Errorf("prune error = %q", st.Items[0].Error)
	}
}

func TestResticFiles(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(e.dir, "stacks")
	for _, p := range []string{"a/variables.age", "b/variables.age", "b/root/deep/variables.age", "stacks.lock.json", "c/other"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o700)
		os.WriteFile(filepath.Join(root, p), []byte("x"), 0o600)
	}
	files, err := expandFiles(root, []string{"/stacks.lock.json", "/*/variables.age", "/missing"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(strings.Join(files, " "), root, "")
	if got != "/stacks.lock.json /a/variables.age /b/variables.age" {
		t.Errorf("expandFiles = %q", got)
	}
	deep, _ := expandFiles(root, []string{"**/deep/*.age"})
	if len(deep) != 1 || !strings.HasSuffix(deep[0], "b/root/deep/variables.age") {
		t.Errorf("deep pattern = %v", deep)
	}

	e.spec.Folders = []runfile.Folder{
		{Label: "core", Path: root, RemotePath: "stacks", Files: []string{"/stacks.lock.json", "/*/variables.age"}},
		{Label: "none", Path: root, RemotePath: "x", Files: []string{"/nothing-here"}},
	}
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:b"), e.restic(2, "repo", true)}
	_, st := e.run()
	want(t, st, runfile.ResultPartial, "bucket=success bucket/core=success bucket/none=success repo=success repo/core=success repo/none=failure")
	for _, c := range e.calls() {
		line := strings.Join(c.Args, " ")
		if c.Tool == "rclone" && c.Args[0] == "copy" && strings.Contains(line, " r:b/stacks ") &&
			!strings.HasSuffix(line, "--filter + /stacks.lock.json --filter + /*/variables.age --filter - **") {
			t.Errorf("rclone file filters = %q", line)
		}
		if c.Tool == "restic" && c.Args[0] == "backup" && !strings.Contains(line, "--files-from-verbatim") {
			t.Errorf("restic argv = %q, want a file list", line)
		}
	}
}

func TestNoisyOutput(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_RCLONE", "noise")
	e.spec.Folders = e.spec.Folders[:1]
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
	done := make(chan struct{})
	var st *runfile.State
	go func() { defer close(done); _, st = e.run() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the runner hung on a megabyte line")
	}
	// A megabyte line, unknown JSON, plain text and a cut-off line are noise,
	// not errors; the stats around them still count.
	want(t, st, runfile.ResultSuccess, "bucket=success bucket/good=success")
	if st.Items[1].Bytes != 300 {
		t.Errorf("bytes = %d, want 300", st.Items[1].Bytes)
	}
}

func TestReadLinesKeepsOnlyTheHead(t *testing.T) {
	in := strings.Repeat("a", 300_000) + "\nshort\n\n" + strings.Repeat("b", 10) // no final newline
	var got []string
	readLines(strings.NewReader(in), 1000, func(b []byte) { got = append(got, string(b)) })
	if len(got) != 3 || len(got[0]) != 1000 || got[1] != "short" || got[2] != "bbbbbbbbbb" {
		t.Errorf("lines = %d, first %d bytes, rest %q", len(got), len(got[0]), got[1:])
	}
}

func TestCheckOnly(t *testing.T) {
	e := newEnv(t)
	existing := e.restic(3, "existing", false)
	os.MkdirAll(existing.Repository, 0o700)
	os.WriteFile(filepath.Join(existing.Repository, "config"), nil, 0o600)
	e.spec.CheckOnly = true
	e.spec.Destinations = []runfile.Destination{
		e.rclone(1, "bucket", "r:bucket"), e.restic(2, "new", true), existing,
		e.restic(4, "gone", false), e.rclone(5, "down", "r:unreachable"),
	}
	_, st := e.run()
	want(t, st, runfile.ResultPartial, "bucket=success new=success existing=success gone=failure down=failure")
	if st.Items[1].Note != "the repository does not exist yet; the first run creates it" {
		t.Errorf("note = %q", st.Items[1].Note)
	}
	if got := strings.Join(e.verbs("restic"), " "); got != "cat cat cat" {
		t.Errorf("restic calls = %q: a check must not init, back up or prune", got)
	}
	if got := strings.Join(e.verbs("rclone"), " "); got != "lsd lsd" {
		t.Errorf("rclone calls = %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "repo-new")); !os.IsNotExist(err) {
		t.Errorf("a check created the repository")
	}
}

func TestPreview(t *testing.T) {
	e := newEnv(t)
	e.spec.Preview = true
	e.spec.Folders = e.spec.Folders[:1]
	e.spec.Folders[0].Excludes = []string{"/cache/**"}
	e.spec.Folders[0].SizeOnly = true
	sync := e.rclone(1, "bucket", "r:bucket")
	sync.DeleteRemoved = true
	e.spec.Destinations = []runfile.Destination{sync, e.rclone(2, "keeps", "r:other"), e.restic(3, "repo", true)}
	code, st := e.run()
	if code != exitOK || !st.Preview {
		t.Errorf("exit %d, preview %v", code, st.Preview)
	}
	want(t, st, runfile.ResultSuccess, "bucket=success bucket/good=success keeps=success keeps/good=success repo=skipped repo/good=skipped")
	pv := st.Items[1].Preview
	if pv == nil {
		t.Fatal("the folder has no preview")
	}
	// file.txt is in the source folder (1 byte); the others are not.
	if pv.New != 151 || pv.NewBytes != 1 || pv.Changed != 1 || pv.Unchanged != 2 || pv.Extra != 1 || !pv.Deletes {
		t.Errorf("preview = %+v", *pv)
	}
	if len(pv.NewSample) != runfile.SampleSize || pv.NewSample[0] != "file.txt" || pv.ExtraSample[0] != "only/there.txt" {
		t.Errorf("samples: %d new, first %q, extra %v", len(pv.NewSample), pv.NewSample[0], pv.ExtraSample)
	}
	if st.Items[3].Preview == nil || st.Items[3].Preview.Deletes {
		t.Errorf("a destination that keeps files: %+v", st.Items[3].Preview)
	}
	if st.Items[4].Note == "" || st.Items[5].Preview != nil {
		t.Errorf("restic items: %+v", st.Items[4:])
	}
	if len(e.verbs("restic")) != 0 {
		t.Errorf("a preview called restic: %v", e.verbs("restic"))
	}
	// Every call that could write is a dry run with the run's own arguments.
	for _, c := range e.calls() {
		if c.Args[0] == "lsd" {
			continue
		}
		joined := strings.Join(c.Args, " ")
		if !strings.Contains(joined, "--dry-run") {
			t.Errorf("a preview ran rclone without --dry-run: %s", joined)
		}
		for _, w := range []string{"--filter - /cache/**", "--size-only", "--retries 1"} {
			if !strings.Contains(joined, w) {
				t.Errorf("missing %q in: %s", w, joined)
			}
		}
	}
	if got := strings.Join(e.verbs("rclone"), " "); got != "lsd sync lsd copy" {
		t.Errorf("rclone calls = %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(e.spec.WorkDir, "*")); len(left) != 0 {
		t.Errorf("files left in the work directory: %v", left)
	}
}

// A preview is asked for twice, in the spec and on the command line. One
// without the other is refused, so a preview's spec is never copied.
func TestPreviewNeedsBoth(t *testing.T) {
	for _, tc := range []struct{ spec, flag bool }{{true, false}, {false, true}} {
		e := newEnv(t)
		e.spec.Preview = tc.spec
		e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
		args := []string{"--spec", e.writeSpec(), "--state", e.state}
		if tc.flag {
			args = append(args, "--preview")
		}
		var stderr bytes.Buffer
		if code := realMain(args, &stderr); code != exitUsage {
			t.Errorf("spec %v, flag %v: exit %d, want 2", tc.spec, tc.flag, code)
		}
		if len(e.calls()) != 0 {
			t.Errorf("spec %v, flag %v: a tool was called: %v", tc.spec, tc.flag, e.calls())
		}
	}
}

func TestPanicIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
	r := &runner{spec: e.spec, log: &lineLog{w: &bytes.Buffer{}}}
	calls := 0
	r.now = func() time.Time {
		if calls++; calls == 3 {
			panic("boom")
		}
		return time.Now()
	}
	r.track = newTracker(e.state, initialState(e.spec, time.Now()), r.log)
	if got := r.run(context.Background()); got != runfile.ResultFailure {
		t.Errorf("result = %q", got)
	}
	st := e.readState()
	if st.Result != runfile.ResultFailure || !strings.Contains(st.Error, "runner panic: boom") {
		t.Errorf("state = %q / %q", st.Result, st.Error)
	}
	for _, it := range st.Items {
		if it.Result == runfile.ResultPending || it.Result == runfile.ResultRunning {
			t.Errorf("item left %s after a panic", it.Result)
		}
	}
}

func TestBadSpec(t *testing.T) {
	e := newEnv(t)
	e.spec.Version = 99
	spec := e.writeSpec()
	var stderr bytes.Buffer
	if code := realMain([]string{"--spec", spec, "--state", e.state}, &stderr); code != exitUsage {
		t.Errorf("exit code %d, want 2", code)
	}
	if _, err := os.Stat(spec); !os.IsNotExist(err) {
		t.Errorf("an unreadable spec still holds secrets and must be deleted")
	}
	if code := realMain(nil, &stderr); code != exitUsage {
		t.Errorf("no arguments: exit code %d, want 2", code)
	}
}

// startRunner starts the runner as its own process, with a copy that hangs.
func startRunner(t *testing.T, e *env) *exec.Cmd {
	t.Helper()
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
	self, _ := os.Executable()
	cmd := exec.Command(self, "--spec", e.writeSpec(), "--state", e.state)
	cmd.Env = append(os.Environ(), "RUNNER_MAIN=1", "FAKE_RCLONE=hang")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	// Wait until the copy is in flight and has reported progress.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if st, err := runfile.ReadState(e.state); err == nil && st.Progress != nil && st.Progress.BytesDone == 100 {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatal("the copy never reported progress")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSigtermEndsCancelled(t *testing.T) {
	e := newEnv(t)
	cmd := startRunner(t, e)
	if st := e.readState(); st.Final() || st.Items[1].Result != runfile.ResultRunning {
		t.Fatalf("mid-run state: result %q, folder %q", st.Result, st.Items[1].Result)
	}
	start := time.Now()
	cmd.Process.Signal(syscall.SIGTERM)
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != exitFailed {
		t.Errorf("runner exit = %v, want exit code 1", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("stopping took %s; the child was not signalled", d)
	}
	st := e.readState()
	want(t, st, runfile.ResultCancelled, "bucket=cancelled bucket/good=cancelled bucket/bad=cancelled")
}

func TestSigtermEndsAPreviewCancelled(t *testing.T) {
	e := newEnv(t)
	e.spec.Preview = true
	e.spec.Destinations = []runfile.Destination{e.rclone(1, "bucket", "r:bucket")}
	self, _ := os.Executable()
	cmd := exec.Command(self, "--spec", e.writeSpec(), "--state", e.state, "--preview")
	cmd.Env = append(os.Environ(), "RUNNER_MAIN=1", "FAKE_RCLONE=hang")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	deadline := time.Now().Add(20 * time.Second)
	for {
		if st, err := runfile.ReadState(e.state); err == nil && st.Progress != nil && st.Progress.FilesDone > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the preview never reported progress")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("stopping took %s", d)
	}
	st := e.readState()
	want(t, st, runfile.ResultCancelled, "bucket=cancelled bucket/good=cancelled bucket/bad=cancelled")
	if left, _ := filepath.Glob(filepath.Join(e.spec.WorkDir, "*")); len(left) != 0 {
		t.Errorf("files left in the work directory: %v", left)
	}
}

func TestKillLeavesReadableState(t *testing.T) {
	e := newEnv(t)
	cmd := startRunner(t, e)
	cmd.Process.Kill()
	cmd.Wait()
	st := e.readState()
	if st.Final() {
		t.Errorf("a killed runner cannot have written a final result, got %q", st.Result)
	}
	if got := results(st); got != "bucket=running bucket/good=running bucket/bad=pending" {
		t.Errorf("items after kill -9: %s", got)
	}
	if st.Progress == nil || st.Progress.Folder != "good" || st.Progress.BytesTotal != 300 {
		t.Errorf("progress after kill -9: %+v", st.Progress)
	}
	// No half-written temp files next to it.
	left, _ := filepath.Glob(filepath.Join(e.dir, ".state.json.*"))
	if len(left) > 1 {
		t.Errorf("%d temp state files left behind", len(left))
	}
}

// The runner's promise: nothing outside the standard library.
func TestStandardLibraryOnly(t *testing.T) {
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if f := strings.Fields(line); len(f) > 0 && (f[0] == "require" || f[0] == "replace") {
			t.Errorf("go.mod must have no dependencies, found: %s", line)
		}
	}
	if _, err := os.Stat("go.sum"); err == nil {
		t.Errorf("go.sum exists; the module must have no dependencies")
	}
}

func TestBwlimitKiB(t *testing.T) {
	for in, want := range map[string]int64{"4M": 4096, "512k": 512, "100": 100, "1G": 1 << 20, "2048B": 2} {
		if got, ok := bwlimitKiB(in); !ok || got != want {
			t.Errorf("bwlimitKiB(%q) = %d, %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "off", "08:00,512k 12:00,10M", "fast"} {
		if _, ok := bwlimitKiB(in); ok {
			t.Errorf("bwlimitKiB(%q) should not convert", in)
		}
	}
}
