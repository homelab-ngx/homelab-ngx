package store

import "context"

// PoolRecord is a DB-owned storage pool row.
type PoolRecord struct {
	UUID       string
	Mountpoint string
	AutoMount  bool
	Name       string
	Options    string
}

// Pools returns all pools ordered by UUID.
func (s *Store) Pools(ctx context.Context) ([]PoolRecord, error) {
	rows, err := s.q.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PoolRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, PoolRecord{UUID: r.Uuid, Mountpoint: r.Mountpoint, AutoMount: r.AutoMount, Name: r.Name, Options: r.Options})
	}
	return out, nil
}

// UpsertPool inserts or updates a pool by UUID.
func (s *Store) UpsertPool(ctx context.Context, p PoolRecord) error {
	return s.q.UpsertPool(ctx, UpsertPoolParams{
		Uuid: p.UUID, Mountpoint: p.Mountpoint, AutoMount: p.AutoMount,
		Name: p.Name, Options: p.Options, UpdatedAt: s.nowMS(),
	})
}

// DeletePool removes a pool's row; a uuid with no row is not an error.
func (s *Store) DeletePool(ctx context.Context, uuid string) error { return s.q.DeletePool(ctx, uuid) }
