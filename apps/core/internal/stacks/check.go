package stacks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/notifications"
)

// The update check reads every stack's source this often, starting a little
// after the daemon starts so a boot is not slowed by it.
const (
	checkEvery      = 6 * time.Hour
	firstCheckAfter = 10 * time.Minute
)

// updateScope keys the "commits waiting" problems. It is not under "stack:",
// which the monitor resolves on every healthy pass.
const updateScope = "stack-update:"

// RunChecks checks the stacks' sources for new commits until ctx ends.
func (s *Service) RunChecks(ctx context.Context) {
	t := time.NewTimer(firstCheckAfter)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.CheckUpdates(ctx); err != nil {
				s.log.Warn("stack update check failed", "error", err)
			}
			t.Reset(checkEvery)
		}
	}
}

// CheckUpdates fetches every stack's source, which approves a new commit
// that asks for nothing new, and tells the operator which stacks have
// commits waiting. A source that cannot be read is logged and keeps what was
// fetched before.
func (s *Service) CheckUpdates(ctx context.Context) ([]Stack, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, err := s.Fetch(ctx, r.Name); err != nil {
			s.log.Warn("stack source not read", "stack", r.Name, "error", err)
		}
	}
	s.notifyUpdates(ctx)
	return s.List(ctx)
}

// notifyUpdates keeps one open problem per stack with commits waiting:
// either approved and not yet running, or waiting for approval. It reads the
// database only, so the monitor runs it every pass and a problem closes as
// soon as the stack is updated.
func (s *Service) notifyUpdates(ctx context.Context) {
	if s.notifier == nil {
		return
	}
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return
	}
	problems := []notifications.Problem{}
	for _, r := range rows {
		g, err := s.generation(ctx, r)
		if err != nil || g == nil {
			continue // never built: nothing is out of date yet
		}
		key := updateScope + r.Name
		switch {
		case r.FetchedRev != r.ApprovedRev:
			detail := fmt.Sprintf("Commit %s asks for more than was approved.", short(r.FetchedRev))
			if fetched, err := decode(r.FetchedManifest); err == nil {
				approved, _, _ := approvedManifest(r)
				var asks []string
				for _, n := range NewGrants(approved, fetched) {
					asks = append(asks, n.Kind+" "+n.Detail)
				}
				detail = fmt.Sprintf("Commit %s asks for: %s. Approve it with `homelab stacks approve %s %s`.",
					short(r.FetchedRev), strings.Join(asks, "; "), r.Name, r.FetchedRev)
				detail += s.waitingValues(r.Name, fetched)
			}
			problems = append(problems, notifications.Problem{
				Key: key, Title: fmt.Sprintf("Stack %s has an update that needs approval", title(r)), Detail: detail,
				Path: "/stacks/" + r.Name, Event: "stack.updates_available",
			})
		case pending(r, *g):
			detail := fmt.Sprintf("It runs %s; commit %s is approved. Update it with `homelab stacks update %s`.",
				short(g.Rev), short(r.ApprovedRev), r.Name)
			if g.Rev == r.ApprovedRev && g.NixpkgsRev != r.NixpkgsRev {
				detail = fmt.Sprintf("Its nixpkgs moved to %s. Update it with `homelab stacks update %s`.", short(r.NixpkgsRev), r.Name)
			} else if g.Rev == r.ApprovedRev {
				detail = fmt.Sprintf("The owners of its folders changed (now %s); it is built with them on its next update: `homelab stacks update %s`.",
					orNone(r.HostIds), r.Name)
			}
			if approved, _, err := approvedManifest(r); err == nil {
				if w := s.waitingValues(r.Name, approved); w != "" {
					detail = fmt.Sprintf("It runs %s; commit %s is approved.%s", short(g.Rev), short(r.ApprovedRev), w)
				}
			}
			problems = append(problems, notifications.Problem{
				Key: key, Title: fmt.Sprintf("Stack %s has an update", title(r)), Detail: detail,
				Path: "/stacks/" + r.Name, Event: "stack.updates_available",
			})
		}
	}
	if _, err := s.notifier.Reconcile(ctx, updateScope, problems); err != nil {
		s.log.Warn("stack update notifications not sent", "error", err)
	}
}

// waitingValues says which variables m requires that still need a value, as
// a sentence to append to a problem's detail; "" when none do.
func (s *Service) waitingValues(name string, m Manifest) string {
	missing, err := s.missingValues(name, m)
	if err != nil || len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf(" It waits for values of %s: set them on the stack's page or with `homelab stacks var set %s <NAME> --value-stdin`, then update it.",
		strings.Join(missing, ", "), name)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
