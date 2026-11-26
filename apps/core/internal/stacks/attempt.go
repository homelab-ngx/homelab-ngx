package stacks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/sysupdates"
)

// Kinds and results of a stored attempt (stack_updates).
const (
	attemptInstall  = "install"
	attemptUpdate   = "update"
	attemptRollback = "rollback"

	resultRunning     = "running"
	resultHealthy     = "healthy"
	resultRolledBack  = "rolled_back"
	resultBuildFailed = "build_failed"
	resultFailed      = "failed"
)

// maxAttemptLog caps the log kept per attempt. A failed nix build prints its
// last lines; the cap keeps the head, which names the step, and the tail,
// which has the error.
const maxAttemptLog = 256 << 10

// attemptLog is an attempt's log: every phase, and the output of the build.
// It keeps the first half of the cap and the most recent rest.
type attemptLog struct {
	head, tail strings.Builder
	dropped    int
}

func (l *attemptLog) line(s string) {
	s += "\n"
	if l.head.Len()+len(s) <= maxAttemptLog/2 {
		l.head.WriteString(s)
		return
	}
	l.tail.WriteString(s)
	if over := l.tail.Len() - maxAttemptLog/2; over > 0 {
		rest := l.tail.String()[over:]
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:]
		}
		l.dropped += l.tail.Len() - len(rest)
		l.tail.Reset()
		l.tail.WriteString(rest)
	}
}

func (l *attemptLog) String() string {
	if l.dropped == 0 {
		return l.head.String() + l.tail.String()
	}
	return fmt.Sprintf("%s[… %d bytes left out …]\n%s", l.head.String(), l.dropped, l.tail.String())
}

// startAttempt stores the attempt an operation makes. A failure to store it
// is logged, not fatal: the operation is what matters.
func (s *Service) startAttempt(ctx context.Context, o *operation, name, kind, rev string, from int64) {
	id, err := s.st.Queries().InsertStackUpdate(ctx, store.InsertStackUpdateParams{
		Stack: name, Kind: kind, Rev: rev, FromGeneration: from, Now: s.now().UnixMilli(),
	})
	if err != nil {
		s.log.Warn("stack attempt not recorded", "stack", name, "error", err)
		return
	}
	s.mu.Lock()
	o.attempt = id
	s.mu.Unlock()
}

// attemptBuilt stores the generation an attempt built and the packages that
// changed from the generation before it, which the operator reads while the
// switch and health check run.
func (s *Service) attemptBuilt(ctx context.Context, o *operation, to int64, fromSystem, toSystem string) {
	s.mu.Lock()
	id := o.attempt
	s.mu.Unlock()
	if id == 0 {
		return
	}
	changes := []sysupdates.UpdateChange{}
	if fromSystem != "" && fromSystem != toSystem {
		res, err := s.run(ctx, 2*time.Minute, "nix", "--extra-experimental-features", "nix-command",
			"store", "diff-closures", fromSystem, toSystem)
		if err != nil {
			s.logf(o, "package changes unavailable: %v", err)
		} else if c := sysupdates.ParseDiffClosures(res.Stdout); c != nil {
			changes = c
		}
	}
	b, _ := json.Marshal(changes)
	if err := s.st.Queries().SetStackUpdateBuilt(ctx, store.SetStackUpdateBuiltParams{
		ID: id, ToGeneration: to, Changes: string(b),
	}); err != nil {
		s.log.Warn("stack attempt not updated", "attempt", id, "error", err)
	}
}

// endAttempt stores how the operation's attempt ended. A daemon that is
// shutting down leaves it running; the next one marks it interrupted.
func (s *Service) endAttempt(ctx context.Context, o *operation, err error) {
	s.mu.Lock()
	id, result, log := o.attempt, o.result, o.log.String()
	s.mu.Unlock()
	if id == 0 || ctx.Err() != nil {
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
		if result == "" {
			result = resultFailed
		}
	} else if result == "" {
		result = resultHealthy
	}
	if ferr := s.st.Queries().FinishStackUpdate(ctx, store.FinishStackUpdateParams{
		ID: id, Now: s.now().UnixMilli(), Result: result, Reason: reason, Log: log,
	}); ferr != nil {
		s.log.Warn("stack attempt not finished", "attempt", id, "error", ferr)
	}
}

// setResult records an outcome that is not plain success or failure.
func (s *Service) setResult(o *operation, result string) {
	s.mu.Lock()
	o.result = result
	s.mu.Unlock()
}

// logf adds a line to the operation's attempt log.
func (s *Service) logf(o *operation, format string, a ...any) {
	s.mu.Lock()
	o.log.line(time.Now().Format("15:04:05 ") + fmt.Sprintf(format, a...))
	s.mu.Unlock()
}

// buildLogged builds the output fragment (#attr.path) of the flake
// reference url, with every output line in the attempt log, and returns its
// store path.
func (s *Service) buildLogged(ctx context.Context, o *operation, sshKey, url, fragment string, extra ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	ref, err := s.buildRef(ctx, url)
	if err != nil {
		return "", fmt.Errorf("building: %w", err)
	}
	args := []string{"--extra-experimental-features", "nix-command flakes",
		"build", "--no-link", "--print-out-paths", "--print-build-logs"}
	args = append(append(args, extra...), "--", ref+fragment)
	res, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "nix",
		Args: args, Env: s.nixEnvFor(sshKey),
		OnLine: func(_, line string) {
			s.mu.Lock()
			o.log.line(line)
			s.mu.Unlock()
		},
	})
	if err != nil {
		return "", fmt.Errorf("building: %w", err)
	}
	return buildPath(res.Stdout)
}

// Updates lists attempts, newest first; stack "" lists every stack's.
func (s *Service) Updates(ctx context.Context, stack string, limit int) ([]StackUpdate, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.st.Queries().ListStackUpdates(ctx, store.ListStackUpdatesParams{Stack: stack, Lim: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]StackUpdate, 0, len(rows))
	for _, r := range rows {
		out = append(out, attemptView(store.StackUpdate{
			ID: r.ID, Stack: r.Stack, Kind: r.Kind, Rev: r.Rev, FromGeneration: r.FromGeneration,
			ToGeneration: r.ToGeneration, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			Result: r.Result, Reason: r.Reason, Changes: r.Changes,
		}))
	}
	return out, nil
}

// UpdateAttempt returns one attempt with its log.
func (s *Service) UpdateAttempt(ctx context.Context, id int64) (StackUpdate, error) {
	r, err := s.st.Queries().GetStackUpdate(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return StackUpdate{}, httpx.NotFound("unknown_update", "no stack update %d", id)
	}
	if err != nil {
		return StackUpdate{}, err
	}
	v := attemptView(r)
	// A running attempt's log is in memory until it ends.
	if r.Result == resultRunning {
		s.mu.Lock()
		if o := s.ops[r.Stack]; o != nil && o.attempt == id {
			r.Log = o.log.String()
		}
		s.mu.Unlock()
	}
	v.Log = r.Log
	return v, nil
}

func attemptView(r store.StackUpdate) StackUpdate {
	v := StackUpdate{
		Id: r.ID, Stack: r.Stack, Kind: StackUpdateKind(r.Kind), Rev: r.Rev,
		FromGeneration: int(r.FromGeneration), ToGeneration: int(r.ToGeneration),
		StartedAt: time.UnixMilli(r.StartedAt), Result: StackUpdateResult(r.Result),
		Reason: r.Reason, Changes: []sysupdates.UpdateChange{},
	}
	if r.FinishedAt != 0 {
		t := time.UnixMilli(r.FinishedAt)
		v.FinishedAt = &t
	}
	if r.Changes != "" {
		_ = json.Unmarshal([]byte(r.Changes), &v.Changes)
	}
	if v.Changes == nil {
		v.Changes = []sysupdates.UpdateChange{}
	}
	return v
}
