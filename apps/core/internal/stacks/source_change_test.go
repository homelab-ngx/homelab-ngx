package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSetSourceMovesTheStackAndApprovesTheSameManifest(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newRuntime(t)
	ctx := context.Background()
	svc.host.MirrorsDir = t.TempDir()
	mirror := filepath.Join(svc.host.MirrorsDir, "immich.git")
	if err := os.Mkdir(mirror, 0o700); err != nil {
		t.Fatal(err)
	}
	draft := "git+file:///var/lib/homelab-daemon/drafts/immich"

	if _, err := svc.SetSource(ctx, "immich", "not a source"); problemCode(err) != "invalid_source" {
		t.Errorf("err = %v", err)
	}
	// A failed fetch keeps the old source.
	fs.err = errors.New("unreachable")
	if _, err := svc.SetSource(ctx, "immich", draft); err == nil {
		t.Fatal("a failed fetch moved the stack")
	}
	if s, _ := svc.Get(ctx, "immich"); s.Source != src {
		t.Errorf("source = %q after a failed move", s.Source)
	}

	fs.set("ddddddd4", immichManifest)
	s, err := svc.SetSource(ctx, "immich", draft)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != draft || s.Fetched.Rev != "ddddddd4" || s.Approved == nil || s.Approved.Rev != "ddddddd4" || len(s.NewGrants) != 0 {
		t.Errorf("source=%q fetched=%s approved=%v grants=%v", s.Source, s.Fetched.Rev, s.Approved, s.NewGrants)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Errorf("the old source's mirror is still there: %v", err)
	}
}
