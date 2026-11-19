package stacks

// The Nix store's room. Every stack is a system in the store, each with its
// own nixpkgs, so the store grows with every update. A stack keeps its
// current generation and the newest few it can go back to; the rest leave
// its profile, and the store's garbage is collected after a stack was
// updated, rolled back or removed, and when the disk runs low.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/store"
)

const (
	// keepGenerations is how many healthy generations a stack keeps to go
	// back to, besides the one it runs.
	keepGenerations = 2
	// storeFloor is the free space under which the store is collected
	// whether or not a stack changed. nix collects by itself during a build
	// that leaves less than half of it (min-free in the image's nix.conf).
	storeFloor = 8 << 30
	// storeEvery is how often the free space is looked at and the store
	// measured.
	storeEvery   = 24 * time.Hour
	storeSettle  = 5 * time.Minute // after the daemon starts, before the first look
	collectTime  = 2 * time.Hour
	measureTime  = 30 * time.Minute
	storeDataDir = "/var"
)

// collectDelay is how long a collection waits after it was asked for, so
// that stacks updated one after another are collected for once. A variable
// for tests.
var collectDelay = 2 * time.Minute

// NixStore is the Nix store's use of its disk.
type NixStore struct {
	UsedBytes    int64      `json:"used_bytes" doc:"Size of the store; -1 until it is measured."`
	MeasuredAt   *time.Time `json:"measured_at,omitempty" doc:"When used_bytes was measured."`
	FreeBytes    int64      `json:"free_bytes" doc:"Free on the file system the store is on."`
	TotalBytes   int64      `json:"total_bytes" doc:"Size of the file system the store is on."`
	OwnPartition bool       `json:"own_partition" doc:"The store has a partition of its own (a host installed before the store moved to /var); otherwise it shares the host's data partition."`
	CollectedAt  *time.Time `json:"collected_at,omitempty" doc:"When garbage was last collected."`
	FreedBytes   int64      `json:"freed_bytes" doc:"What that collection freed."`
	Collecting   bool       `json:"collecting" doc:"A collection is running or about to."`
}

// storeRecord is what the daemon remembers of the store between restarts.
type storeRecord struct {
	UsedBytes   int64      `json:"used_bytes"`
	MeasuredAt  *time.Time `json:"measured_at,omitempty"`
	CollectedAt *time.Time `json:"collected_at,omitempty"`
	FreedBytes  int64      `json:"freed_bytes"`
}

// storeGate keeps a collection and a build apart. A build's result is in
// the store before anything refers to it (`nix build --no-link`, then the
// profile is set), and a collection in between would delete it. Builds run
// together; a collection waits for them, and they wait for it.
type storeGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	builds  int
	running bool
	pending bool
	timer   *time.Timer
	fn      func()
}

func newStoreGate() *storeGate {
	g := &storeGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// nixGate is the host's: builds start from more than one place (a stack's
// update, a draft's build).
var nixGate = newStoreGate()

// hold marks a build as running until the returned function is called.
func (g *storeGate) hold() (release func()) {
	g.mu.Lock()
	for g.running {
		g.cond.Wait()
	}
	g.builds++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.builds--
			g.arm()
			g.mu.Unlock()
		})
	}
}

// request asks for one run of fn once no build runs; asking again before it
// starts changes nothing.
func (g *storeGate) request(fn func()) {
	g.mu.Lock()
	g.fn, g.pending = fn, true
	g.arm()
	g.mu.Unlock()
}

// arm starts the wait before a pending run. g.mu is held.
func (g *storeGate) arm() {
	if !g.pending || g.running || g.builds > 0 {
		return
	}
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(collectDelay, g.fire)
}

func (g *storeGate) fire() {
	g.mu.Lock()
	if !g.pending || g.running || g.builds > 0 {
		g.mu.Unlock()
		return
	}
	g.pending, g.running = false, true
	fn := g.fn
	g.mu.Unlock()
	fn()
	g.mu.Lock()
	g.running = false
	g.cond.Broadcast()
	g.arm()
	g.mu.Unlock()
}

func (g *storeGate) busy() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending || g.running
}

// prune takes the generations a stack no longer keeps out of its profile
// and the database: all but the one it runs and the newest keepGenerations
// healthy others. It returns how many went.
func (s *Service) prune(ctx context.Context, name string) int {
	r, err := s.row(ctx, name)
	if err != nil {
		return 0
	}
	// An interrupted operation still names the generation it goes back to.
	if r.OpKind != "" {
		return 0
	}
	gens, err := s.st.Queries().ListStackGenerations(ctx, name)
	if err != nil {
		s.log.Warn("stack generations not read", "stack", name, "error", err)
		return 0
	}
	var drop []int64
	kept := 0
	for _, g := range gens { // newest first
		switch {
		case g.ID == r.Generation:
		case g.Failed == 0 && kept < keepGenerations:
			kept++
		default:
			drop = append(drop, g.ID)
		}
	}
	if len(drop) == 0 {
		return 0
	}
	args := []string{"--profile", s.profile(name), "--delete-generations"}
	for _, id := range drop {
		args = append(args, strconv.FormatInt(id, 10))
	}
	if _, err := s.run(ctx, 5*time.Minute, "nix-env", args...); err != nil {
		s.log.Warn("old stack generations not removed", "stack", name, "error", err)
		return 0
	}
	for _, id := range drop {
		if err := s.st.Queries().DeleteStackGeneration(ctx, store.DeleteStackGenerationParams{Stack: name, ID: id}); err != nil {
			s.log.Warn("stack generation not forgotten", "stack", name, "generation", id, "error", err)
		}
	}
	s.log.Info("removed old stack generations", "stack", name, "generations", drop)
	return len(drop)
}

// tidy runs after an operation that may have built: the stack's old
// generations go, and the store is collected.
func (s *Service) tidy(ctx context.Context, name, kind string) {
	n := s.prune(ctx, name)
	// A start builds only a stack that has no generation yet.
	if n > 0 || kind != opStart {
		s.collectSoon()
	}
}

// collectSoon asks for a collection of the store's garbage.
func (s *Service) collectSoon() {
	if s.host.StoreDir == "" {
		return
	}
	nixGate.request(s.collect)
}

// collect deletes what nothing in the store refers to any more, then
// measures the store.
func (s *Service) collect() {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	_, before, _ := s.statfs(s.storeMount())
	if _, err := s.run(ctx, collectTime, "nix-collect-garbage"); err != nil {
		s.log.Warn("the Nix store was not collected", "error", err)
		return
	}
	_, after, _ := s.statfs(s.storeMount())
	now := s.now()
	var freed int64
	if after > before {
		freed = int64(after - before)
	}
	s.storeMu.Lock()
	rec := s.storeRecordLocked()
	rec.CollectedAt, rec.FreedBytes = &now, freed
	s.saveStoreLocked(rec)
	s.storeMu.Unlock()
	s.log.Info("collected the Nix store", "freed_bytes", freed)
	s.measureStore(ctx)
}

// measureStore reads the store's size, which takes a while on a large one.
func (s *Service) measureStore(ctx context.Context) {
	if _, err := os.Stat(s.host.StoreDir); err != nil {
		return // nothing was built yet
	}
	res, err := s.run(ctx, measureTime, "du", "-sxB1", s.host.StoreDir)
	if err != nil {
		s.log.Warn("the Nix store was not measured", "error", err)
		return
	}
	f := strings.Fields(res.Stdout)
	if len(f) == 0 {
		return
	}
	used, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return
	}
	now := s.now()
	s.storeMu.Lock()
	rec := s.storeRecordLocked()
	rec.UsedBytes, rec.MeasuredAt = used, &now
	s.saveStoreLocked(rec)
	s.storeMu.Unlock()
}

func (s *Service) storeRecordLocked() storeRecord {
	if s.storeRec == nil {
		rec := storeRecord{UsedBytes: -1}
		if b, err := os.ReadFile(s.host.StoreFile); err == nil {
			_ = json.Unmarshal(b, &rec)
		}
		s.storeRec = &rec
	}
	return *s.storeRec
}

func (s *Service) saveStoreLocked(rec storeRecord) {
	s.storeRec = &rec
	if s.host.StoreFile == "" {
		return
	}
	b, err := json.Marshal(rec)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(s.host.StoreFile), 0o750); err == nil {
			err = os.WriteFile(s.host.StoreFile, b, 0o640)
		}
	}
	if err != nil {
		s.log.Warn("the Nix store's record was not saved", "error", err)
	}
}

// NixStore reports the store's use of its disk.
func (s *Service) NixStore(context.Context) NixStore {
	s.storeMu.Lock()
	rec := s.storeRecordLocked()
	s.storeMu.Unlock()
	out := NixStore{
		UsedBytes: rec.UsedBytes, MeasuredAt: rec.MeasuredAt, CollectedAt: rec.CollectedAt, FreedBytes: rec.FreedBytes,
		Collecting: nixGate.busy(),
	}
	if s.host.StoreDir == "" {
		return out
	}
	if total, free, err := s.statfs(s.storeMount()); err == nil {
		out.TotalBytes, out.FreeBytes = int64(total), int64(free)
	}
	out.OwnPartition = !sameFilesystem(s.storeMount(), storeDataDir)
	return out
}

// storeMount is a folder on the store's file system: the store, or the
// folder it is in until nix has made it (a host that built nothing yet).
func (s *Service) storeMount() string {
	if _, err := os.Stat(s.host.StoreDir); err != nil {
		return filepath.Dir(s.host.StoreDir)
	}
	return s.host.StoreDir
}

// WatchStore looks at the store's disk until ctx ends: it collects when
// less than storeFloor is free, and keeps the store's size current.
func (s *Service) WatchStore(ctx context.Context) {
	if s.host.StoreDir == "" {
		return
	}
	t := time.NewTimer(storeSettle)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, free, err := s.statfs(s.storeMount()); err == nil && free < storeFloor {
			s.log.Info("the Nix store's disk is low; collecting", "free_bytes", free)
			s.collectSoon()
		} else {
			s.measureStore(ctx)
		}
		t.Reset(storeEvery)
	}
}

// diskRoom is the size of the file system at path and what is free on it.
func diskRoom(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}

func sameFilesystem(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return true
	}
	return uint64(sa.Dev) == uint64(sb.Dev)
}
