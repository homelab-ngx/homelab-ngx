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

// Delivery is one attempt to deliver an Entry on a channel.
type Delivery struct {
	At      time.Time
	Channel string
	OK      bool
	Error   string
}

// Entry is one logged notification with its delivery attempts.
type Entry struct {
	ID         int64 // assigned by the Store
	At         time.Time
	Key        string // problem key; "" for plain sends and multi-problem messages
	Kind       string
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

// Store persists problem state and the notification log. It is defined by
// the consumer; NewDBStore backs it with the SQLite store. A nil Store passed
// to New means in-memory.
type Store interface {
	// Problems returns every open problem.
	Problems(ctx context.Context) ([]ProblemState, error)
	// Commit applies c atomically.
	Commit(ctx context.Context, c Change) error
	// Log returns entries with ID < before (all when before is 0), newest
	// first, at most limit of them, deliveries included.
	Log(ctx context.Context, before int64, limit int) ([]Entry, error)
}
