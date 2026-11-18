package config

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// OverrideStore persists the pools. *store.Store satisfies
// it through a small adapter at the composition root (config does not import
// internal/store).
type OverrideStore interface {
	Pools(ctx context.Context) ([]Pool, error)
	UpsertPool(ctx context.Context, p Pool) error
}

// Manager owns the current Snapshot. Invariant: snapshots are immutable;
// all writes go through mu -> DB -> re-read overrides -> build -> Store ->
// notify. Readers call Current() (lock-free) once per request or tick.
type Manager struct {
	path string
	ov   OverrideStore

	cur atomic.Pointer[Snapshot]

	mu   sync.Mutex // serializes writers (and Load)
	gen  uint64
	nix  *Nix
	subs []chan *Snapshot
}

func NewManager(nixPath string, ov OverrideStore) *Manager {
	return &Manager{path: nixPath, ov: ov}
}

// Load reads the nix file and the overrides, builds a snapshot and publishes
// it. Call once at boot; calling again re-reads both (reload).
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	nix, err := LoadNix(m.path)
	if err != nil {
		return err
	}
	m.nix = nix
	_, err = m.rebuildLocked(ctx)
	return err
}

// Current returns the latest snapshot, or nil before the first successful Load.
func (m *Manager) Current() *Snapshot { return m.cur.Load() }

// Subscribe returns a channel that receives each new snapshot. Buffered(1),
// latest-wins: a slow reader skips intermediate snapshots, never blocks writers.
func (m *Manager) Subscribe() <-chan *Snapshot {
	ch := make(chan *Snapshot, 1)
	m.mu.Lock()
	m.subs = append(m.subs, ch)
	m.mu.Unlock()
	return ch
}

// SetPoolAutoMount upserts the pool row, keeping its name/options. mountpoint
// is used when the pool is new (or when non-empty, to update it).
func (m *Manager) SetPoolAutoMount(ctx context.Context, uuid, mountpoint string, auto bool) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireLoaded(); err != nil {
		return nil, err
	}
	if uuid == "" {
		return nil, errors.New("pool uuid is required")
	}
	pools, err := m.ov.Pools(ctx)
	if err != nil {
		return nil, fmt.Errorf("read pools: %w", err)
	}
	p := Pool{UUID: uuid}
	for _, e := range pools {
		if e.UUID == uuid {
			p = e
			break
		}
	}
	if mountpoint != "" {
		p.Mountpoint = mountpoint
	}
	if p.Mountpoint == "" {
		return nil, fmt.Errorf("pool %s: mountpoint unknown", uuid)
	}
	p.AutoMount = auto
	if err := m.ov.UpsertPool(ctx, p); err != nil {
		return nil, fmt.Errorf("persist pool: %w", err)
	}
	return m.rebuildLocked(ctx)
}

// UpsertPool stores a whole pool record (used when a mount records name/options).
func (m *Manager) UpsertPool(ctx context.Context, p Pool) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireLoaded(); err != nil {
		return nil, err
	}
	if p.UUID == "" || p.Mountpoint == "" {
		return nil, errors.New("pool uuid and mountpoint are required")
	}
	if err := m.ov.UpsertPool(ctx, p); err != nil {
		return nil, fmt.Errorf("persist pool: %w", err)
	}
	return m.rebuildLocked(ctx)
}

func (m *Manager) requireLoaded() error {
	if m.nix == nil {
		return errors.New("config not loaded")
	}
	return nil
}

// rebuildLocked re-reads overrides, builds, swaps and notifies. m.mu held.
func (m *Manager) rebuildLocked(ctx context.Context) (*Snapshot, error) {
	pools, err := m.ov.Pools(ctx)
	if err != nil {
		return nil, fmt.Errorf("read pools: %w", err)
	}
	m.gen++
	snap := build(m.nix, pools, m.gen, time.Now())
	m.cur.Store(snap)
	for _, ch := range m.subs {
		select { // latest wins
		case <-ch:
		default:
		}
		select {
		case ch <- snap:
		default:
		}
	}
	return snap, nil
}
