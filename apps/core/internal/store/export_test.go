package store

import (
	"database/sql"
	"time"
)

// DB exposes the pool to the external tests.
func DB(s *Store) *sql.DB { return s.db }

// SetNow replaces the store's clock in tests.
func SetNow(s *Store, now func() time.Time) { s.now = now }
