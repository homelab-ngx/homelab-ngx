package sysupdates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// A core deploy puts the host on a commit of main: fetch, build, switch,
// health check, and a rollback when the daemon or Caddy does not come back.
// The switch usually restarts the daemon, and a restart kills everything in
// the daemon's cgroup, so the work runs in a transient unit of its own (the
// daemon binary's `core-deploy` subcommand, see RunDeploy). That process owns
// the record and writes it to a file; the daemon starts it and reads the file.

const (
	deployUnit    = "homelab-core-deploy.service"
	deployFile    = "core-deploys.json"
	keepDeploys   = 30
	mainRevMaxAge = 5 * time.Minute
	// A record still "running" this long after its unit is gone was killed.
	deployStartGrace = 20 * time.Second
)

// commitFile holds the commit the running system was built from.
var commitFile = "/etc/homelab-daemon/commit"

// CoreDeploy results.
const (
	DeployRunning     = "running"
	DeployHealthy     = "healthy"
	DeployRolledBack  = "rolled_back"
	DeployBuildFailed = "build_failed"
	DeployFailed      = "failed"
	DeployInterrupted = "interrupted"
)

// CoreDeploy kinds.
const (
	KindDeploy   = "deploy"
	KindRollback = "rollback"
)

// CoreDeploy is one deploy of core from main, or one rollback to the
// generation before.
type CoreDeploy struct {
	Id          int64      `json:"id" doc:"Start time in unix milliseconds."`
	Kind        string     `json:"kind" enum:"deploy,rollback,build,download,core-update,image-update" doc:"deploy and rollback on a NixOS host; build, download, core-update and image-update on an image host."`
	Rev         string     `json:"rev" doc:"The commit of main being deployed or built; empty until it is fetched, and for a rollback."`
	Version     string     `json:"version,omitempty" doc:"Image host: the image or core version built or installed."`
	Phase       string     `json:"phase" doc:"What it is doing now, or did last."`
	Result      string     `json:"result" enum:"running,healthy,rolled_back,build_failed,failed,interrupted"`
	Reason      string     `json:"reason" doc:"Why it failed or was rolled back."`
	FailedUnits []string   `json:"failed_units" doc:"Units that failed during the switch. They do not undo the deploy."`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Log         string     `json:"log" doc:"The end of the build and switch output."`
}

// CoreStatus is where core stands against main.
type CoreStatus struct {
	Configured      bool   `json:"configured" doc:"The host names the repository core is deployed from."`
	Deploying       bool   `json:"deploying"`
	MainRev         string `json:"main_rev" doc:"The newest commit of main the daemon has seen; empty = not checked yet."`
	RunningRev      string `json:"running_rev" doc:"The commit the running system was built from."`
	UpdateAvailable bool   `json:"update_available"`
}

// Errors the deploy operations answer with.
var (
	ErrDeployRunning    = errors.New("a core deploy is already running")
	ErrDeployNotSet     = errors.New("this host does not name the repository core is deployed from")
	ErrSecretsNotPushed = errors.New("a secrets change is committed on the host but not pushed; a deploy from main would not include it")
)

const (
	errDeployInterrupted  = "the deploy stopped before it finished"
	errDeployStartTimeout = 30 * time.Second
)

// DeployConfig is what a deploy needs from the composition root.
type DeployConfig struct {
	Source string // flake reference of the repository, e.g. github:owner/repo; "" = deploys off
	Host   string // nixosConfigurations attribute
	Socket string // the daemon's unix socket, for the health check
	Exe    string // the daemon binary that runs the deploy
	// Unpushed reports a secrets change committed in the host's checkout
	// that main does not have yet. May be nil.
	Unpushed func(ctx context.Context) (bool, error)
}

// SetDeploy turns core deploys on.
func (m *Module) SetDeploy(c DeployConfig) { m.dep = c }

// ── records ─────────────────────────────────────────────────────────────

// deployStore is the file of deploy records, newest first.
type deployStore struct{ path string }

func (s deployStore) load() []CoreDeploy {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var list []CoreDeploy
	if err := json.Unmarshal(data, &list); err != nil {
		log.Error("failed to parse the core deploy records", "file", s.path, "error", err)
		return nil
	}
	return list
}

// put writes d over the record with its id, or adds it as the newest.
func (s deployStore) put(d CoreDeploy) error {
	list := s.load()
	found := false
	for i := range list {
		if list[i].Id == d.Id {
			list[i], found = d, true
		}
	}
	if !found {
		list = append([]CoreDeploy{d}, list...)
	}
	if len(list) > keepDeploys {
		list = list[:keepDeploys]
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ── daemon side ─────────────────────────────────────────────────────────

type deployState struct {
	mu      sync.Mutex // one start at a time
	revMu   sync.Mutex // guards the main check
	mainRev string
	checked time.Time
	check   bool // a main check is running
}

func (m *Module) deploys() deployStore {
	return deployStore{path: filepath.Join(filepath.Dir(m.file), deployFile)}
}

// deployActive: the deploy's unit is running.
func (m *Module) deployActive(ctx context.Context) bool {
	res, _ := m.r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "systemctl", Args: []string{"is-active", deployUnit}, ExpectExit: []int{3, 4}})
	s := strings.TrimSpace(res.Stdout)
	return s == "active" || s == "activating"
}

// StartDeploy starts a deploy of main, or with kind KindRollback a switch to
// the generation before. It returns once the unit is started.
func (m *Module) StartDeploy(ctx context.Context, kind string) (CoreDeploy, error) {
	if m.image {
		return CoreDeploy{}, ErrImageHost
	}
	if kind == KindDeploy && m.dep.Source == "" {
		return CoreDeploy{}, ErrDeployNotSet
	}
	return m.start(ctx, kind)
}

// start records an operation of kind and starts it in the deploy unit, with
// extra arguments for its run.
func (m *Module) start(ctx context.Context, kind string, extra ...string) (CoreDeploy, error) {
	m.ds.mu.Lock()
	defer m.ds.mu.Unlock()
	if m.deployActive(ctx) {
		return CoreDeploy{}, ErrDeployRunning
	}
	if kind == KindDeploy && m.dep.Unpushed != nil {
		if unpushed, err := m.dep.Unpushed(ctx); err != nil {
			log.Warn("could not check for unpushed secrets", "error", err)
		} else if unpushed {
			return CoreDeploy{}, ErrSecretsNotPushed
		}
	}
	now := m.now()
	d := CoreDeploy{Id: now.UnixMilli(), Kind: kind, Phase: "Starting", Result: DeployRunning, StartedAt: now, FailedUnits: []string{}}
	st := m.deploys()
	if err := st.put(d); err != nil {
		return CoreDeploy{}, fmt.Errorf("recording the deploy: %w", err)
	}
	args := []string{
		"--unit=" + deployUnit, "--collect", "--no-block", "--quiet",
		"--description=Homelab core " + kind,
		"--setenv=PATH=" + os.Getenv("PATH"), "--setenv=HOME=/root",
		"--", m.dep.Exe, "core-deploy",
		"--id", strconv.FormatInt(d.Id, 10), "--kind", kind,
		"--source", m.dep.Source, "--host", m.dep.Host,
		"--socket", m.dep.Socket, "--state", st.path,
	}
	// A release server other than the default (the image check's) reaches
	// the download in its unit.
	for _, k := range []string{releaseURLEnv, releaseKeyringEnv} {
		if v := os.Getenv(k); v != "" {
			args = slices.Insert(args, 1, "--setenv="+k+"="+v)
		}
	}
	args = append(args, extra...)
	rctx, cancel := context.WithTimeout(ctx, errDeployStartTimeout)
	defer cancel()
	if _, err := m.r.Run(rctx, cmdrunner.Cmd{Module: "sysupdates", Name: "systemd-run", Args: args}); err != nil {
		fin := m.now()
		d.Result, d.Reason, d.FinishedAt = DeployFailed, "the deploy could not be started: "+err.Error(), &fin
		_ = st.put(d)
		return d, fmt.Errorf("starting the deploy: %w", err)
	}
	log.Info("core "+kind+" started", "id", d.Id)
	return d, nil
}

// Deploys lists the deploy records, newest first. A record left running by a
// deploy that was killed is closed as interrupted.
func (m *Module) Deploys(ctx context.Context) []CoreDeploy {
	st := m.deploys()
	list := st.load()
	if len(list) > 0 && list[0].Result == DeployRunning &&
		m.now().Sub(list[0].StartedAt) > deployStartGrace && !m.deployActive(ctx) {
		// Read again: the deploy may have written its result while we asked.
		if list = st.load(); len(list) > 0 && list[0].Result == DeployRunning {
			fin := m.now()
			list[0].Result, list[0].Reason, list[0].FinishedAt = DeployInterrupted, errDeployInterrupted, &fin
			if err := st.put(list[0]); err != nil {
				log.Warn("could not close the interrupted deploy", "error", err)
			}
		}
	}
	if list == nil {
		list = []CoreDeploy{}
	}
	return list
}

// CoreStatus compares the running commit with main. The main check runs in
// the background when the last one is old, so the status never waits on
// GitHub; refresh forces one.
func (m *Module) CoreStatus(ctx context.Context, refresh bool) CoreStatus {
	st := CoreStatus{Configured: m.dep.Source != "", RunningRev: runningRev()}
	if !st.Configured {
		return st
	}
	st.Deploying = m.deployActive(ctx)
	// A deploy fetched main itself: what it deployed is newer than a check
	// from before it, so that check is redone and the deployed commit stands
	// in until then.
	var deployed *CoreDeploy
	if list := m.deploys().load(); len(list) > 0 && list[0].Kind == KindDeploy && list[0].Result == DeployHealthy && list[0].FinishedAt != nil {
		deployed = &list[0]
	}
	m.ds.revMu.Lock()
	stale := m.now().Sub(m.ds.checked) > mainRevMaxAge
	overtaken := deployed != nil && deployed.FinishedAt.After(m.ds.checked)
	if (stale || refresh || overtaken) && !m.ds.check {
		m.ds.check = true
		go m.checkMain()
	}
	st.MainRev = m.ds.mainRev
	m.ds.revMu.Unlock()
	if overtaken {
		st.MainRev = deployed.Rev
	}
	st.UpdateAvailable = behind(st.RunningRev, st.MainRev)
	return st
}

func (m *Module) checkMain() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	meta, err := flakeMetadata(ctx, m.r, m.dep.Source)
	m.ds.revMu.Lock()
	defer m.ds.revMu.Unlock()
	m.ds.check = false
	m.ds.checked = m.now()
	if err != nil {
		log.Warn("could not look up main", "error", err)
		return
	}
	m.ds.mainRev = meta.Revision
}

// behind: the running system was not built from main's newest commit. The
// running commit is a short revision, or a dirty one from a local build.
func behind(running, mainRev string) bool {
	return running != "" && mainRev != "" && !strings.HasPrefix(mainRev, running)
}

func runningRev() string {
	b, err := os.ReadFile(commitFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// flakeMeta is what `nix flake metadata` says about the repository now.
type flakeMeta struct {
	Revision string `json:"revision"`
	URL      string `json:"url"` // locked: names the revision and its hash
}

func flakeMetadata(ctx context.Context, r cmdrunner.Runner, source string) (flakeMeta, error) {
	res, err := r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "nix", Args: []string{
		"--extra-experimental-features", "nix-command flakes", "flake", "metadata", "--json", "--refresh", "--", source,
	}})
	if err != nil {
		return flakeMeta{}, err
	}
	var meta flakeMeta
	if err := json.Unmarshal([]byte(res.Stdout), &meta); err != nil {
		return flakeMeta{}, fmt.Errorf("reading the flake metadata: %w", err)
	}
	if meta.Revision == "" || meta.URL == "" {
		return flakeMeta{}, errors.New("the flake metadata names no revision")
	}
	return meta, nil
}
