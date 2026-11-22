package drafts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store"
)

// A fork is a draft that starts from an installed stack's approved commit
// instead of the template: the files of that commit's copy in the store, as
// the draft's first commit. The draft records where it came from, and holds
// no history of the stack's repo. A copy leaves the stack on its source and
// installs under a name of its own; converting moves the stack itself onto
// the draft, so the stack's next updates are the draft's saves.

// Fork makes the draft name from the installed stack from, which keeps its
// source.
func (s *Service) Fork(ctx context.Context, name, from string) (Draft, error) {
	if !stacks.ValidName(name) {
		return Draft{}, httpx.BadRequest("invalid_name",
			"%q is not a stack name: use 1-12 characters of a-z, 0-9 and '-', starting with a letter", name)
	}
	if err := s.fork(ctx, name, from, ""); err != nil {
		return Draft{}, err
	}
	s.log.Info("draft forked", "draft", name, "stack", from)
	return s.Get(ctx, name)
}

// Convert forks the installed stack into a draft of the same name and moves
// the stack onto it. The commit it then reads has the stack's own manifest,
// so it is approved by itself and waits as an update; the running stack, its
// variables and data are left alone. A stack that runs from a draft already
// is refused.
func (s *Service) Convert(ctx context.Context, stack string) (Draft, error) {
	st, err := s.stack(ctx, stack)
	if err != nil {
		return Draft{}, err
	}
	if d := s.draftOf(st.Source); d != "" {
		return Draft{}, httpx.Conflict("already_draft", "%s runs from the draft %s already; edit that", stack, d)
	}
	if err := s.fork(ctx, stack, stack, stack); err != nil {
		return Draft{}, err
	}
	if _, err := s.stacks.SetSource(ctx, stack, s.source(stack)); err != nil {
		s.drop(ctx, stack)
		return Draft{}, fmt.Errorf("moving %s onto its draft: %w", stack, err)
	}
	s.log.Info("stack converted to a draft", "stack", stack, "from", st.Source)
	return s.Get(ctx, stack)
}

// fork makes the draft name from a copy of stack from's approved commit.
func (s *Service) fork(ctx context.Context, name, from, ownStack string) error {
	if s.stacks == nil {
		return errors.New("no stacks to fork")
	}
	if _, err := s.stack(ctx, from); err != nil {
		return err
	}
	dir, source, rev, err := s.stacks.ApprovedTree(ctx, from)
	if err != nil {
		return err
	}
	if err := s.createFrom(ctx, name, dir, fmt.Sprintf("Copy of %s at %s", source, short(rev)), ownStack); err != nil {
		return err
	}
	if err := s.st.Queries().SetDraftFork(ctx, store.SetDraftForkParams{ForkedFrom: source, ForkedRev: rev, Name: name}); err != nil {
		s.drop(ctx, name)
		return err
	}
	return nil
}

// stack is the installed stack name.
func (s *Service) stack(ctx context.Context, name string) (stacks.Stack, error) {
	list, err := s.stacks.List(ctx)
	if err != nil {
		return stacks.Stack{}, err
	}
	for _, st := range list {
		if st.Name == name {
			return st, nil
		}
	}
	return stacks.Stack{}, httpx.NotFound("unknown_stack", "no stack named %q is installed", name)
}

// draftOf is the draft a stack source is, or "".
func (s *Service) draftOf(source string) string {
	src, _, _ := strings.Cut(source, "?")
	if name, ok := strings.CutPrefix(src, "git+file://"+s.dir+"/"); ok && stacks.ValidName(name) {
		return name
	}
	return ""
}

// drop removes a draft that was just made, after a later step failed.
func (s *Service) drop(ctx context.Context, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.RemoveAll(s.repo(name))
	if err := s.st.Queries().DeleteDraft(ctx, name); err != nil {
		s.log.Warn("forked draft not removed", "draft", name, "error", err)
	}
}
