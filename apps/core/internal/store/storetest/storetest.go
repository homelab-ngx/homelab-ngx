// Package storetest gives tests a migrated store without replaying every
// migration per test: the schema is migrated once per test binary into a
// template, and each test gets its own copy of that file.
package storetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"homelab-ngx.com/core/internal/store"
)

var (
	once     sync.Once
	template []byte
	tmplErr  error
)

// image returns the bytes of a freshly migrated database.
func image() ([]byte, error) {
	once.Do(func() {
		dir, err := os.MkdirTemp("", "storetest-")
		if err != nil {
			tmplErr = err
			return
		}
		defer os.RemoveAll(dir)
		p := filepath.Join(dir, "homelab.db")
		s, err := store.Open(context.Background(), p, store.Options{Migrate: true})
		if err != nil {
			tmplErr = err
			return
		}
		if err := s.CheckpointTruncate(context.Background()); err != nil {
			s.Close()
			tmplErr = err
			return
		}
		if tmplErr = s.Close(); tmplErr != nil {
			return
		}
		template, tmplErr = os.ReadFile(p)
	})
	return template, tmplErr
}

// Path writes a migrated database into a new temporary directory and
// returns its path, for tests that open it themselves.
func Path(t testing.TB) string {
	t.Helper()
	b, err := image()
	if err != nil {
		t.Fatalf("storetest: migrating the template: %v", err)
	}
	p := filepath.Join(t.TempDir(), "homelab.db")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Open returns a migrated store of the test's own, closed when the test
// ends, and the path of its file.
func Open(t testing.TB) (*store.Store, string) {
	t.Helper()
	p := Path(t)
	s, err := store.Open(context.Background(), p, store.Options{Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}
