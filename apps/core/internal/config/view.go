package config

import "time"

// The wire shape of the effective configuration, used by GET /config. The
// *View functions are the single mapping from a Snapshot to it.

// PoolConfig is a stored pool record (pools are owned by the runtime
// database, not nix).
type PoolConfig struct {
	AutoMount  bool   `json:"auto_mount"`
	Mountpoint string `json:"mountpoint"`
	Name       string `json:"name"`
	Options    string `json:"options"`
	Uuid       string `json:"uuid"`
}

// ConfigView is the effective configuration snapshot.
type ConfigView struct {
	CoreServices []string     `json:"core_services"`
	Generation   int64        `json:"generation"`
	LoadedAt     time.Time    `json:"loaded_at"`
	Pools        []PoolConfig `json:"pools"`
}

// PoolView renders one stored pool.
func PoolView(p Pool) PoolConfig {
	return PoolConfig{
		Uuid: p.UUID, Mountpoint: p.Mountpoint, Name: p.Name, Options: p.Options, AutoMount: p.AutoMount,
	}
}

// View renders a whole snapshot; every array is non-nil.
func View(s *Snapshot) ConfigView {
	v := ConfigView{
		Generation:   int64(s.Generation),
		LoadedAt:     s.LoadedAt,
		Pools:        make([]PoolConfig, 0, len(s.Pools)),
		CoreServices: []string{},
	}
	if s.Nix != nil {
		v.CoreServices = nonNil(s.Nix.CoreServices)
	}
	for _, p := range s.Pools {
		v.Pools = append(v.Pools, PoolView(p))
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
