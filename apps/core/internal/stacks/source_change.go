package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// ApprovedTree is the stack's approved commit as a folder: its copy in the
// Nix store, read-only, with the stack's source and the commit. A fork
// starts from it.
func (s *Service) ApprovedTree(ctx context.Context, name string) (dir, source, rev string, err error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return "", "", "", err
	}
	if r.ApprovedRev == "" || r.ApprovedUrl == "" {
		return "", "", "", httpx.Conflict("nothing_approved", "%s has no approved commit to copy", name)
	}
	f, err := prefetchWithExtraEnv(ctx, s.r, r.ApprovedUrl, s.sshEnvFor(r.SshKey))
	if err != nil {
		return "", "", "", err
	}
	if !strings.HasPrefix(f.StorePath, "/nix/store/") {
		return "", "", "", errors.New("nix flake prefetch: no store path for the approved commit")
	}
	return f.StorePath, r.Source, r.ApprovedRev, nil
}

// SetSource moves the stack onto another source and fetches it there: the
// commit it finds is approved by itself when it asks for nothing new, and
// shows as an update otherwise. The running generation, the variables and
// the data stay. Used by a fork, and by patchStack when a stack's repo has
// moved. When the fetch fails the stack keeps its old source.
func (s *Service) SetSource(ctx context.Context, name, source string) (Stack, error) {
	if !ValidSource(source) {
		return Stack{}, httpx.Unprocessable("invalid_source", "%q is not a stack source", source)
	}
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	if r.Source == source {
		return s.Get(ctx, name)
	}
	f, m, sshKey, err := s.fetchForAdd(ctx, source, name, name)
	if err != nil {
		return Stack{}, err
	}
	set := func(src, key string) error {
		if err := s.st.Queries().SetStackSource(ctx, store.SetStackSourceParams{Name: name, Source: src}); err != nil {
			return err
		}
		return s.st.Queries().SetStackSSHKey(ctx, store.SetStackSSHKeyParams{Name: name, SshKey: key})
	}
	if err := set(source, sshKey); err != nil {
		return Stack{}, err
	}
	st, err := s.applyFetched(ctx, r, source, sshKey, f, m)
	if err != nil {
		if rerr := set(r.Source, r.SshKey); rerr != nil {
			return Stack{}, errors.Join(err, fmt.Errorf("restoring the source: %w", rerr))
		}
		return Stack{}, err
	}
	// The commit list of the old source is no use to the new one.
	if s.host.MirrorsDir != "" {
		_ = os.RemoveAll(filepath.Join(s.host.MirrorsDir, name+".git"))
	}
	s.log.Info("stack source changed", "stack", name, "from", r.Source, "to", source)
	return st, nil
}
