package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/configstore"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/storage/bcachefs"
	"homelab-ngx.com/core/internal/store/storetest"
)

const poolUUID = "f6f35d64-0b1c-4a6e-9a47-7a1d3c2e9b10"

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func lsblk(mounted bool) string {
	mp := "null"
	if mounted {
		mp = `"/var/mnt/pool"`
	}
	dev := func(n string, size int, mount string) string {
		return fmt.Sprintf(`{"name":%q,"path":"/dev/%s","size":%d,"type":"disk","model":"M","serial":"s","fstype":"bcachefs","mountpoint":%s,"label":null}`, n, n, size, mount)
	}
	return `{"blockdevices":[` + strings.Join([]string{
		dev("sda", 1000, "null"), dev("sdb", 4000, mp), dev("sdc", 4000, "null"), dev("sdd", 4000, "null"),
	}, ",") + `]}`
}

func superFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("bcachefs", "testdata", "show-super.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type harness struct {
	svc  *Service
	fake *cmdrunnertest.Fake
	cfg  *config.Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	doc := map[string]any{
		"version":       config.SupportedVersion,
		"db":            map[string]any{"snapshot_schedule": "15 3 * * *", "snapshot_keep": 7, "retention_days": 90},
		"core_services": []string{},
	}
	data, _ := json.Marshal(doc)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o444); err != nil {
		t.Fatal(err)
	}
	st, _ := storetest.Open(t)
	cfg := config.NewManager(path, configstore.New(st))
	if err := cfg.Load(ctx); err != nil {
		t.Fatal(err)
	}
	fake := cmdrunnertest.NewFake()
	fake.On("bcachefs", "show-super", "/dev/sda").Reply(superFixture(t))
	fake.On("smartctl").Reply(`{"model_name":"Disk Model X"}`)
	svc := New(bcachefs.New(fake), cfg,
		slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	svc.discoverTimeout, svc.retryInterval = 40*time.Millisecond, 5*time.Millisecond
	svc.mkdir = func(string) error { return nil }
	return &harness{svc: svc, fake: fake, cfg: cfg}
}

func (h *harness) pool(t *testing.T) config.Pool {
	t.Helper()
	p, ok := h.cfg.Current().Pool(poolUUID)
	if !ok {
		t.Fatal("pool not stored")
	}
	return p
}

func TestStatusShapeNamesAndCache(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("lsblk").Reply(lsblk(true))
	h.fake.On("bcachefs", "subvolume", "list", "/var/mnt/pool").Reply("Path ID Created Flags Size\nmedia 2 2026-05-27 23:47 -\n")
	ctx := context.Background()

	st, err := h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Pools) != 1 || st.Unassigned == nil || len(st.Unassigned) != 0 {
		t.Fatalf("status = %+v", st)
	}
	p := st.Pools[0]
	if p.Uuid != poolUUID || p.Name != "pool" || p.State != "mounted" || p.Mountdir != "/var/mnt/pool" || p.AutoMount || len(p.Disks) != 4 {
		t.Fatalf("pool = %+v", p)
	}
	if len(st.Subvolumes) != 1 || st.Subvolumes[0].Path != "/var/mnt/pool/media" || st.Subvolumes[0].UsedBytes != -1 {
		t.Fatalf("subvolumes = %+v", st.Subvolumes)
	}

	// TTL cache: a second call does not re-enumerate; a mutation drops it.
	before := len(h.fake.CallsMatching("lsblk"))
	if _, err := h.svc.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(h.fake.CallsMatching("lsblk")); got != before {
		t.Fatalf("cached call ran lsblk again (%d -> %d)", before, got)
	}
	h.fake.On("bcachefs", "subvolume", "create").Reply("")
	if err := h.svc.CreateSubvolume(ctx, "/var/mnt/pool/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(h.fake.CallsMatching("lsblk")); got == before {
		t.Fatal("create subvolume must invalidate the cache")
	}
}

func TestStatusTTLExpiry(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("lsblk").Reply(lsblk(false))
	now := time.Now()
	h.svc.now = func() time.Time { return now }
	ctx := context.Background()
	_, _ = h.svc.Status(ctx)
	n := len(h.fake.CallsMatching("lsblk"))
	now = now.Add(4 * time.Second)
	_, _ = h.svc.Status(ctx)
	if len(h.fake.CallsMatching("lsblk")) != n {
		t.Fatal("cache should still be fresh at 4s")
	}
	now = now.Add(2 * time.Second)
	_, _ = h.svc.Status(ctx)
	if len(h.fake.CallsMatching("lsblk")) == n {
		t.Fatal("cache should have expired at 6s, before the page's next 10s poll")
	}
}

// blockingLsblk makes the first lsblk call signal started and wait for
// release before answering with first; later lsblk calls answer with rest.
func blockingLsblk(h *harness, first cmdrunner.Result, firstErr error, rest string) (started, release chan struct{}) {
	started, release = make(chan struct{}), make(chan struct{})
	h.fake.On("lsblk").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		close(started)
		<-release
		return first, firstErr
	}).Reply(rest)
	return started, release
}

// statusAll runs n concurrent Status calls and returns their results once
// all have joined the compute and release is closed. It runs inside a
// synctest bubble, with started and release made there too.
func statusAll(h *harness, n int, started, release chan struct{}) ([]StorageStatus, []error) {
	out, errs := make([]StorageStatus, n), make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); out[i], errs[i] = h.svc.Status(context.Background()) }()
	}
	<-started
	synctest.Wait() // every caller is waiting on the shared compute
	close(release)
	wg.Wait()
	return out, errs
}

func TestStatusConcurrentColdCallsComputeOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var out []StorageStatus
	var errs []error
	synctest.Test(t, func(*testing.T) {
		started, release := blockingLsblk(h, cmdrunner.Result{Stdout: lsblk(false)}, nil, lsblk(false))
		out, errs = statusAll(h, 10, started, release)
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(out[i].Pools) != 1 || !reflect.DeepEqual(out[i], out[0]) {
			t.Fatalf("call %d got %+v, call 0 got %+v", i, out[i], out[0])
		}
	}
	concurrent := len(h.fake.CallsMatching("lsblk"))

	// One compute on its own, for the per-compute lsblk count.
	h.svc.invalidate()
	if _, err := h.svc.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if single := len(h.fake.CallsMatching("lsblk")) - concurrent; concurrent != single {
		t.Fatalf("10 concurrent cold calls ran lsblk %d times, one compute runs it %d times", concurrent, single)
	}
}

func TestStatusErrorReachesAllWaitersAndIsNotCached(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fail := &cmdrunner.Error{Result: cmdrunner.Result{ExitCode: 1}, Err: fmt.Errorf("lsblk: boom")}
	var errs []error
	synctest.Test(t, func(*testing.T) {
		started, release := blockingLsblk(h, cmdrunner.Result{ExitCode: 1}, fail, lsblk(false))
		_, errs = statusAll(h, 10, started, release)
	})
	for i, err := range errs {
		if err == nil {
			t.Fatalf("call %d: expected the compute error", i)
		}
	}
	st, err := h.svc.Status(context.Background())
	if err != nil || len(st.Pools) != 1 {
		t.Fatalf("after an error the next call must recompute: %+v %v", st, err)
	}
}

func TestStatusFirstCallerCancelDoesNotFailWaiters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	synctest.Test(t, func(t *testing.T) {
		started, release := blockingLsblk(h, cmdrunner.Result{Stdout: lsblk(false)}, nil, lsblk(false))
		ctx, cancel := context.WithCancel(context.Background())
		firstDone := make(chan struct{})
		go func() { _, _ = h.svc.Status(ctx); close(firstDone) }()
		<-started

		out, errs := make([]StorageStatus, 3), make([]error, 3)
		var wg sync.WaitGroup
		for i := range out {
			wg.Add(1)
			go func() { defer wg.Done(); out[i], errs[i] = h.svc.Status(context.Background()) }()
		}
		synctest.Wait() // the waiters have joined the compute
		cancel()
		close(release)
		wg.Wait()
		<-firstDone
		for i := range out {
			if errs[i] != nil || len(out[i].Pools) != 1 {
				t.Fatalf("waiter %d: %+v %v", i, out[i], errs[i])
			}
		}
	})
}

func TestStatusInvalidateDuringComputeDropsStaleResult(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// The in-flight compute sees the pool unmounted; after the mount, it is.
	started, release := blockingLsblk(h, cmdrunner.Result{Stdout: lsblk(false)}, nil, lsblk(true))
	h.fake.On("bcachefs", "subvolume", "list", "/var/mnt/pool").Reply("")
	h.fake.On("mount").Reply("")
	staleDone := make(chan struct{})
	go func() { _, _ = h.svc.Status(context.Background()); close(staleDone) }()
	<-started

	if err := h.svc.Mount(context.Background(), []string{"/dev/sdb"}, "pool"); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.svc.Status(context.Background())
	if err != nil || len(fresh.Pools) != 1 || fresh.Pools[0].State != "mounted" {
		t.Fatalf("call after invalidate must recompute: %+v %v", fresh, err)
	}
	close(release)
	<-staleDone

	got, err := h.svc.Status(context.Background())
	if err != nil || len(got.Pools) != 1 || got.Pools[0].State != "mounted" {
		t.Fatalf("stale in-flight result was cached: %+v %v", got, err)
	}
}

func TestStatusUsesStoredPoolName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("lsblk").Reply(lsblk(false))
	ctx := context.Background()
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", Name: "Main Pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	st, err := h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := st.Pools[0]; p.Name != "Main Pool" || !p.AutoMount {
		t.Fatalf("pool = %+v", p)
	}
}

func TestStatusDiscoveryFailureIsError(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("lsblk").Fail("lsblk: boom")
	if _, err := h.svc.Status(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestMountRemembersPool(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("lsblk").Reply(lsblk(true))
	h.fake.On("mount").Reply("")
	ctx := context.Background()
	if err := h.svc.Mount(ctx, []string{"/dev/sdb", "/dev/sda"}, "pool"); err != nil {
		t.Fatal(err)
	}
	if p := h.pool(t); !p.AutoMount || p.Mountpoint != "/var/mnt/pool" {
		t.Fatalf("pool = %+v", p)
	}
}

// chattrs are the chattr calls the fake saw, as "flag path".
func chattrs(h *harness) []string {
	var out []string
	for _, c := range h.fake.Calls() {
		if c.Name == "chattr" {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

func TestMountNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("no name is pool, under the root", func(t *testing.T) {
		h := newHarness(t)
		h.fake.On("lsblk").Reply(lsblk(true))
		h.fake.On("mount").Reply("")
		if err := h.svc.Mount(ctx, []string{"/dev/sdb"}, ""); err != nil {
			t.Fatal(err)
		}
		if p := h.pool(t); p.Mountpoint != "/var/mnt/pool" {
			t.Fatalf("pool = %+v", p)
		}
	})
	t.Run("no name is the name it had", func(t *testing.T) {
		h := newHarness(t)
		h.fake.On("lsblk").Reply(lsblk(false))
		h.fake.On("mount").Reply("")
		if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/tank"}); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Mount(ctx, []string{"/dev/sdb"}, ""); err != nil {
			t.Fatal(err)
		}
		var mounts []string
		for _, c := range h.fake.Calls() {
			if c.Name == "mount" {
				mounts = append(mounts, c.Args[len(c.Args)-1])
			}
		}
		if !reflect.DeepEqual(mounts, []string{"/var/mnt/tank"}) {
			t.Fatalf("mounted at %v", mounts)
		}
	})
	t.Run("a name is one directory", func(t *testing.T) {
		h := newHarness(t)
		h.fake.On("lsblk").Reply(lsblk(false))
		for _, name := range []string{"Pool", "a/b", "..", "-x", "/var/mnt/pool", strings.Repeat("a", 32)} {
			if err := h.svc.Mount(ctx, []string{"/dev/sdb"}, name); !errors.Is(err, ErrMountName) {
				t.Errorf("%q: err = %v", name, err)
			}
		}
	})
	t.Run("another pool's name is refused", func(t *testing.T) {
		h := newHarness(t)
		h.fake.On("lsblk").Reply(lsblk(false))
		if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: "another", Mountpoint: "/var/mnt/pool"}); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Mount(ctx, []string{"/dev/sdb"}, "pool"); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestUnmountedDirectoryIsGuarded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("a failed mount", func(t *testing.T) {
		h := newHarness(t)
		h.fake.On("lsblk").Reply(lsblk(false))
		h.fake.On("chattr").Reply("")
		h.fake.On("mount").Fail("wrong fs type")
		if err := h.svc.Mount(ctx, []string{"/dev/sdb"}, "pool"); err == nil {
			t.Fatal("expected error")
		}
		if got, want := chattrs(h), []string{"-i /var/mnt/pool", "+i /var/mnt/pool"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("chattr calls = %v, want %v", got, want)
		}
	})
	t.Run("an unmount", func(t *testing.T) {
		h := newHarness(t)
		h.fake.On("chattr").Reply("")
		h.fake.On("umount").Reply("")
		if err := h.svc.Unmount(ctx, "/var/mnt/pool"); err != nil {
			t.Fatal(err)
		}
		if got, want := chattrs(h), []string{"+i /var/mnt/pool"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("chattr calls = %v, want %v", got, want)
		}
	})
}

func TestAutoMountSkipsPoolOutsideRoot(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	h.fake.On("lsblk").Reply(lsblk(false))
	h.fake.On("mount").Reply("")
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	h.svc.AutoMount(ctx)
	for _, c := range h.fake.Calls() {
		if c.Name == "mount" {
			t.Fatalf("mounted outside the root: %v", c.Args)
		}
	}
}

func TestMountFailureLeavesConfigAlone(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("mount").Fail("wrong fs type")
	if err := h.svc.Mount(context.Background(), []string{"/dev/sdb"}, "pool"); err == nil {
		t.Fatal("expected error")
	}
	if len(h.cfg.Current().Pools) != 0 {
		t.Fatal("failed mount must not be remembered")
	}
}

func TestUnmountRecordsAutoMountFalse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", Name: "keep", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	h.fake.On("umount").Reply("")
	if err := h.svc.Unmount(ctx, "/var/mnt/pool"); err != nil {
		t.Fatal(err)
	}
	if p := h.pool(t); p.AutoMount || p.Name != "keep" {
		t.Fatalf("pool = %+v (entry must be kept, auto_mount off)", p)
	}
}

func TestUnmountUnknownPoolIsNoop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("systemctl", "is-active").Fail("inactive")
	h.fake.On("umount").Reply("")
	if err := h.svc.Unmount(context.Background(), "/mnt/other"); err != nil {
		t.Fatal(err)
	}
	if len(h.cfg.Current().Pools) != 0 {
		t.Fatal("no pool entry should appear")
	}
}

func TestSetAutoMount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SetAutoMount(ctx, "nope", true); err == nil {
		t.Fatal("unknown pool must error")
	}
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", AutoMount: false}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetAutoMount(ctx, poolUUID, true); err != nil {
		t.Fatal(err)
	}
	if p := h.pool(t); !p.AutoMount || p.Mountpoint != "/var/mnt/pool" {
		t.Fatalf("pool = %+v", p)
	}
}

func TestAutoMountMountsStoredPool(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	h.fake.On("lsblk").Reply(lsblk(false))
	h.fake.On("chattr").Reply("")
	h.fake.On("mount").Reply("")
	h.svc.AutoMount(ctx)

	calls := h.fake.Calls()
	var order []string
	for _, c := range calls {
		if c.Name == "chattr" || c.Name == "mount" {
			order = append(order, c.Name+" "+strings.Join(c.Args, " "))
		}
	}
	want := []string{"chattr -i /var/mnt/pool", "mount -t bcachefs /dev/sda:/dev/sdb:/dev/sdc:/dev/sdd /var/mnt/pool"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		// device order follows lsblk
		t.Fatalf("calls = %v, want %v", order, want)
	}
}

func TestAutoMountSkipsAlreadyMountedAndNoAutoPools(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	h.fake.On("lsblk").Reply(lsblk(true))
	h.svc.AutoMount(ctx) // no stored pools: nothing runs
	if len(h.fake.Calls()) != 0 {
		t.Fatalf("no pools stored but commands ran: %v", h.fake.Calls())
	}
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	h.fake.On("mount").Reply("")
	h.svc.AutoMount(ctx)
	if len(h.fake.CallsMatching("mount")) != 0 {
		t.Fatal("already mounted pool must not be mounted again")
	}
}

func TestAutoMountRetriesUntilDiscovered(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	h.fake.On("lsblk").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		if n.Add(1) < 3 {
			return cmdrunner.Result{Stdout: `{"blockdevices":[]}`}, nil // disks still enumerating
		}
		return cmdrunner.Result{Stdout: lsblk(false)}, nil
	})
	h.fake.On("chattr").Reply("")
	h.fake.On("mount").Reply("")
	h.svc.AutoMount(ctx)
	if n.Load() < 3 || len(h.fake.CallsMatching("mount")) != 1 {
		t.Fatalf("lsblk ran %d times, mounts %d", n.Load(), len(h.fake.CallsMatching("mount")))
	}
}

func TestAutoMountGivesUpAfterDiscoveryTimeout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.cfg.UpsertPool(ctx, config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	h.fake.On("lsblk").Reply(`{"blockdevices":[]}`)
	done := make(chan struct{})
	go func() { h.svc.AutoMount(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AutoMount did not give up")
	}
	if len(h.fake.CallsMatching("mount")) != 0 {
		t.Fatal("nothing to mount")
	}
}

func TestAutoMountStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.svc.discoverTimeout = time.Hour
	if _, err := h.cfg.UpsertPool(context.Background(), config.Pool{UUID: poolUUID, Mountpoint: "/var/mnt/pool", AutoMount: true}); err != nil {
		t.Fatal(err)
	}
	polled := make(chan struct{})
	var once sync.Once
	h.fake.On("lsblk").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		once.Do(func() { close(polled) })
		return cmdrunner.Result{Stdout: `{"blockdevices":[]}`}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.svc.AutoMount(ctx); close(done) }()
	<-polled // AutoMount is in its retry loop
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AutoMount ignored cancellation")
	}
}

func TestSubvolumeUsage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("ionice", "-c3", "nice", "-n19", "bcdu", "--json", "-d", "1", "/var/mnt/pool").
		Reply(`{"folders":[{"path":"/var/mnt/pool","total":50},{"path":"/var/mnt/pool/a","total":42}]}`)
	got := h.svc.SubvolumeUsage(context.Background(), []string{"/var/mnt/pool/a", "/var/mnt/pool/b"})
	if got["/var/mnt/pool/a"] != 42 || got["/var/mnt/pool/b"] != -1 {
		t.Fatalf("usage = %v", got)
	}
}

func TestFolderSizesPersistAndMerge(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st, _ := storetest.Open(t)
	h.svc.UseStore(st)
	h.fake.On("lsblk").Reply(lsblk(true))
	h.fake.On("bcachefs", "subvolume", "list", "/var/mnt/pool").Reply("Path ID Created Flags Size\nmedia 2 2026-05-27 23:47 -\n")
	h.fake.On("ionice", "-c3", "nice", "-n19", "bcdu", "--json", "-d", "0", "/var/mnt/pool/media").
		Reply(`{"folders":[{"path":"/var/mnt/pool/media","total":42}]}`)
	ctx := context.Background()

	h.svc.measureStale(ctx, sizeInterval)
	got, err := h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sv := got.Subvolumes[0]; sv.UsedBytes != 42 || sv.MeasuredAt == nil {
		t.Fatalf("subvolume = %+v", sv)
	}
	// A fresh measurement is not repeated.
	h.svc.measureStale(ctx, sizeInterval)
	if n := len(h.fake.CallsMatching("ionice")); n != 1 {
		t.Fatalf("bcdu ran %d times, want 1", n)
	}

	// A restarted daemon reads the stored sizes back.
	h2 := newHarness(t)
	h2.svc.UseStore(st)
	h2.fake.On("lsblk").Reply(lsblk(true))
	h2.fake.On("bcachefs", "subvolume", "list", "/var/mnt/pool").Reply("Path ID Created Flags Size\nmedia 2 2026-05-27 23:47 -\n")
	h2.svc.loadSizes(ctx)
	got, err = h2.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subvolumes[0].UsedBytes != 42 {
		t.Fatalf("restored subvolume = %+v", got.Subvolumes[0])
	}
}

func TestDiskHealthKeepsLastReadingWhileAsleep(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.fake.On("lsblk").Reply(lsblk(false))
	reading := `{"smart_status":{"passed":true},"temperature":{"current":31},"power_on_time":{"hours":26578},` +
		`"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":2}},{"id":197,"raw":{"value":1}},{"id":198,"raw":{"value":1}}]}}`
	asleep := `{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode, exit(2)"}]}}`
	// The first pass reads all four disks; after that they are asleep.
	h.fake.On("smartctl", "-j", "-n", "standby").Reply(reading).Reply(reading).Reply(reading).Reply(reading).FailCode(2, asleep)
	ctx := context.Background()

	h.svc.checkHealth(ctx)
	h.svc.checkHealth(ctx) // every disk asleep now
	got, err := h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got.Pools[0].Disks {
		if d.Health == nil {
			t.Fatalf("%s lost its reading", d.Path)
		}
	}
	if hd := got.Pools[0].Disks[0].Health; !hd.Passed || hd.TemperatureC != 31 || hd.PowerOnHours != 26578 || hd.BadSectors != 3 {
		t.Fatalf("health = %+v", hd)
	}
}

// ---- HTTP ----

// serveAPI registers the storage operations on a real huma API and returns
// a function that serves one request through it.
func serveAPI(t *testing.T, h *harness) func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	NewHTTP(h.svc).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	return func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}
}

func TestHTTPProblems(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	do := serveAPI(t, h)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	h.fake.On("mount").Fail("mount: wrong fs type")

	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"subvolume not empty", "DELETE", "/api/storage/subvolume?path=" + dir, "", http.StatusConflict, "subvolume_not_empty"},
		{"unknown pool", "PATCH", "/api/storage/pools/nope", `{"auto_mount":true}`, http.StatusNotFound, "unknown_pool"},
		{"command failure carries its message", "POST", "/api/storage/mount", `{"devices":["/dev/sdb"],"name":"pool"}`, http.StatusInternalServerError, "storage_failed"},
		{"missing body", "POST", "/api/storage/mount", "", http.StatusBadRequest, "bad_request"},
		{"no devices", "POST", "/api/storage/mount", `{"devices":[],"name":"pool"}`, http.StatusUnprocessableEntity, "bad_request"},
		{"missing auto_mount", "PATCH", "/api/storage/pools/nope", `{}`, http.StatusUnprocessableEntity, "bad_request"},
		{"missing path", "DELETE", "/api/storage/subvolume", "", http.StatusUnprocessableEntity, "bad_request"},
	} {
		w, m := do(c.method, c.path, c.body)
		if w.Code != c.status || m["code"] != c.code {
			t.Errorf("%s: got %d %v, want %d %s: %s", c.name, w.Code, m["code"], c.status, c.code, w.Body)
		}
	}
	if _, m := do("POST", "/api/storage/mount", `{"devices":["/dev/sdb"],"name":"pool"}`); !strings.Contains(fmt.Sprint(m["detail"]), "wrong fs type") {
		t.Errorf("storage_failed lost mount's message: %v", m)
	}
}

func TestHTTPHappyPaths(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	do := serveAPI(t, h)
	h.fake.On("lsblk").Reply(lsblk(false))
	h.fake.On("mount").Reply("")
	h.fake.On("umount").Reply("")
	h.fake.On("bcachefs", "subvolume").Reply("")
	h.fake.On("ionice").Reply(`{"folders":[{"path":"/var/mnt/pool/x","total":7}]}`)

	w, _ := do("GET", "/api/storage", "")
	var st StorageStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); w.Code != http.StatusOK || err != nil || len(st.Pools) != 1 || st.Pools[0].State != Unmounted {
		t.Fatalf("storage: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/storage/mount", `{"devices":["/dev/sdb"],"name":"pool"}`, http.StatusNoContent},
		{"POST", "/api/storage/unmount", `{"mountpoint":"/var/mnt/pool"}`, http.StatusNoContent},
		{"POST", "/api/storage/subvolume", `{"path":"/var/mnt/pool/x"}`, http.StatusCreated},
		{"DELETE", "/api/storage/subvolume?path=" + t.TempDir(), "", http.StatusNoContent},
	} {
		if w, _ := do(c.method, c.path, c.body); w.Code != c.status {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	w, _ = do("POST", "/api/storage/subvolume-usage", `{"paths":["/var/mnt/pool/x"]}`)
	var u SubvolumeUsage
	if err := json.Unmarshal(w.Body.Bytes(), &u); w.Code != http.StatusOK || err != nil || u.Usage["/var/mnt/pool/x"] != 7 {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
}

func TestStatusCancelledCallerLeavesAHungCompute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started, release := blockingLsblk(h, cmdrunner.Result{Stdout: lsblk(false)}, nil, lsblk(false))
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := h.svc.Status(ctx); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller stayed blocked on the shared compute")
	}
}

func TestCanonicalWritesOutTheMntLink(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/mnt/pool/media":     "/var/mnt/pool/media",
		"/var/mnt/pool/media": "/var/mnt/pool/media",
		"/mntx/pool":          "/mntx/pool",
		"/srv/x":              "/srv/x",
	} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}
