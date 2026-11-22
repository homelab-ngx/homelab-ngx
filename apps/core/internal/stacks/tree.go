package stacks

import (
	"context"
	"os"
	"sync"

	"golang.org/x/sync/singleflight"

	"homelab-ngx.com/core/internal/httpx"
)

// maxTrees bounds the store paths remembered, one per locked source.
const maxTrees = 256

// Tree is the stack's commit as a folder: its copy in the Nix store,
// read-only, with the stack's source and the commit. It is the approved
// commit, or the fetched one while nothing is approved, as Icon reads.
func (s *Service) Tree(ctx context.Context, name string) (dir, source, rev string, err error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return "", "", "", err
	}
	url, rev := r.FetchedUrl, r.FetchedRev
	if r.ApprovedRev != "" {
		url, rev = r.ApprovedUrl, r.ApprovedRev
	}
	if url == "" {
		return "", "", "", httpx.Conflict("nothing_fetched", "%s has no commit fetched yet", name)
	}
	dir, err = s.trees.storePath(ctx, url)
	if err != nil {
		return "", "", "", err
	}
	return dir, r.Source, rev, nil
}

// treeCache keeps the store path of each commit's tree, for the icons and
// for Tree.
type treeCache struct {
	// lookup returns the store path of a locked flake reference.
	lookup func(ctx context.Context, url string) (string, error)

	flight singleflight.Group
	mu     sync.Mutex
	paths  map[string]string // locked flake reference: store path
}

func newTreeCache(lookup func(context.Context, string) (string, error)) *treeCache {
	return &treeCache{lookup: lookup, paths: map[string]string{}}
}

// storePath is the tree of the commit at url, looked up once per commit and
// again when the store has collected it.
func (c *treeCache) storePath(ctx context.Context, url string) (string, error) {
	c.mu.Lock()
	p, ok := c.paths[url]
	c.mu.Unlock()
	if ok {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Shared with every caller waiting on the same commit: one that goes
	// away does not cancel it for the others.
	ctx = context.WithoutCancel(ctx)
	v, err, _ := c.flight.Do(url, func() (any, error) {
		p, err := c.lookup(ctx, url)
		if err != nil {
			return "", httpx.BadGateway("fetch_failed", "reading the stack's commit: %v", err)
		}
		c.mu.Lock()
		if len(c.paths) >= maxTrees {
			clear(c.paths)
		}
		c.paths[url] = p
		c.mu.Unlock()
		return p, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}
