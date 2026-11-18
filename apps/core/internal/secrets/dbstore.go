package secrets

import (
	"context"
	"time"

	"homelab-ngx.com/core/internal/store"
)

type dbStore struct{ s *store.Store }

// NewRotationStore backs a RotationStore with the secret_rotations table.
func NewRotationStore(s *store.Store) RotationStore { return dbStore{s} }

func (d dbStore) Rotations(ctx context.Context) (map[string]time.Time, error) {
	rows, err := d.s.Queries().ListSecretRotations(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.Name] = time.UnixMilli(r.RotatedAt)
	}
	return out, nil
}

func (d dbStore) SetRotation(ctx context.Context, name string, at time.Time) error {
	return d.s.Queries().SetSecretRotation(ctx, store.SetSecretRotationParams{Name: name, RotatedAt: at.UnixMilli()})
}
