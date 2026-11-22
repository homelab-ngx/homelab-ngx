package drafts

import (
	"context"
	"errors"
)

// StackFiles is an installed stack's files, read from the commit it runs
// or, while nothing is approved, the one it fetched. The editor shows them
// read-only; a change is kept by forking the stack.
func (s *Service) StackFiles(ctx context.Context, name string) (StackFiles, error) {
	if s.stacks == nil {
		return StackFiles{}, errors.New("no stacks to read")
	}
	dir, source, rev, err := s.stacks.Tree(ctx, name)
	if err != nil {
		return StackFiles{}, err
	}
	t, err := readTreeUpTo(dir, true)
	if err != nil {
		return StackFiles{}, err
	}
	return StackFiles{Source: source, Rev: rev, Files: t.list()}, nil
}
