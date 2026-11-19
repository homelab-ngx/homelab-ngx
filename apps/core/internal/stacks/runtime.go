package stacks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
)

// Notifier tells the operator about a stack that rolled back or did not
// start (*notifications.Notifier).
type Notifier interface {
	Send(ctx context.Context, m notifications.Message) (string, error)
	// Report and Resolve open and close a problem the operator is reminded
	// of until it is gone.
	Report(ctx context.Context, p notifications.Problem) (notifications.Result, error)
	Resolve(ctx context.Context, key string) (notifications.Result, error)
	// Reconcile sets every open problem under a key prefix at once.
	Reconcile(ctx context.Context, scope string, current []notifications.Problem) (notifications.Result, error)
}

// Operation kinds.
const (
	opStart    = "start"
	opStop     = "stop"
	opUpdate   = "update"
	opRollback = "rollback"
)

// operation is one start, stop, update or rollback of a stack. They take
// minutes (a build, a boot, a health check), so they run in the background
// and the API reports the latest one per stack.
type operation struct {
	kind     string
	phase    string
	started  time.Time
	finished time.Time
	err      string
	done     chan struct{}
	// attempt is the stack_updates row of an install, update or rollback;
	// 0 for anything else. result overrides the outcome finish derives.
	attempt int64
	result  string
	log     attemptLog
}

// begin registers an operation on the stack, or refuses when one is running.
func (s *Service) begin(name, kind string) (*operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.ops[name]; o != nil && o.finished.IsZero() {
		return nil, httpx.Conflict("busy", "stack %q is busy with a %s (%s)", name, o.kind, o.phase)
	}
	o := &operation{kind: kind, phase: "starting", started: s.now(), done: make(chan struct{})}
	s.ops[name] = o
	return o, nil
}

func (s *Service) phase(o *operation, format string, a ...any) {
	s.mu.Lock()
	o.phase = fmt.Sprintf(format, a...)
	o.log.line(s.now().Format("15:04:05 ") + o.phase)
	s.mu.Unlock()
}

func (s *Service) finish(name string, o *operation, err error) {
	if err != nil {
		s.logf(o, "error: %v", err)
	}
	s.mu.Lock()
	bg := s.ctx
	s.mu.Unlock()
	s.endAttempt(bg, o, err)
	if o.kind == opStart || o.kind == opUpdate || o.kind == opRollback {
		s.tidy(bg, name, o.kind)
	}
	s.mu.Lock()
	o.finished = s.now()
	if err != nil {
		o.err = err.Error()
	}
	s.mu.Unlock()
	close(o.done)
	if err != nil {
		s.log.Error("stack operation failed", "stack", name, "operation", o.kind, "phase", o.phase, "error", err)
	} else {
		s.log.Info("stack operation finished", "stack", name, "operation", o.kind, "result", o.phase)
	}
}

func (s *Service) operationView(name string) *StackOperation {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.ops[name]
	if o == nil {
		return nil
	}
	v := &StackOperation{Kind: StackOperationKind(o.kind), Phase: o.phase, StartedAt: o.started, Error: o.err}
	if !o.finished.IsZero() {
		t := o.finished
		v.FinishedAt = &t
	}
	return v
}

// launch checks that the stack exists, registers the operation and runs fn
// in the background. The answer is the stack with the operation on it.
func (s *Service) launch(ctx context.Context, name, kind string, check func(store.Stack) error, fn func(context.Context, *operation) error) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	if check != nil {
		if err := check(r); err != nil {
			return Stack{}, err
		}
	}
	o, err := s.begin(name, kind)
	if err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	bg := s.ctx
	s.mu.Unlock()
	go func() { s.finish(name, o, fn(bg, o)) }()
	return s.Get(ctx, name)
}

func notApproved(name string) *httpx.Problem {
	return httpx.Conflict("not_approved", "stack %q has no approved commit; approve it first", name)
}

// Start boots the stack on its current generation, building the approved
// commit first when nothing is built.
func (s *Service) Start(ctx context.Context, name string) (Stack, error) {
	return s.launch(ctx, name, opStart, func(r store.Stack) error {
		if r.Generation == 0 && r.ApprovedRev == "" {
			return notApproved(name)
		}
		if r.Generation == 0 {
			return s.variablesGate(r)
		}
		return nil
	}, func(ctx context.Context, o *operation) error { return s.start(ctx, o, name) })
}

// Stop shuts the stack down and takes its routes and rules away.
func (s *Service) Stop(ctx context.Context, name string) (Stack, error) {
	return s.launch(ctx, name, opStop, nil,
		func(ctx context.Context, o *operation) error { return s.stop(ctx, o, name) })
}

// Update builds the approved commit and switches the stack to it. An
// unhealthy result is rolled back.
func (s *Service) Update(ctx context.Context, name string) (Stack, error) {
	return s.launch(ctx, name, opUpdate, func(r store.Stack) error {
		if r.ApprovedRev == "" {
			return notApproved(name)
		}
		if err := s.variablesGate(r); err != nil {
			return err
		}
		if g, err := s.generation(ctx, r); err != nil {
			return err
		} else if g != nil && !pending(r, *g) {
			return httpx.Conflict("up_to_date", "stack %q already runs the approved commit %s", name, r.ApprovedRev)
		}
		return nil
	}, func(ctx context.Context, o *operation) error { return s.update(ctx, o, name) })
}

// Rollback switches the stack to generation to, or to the generation before
// the current one when to is 0.
func (s *Service) Rollback(ctx context.Context, name string, to int64) (Stack, error) {
	return s.launch(ctx, name, opRollback, func(r store.Stack) error {
		_, err := s.target(ctx, r, to)
		return err
	}, func(ctx context.Context, o *operation) error { return s.rollback(ctx, o, name, to) })
}

// target is the generation a rollback goes to: to, or the previous one.
func (s *Service) target(ctx context.Context, r store.Stack, to int64) (store.StackGeneration, error) {
	if to == 0 {
		return s.previous(ctx, r)
	}
	unavailable := func(why string) error {
		return httpx.Conflict("generation_unavailable", "stack %q cannot go back to generation %d: %s", r.Name, to, why)
	}
	if to == r.Generation {
		return store.StackGeneration{}, unavailable("it is the current one")
	}
	g, err := s.st.Queries().GetStackGeneration(ctx, store.GetStackGenerationParams{Stack: r.Name, ID: to})
	if errors.Is(err, sql.ErrNoRows) {
		return store.StackGeneration{}, unavailable("it was never built")
	}
	if err != nil {
		return store.StackGeneration{}, err
	}
	if g.Failed != 0 {
		return store.StackGeneration{}, unavailable("it was rolled back from as unhealthy")
	}
	return g, nil
}

// UpdateAll updates, one after another in the background, every installed
// stack whose approved commit is not the one it runs and whose required
// variables all have values. A stack waiting for approval has an older
// approved commit, so it is not among them. It returns the stacks it will update, in order.
func (s *Service) UpdateAll(ctx context.Context) ([]string, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	queued := []string{}
	for _, r := range rows {
		if r.ApprovedRev == "" || r.Generation == 0 {
			continue
		}
		g, err := s.generation(ctx, r)
		if err != nil || g == nil || !pending(r, *g) {
			continue
		}
		if s.variablesGate(r) != nil {
			continue
		}
		queued = append(queued, r.Name)
	}
	s.mu.Lock()
	bg := s.ctx
	s.mu.Unlock()
	go func() {
		for _, name := range queued {
			if bg.Err() != nil {
				return
			}
			s.updateAndWait(bg, name)
		}
	}()
	return queued, nil
}

// updateAndWait starts an update of one stack and waits for it to end. A
// stack the monitor is checking is busy for a moment, so a refusal for that
// is tried again.
func (s *Service) updateAndWait(ctx context.Context, name string) {
	for try := 0; ; try++ {
		_, err := s.Update(ctx, name)
		var p *httpx.Problem
		if errors.As(err, &p) && p.Code == "busy" && try < 30 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err != nil {
			s.log.Warn("update all: stack not updated", "stack", name, "error", err)
			return
		}
		break
	}
	s.mu.Lock()
	o := s.ops[name]
	s.mu.Unlock()
	if o != nil {
		select {
		case <-o.done:
		case <-ctx.Done():
		}
	}
}

// StartWanted starts every stack the operator left running, and finishes
// any install or update the last daemon was interrupted in. The daemon calls
// it once at startup, after the pools are mounted: after a reboot it boots
// the stacks, after a daemon restart it finds them running and only puts
// their rules and routes back. It also makes ctx the lifetime of later
// operations.
func (s *Service) StartWanted(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	if err := s.st.Queries().InterruptStackUpdates(ctx, s.now().UnixMilli()); err != nil {
		s.log.Warn("cannot close interrupted stack attempts", "error", err)
	}
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		s.log.Error("cannot list stacks to start", "error", err)
		return
	}
	for _, r := range rows {
		if r.OpKind != "" {
			if o, err := s.begin(r.Name, r.OpKind); err == nil {
				s.finish(r.Name, o, s.resume(ctx, o, r))
			}
			continue
		}
		if r.WantRunning == 0 {
			continue
		}
		o, err := s.begin(r.Name, opStart)
		if err != nil {
			continue
		}
		err = s.start(ctx, o, r.Name)
		s.finish(r.Name, o, err)
		if err != nil && ctx.Err() == nil {
			s.notify(ctx, r.Name, "Stack "+r.Name+" did not start", err.Error())
		}
	}
}

func (s *Service) notify(ctx context.Context, name, title, body string) {
	if s.notifier == nil {
		return
	}
	if _, err := s.notifier.Send(ctx, notifications.Message{
		Severity: notifications.Alert, Title: title, Body: body, Path: "/stacks/" + name, Key: "stack:" + name,
	}); err != nil {
		s.log.Warn("stack notification not delivered", "stack", name, "error", err)
	}
}

// generation returns the stack's current generation, or nil when nothing is
// built.
func (s *Service) generation(ctx context.Context, r store.Stack) (*store.StackGeneration, error) {
	if r.Generation == 0 {
		return nil, nil
	}
	g, err := s.st.Queries().GetStackGeneration(ctx, store.GetStackGenerationParams{Stack: r.Name, ID: r.Generation})
	if err != nil {
		return nil, fmt.Errorf("stack %s: generation %d: %w", r.Name, r.Generation, err)
	}
	return &g, nil
}

// previous returns the generation a rollback goes to.
func (s *Service) previous(ctx context.Context, r store.Stack) (store.StackGeneration, error) {
	g, err := s.st.Queries().PreviousStackGeneration(ctx, store.PreviousStackGenerationParams{Stack: r.Name, ID: r.Generation})
	if errors.Is(err, sql.ErrNoRows) {
		return store.StackGeneration{}, httpx.Conflict("no_previous_generation",
			"stack %q has no earlier generation to go back to", r.Name)
	}
	return g, err
}

// buildApproved builds the approved commit into a new generation of the
// stack's profile and makes it the current one.
func (s *Service) buildApproved(ctx context.Context, o *operation, r store.Stack) (store.StackGeneration, error) {
	if r.ApprovedRev == "" {
		return store.StackGeneration{}, notApproved(r.Name)
	}
	m, err := decode(r.ApprovedManifest)
	if err != nil {
		return store.StackGeneration{}, err
	}
	if m.Version < MinBuildVersion {
		s.setResult(o, resultBuildFailed)
		return store.StackGeneration{}, httpx.Conflict("manifest_too_old",
			"stack %q is approved at manifest version %d; stacks are built from version %d on, so fetch and approve a newer commit", r.Name, m.Version, MinBuildVersion)
	}
	// Until the profile names the build, only this keeps a collection of
	// the store's garbage away from it.
	defer nixGate.hold()()
	nixpkgsRev, err := s.nixpkgsFor(ctx, r, m)
	if err != nil {
		s.setResult(o, resultBuildFailed)
		return store.StackGeneration{}, err
	}
	s.phase(o, "building %s with nixpkgs %s", short(r.ApprovedRev), short(nixpkgsRev))
	system, hostIDs, err := s.buildWrapped(ctx, o, r, m, nixpkgsRev)
	if err != nil {
		s.setResult(o, resultBuildFailed)
		return store.StackGeneration{}, err
	}
	id, err := s.setProfile(ctx, r.Name, system)
	if err != nil {
		return store.StackGeneration{}, err
	}
	g := store.StackGeneration{
		Stack: r.Name, ID: id, Rev: r.ApprovedRev, Url: r.ApprovedUrl,
		Manifest: r.ApprovedManifest, System: system, BuiltAt: s.now().UnixMilli(), NixpkgsRev: nixpkgsRev,
		HostIds: hostIDs,
	}
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.UpsertStackGeneration(ctx, store.UpsertStackGenerationParams{
			Stack: g.Stack, ID: g.ID, Rev: g.Rev, Url: g.Url, Manifest: g.Manifest, System: g.System, Now: g.BuiltAt,
			NixpkgsRev: g.NixpkgsRev, HostIds: g.HostIds,
		}); err != nil {
			return err
		}
		if err := q.SetStackHostIDs(ctx, store.SetStackHostIDsParams{Name: r.Name, HostIds: g.HostIds}); err != nil {
			return err
		}
		if err := q.SetStackGeneration(ctx, store.SetStackGenerationParams{Name: r.Name, Generation: id}); err != nil {
			return err
		}
		// From here the new generation is the current one, and an
		// interrupted operation has something to verify or undo.
		return q.SetStackOpStep(ctx, store.SetStackOpStepParams{Name: r.Name, Step: stepActivating})
	})
	return g, err
}

// ensureSubnet returns the stack's subnet, allocating one on first use.
func (s *Service) ensureSubnet(ctx context.Context, name string) (string, error) {
	var subnet string
	err := s.st.Tx(ctx, func(q *store.Queries) error {
		rows, err := q.ListStacks(ctx)
		if err != nil {
			return err
		}
		used := make([]string, 0, len(rows))
		for _, r := range rows {
			if r.Name == name && r.Subnet != "" {
				subnet = r.Subnet
				return nil
			}
			used = append(used, r.Subnet)
		}
		if subnet, err = allocSubnet(used); err != nil {
			return err
		}
		return q.SetStackSubnet(ctx, store.SetStackSubnetParams{Name: name, Subnet: subnet})
	})
	return subnet, err
}

// connect puts the host half of a running stack in place: firewall, veth
// address, routes. Every step replaces what is there, so it is also how a
// changed manifest takes effect.
func (s *Service) connect(ctx context.Context, name, subnet string, m Manifest) error {
	if err := s.applyRules(ctx, name, subnet, m); err != nil {
		return err
	}
	if err := s.linkUp(ctx, name, subnet); err != nil {
		return err
	}
	return s.writeRoutes(ctx, name, stackAddr(subnet), m.Routes)
}

func (s *Service) start(ctx context.Context, o *operation, name string) error {
	r, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	g, err := s.generation(ctx, r)
	if err != nil {
		return err
	}
	installed := g == nil
	if g == nil {
		// An install. A plain start of something already built is not
		// stored: nothing about it needs finishing or undoing.
		if err := s.recordOp(ctx, name, opStart, nil); err != nil {
			return err
		}
		defer s.clearOp(ctx, name)
		if o.attempt == 0 {
			s.startAttempt(ctx, o, name, attemptInstall, r.ApprovedRev, 0)
		}
		built, err := s.buildApproved(ctx, o, r)
		if err != nil {
			return err
		}
		s.attemptBuilt(ctx, o, built.ID, "", built.System)
		g = &built
	}
	m, err := decode(g.Manifest)
	if err != nil {
		return err
	}
	subnet, err := s.ensureSubnet(ctx, name)
	if err != nil {
		return err
	}
	if !s.isRunning(ctx, name) {
		s.phase(o, "starting the container")
		if err := s.prepare(ctx, name, subnet); err != nil {
			return err
		}
		// The rules go in before the container exists, so its interface
		// never carries traffic without them; boot gives it its address.
		if err := s.applyRules(ctx, name, subnet, m); err != nil {
			return err
		}
		if err := s.boot(ctx, name, subnet, m); err != nil {
			return err
		}
	}
	if err := s.connect(ctx, name, subnet, m); err != nil {
		return err
	}
	if err := s.st.Queries().SetStackWantRunning(ctx, store.SetStackWantRunningParams{Name: name, Want: 1}); err != nil {
		return err
	}
	s.phase(o, "waiting for it to be healthy")
	if err := s.waitHealthy(ctx, name, stackAddr(subnet), m.Health); err != nil {
		return fmt.Errorf("the stack is started but not healthy: %w", err)
	}
	s.phase(o, "running generation %d (%s)", g.ID, short(g.Rev))
	if installed {
		s.backupFoldersAdded(ctx, name, nil, *g)
	}
	return nil
}

func (s *Service) stop(ctx context.Context, o *operation, name string) error {
	if err := s.st.Queries().SetStackWantRunning(ctx, store.SetStackWantRunningParams{Name: name, Want: 0}); err != nil {
		return err
	}
	s.phase(o, "stopping the container")
	// The routes go even when the container does not stop. The firewall
	// stays until it has: the host trusts the stack's interface, so a
	// container that outlives its rules could reach everything.
	err := errors.Join(s.removeRoutes(ctx, name), s.halt(ctx, name))
	if !s.isStopped(ctx, name) {
		return errors.Join(err, errors.New("the container is still running; its firewall rules are left in place"))
	}
	if err = errors.Join(err, s.removeRules(ctx, name)); err == nil {
		s.phase(o, "stopped")
	}
	return err
}

// activate moves a running stack from the manifest it was booted under to
// generation g, which the profile already points at. The system is switched
// in place; the container is restarted instead when its arguments changed
// (binds only a new container gets), or when the switch fails. The arguments
// are compared with the ones the container was booted with, so a path value
// changed since is applied too; a container booted before they were recorded
// is compared with what from gives.
func (s *Service) activate(ctx context.Context, o *operation, r store.Stack, from Manifest, g store.StackGeneration) error {
	m, err := decode(g.Manifest)
	if err != nil {
		return err
	}
	name := r.Name
	// The record is read again: an earlier activation in this operation
	// (the one a rollback undoes) may have booted the container since r.
	cur, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	booted := bootedArgs(cur)
	if booted == nil {
		if booted, err = s.bootArgs(name, from); err != nil {
			return err
		}
	}
	next, err := s.bootArgs(name, m)
	if err != nil {
		return fmt.Errorf("binding the stack's paths: %w", err)
	}
	// What /run/secrets holds can depend on the manifest version (from
	// version 7 the env file carries no secrets), and a switch in place
	// boots nothing that would render it: render it for the generation
	// activated, before its services start.
	if m.Version >= 4 {
		if err := s.renderVars(ctx, name, m); err != nil {
			return fmt.Errorf("delivering the stack's variables: %w", err)
		}
	}
	restart := !slices.Equal(booted, next)
	if !restart {
		s.phase(o, "switching to generation %d", g.ID)
		// A switch that fails leaves the system between two generations.
		// Booting the target generation is the defined state to judge.
		if err := s.switchSystem(ctx, name); err != nil {
			s.log.Warn("switching in place failed; restarting the container instead", "stack", name, "generation", g.ID, "error", err)
			restart = true
		}
	}
	if restart {
		s.phase(o, "restarting the container on generation %d", g.ID)
		if err := s.halt(ctx, name); err != nil {
			return err
		}
		// The link gets its address during the boot, so the new
		// generation's rules go in first.
		if err := s.applyRules(ctx, name, r.Subnet, m); err != nil {
			return err
		}
		if err := s.boot(ctx, name, r.Subnet, m); err != nil {
			return err
		}
	}
	if err := s.connect(ctx, name, r.Subnet, m); err != nil {
		return err
	}
	s.phase(o, "waiting for generation %d to be healthy", g.ID)
	return s.waitHealthy(ctx, name, stackAddr(r.Subnet), m.Health)
}

func (s *Service) update(ctx context.Context, o *operation, name string) error {
	r, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	prev, err := s.generation(ctx, r)
	if err != nil {
		return err
	}
	if err := s.recordOp(ctx, name, opUpdate, prev); err != nil {
		return err
	}
	var fromGen int64
	var fromSystem string
	if prev != nil {
		fromGen, fromSystem = prev.ID, prev.System
	}
	s.startAttempt(ctx, o, name, attemptUpdate, r.ApprovedRev, fromGen)
	g, err := s.buildApproved(ctx, o, r)
	if err != nil {
		s.clearOp(ctx, name)
		return err
	}
	s.attemptBuilt(ctx, o, g.ID, fromSystem, g.System)
	if prev == nil || !s.isRunning(ctx, name) {
		s.clearOp(ctx, name)
		s.phase(o, "built generation %d (%s); the stack is not running", g.ID, short(g.Rev))
		return nil
	}
	from, err := decode(prev.Manifest)
	if err != nil {
		return err
	}
	return s.settle(ctx, o, r, g, prev, s.activate(ctx, o, r, from, g))
}

// settle ends an install or update whose activation of g returned cause: a
// healthy generation stays, an unhealthy one is rolled back to prev. A
// daemon that is shutting down does neither; the stored operation makes the
// next daemon finish it.
func (s *Service) settle(ctx context.Context, o *operation, r store.Stack, g store.StackGeneration, prev *store.StackGeneration, cause error) error {
	if cause == nil {
		s.clearOp(ctx, r.Name)
		s.phase(o, "running generation %d (%s)", g.ID, short(g.Rev))
		s.backupFoldersAdded(ctx, r.Name, prev, g)
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("interrupted while activating generation %d (%s): %w", g.ID, short(g.Rev), ctx.Err())
	}
	if prev == nil {
		s.clearOp(ctx, r.Name)
		return cause
	}
	err := s.revert(ctx, o, r, g, *prev, cause)
	s.clearOp(ctx, r.Name)
	return err
}

// revert takes a running stack from the unhealthy generation g back to
// prev, the generation that ran before. g is marked failed and is never a
// rollback target again. The returned error says what happened; it is never
// nil.
func (s *Service) revert(ctx context.Context, o *operation, r store.Stack, g, prev store.StackGeneration, cause error) error {
	name := r.Name
	s.log.Warn("stack update is unhealthy; rolling back", "stack", name, "generation", g.ID, "error", cause)
	back := func() error {
		failed, err := decode(g.Manifest)
		if err != nil {
			return err
		}
		// A commit that builds the same system (only the manifest changed)
		// reuses the generation, so there is no other generation to go to:
		// the row gets the commit and manifest that ran before.
		if g.ID == prev.ID {
			if err := s.st.Queries().UpsertStackGeneration(ctx, store.UpsertStackGenerationParams{
				Stack: name, ID: prev.ID, Rev: prev.Rev, Url: prev.Url, Manifest: prev.Manifest, System: prev.System, Now: prev.BuiltAt,
				NixpkgsRev: prev.NixpkgsRev, HostIds: prev.HostIds,
			}); err != nil {
				return err
			}
			return s.activate(ctx, o, r, failed, prev)
		}
		if err := s.st.Queries().MarkStackGenerationFailed(ctx, store.MarkStackGenerationFailedParams{Stack: name, ID: g.ID}); err != nil {
			return err
		}
		if err := s.switchProfile(ctx, name, prev.ID); err != nil {
			return err
		}
		if err := s.st.Queries().SetStackGeneration(ctx, store.SetStackGenerationParams{Name: name, Generation: prev.ID}); err != nil {
			return err
		}
		return s.activate(ctx, o, r, failed, prev)
	}
	if err := back(); err != nil {
		err = fmt.Errorf("generation %d (%s) was not healthy (%w), and going back to generation %d failed: %w",
			g.ID, short(g.Rev), cause, prev.ID, err)
		s.notify(ctx, name, "Stack "+name+": update failed and so did the rollback", err.Error())
		return err
	}
	s.setResult(o, resultRolledBack)
	err := fmt.Errorf("generation %d (%s) was not healthy (%w); rolled back to generation %d (%s)",
		g.ID, short(g.Rev), cause, prev.ID, short(prev.Rev))
	if g.ID == prev.ID {
		err = fmt.Errorf("commit %s was not healthy (%w); generation %d is back on commit %s", short(g.Rev), cause, prev.ID, short(prev.Rev))
	}
	s.notify(ctx, name, "Stack "+name+": update rolled back", err.Error())
	return err
}

// Steps of a stored operation.
const (
	stepBuilding   = "building"
	stepActivating = "activating"
)

// recordOp stores an install or update before it changes anything, with the
// generation to go back to.
func (s *Service) recordOp(ctx context.Context, name, kind string, prev *store.StackGeneration) error {
	p := ""
	if prev != nil {
		b, err := json.Marshal(prev)
		if err != nil {
			return err
		}
		p = string(b)
	}
	return s.st.Queries().SetStackOp(ctx, store.SetStackOpParams{
		Name: name, Kind: kind, Step: stepBuilding, Prev: p, Now: s.now().UnixMilli(),
	})
}

// clearOp forgets the stored operation once it has ended. While the daemon
// is shutting down it stays, so the next daemon finishes it.
func (s *Service) clearOp(ctx context.Context, name string) {
	if ctx.Err() != nil {
		return
	}
	if err := s.st.Queries().ClearStackOp(ctx, name); err != nil {
		s.log.Warn("could not clear the stored stack operation", "stack", name, "error", err)
	}
}

// resume finishes the install or update a daemon restart interrupted. One
// that was still building is abandoned with nothing changed. One that had
// switched generations is activated again and health-checked, and rolled
// back like any update when it is not healthy.
func (s *Service) resume(ctx context.Context, o *operation, r store.Stack) error {
	name := r.Name
	var prev *store.StackGeneration
	if r.OpPrev != "" {
		prev = &store.StackGeneration{}
		if err := json.Unmarshal([]byte(r.OpPrev), prev); err != nil {
			return fmt.Errorf("stored operation: %w", err)
		}
	}
	s.log.Info("finishing an interrupted stack operation", "stack", name, "operation", r.OpKind, "step", r.OpStep)
	if r.OpStep != stepActivating {
		// The profile may already point at the new build; the database does not.
		if prev != nil {
			if err := s.switchProfile(ctx, name, prev.ID); err != nil {
				return err
			}
		}
		s.clearOp(ctx, name)
		if r.OpKind == opStart || r.WantRunning != 0 {
			return s.start(ctx, o, name)
		}
		s.phase(o, "the interrupted %s was abandoned before anything changed", r.OpKind)
		return nil
	}
	cur, err := s.generation(ctx, r)
	if err != nil {
		return err
	}
	running := s.isRunning(ctx, name)
	if cur == nil || (!running && r.WantRunning == 0 && r.OpKind != opStart) {
		s.clearOp(ctx, name)
		s.phase(o, "the interrupted %s had nothing left to do", r.OpKind)
		return nil
	}
	var cause error
	if running && prev != nil {
		from, err := decode(prev.Manifest)
		if err != nil {
			return err
		}
		cause = s.activate(ctx, o, r, from, *cur)
	} else {
		cause = s.start(ctx, o, name)
	}
	err = s.settle(ctx, o, r, *cur, prev, cause)
	if err != nil && prev == nil && ctx.Err() == nil {
		s.notify(ctx, name, "Stack "+name+" did not start", err.Error())
	}
	return err
}

func (s *Service) rollback(ctx context.Context, o *operation, name string, to int64) error {
	r, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	cur, err := s.generation(ctx, r)
	if err != nil {
		return err
	}
	prev, err := s.target(ctx, r, to)
	if err != nil {
		return err
	}
	s.startAttempt(ctx, o, name, attemptRollback, prev.Rev, r.Generation)
	curSystem := ""
	if cur != nil {
		curSystem = cur.System
	}
	s.attemptBuilt(ctx, o, prev.ID, curSystem, prev.System)
	if err := s.switchProfile(ctx, name, prev.ID); err != nil {
		return err
	}
	if err := s.st.Queries().SetStackGeneration(ctx, store.SetStackGenerationParams{Name: name, Generation: prev.ID}); err != nil {
		return err
	}
	if !s.isRunning(ctx, name) {
		s.phase(o, "back on generation %d (%s); the stack is not running", prev.ID, short(prev.Rev))
		return nil
	}
	from, err := decode(cur.Manifest)
	if err != nil {
		return err
	}
	if err := s.activate(ctx, o, r, from, prev); err != nil {
		return fmt.Errorf("generation %d is not healthy: %w", prev.ID, err)
	}
	s.phase(o, "running generation %d (%s)", prev.ID, short(prev.Rev))
	return nil
}

// forget removes what the runtime keeps for a stack outside the database:
// its routes, its rules and its profile, which leaves its generations to
// the next collection of the store (nixstore.go). The state directory
// stays: it holds the stack's data.
func (s *Service) forget(ctx context.Context, name string) error {
	return errors.Join(s.removeRoutes(ctx, name), s.removeRules(ctx, name), os.RemoveAll(s.profileDir(name)))
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// Monitor watches the stacks the operator left running until ctx ends. A
// stack whose container is gone is started again. A stack whose health
// check fails unhealthyAfter times in a row is reported as a problem, which
// closes when the check passes again. Stacks with an operation running are
// left to that operation.
func (s *Service) Monitor(ctx context.Context) {
	fails := map[string]int{}
	t := time.NewTicker(s.monitorEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.monitorOnce(ctx, fails)
		}
	}
}

// unhealthyAfter is how many failed checks in a row make a stack a problem:
// one failed probe is a restart inside the stack more often than an outage.
const unhealthyAfter = 3

func (s *Service) monitorOnce(ctx context.Context, fails map[string]int) {
	// Starting and stopping change want_running; the lock follows here, and
	// a finished update closes its "commits waiting" problem.
	s.writeLock(ctx)
	s.notifyUpdates(ctx)
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		s.log.Warn("stack monitor cannot list stacks", "error", err)
		return
	}
	for _, r := range rows {
		name, key := r.Name, "stack:"+r.Name
		if r.WantRunning == 0 {
			delete(fails, name)
			continue
		}
		// The check holds the stack's operation slot so no start, stop or
		// update runs under it. A plain health check then gives the slot
		// back with the operator's last operation in it; a start of a dead
		// stack is an operation of its own and stays.
		s.mu.Lock()
		last := s.ops[name]
		s.mu.Unlock()
		o, err := s.begin(name, opStart)
		if err != nil {
			continue // an operation is running; it reports its own outcome
		}
		giveBack := func() {
			s.mu.Lock()
			if last != nil {
				s.ops[name] = last
			} else {
				delete(s.ops, name)
			}
			s.mu.Unlock()
			close(o.done)
		}
		// rows is as old as the pass, and a start earlier in it can take
		// minutes. A stop or removal that finished since then holds for
		// as long as the stack's slot is held, so the row is read again.
		if r, err = s.row(ctx, name); err != nil || r.WantRunning == 0 {
			giveBack()
			delete(fails, name)
			continue
		}
		if !s.isRunning(ctx, name) {
			s.log.Warn("stack is not running; starting it", "stack", name)
			err = s.start(ctx, o, name)
			s.finish(name, o, err)
		} else {
			err = s.probeStack(ctx, r)
			giveBack()
		}
		if ctx.Err() != nil || s.notifier == nil {
			continue
		}
		if err == nil {
			if fails[name] >= unhealthyAfter {
				s.log.Info("stack is healthy again", "stack", name)
			}
			delete(fails, name)
			if _, err := s.notifier.Resolve(ctx, key); err != nil {
				s.log.Warn("stack problem not resolved", "stack", name, "error", err)
			}
			continue
		}
		fails[name]++
		if fails[name] < unhealthyAfter {
			continue
		}
		s.log.Warn("stack is unhealthy", "stack", name, "error", err)
		if _, rerr := s.notifier.Report(ctx, notifications.Problem{
			Key: key, Title: "Stack " + title(r) + " is unhealthy", Detail: err.Error(), Path: "/stacks/" + name,
		}); rerr != nil {
			s.log.Warn("stack problem not reported", "stack", name, "error", rerr)
		}
	}
}

// probeStack runs the health check of the stack's current generation once.
func (s *Service) probeStack(ctx context.Context, r store.Stack) error {
	g, err := s.generation(ctx, r)
	if err != nil || g == nil {
		return err
	}
	m, err := decode(g.Manifest)
	if err != nil {
		return err
	}
	return s.checkHealth(ctx, r.Name, stackAddr(r.Subnet), m.Health)
}
