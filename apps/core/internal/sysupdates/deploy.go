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

// An update (image.go, fetch.go) can restart the daemon, and a restart kills
// everything in the daemon's cgroup, so the work runs in a transient unit of
// its own (the daemon binary's `core-deploy` subcommand, see RunDeploy). That
// process owns the record and writes it to a file; the daemon starts it and
// reads the file.

const (
	deployUnit  = "homelab-core-deploy.service"
	deployFile  = "core-deploys.json"
	keepDeploys = 30
	// A record still "running" this long after its unit is gone was killed.
	deployStartGrace = 20 * time.Second
)

// CoreDeploy results.
const (
	DeployRunning     = "running"
	DeployHealthy     = "healthy"
	DeployRolledBack  = "rolled_back"
	DeployBuildFailed = "build_failed"
	DeployFailed      = "failed"
	DeployInterrupted = "interrupted"
)

// CoreDeploy is one update operation: a build, a download or an install.
type CoreDeploy struct {
	Id          int64      `json:"id" doc:"Start time in unix milliseconds."`
	Kind        string     `json:"kind" enum:"build,download,core-update,image-update,install"`
	Rev         string     `json:"rev" doc:"The commit of main a build is of; empty until it is fetched, and for every other kind."`
	Version     string     `json:"version,omitempty" doc:"The image or core version built, downloaded or installed."`
	Phase       string     `json:"phase" doc:"What it is doing now, or did last."`
	Result      string     `json:"result" enum:"running,healthy,rolled_back,build_failed,failed,interrupted"`
	Reason      string     `json:"reason" doc:"Why it failed or was rolled back."`
	FailedUnits []string   `json:"failed_units" doc:"Units that failed during the operation. They do not undo it."`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Log         string     `json:"log" doc:"The end of the operation's output."`
}

// Errors the deploy operations answer with.
var (
	ErrDeployRunning = errors.New("an update is already running")
	ErrDeployNotSet  = errors.New("this host does not name the repository a build fetches")
)

const (
	errDeployInterrupted  = "the update stopped before it finished"
	errDeployStartTimeout = 30 * time.Second
)

// DeployConfig is what an update needs from the composition root.
type DeployConfig struct {
	Source string // the repository a build fetches, e.g. github:owner/repo; "" = builds off
	Socket string // the daemon's unix socket, for the health check
	Exe    string // the daemon binary that runs the update
}

// SetDeploy sets what the updates run with.
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
		log.Error("failed to parse the update records", "file", s.path, "error", err)
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
	mu sync.Mutex // one start at a time
}

func (m *Module) deploys() deployStore {
	return deployStore{path: filepath.Join(m.dir, deployFile)}
}

// deployActive: the deploy's unit is running.
func (m *Module) deployActive(ctx context.Context) bool {
	res, _ := m.r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "systemctl", Args: []string{"is-active", deployUnit}, ExpectExit: []int{3, 4}})
	s := strings.TrimSpace(res.Stdout)
	return s == "active" || s == "activating"
}

// start records an operation of kind and starts it in the deploy unit, with
// extra arguments for its run.
func (m *Module) start(ctx context.Context, kind string, extra ...string) (CoreDeploy, error) {
	m.ds.mu.Lock()
	defer m.ds.mu.Unlock()
	if m.deployActive(ctx) {
		return CoreDeploy{}, ErrDeployRunning
	}
	now := m.now()
	d := CoreDeploy{Id: now.UnixMilli(), Kind: kind, Phase: "Starting", Result: DeployRunning, StartedAt: now, FailedUnits: []string{}}
	st := m.deploys()
	if err := st.put(d); err != nil {
		return CoreDeploy{}, fmt.Errorf("recording the update: %w", err)
	}
	args := []string{
		"--unit=" + deployUnit, "--collect", "--no-block", "--quiet",
		"--description=Homelab core " + kind,
		"--setenv=PATH=" + os.Getenv("PATH"), "--setenv=HOME=/root",
		"--", m.dep.Exe, "core-deploy",
		"--id", strconv.FormatInt(d.Id, 10), "--kind", kind,
		"--source", m.dep.Source,
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
		d.Result, d.Reason, d.FinishedAt = DeployFailed, "the update could not be started: "+err.Error(), &fin
		_ = st.put(d)
		return d, fmt.Errorf("starting the update: %w", err)
	}
	log.Info("core "+kind+" started", "id", d.Id)
	return d, nil
}

// Deploys lists the update records, newest first. A record left running by
// an update that was killed is closed as interrupted.
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
				log.Warn("could not close the interrupted update", "error", err)
			}
		}
	}
	if list == nil {
		list = []CoreDeploy{}
	}
	return list
}

// Busy names the update running now, for the operator to see before a
// reboot interrupts it.
func (m *Module) Busy(ctx context.Context) []string {
	if !m.deployActive(ctx) {
		return nil
	}
	if list := m.deploys().load(); len(list) > 0 && list[0].Result == DeployRunning {
		return []string{"update: " + list[0].Kind + " (" + list[0].Phase + ")"}
	}
	return []string{"update"}
}
