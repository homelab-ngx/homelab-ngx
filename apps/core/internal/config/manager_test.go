package config_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

// storeAdapter maps *store.Store onto config.OverrideStore. The composition
// root (cmd/daemon) carries the production copy of this.
type storeAdapter struct{ s *store.Store }

func (a storeAdapter) Pools(ctx context.Context) ([]config.Pool, error) {
	in, err := a.s.Pools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]config.Pool, 0, len(in))
	for _, p := range in {
		out = append(out, config.Pool{UUID: p.UUID, Mountpoint: p.Mountpoint, Name: p.Name, Options: p.Options, AutoMount: p.AutoMount})
	}
	return out, nil
}
func (a storeAdapter) UpsertPool(ctx context.Context, p config.Pool) error {
	return a.s.UpsertPool(ctx, store.PoolRecord{UUID: p.UUID, Mountpoint: p.Mountpoint, Name: p.Name, Options: p.Options, AutoMount: p.AutoMount})
}

func (a storeAdapter) DeletePool(ctx context.Context, uuid string) error {
	return a.s.DeletePool(ctx, uuid)
}

func writeNix(t *testing.T) string {
	t.Helper()
	doc := map[string]any{
		"version": config.SupportedVersion,
		"db":      map[string]any{"snapshot_schedule": "15 3 * * *", "snapshot_keep": 7, "retention_days": 90},
	}
	data, _ := json.Marshal(doc)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, data, 0o444); err != nil {
		t.Fatal(err)
	}
	return p
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, _ := storetest.Open(t)
	return s
}

func TestManagerWithRealStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore(t)
	m := config.NewManager(writeNix(t), storeAdapter{st})
	if m.Current() != nil {
		t.Fatal("Current before Load must be nil")
	}
	if _, err := m.SetPoolAutoMount(ctx, "u1", "/pool", true); err == nil {
		t.Fatal("setter before Load must fail")
	}
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	snap := m.Current()
	if snap.Generation != 1 || len(snap.Pools) != 0 {
		t.Fatalf("gen=%d pools=%v", snap.Generation, snap.Pools)
	}
	sub := m.Subscribe()

	if _, err := m.SetPoolAutoMount(ctx, "u1", "", true); err == nil {
		t.Error("new pool without mountpoint accepted")
	}
	next, err := m.SetPoolAutoMount(ctx, "u1", "/pool", true)
	if err != nil {
		t.Fatal(err)
	}
	// Old snapshot is untouched.
	if len(snap.Pools) != 0 {
		t.Error("previous snapshot mutated")
	}
	if got := <-sub; got.Generation != next.Generation {
		t.Errorf("subscriber got gen %d, want %d", got.Generation, next.Generation)
	}
	if _, err = m.UpsertPool(ctx, config.Pool{UUID: "u1", Mountpoint: "/pool", Name: "main", Options: "x"}); err != nil {
		t.Fatal(err)
	}
	next, err = m.SetPoolAutoMount(ctx, "u1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := next.Pool("u1"); !ok || p.AutoMount || p.Name != "main" || p.Options != "x" {
		t.Errorf("pool = %+v", p)
	}

	// A fresh manager over the same DB sees the persisted pool.
	m2 := config.NewManager(writeNix(t), storeAdapter{st})
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p, ok := m2.Current().Pool("u1"); !ok || p.Name != "main" {
		t.Errorf("pool not persisted: %+v", p)
	}
}

// memStore is a goroutine-safe in-memory OverrideStore for the race test.
type memStore struct {
	mu    sync.Mutex
	pools map[string]config.Pool
}

func (m *memStore) Pools(context.Context) ([]config.Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]config.Pool, 0, len(m.pools))
	for _, p := range m.pools {
		out = append(out, p)
	}
	return out, nil
}
func (m *memStore) UpsertPool(_ context.Context, p config.Pool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pools[p.UUID] = p
	return nil
}

func (m *memStore) DeletePool(_ context.Context, uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pools, uuid)
	return nil
}

func TestManagerConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := config.NewManager(writeNix(t), &memStore{pools: map[string]config.Pool{}})
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	sub := m.Subscribe()
	done := make(chan struct{})
	var readers, writers sync.WaitGroup

	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			var lastGen uint64
			for {
				select {
				case <-done:
					return
				default:
				}
				s := m.Current()
				if s.Generation < lastGen {
					t.Errorf("generation went backwards: %d < %d", s.Generation, lastGen)
					return
				}
				lastGen = s.Generation
				for _, p := range s.Pools { // read through the whole snapshot
					_ = p.AutoMount
				}
			}
		}()
	}
	readers.Add(1)
	go func() { // slow subscriber must never block writers
		defer readers.Done()
		for {
			select {
			case <-done:
				return
			case <-sub:
				time.Sleep(time.Millisecond)
			}
		}
	}()
	for w := 0; w < 2; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; i < 200; i++ {
				on := i%2 == w
				if _, err := m.SetPoolAutoMount(ctx, "u1", "/pool", on); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	writers.Wait()
	close(done)
	readers.Wait()
	if g := m.Current().Generation; g != 401 {
		t.Errorf("generation = %d, want 401", g)
	}
}
