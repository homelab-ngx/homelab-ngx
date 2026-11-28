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
	readUpTo int64
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

func (m *memStore) Commit(_ context.Context, c Change) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range c.Upsert {
		m.problems[p.Key] = p
	}
	for _, k := range c.Delete {
		delete(m.problems, k)
	}
	committed := make([]Entry, 0, len(c.Entries))
	for _, e := range c.Entries {
		e.ID = m.nextID
		m.nextID++
		if e.Deliveries == nil {
			e.Deliveries = []Delivery{}
		}
		m.entries = append(m.entries, e)
		committed = append(committed, e)
	}
	if len(m.entries) > memMaxEntries {
		m.entries = append([]Entry(nil), m.entries[len(m.entries)-memMaxEntries:]...)
	}
	return committed, nil
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

func (m *memStore) ReadUpTo(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readUpTo, nil
}

func (m *memStore) SetReadUpTo(_ context.Context, id int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id > m.readUpTo {
		m.readUpTo = id
	}
	return m.readUpTo, nil
}

func (m *memStore) CountUnread(_ context.Context, readUpTo int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.entries {
		if e.ID <= readUpTo {
			continue
		}
		if (e.Kind == KindOpened || e.Kind == KindReminded) && e.Key != "" {
			if _, isOpen := m.problems[e.Key]; isOpen {
				continue
			}
		}
		n++
	}
	return n, nil
}

func (m *memStore) LatestID(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextID - 1, nil
}
