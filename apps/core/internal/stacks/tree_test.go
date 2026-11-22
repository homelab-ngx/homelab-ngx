package stacks

import (
	"context"
	"errors"
	"testing"

	"homelab-ngx.com/core/internal/store"
)

func TestTreeReadsTheApprovedCommit(t *testing.T) {
	t.Parallel()
	svc, _, _, tree, lookups := iconService(t, nil)
	dir, source, rev, err := svc.Tree(context.Background(), "players")
	if err != nil || dir != tree || source != src || rev != "aaaaaaa1" {
		t.Fatalf("Tree = %q %q %q %v, want %q %q aaaaaaa1", dir, source, rev, err, tree, src)
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("%d store path lookups, want 1", n)
	}
}

func TestTreeAndIconsShareOneLookupPerCommit(t *testing.T) {
	t.Parallel()
	svc, op, _, tree, lookups := iconService(t, nil)
	wantIcon(t, getIcon(op, "players/icon?unit=jellyfin.service"), "image/svg+xml", testSVG)
	if dir, _, _, err := svc.Tree(context.Background(), "players"); err != nil || dir != tree {
		t.Fatalf("Tree = %q %v", dir, err)
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("%d store path lookups for icons and files of one commit, want 1", n)
	}
}

func TestTreeReadsTheFetchedCommitWhileNothingIsApproved(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	fs.set("bbbbbbb1", v9Manifest)
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	svc.trees.lookup = func(_ context.Context, url string) (string, error) {
		if url != "github:o/r/bbbbbbb1" {
			t.Errorf("tree of %q, want the fetched commit", url)
		}
		return tree, nil
	}
	dir, _, rev, err := svc.Tree(context.Background(), "players")
	if err != nil || dir != tree || rev != "bbbbbbb1" {
		t.Errorf("Tree = %q %q %v", dir, rev, err)
	}
}

func TestTreeRefusals(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := iconService(t, nil)
	ctx := context.Background()
	if err := svc.st.Queries().InsertStack(ctx, store.InsertStackParams{Name: "empty", Source: src, Manifest: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Tree(ctx, "nope"); problemCode(err) != "unknown_stack" {
		t.Errorf("unknown stack: %v", err)
	}
	if _, _, _, err := svc.Tree(ctx, "empty"); problemCode(err) != "nothing_fetched" {
		t.Errorf("nothing fetched: %v", err)
	}
	svc.trees.lookup = func(context.Context, string) (string, error) { return "", errors.New("offline") }
	svc.trees.paths = map[string]string{}
	if _, _, _, err := svc.Tree(ctx, "players"); problemCode(err) != "fetch_failed" {
		t.Errorf("prefetch failed: %v", err)
	}
}
