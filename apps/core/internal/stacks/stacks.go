// Package stacks is the registry of service stacks: separate NixOS systems,
// each built from its own repo. It stores which stacks are installed, the
// commit each one's source was last read at, and the commit whose grants the
// operator approved, and it runs them: each stack is built into its own
// system profile and booted under systemd-nspawn on a private network, with
// the binds, firewall rules and Caddy routes its approved manifest grants.
package stacks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/services"
	"homelab-ngx.com/core/internal/store"
)

// Source reads the newest commit of a stack's repo (NixSource).
type Source interface {
	Fetch(ctx context.Context, source string) (Fetched, error)
}

// Service owns the stacks tables and the running stacks.
type Service struct {
	st      *store.Store
	src     Source
	history History
	host    Host
	// hostMu guards host.DashboardURL and the host networks (LAN, VPN, Tailnet), which the host settings change.
	hostMu sync.RWMutex
	// netMu is held while a stack's firewall is loaded or removed, so rules
	// loaded for the host's new networks are never replaced by rules an
	// operation worked out from the old ones.
	netMu    sync.Mutex
	r        cmdrunner.Runner
	notifier Notifier
	log      *slog.Logger
	now      func() time.Time

	// probe answers the manifest's HTTP health check; a seam for tests.
	probe         func(ctx context.Context, url string) error
	healthTimeout time.Duration
	healthEvery   time.Duration
	monitorEvery  time.Duration

	mu      sync.Mutex
	ops     map[string]*operation // latest operation per stack
	units   []Unit                // the last Units answer
	unitsAt time.Time
	usageFn func(key string) *services.Usage // the metrics sampler's latest figures
	ctx     context.Context                  // lifetime of background operations
	// goingDown: the host is shutting down. No operation begins and no
	// container boots (power.go).
	goingDown bool

	auth apiAuth // the stack listener's callers

	varsMu sync.Mutex // held while a stack's variables are read or written
	// statDir checks that a path value is a directory on the host; a seam
	// for tests.
	statDir func(path string) error
	// resolveNixpkgs is the commit a manifest's nixpkgs names now; a seam
	// for tests, which have no network.
	resolveNixpkgs func(ctx context.Context, spec string) (string, error)
	// nixpkgsRelease reads a nixpkgs commit's release and date; a seam for
	// tests, which have no network.
	nixpkgsRelease func(ctx context.Context, rev string) (NixpkgsRelease, error)
	// timeZone is the host's time zone, handed to version 7 stacks.
	timeZone func() string
	// statOwner reads who owns a host directory; a seam for tests.
	statOwner func(path string) (uid, gid uint32, err error)
	// chown sets a folder's owner (-1 leaves one as it is), with everything
	// in it when recursive; a seam for tests.
	chown func(ctx context.Context, path string, uid, gid int, recursive bool) error
	// places is where a folder can be made for a path variable; nil: nowhere.
	places Places
	idMu   sync.Mutex // held while a stack id is looked up or given
	// listChannels reads the nixpkgs channels offered; a seam for tests.
	listChannels func(ctx context.Context) ([]string, error)
	// channels is the last list read, at channelsAt; guarded by mu.
	channels   []string
	channelsAt time.Time

	// statfs reads a file system's size and free space; a seam for tests.
	statfs func(path string) (total, free uint64, err error)
	// storeRec is what is known of the Nix store's size and its last
	// collection (nixstore.go); guarded by storeMu.
	storeMu  sync.Mutex
	storeRec *storeRecord
	// diskRecs is each stack's last measurement of its services' disk use
	// (disk.go); guarded by diskMu. diskWalk is held while a root is walked,
	// so two measurements never read the disk at once.
	diskMu   sync.Mutex
	diskRecs map[string]diskRecord
	diskWalk sync.Mutex

	// bootID is this boot's id (/proc/sys/kernel/random/boot_id); a seam
	// for tests. StartWanted applies autostart once per boot.
	bootID func() string

	// backupAdded hears of recommended backup folders a healthy install or
	// update brought (OnBackupFoldersAdded); guarded by mu.
	backupAdded func(ctx context.Context, stack string, keys []string)

	trees *treeCache // each commit's tree in the store (tree.go)
	icons *iconCache // the stacks' and services' icons (iconserve.go)
}

// New wires the service. notifier may be nil.
func New(st *store.Store, src Source, host Host, notifier Notifier, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		st: st, src: src, host: host, r: host.Runner, notifier: notifier, log: log, now: time.Now,
		probe: httpProbe, healthTimeout: 5 * time.Minute, healthEvery: 3 * time.Second, monitorEvery: time.Minute,
		ops: map[string]*operation{}, ctx: context.Background(),
		statDir: isDir, timeZone: HostTimeZone, statOwner: statOwner, statfs: diskRoom, bootID: readBootID,
	}
	s.chown = chownPath(host.Runner)
	s.trees = newTreeCache(func(ctx context.Context, url string) (string, error) {
		f, err := prefetch(ctx, s.r, url)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(f.StorePath, "/nix/store/") {
			return "", errors.New("nix flake prefetch: no store path for the commit")
		}
		return f.StorePath, nil
	})
	s.icons = newIconCache(host.IconsDir, s.trees, log)
	s.resolveNixpkgs = func(ctx context.Context, spec string) (string, error) { return ResolveNixpkgs(ctx, s.r, spec) }
	s.listChannels = func(ctx context.Context) ([]string, error) { return ListChannels(ctx, s.r) }
	s.nixpkgsRelease = func(ctx context.Context, rev string) (NixpkgsRelease, error) {
		return ReadNixpkgsRelease(ctx, s.r, rev)
	}
	if host.MirrorsDir != "" {
		s.history = gitHistory{r: host.Runner, dir: host.MirrorsDir, tokenFiles: host.GitHubTokenFiles}
	}
	return s
}

// List returns every stack, sorted by name.
func (s *Service) List(ctx context.Context) ([]Stack, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Stack, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one stack.
func (s *Service) Get(ctx context.Context, name string) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	return s.view(ctx, r)
}

// Add installs the stack source describes under name, or under the name its
// manifest suggests when name is empty. Nothing is approved, built or
// started; the variables its manifest declares can be filled in before the
// first build.
func (s *Service) Add(ctx context.Context, source, name string) (Stack, error) {
	if !ValidSource(source) {
		return Stack{}, httpx.BadRequest("invalid_source",
			"%q is not a git flake reference (github:owner/repo, git+ssh://, git+https:// or git+file://)", source)
	}
	if name != "" && !ValidName(name) {
		return Stack{}, httpx.BadRequest("invalid_name",
			"%q is not a stack name: use 1-32 characters of a-z, 0-9 and '-', starting with a letter", name)
	}
	f, m, err := s.fetch(ctx, source, name)
	if err != nil {
		return Stack{}, err
	}
	if name == "" {
		name = m.Name
	}
	if _, err := s.st.Queries().GetStack(ctx, name); err == nil {
		return Stack{}, httpx.Conflict("stack_exists", "a stack named %q is already installed", name)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Stack{}, err
	}
	if err := s.ifaceFree(ctx, name); err != nil {
		return Stack{}, err
	}
	if err := s.st.Queries().InsertStack(ctx, store.InsertStackParams{
		Name: name, Source: source, Now: s.now().UnixMilli(),
		Rev: f.Rev, Url: f.URL, Manifest: encode(m),
	}); err != nil {
		return Stack{}, err
	}
	s.storeInfo(ctx, name, s.learn(ctx, name, source, f, ""))
	s.log.Info("stack installed", "stack", name, "source", source, "rev", f.Rev)
	s.writeLock(ctx)
	return s.Get(ctx, name)
}

// ifaceFree refuses a name whose host interface an installed stack has: two
// names that agree in their first 12 characters (HostIface).
func (s *Service) ifaceFree(ctx context.Context, name string) error {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return err
	}
	ifc := HostIface(name)
	for _, r := range rows {
		if HostIface(r.Name) == ifc {
			return httpx.Conflict("iface_taken",
				"%q would share the network interface %s with the installed stack %q; pick a name that differs in its first %d characters",
				name, ifc, r.Name, ifaceNameLen)
		}
	}
	return nil
}

// SetLabel sets the name the dashboard shows for the stack; empty clears it.
func (s *Service) SetLabel(ctx context.Context, name, label string) (Stack, error) {
	if _, err := s.row(ctx, name); err != nil {
		return Stack{}, err
	}
	label, err := validLabel(label)
	if err != nil {
		return Stack{}, httpx.BadRequest("invalid_label", "%v", err)
	}
	if err := s.st.Queries().SetStackLabel(ctx, store.SetStackLabelParams{Name: name, Label: label}); err != nil {
		return Stack{}, err
	}
	s.writeLock(ctx)
	return s.Get(ctx, name)
}

// SetAutostart sets whether the stack starts after the host boots. It
// starts or stops nothing now.
func (s *Service) SetAutostart(ctx context.Context, name string, on bool) (Stack, error) {
	if _, err := s.row(ctx, name); err != nil {
		return Stack{}, err
	}
	v := int64(0)
	if on {
		v = 1
	}
	if err := s.st.Queries().SetStackAutostart(ctx, store.SetStackAutostartParams{Name: name, Autostart: v}); err != nil {
		return Stack{}, err
	}
	s.writeLock(ctx)
	return s.Get(ctx, name)
}

// maxLabel is the longest label, in characters.
const maxLabel = 40

// validLabel trims a label and refuses one that is too long or holds
// control characters.
func validLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > maxLabel {
		return "", fmt.Errorf("a label is at most %d characters", maxLabel)
	}
	if !utf8.ValidString(label) || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return "", errors.New("a label cannot hold control characters")
	}
	return label, nil
}

// title is how messages name a stack: its label, else its name.
func title(r store.Stack) string {
	if r.Label != "" {
		return r.Label
	}
	return r.Name
}

// Fetch reads the newest commit of the stack's source. A manifest that asks
// for nothing beyond the approved grants is approved at once.
func (s *Service) Fetch(ctx context.Context, name string) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	f, m, err := s.fetch(ctx, r.Source, name)
	if err != nil {
		return Stack{}, err
	}
	approved, hasApproved, err := approvedManifest(r)
	if err != nil {
		return Stack{}, err
	}
	now := s.now().UnixMilli()
	autoApproved := false
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.SetStackFetched(ctx, store.SetStackFetchedParams{
			Name: name, Rev: f.Rev, Url: f.URL, Manifest: encode(m), Now: now,
		}); err != nil {
			return err
		}
		if hasApproved && f.Rev != r.ApprovedRev && len(NewGrants(approved, m)) == 0 {
			n, err := q.ApproveStackFetched(ctx, store.ApproveStackFetchedParams{Name: name, Rev: f.Rev, Now: now})
			autoApproved = n > 0
			return err
		}
		return nil
	})
	if err != nil {
		return Stack{}, err
	}
	if autoApproved {
		s.materializePathsLogged(name, m)
	}
	// A channel moves without a commit to the repo: look it up on every
	// fetch, which the update check runs.
	s.refreshNixpkgs(ctx, name)
	s.refreshHostIDs(ctx, name)
	since := ""
	if g, err := s.generation(ctx, r); err == nil && g != nil {
		since = g.Rev
	}
	s.storeInfo(ctx, name, s.learn(ctx, name, r.Source, f, since))
	// A commit approved at once may have dropped an API scope.
	s.syncAPITokenLogged(ctx, name)
	s.writeLock(ctx)
	return s.Get(ctx, name)
}

// Approve approves the fetched commit rev and everything its manifest asks
// for.
func (s *Service) Approve(ctx context.Context, name, rev string) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	fetched, err := decode(r.FetchedManifest)
	if err != nil {
		return Stack{}, err
	}
	if r.FetchedRev == rev {
		if err := s.portClash(ctx, name, fetched); err != nil {
			return Stack{}, err
		}
	}
	n, err := s.st.Queries().ApproveStackFetched(ctx, store.ApproveStackFetchedParams{
		Name: name, Rev: rev, Now: s.now().UnixMilli(),
	})
	if err != nil {
		return Stack{}, err
	}
	if n == 0 {
		return Stack{}, httpx.Conflict("stale_rev",
			"stack %q is now at commit %s, not %s; look at it again before approving", name, r.FetchedRev, rev)
	}
	s.log.Info("stack approved", "stack", name, "rev", rev)
	// Approving a path with a default chooses that directory; it stays the
	// stack's when a later commit changes the default.
	s.materializePathsLogged(name, fetched)
	s.syncAPITokenLogged(ctx, name)
	s.refreshNixpkgs(ctx, name)
	s.refreshHostIDs(ctx, name)
	s.writeLock(ctx)
	return s.Get(ctx, name)
}

// portClash refuses a manifest that forwards a host port another stack's
// approved manifest already forwards: only one of them could get it.
func (s *Service) portClash(ctx context.Context, name string, m Manifest) error {
	if len(m.Ports) == 0 {
		return nil
	}
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		other, ok, err := approvedManifest(r)
		if err != nil || !ok || r.Name == name {
			continue
		}
		for _, p := range m.Ports {
			for _, o := range other.Ports {
				if p.Port == o.Port && p.Proto == o.Proto {
					return httpx.Conflict("port_taken", "host port %d/%s is already forwarded to stack %q", p.Port, p.Proto, r.Name)
				}
			}
		}
	}
	return nil
}

// Remove forgets a stopped stack: its approvals, generations, routes, rules,
// profile and delivered variables. Its state directory, with the stored
// variables, is left in place.
func (s *Service) Remove(ctx context.Context, name string) error {
	if _, err := s.row(ctx, name); err != nil {
		return err
	}
	// The removal holds the stack's operation slot, so no start can begin
	// while its rules, profile and row are going away.
	o, err := s.begin(name, "remove")
	if err != nil {
		return err
	}
	defer func() {
		s.mu.Lock()
		delete(s.ops, name)
		s.mu.Unlock()
		close(o.done)
	}()
	if !s.isStopped(ctx, name) {
		return httpx.Conflict("stack_running", "stack %q is not stopped; stop it first", name)
	}
	if err := s.forget(ctx, name); err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteStackGenerations(ctx, name); err != nil {
			return err
		}
		if err := q.DeleteStackUpdates(ctx, name); err != nil {
			return err
		}
		_, err := q.DeleteStack(ctx, name)
		return err
	})
	if err == nil {
		// Its profile is gone: its systems are garbage now.
		s.collectSoon()
	}
	if err != nil {
		return err
	}
	if err := s.removeAPIToken(ctx, name); err != nil {
		s.log.Warn("stack API token file not removed", "stack", name, "error", err)
	}
	if err := s.removeVarsDir(name); err != nil {
		s.log.Warn("stack variables not removed from /run", "stack", name, "error", err)
	}
	s.forgetDisk(name)
	s.log.Info("stack removed", "stack", name)
	s.writeLock(ctx)
	return nil
}

func unknownStack(name string) *httpx.Problem {
	return httpx.NotFound("unknown_stack", "no stack named %q is installed", name)
}

func (s *Service) row(ctx context.Context, name string) (store.Stack, error) {
	r, err := s.st.Queries().GetStack(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Stack{}, unknownStack(name)
	}
	return r, err
}

// fetch reads and validates the newest commit. name, when set, is the name
// the stack is installed under, which its units are shown with.
func (s *Service) fetch(ctx context.Context, source, name string) (Fetched, Manifest, error) {
	f, err := s.src.Fetch(ctx, source)
	if errors.Is(err, ErrNoManifest) {
		return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest", "%v", err)
	}
	if err != nil {
		return Fetched{}, Manifest{}, httpx.BadGateway("fetch_failed", "fetching %s: %v", source, err)
	}
	m, err := ParseManifest(f.Manifest)
	if err != nil {
		return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest", "%s at %s: %v", ManifestFile, f.Rev, err)
	}
	if m.Version < MinBuildVersion {
		return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest",
			"%s at %s is version %d; stacks are built from version %d on (a %s that exposes nixosModules.default)",
			ManifestFile, f.Rev, m.Version, MinBuildVersion, FlakeFile)
	}
	if f.NoFlake {
		return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest",
			"%s at %s: a version 7 stack needs a %s that exposes nixosModules.default", ManifestFile, f.Rev, FlakeFile)
	}
	if f.StorePath != "" {
		if err := CheckIconFiles(f.StorePath, m); err != nil {
			return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest", "%s at %s: %v", ManifestFile, f.Rev, err)
		}
	}
	for i, r := range m.Routes {
		if IsLabel(r.Host) { // left a label: this host has no domain set
			return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest",
				"%s at %s: routes[%d].host %q is a label, and this host has no domain to serve it under (services.homelab-daemon.stacks.domain)", ManifestFile, f.Rev, i, r.Host)
		}
	}
	if name == "" {
		name = m.Name
	}
	if err := hostUnitsClash(name, m); err != nil {
		return Fetched{}, Manifest{}, httpx.Unprocessable("invalid_manifest", "%s at %s: %v", ManifestFile, f.Rev, err)
	}
	return f, m, nil
}

func encode(m Manifest) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("stacks: encoding a manifest: %v", err)) // plain data; cannot fail
	}
	return string(b)
}

// decode reads a manifest this daemon stored. It was validated on the way
// in; it is validated again so a row edited by hand cannot grant anything
// the validator refuses.
func decode(s string) (Manifest, error) {
	m, err := ParseManifest([]byte(s))
	if err != nil {
		return Manifest{}, fmt.Errorf("stored manifest: %w", err)
	}
	return m, nil
}

func approvedManifest(r store.Stack) (Manifest, bool, error) {
	if r.ApprovedRev == "" {
		return Manifest{}, false, nil
	}
	m, err := decode(r.ApprovedManifest)
	return m, true, err
}

func (s *Service) view(ctx context.Context, r store.Stack) (Stack, error) {
	fetched, err := decode(r.FetchedManifest)
	if err != nil {
		return Stack{}, fmt.Errorf("stack %s: %w", r.Name, err)
	}
	approved, hasApproved, err := approvedManifest(r)
	if err != nil {
		return Stack{}, fmt.Errorf("stack %s: %w", r.Name, err)
	}
	v := Stack{
		Name: r.Name, Label: r.Label, Source: r.Source, CreatedAt: time.UnixMilli(r.CreatedAt),
		Fetched:   revision(r.FetchedRev, r.FetchedUrl, fetched, r.FetchedAt),
		NewGrants: []StackGrant{}, IdleGrants: []StackIdleGrant{},
		Running: s.isRunning(ctx, r.Name), WantRunning: r.WantRunning != 0, Autostart: r.Autostart != 0,
		Generation: int(r.Generation), Generations: []StackGeneration{},
		Address:     stackAddr(r.Subnet),
		Operation:   s.operationView(r.Name),
		UpdateState: UpToDate, Commits: []StackCommit{},
		SourceDirty: readInfo(r).Dirty,
		Usage:       s.usageOf(r.Name),
	}
	v.Frozen = v.Running && s.frozen(ctx, r.Name)
	vars, folders, varsErr := s.loadBinds(r.Name)
	if varsErr != nil {
		// A store that cannot be read shows no values rather than failing
		// the whole view.
		s.log.Warn("stack variables not read", "stack", r.Name, "error", varsErr)
	}
	v.Variables, v.VariablesReady = variablesView(vars, approved, fetched, r.ApiTokenHash != "", s.builtins(fetched))
	v.VariablesReady = v.VariablesReady && varsErr == nil
	s.ownerView(ctx, r.Name, v.Variables)
	v.Folders = s.foldersView(ctx, r, folders)
	if m, err := s.runningManifest(ctx, r); err == nil && varsErr == nil {
		for _, g := range idleGrants(m, vars, s.hostNets()) {
			v.IdleGrants = append(v.IdleGrants, StackIdleGrant{Kind: StackGrantKind(g.Kind), Detail: g.Detail, Reason: g.Reason})
		}
	}
	gens, err := s.st.Queries().ListStackGenerations(ctx, r.Name)
	if err != nil {
		return Stack{}, err
	}
	if v.Running && varsErr == nil {
		v.RestartNeeded = s.restartNeeded(r, gens, vars, folders)
	}
	for _, g := range gens {
		v.Generations = append(v.Generations, StackGeneration{
			Id: int(g.ID), Rev: g.Rev, BuiltAt: time.UnixMilli(g.BuiltAt), Failed: g.Failed != 0, NixpkgsRev: g.NixpkgsRev,
		})
	}
	if hasApproved {
		a := revision(r.ApprovedRev, r.ApprovedUrl, approved, r.ApprovedAt)
		v.Approved = &a
	}
	if r.FetchedRev != r.ApprovedRev {
		for _, n := range NewGrants(approved, fetched) {
			v.NewGrants = append(v.NewGrants, StackGrant{Kind: StackGrantKind(n.Kind), Detail: n.Detail})
		}
	}
	running, behind := "", false
	for _, g := range gens {
		if g.ID == r.Generation {
			running, behind = g.Rev, pending(r, g)
		}
	}
	v.NixpkgsRev, v.Nixpkgs = r.NixpkgsRev, r.Nixpkgs
	switch {
	case len(v.NewGrants) > 0:
		v.UpdateState = NeedsApproval
	case r.Generation != 0 && hasApproved && behind:
		v.UpdateState = CommitsAvailable
	}
	if running != r.FetchedRev {
		v.Commits = commitsView(readInfo(r))
	}
	return v, nil
}

// restartNeeded reports whether the running container was booted with other
// arguments than its generation gives under the stored values vars. Not
// known (booted before the record, or no arguments to compare) is false.
func (s *Service) restartNeeded(r store.Stack, gens []store.StackGeneration, vars []storedVar, folders []storedFolder) bool {
	if r.BootedVars != "" && r.BootedVars != s.deliveredDigest(r.Name) {
		return true
	}
	booted := bootedArgs(r)
	if booted == nil {
		return false
	}
	for _, g := range gens {
		if g.ID != r.Generation {
			continue
		}
		m, err := decode(g.Manifest)
		if err != nil {
			return false
		}
		binds, err := hostBinds(m, vars, folders)
		if err != nil {
			return false
		}
		return !sameBoot(booted, nspawnArgs(r.Name, s.stateDir(r.Name), s.profileDir(r.Name), s.varsDir(r.Name), "", m, binds))
	}
	return false
}

func revision(rev, url string, m Manifest, at int64) StackRevision {
	// The API schema mirrors Manifest's JSON, so the conversion is a round
	// trip rather than a field-by-field copy that could drift.
	var am StackManifest
	if err := json.Unmarshal([]byte(encode(m)), &am); err != nil {
		panic(fmt.Sprintf("stacks: manifest does not fit the API schema: %v", err))
	}
	return StackRevision{Rev: rev, Url: url, Manifest: am, At: time.UnixMilli(at)}
}

// SetHostValues changes what the host gives version 8 stacks from now on:
// the dashboard's address and the networks "lan" and the address words
// stand for. When they changed, every running stack's firewall is loaded
// again at once, and its variables are delivered again for its next
// restart (refreshNet).
func (s *Service) SetHostValues(dashboardURL string, nets Nets) {
	s.hostMu.Lock()
	changed := s.host.DashboardURL != dashboardURL || !slices.Equal(s.host.LAN, nets.LAN) ||
		s.host.VPN != nets.VPN || s.host.Tailnet != nets.Tailnet
	s.host.DashboardURL, s.host.LAN = dashboardURL, slices.Clone(nets.LAN)
	s.host.VPN, s.host.Tailnet = nets.VPN, nets.Tailnet
	s.hostMu.Unlock()
	s.mu.Lock()
	bg := s.ctx
	s.mu.Unlock()
	if changed && bg != nil {
		s.refreshNets(bg)
	}
}

func (s *Service) hostNets() Nets {
	s.hostMu.RLock()
	defer s.hostMu.RUnlock()
	return Nets{LAN: s.host.LAN, VPN: s.host.VPN, Tailnet: s.host.Tailnet}
}

func (s *Service) hostDashboardURL() string {
	s.hostMu.RLock()
	defer s.hostMu.RUnlock()
	return s.host.DashboardURL
}
