package drafts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"homelab-ngx.com/core/internal/stacks"
)

// stackTree is a stack's approved commit as the store holds it: read-only.
func stackTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for p, content := range Template("photos") {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(content), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stack.nix"), []byte("{ }\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestForkCopiesTheApprovedCommitAndLeavesTheStack(t *testing.T) {
	t.Parallel()
	s, fs := newService(t)
	ctx := context.Background()
	fs.tree = stackTree(t)
	fs.list = []stacks.Stack{{Name: "photos", Source: "github:o/photos"}}

	if _, err := s.Fork(ctx, "photos", "photos"); code(err) != "stack_exists" {
		t.Errorf("a copy took the stack's name: %v", err)
	}
	if _, err := s.Fork(ctx, "mine", "nope"); code(err) != "unknown_stack" {
		t.Errorf("unknown stack: %v", err)
	}
	d, err := s.Fork(ctx, "mine", "photos")
	if err != nil {
		t.Fatal(err)
	}
	if d.ForkedFrom != "github:o/photos" || d.ForkedRev != "abcdef1234567" || file(d, "stack.nix") != "{ }\n" || len(d.InstalledAs) != 0 {
		t.Errorf("draft = %+v", d)
	}
	if msg, _ := s.git(ctx, s.repo("mine"), "log", "-1", "--format=%s"); msg != "Copy of github:o/photos at abcdef123456" {
		t.Errorf("commit = %q", msg)
	}
	if fs.list[0].Source != "github:o/photos" {
		t.Errorf("the stack moved: %s", fs.list[0].Source)
	}
	l, _ := s.List(ctx)
	if len(l) != 1 || l[0].ForkedFrom != "github:o/photos" {
		t.Errorf("list = %+v", l)
	}
}

func TestConvertMovesTheStackOntoItsDraft(t *testing.T) {
	t.Parallel()
	s, fs := newService(t)
	ctx := context.Background()
	fs.tree = stackTree(t)
	fs.list = []stacks.Stack{{Name: "photos", Source: "github:o/photos"}}

	// A stack that cannot be moved keeps no draft.
	fs.moveErr = errors.New("fetch failed")
	if _, err := s.Convert(ctx, "photos"); err == nil {
		t.Fatal("converted although the move failed")
	}
	if _, err := os.Stat(s.repo("photos")); !os.IsNotExist(err) {
		t.Errorf("the draft stayed: %v", err)
	}
	fs.moveErr = nil

	d, err := s.Convert(ctx, "photos")
	if err != nil {
		t.Fatal(err)
	}
	if fs.list[0].Source != s.source("photos") || len(d.InstalledAs) != 1 || d.InstalledAs[0] != "photos" || d.ForkedFrom != "github:o/photos" {
		t.Errorf("source = %s, draft = %+v", fs.list[0].Source, d)
	}
	if _, err := s.Convert(ctx, "photos"); code(err) != "already_draft" {
		t.Errorf("second convert: %v", err)
	}
	// Its saves are the stack's updates.
	if _, err := s.Save(ctx, "photos", DraftSave{Changes: []DraftChange{{Path: "stack.nix", Content: strp("{ x = 1; }\n")}}}); err != nil {
		t.Fatal(err)
	}
	if len(fs.fetched) != 1 || fs.fetched[0] != "photos" {
		t.Errorf("fetched = %v", fs.fetched)
	}
}

func strp(s string) *string { return &s }
