package notifications

import (
	"context"
	"time"
)

// Entry kinds recorded in the notification log.
const (
	KindSent     = "sent"
	KindOpened   = "opened"
	KindReminded = "reminded"
	KindResolved = "resolved"
)

// ProblemState is one open (deduplicated) problem.
type ProblemState struct {
	Key        string
	Title      string
	OpenedAt   time.Time
	LastSentAt time.Time
	Count      int
}

// Delivery is one attempt to deliver an Entry on a channel (or a filter skip).
type Delivery struct {
	At       time.Time
	Channel  string
	OK       bool
	Filtered bool
	Error    string
}

// Entry is one logged notification with its delivery attempts.
type Entry struct {
	ID         int64 // assigned by the Store
	At         time.Time
	Key        string // problem key; "" for plain sends and multi-problem messages
	Kind       string
	Event      string
	Path       string
	Severity   Severity
	Title      string
	Body       string
	Deliveries []Delivery
}

// Change is one atomic write: problem upserts/deletes plus log entries.
type Change struct {
	Upsert  []ProblemState
	Delete  []string
	Entries []Entry
}

// Store persists problem state, the notification log, and the read marker.
// It is defined by the consumer; NewDBStore backs it with the SQLite store.
// A nil Store passed to New means in-memory.
type Store interface {
	// Problems returns every open problem.
	Problems(ctx context.Context) ([]ProblemState, error)
	// Commit applies c atomically and returns the committed entries with assigned IDs.
	Commit(ctx context.Context, c Change) ([]Entry, error)
	// Log returns entries with ID < before (all when before is 0), newest
	// first, at most limit of them, deliveries included.
	Log(ctx context.Context, before int64, limit int) ([]Entry, error)
	// ReadUpTo returns the highest entry ID marked read.
	ReadUpTo(ctx context.Context) (int64, error)
	// SetReadUpTo advances the read marker monotonically to max(current, id).
	SetReadUpTo(ctx context.Context, id int64) (int64, error)
	// CountUnread returns how many log entries above readUpTo should still be
	// counted as unread (excluding currently-open problem entries counted under Open).
	CountUnread(ctx context.Context, readUpTo int64) (int, error)
	// LatestID returns the highest entry ID stored, or 0 when empty.
	LatestID(ctx context.Context) (int64, error)
}
