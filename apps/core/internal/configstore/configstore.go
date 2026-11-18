// Package configstore adapts *store.Store to config.OverrideStore. config
// deliberately does not import store; the composition root (and tests of
// packages that need a real config.Manager) use this adapter.
package configstore

import (
	"context"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/store"
)

type adapter struct{ s *store.Store }

// New returns the config.OverrideStore backed by s.
func New(s *store.Store) config.OverrideStore { return adapter{s} }

func (a adapter) Pools(ctx context.Context) ([]config.Pool, error) {
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

func (a adapter) UpsertPool(ctx context.Context, p config.Pool) error {
	return a.s.UpsertPool(ctx, store.PoolRecord{UUID: p.UUID, Mountpoint: p.Mountpoint, Name: p.Name, Options: p.Options, AutoMount: p.AutoMount})
}
