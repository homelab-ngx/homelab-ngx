package notifications

import (
	"context"
	"sort"
	"sync"
)

const memMaxEntries = 200

// memStore is the process-local Store used when New gets a nil Store.
type memStore struct {
	mu       sync.Mutex
	problems map[string]ProblemState
	entries  []Entry // oldest first
	nextID   int64
}

func newMemStore() *memStore {
	return &memStore{problems: map[string]ProblemState{}, nextID: 1}
}

func (m *memStore) Problems(context.Context) ([]ProblemState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ProblemState, 0, len(m.problems))
	for _, p := range m.problems {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memStore) Commit(_ context.Context, c Change) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range c.Upsert {
		m.problems[p.Key] = p
	}
	for _, k := range c.Delete {
		delete(m.problems, k)
	}
	for _, e := range c.Entries {
		e.ID = m.nextID
		m.nextID++
		m.entries = append(m.entries, e)
	}
	if len(m.entries) > memMaxEntries {
		m.entries = append([]Entry(nil), m.entries[len(m.entries)-memMaxEntries:]...)
	}
	return nil
}

func (m *memStore) Log(_ context.Context, before int64, limit int) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for i := len(m.entries) - 1; i >= 0 && len(out) < limit; i-- {
		if e := m.entries[i]; before == 0 || e.ID < before {
			out = append(out, e)
		}
	}
	return out, nil
}
