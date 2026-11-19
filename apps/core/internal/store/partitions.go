package store

import "context"

// PartitionRecord is a partition the daemon mounts: a file system that is
// not a bcachefs pool, kept by its UUID.
type PartitionRecord struct {
	UUID      string
	Name      string
	Fstype    string
	ReadOnly  bool
	AutoMount bool
}

// Partitions returns the remembered partitions ordered by UUID.
func (s *Store) Partitions(ctx context.Context) ([]PartitionRecord, error) {
	rows, err := s.q.ListPartitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PartitionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, PartitionRecord{UUID: r.Uuid, Name: r.Name, Fstype: r.Fstype, ReadOnly: r.ReadOnly, AutoMount: r.AutoMount})
	}
	return out, nil
}

// UpsertPartition inserts or updates a partition by UUID.
func (s *Store) UpsertPartition(ctx context.Context, p PartitionRecord) error {
	return s.q.UpsertPartition(ctx, UpsertPartitionParams{
		Uuid: p.UUID, Name: p.Name, Fstype: p.Fstype, ReadOnly: p.ReadOnly, AutoMount: p.AutoMount, UpdatedAt: s.nowMS(),
	})
}
