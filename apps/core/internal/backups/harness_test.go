package backups

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"homelab-ngx.com/core/backup-runner/runfile"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/notifications/notifytest"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
	"homelab-ngx.com/core/internal/systemd"
)

// The tests' folders live under the test TMPDIR, which is under /tmp; the
// rule that keeps /tmp out of a backup is tested on its own
// (TestPathSourceRules).
func init() { pathDenied = []string{"/proc", "/sys", "/dev", "/run", "/nix"} }

type fakePinger struct {
	mu    sync.Mutex
	pings []string
}

func (f *fakePinger) Ping(_ context.Context, url string) {
	f.mu.Lock()
	f.pings = append(f.pings, url)
	f.mu.Unlock()
}

func (f *fakePinger) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pings...)
}

// fakeStacks is the stacks as backups sees them.
type fakeStacks struct {
	mu       sync.Mutex
	folders  []StackFolders
	err      error
	prepErr  map[string]error // by stack
	prepared []string         // "stack/unit"
	reads    int
}

func (f *fakeStacks) BackupFolders(context.Context) ([]StackFolders, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.folders, f.err
}

func (f *fakeStacks) RunPrepare(_ context.Context, stack, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, stack+"/"+unit)
	return f.prepErr[stack]
}

type harness struct {
	t      *testing.T
	svc    *Service
	fake   *cmdrunnertest.Fake
	st     *store.Store
	q      *store.Queries
	hc     *fakePinger
	stacks *fakeStacks
	notify *notifications.Notifier
	dir    string // holds every directory of the service
	data   string // a folder to back up

	mu       sync.Mutex
	now      time.Time
	specs    []*runfile.Spec                    // every spec a launch carried
	launches []cmdrunner.Cmd                    // every systemd-run call
	state    func(*runfile.Spec) *runfile.State // what the "runner" leaves; nil leaves nothing
	active   bool                               // what systemctl show says about the unit
	snapErr  error
	snaps    int
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func newKeyFile(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(p, []byte("# public key: "+id.Recipient().String()+"\n"+id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, _ := storetest.Open(t)
	dir := t.TempDir()
	h := &harness{
		t: t, fake: cmdrunnertest.NewFake(), st: st, q: st.Queries(), hc: &fakePinger{}, stacks: &fakeStacks{},
		dir: dir, data: filepath.Join(dir, "data"), now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC),
	}
	for _, d := range []string{h.data, filepath.Join(dir, "dbsnap"), filepath.Join(dir, "stacks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		Runner: "/nix/store/x-runner/bin/homelab-backup-runner", KeyFile: newKeyFile(t),
		StateDir: filepath.Join(dir, "state"), RunDir: filepath.Join(dir, "run"), CacheDir: filepath.Join(dir, "cache"),
		DBBackupDir: filepath.Join(dir, "dbsnap"), StacksDir: filepath.Join(dir, "stacks"),
		Snapshot: func(context.Context) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.snaps++
			return h.snapErr
		},
		PoolMounts: func() []string { return []string{"/pool"} },
	}
	h.svc = New(cfg, systemd.New(h.fake, systemd.WithPollInterval(time.Millisecond)), h.fake, st, h.stacks, nil, h.hc,
		slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	h.svc.pollEvery, h.svc.retryEvery = time.Millisecond, time.Millisecond
	h.svc.now = h.clock
	h.svc.started = h.clock()

	// The runner: record the spec, delete it as the real one does, and leave
	// the state the test asked for.
	h.fake.On("systemd-run").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		specPath, statePath := argAfter(c.Args, "--spec"), argAfter(c.Args, "--state")
		spec, err := runfile.ReadSpec(specPath)
		if err != nil {
			return cmdrunner.Result{}, err
		}
		_ = os.Remove(specPath)
		h.mu.Lock()
		h.specs = append(h.specs, spec)
		h.launches = append(h.launches, c)
		mk := h.state
		h.mu.Unlock()
		if mk != nil {
			if st := mk(spec); st != nil {
				if err := runfile.WriteState(statePath, st); err != nil {
					return cmdrunner.Result{}, err
				}
			}
		}
		return cmdrunner.Result{}, nil
	})
	h.fake.On("systemctl", "show").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		unit := ""
		for _, a := range c.Args {
			if strings.HasSuffix(a, ".service") {
				unit = a
			}
		}
		h.mu.Lock()
		state := "inactive"
		if h.active {
			state = "active"
		}
		h.mu.Unlock()
		return cmdrunner.Result{Stdout: "Id=" + unit + "\nActiveState=" + state + "\n"}, nil
	})
	t.Cleanup(h.svc.Wait)
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *harness) setState(fn func(*runfile.Spec) *runfile.State) {
	h.mu.Lock()
	h.state = fn
	h.mu.Unlock()
}

func (h *harness) setActive(v bool) {
	h.mu.Lock()
	h.active = v
	h.mu.Unlock()
}

func (h *harness) lastSpec() *runfile.Spec {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.specs) == 0 {
		h.t.Fatal("no run was launched")
	}
	return h.specs[len(h.specs)-1]
}

func (h *harness) launchCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.launches)
}

// withNotifier gives the service a notifier that posts to a fake webhook
// and returns a reader of the titles it pushed.
func (h *harness) withNotifier() func() []string {
	h.t.Helper()
	var mu sync.Mutex
	var titles []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		titles = append(titles, notifytest.Title(r))
		mu.Unlock()
	}))
	h.t.Cleanup(srv.Close)
	h.notify = notifications.New(notifytest.Config(h.t, srv.URL), nil, nil)
	h.svc.notify = h.notify
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), titles...)
	}
}

// open reports whether the problem key is open.
func (h *harness) open(key string) bool {
	h.t.Helper()
	ps, err := h.notify.OpenProblems(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range ps {
		if p.Key == key {
			return true
		}
	}
	return false
}

const (
	s3Secret   = "s3-SECRET-key-value"
	rcloneTok  = "rclone-SECRET-token"
	resticPass = "restic-SECRET-password"
	resticEnv  = "restic-SECRET-env-value"
)

func s3Input(name string) DestinationInput {
	return DestinationInput{Name: name, Kind: KindS3, DeleteRemoved: true, S3: &S3Input{
		Endpoint: "https://s3.example.com", Region: "eu", Bucket: "bucket", Prefix: "/host/", AccessKeyId: "AKID",
		SecretAccessKey: s3Secret,
	}}
}

func rcloneInput(name, section, path string) DestinationInput {
	return DestinationInput{Name: name, Kind: KindRclone, Rclone: &RcloneInput{Config: section, Path: path}}
}

func resticInput(name, repo, password string) DestinationInput {
	env := map[string]string{"AWS_SECRET_ACCESS_KEY": resticEnv}
	return DestinationInput{Name: name, Kind: KindRestic, Keep: Keep{Daily: 7, Weekly: 4},
		Restic: &ResticInput{Repository: repo, Password: password, Env: &env}}
}

func (h *harness) dest(in DestinationInput) Destination {
	h.t.Helper()
	c, err := h.svc.CreateDestination(context.Background(), in)
	if err != nil {
		h.t.Fatalf("create destination %s: %v", in.Name, err)
	}
	return c.Destination
}

// backup creates a backup of the harness's data folder to one new S3
// destination, scheduled nightly.
func (h *harness) backup(name string) Backup {
	h.t.Helper()
	d := h.dest(s3Input(name + " bucket"))
	b, err := h.svc.Create(context.Background(), BackupInput{
		Name: name, Schedule: "0 4 * * *", DestinationIds: []int64{d.Id},
		Sources: []SourceInput{{Kind: SourcePath, Path: h.data}},
	})
	if err != nil {
		h.t.Fatalf("create backup %s: %v", name, err)
	}
	return b
}

// finalState is a runner's last word: every folder of the spec on every
// destination with result, plus the destination items.
func finalState(spec *runfile.Spec, result, folderResult string) *runfile.State {
	now := time.Date(2026, 11, 16, 12, 0, 5, 0, time.UTC)
	st := &runfile.State{Version: runfile.Version, RunID: spec.RunID, StartedAt: now, UpdatedAt: now, FinishedAt: &now, Result: result}
	for _, d := range spec.Destinations {
		st.Items = append(st.Items, runfile.Item{
			Kind: runfile.ItemDestination, DestinationID: d.ID, Destination: d.Name, Result: runfile.ResultSuccess,
			StartedAt: &now, FinishedAt: &now,
		})
		for _, f := range spec.Folders {
			it := runfile.Item{
				Kind: runfile.ItemFolder, DestinationID: d.ID, Destination: d.Name, Label: f.Label, Path: f.Path,
				Result: folderResult, Files: 3, Bytes: 1000, StartedAt: &now, FinishedAt: &now, Note: f.Note,
			}
			if f.Fail != "" {
				it.Result, it.Error, it.Files, it.Bytes = runfile.ResultFailure, f.Fail, 0, 0
			} else if folderResult != runfile.ResultSuccess {
				it.Error = "copy failed"
			}
			st.Items = append(st.Items, it)
		}
	}
	return st
}

func succeed(spec *runfile.Spec) *runfile.State {
	return finalState(spec, runfile.ResultSuccess, runfile.ResultSuccess)
}

// run triggers the backup and waits for the run to be recorded.
func (h *harness) run(id int64) store.BackupRun {
	h.t.Helper()
	runID, err := h.svc.Trigger(context.Background(), id, TriggerManual)
	if err != nil {
		h.t.Fatalf("trigger: %v", err)
	}
	h.svc.Wait()
	return h.runRow(runID)
}

func (h *harness) runRow(id int64) store.BackupRun {
	h.t.Helper()
	r, err := h.q.GetBackupRun(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) items(runID int64) []store.BackupRunItem {
	h.t.Helper()
	it, err := h.q.ListBackupRunItems(context.Background(), runID)
	if err != nil {
		h.t.Fatal(err)
	}
	return it
}

func invalidCode(err error) string {
	var inv *InvalidError
	if errors.As(err, &inv) {
		return inv.Code
	}
	return ""
}

// call registers the backups operations on a real operator API and answers
// one request, decoding a 2xx body into out.
func call(t *testing.T, h *harness, method, path, body string, out any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	NewHTTP(h.svc).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code < 300 && out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, w.Body)
		}
	}
	return w.Code, m
}

type (
	cmdrunnerCmd    = cmdrunner.Cmd
	cmdrunnerResult = cmdrunner.Result
)
