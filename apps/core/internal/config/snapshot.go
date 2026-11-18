package config

import (
	"sort"
	"time"
)

// Pool is a DB-owned storage pool (nix declares none).
type Pool struct {
	UUID       string
	Mountpoint string
	Name       string
	Options    string
	AutoMount  bool
}

// Snapshot is an immutable view of the effective configuration. Readers take
// one Manager.Current() per request or tick and must never mutate it
// (slices and maps inside are shared).
type Snapshot struct {
	Generation uint64
	LoadedAt   time.Time
	Nix        *Nix
	Pools      []Pool // sorted by UUID
}

// Pool returns the pool with the given UUID.
func (s *Snapshot) Pool(uuid string) (Pool, bool) {
	for _, p := range s.Pools {
		if p.UUID == uuid {
			return p, true
		}
	}
	return Pool{}, false
}

// build is pure: the nix base plus the stored pools.
func build(nix *Nix, pools []Pool, gen uint64, now time.Time) *Snapshot {
	snap := &Snapshot{Generation: gen, LoadedAt: now, Nix: nix}
	snap.Pools = append([]Pool(nil), pools...)
	sort.Slice(snap.Pools, func(i, j int) bool { return snap.Pools[i].UUID < snap.Pools[j].UUID })
	return snap
}
