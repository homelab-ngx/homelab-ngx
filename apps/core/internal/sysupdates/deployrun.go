package sysupdates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

const (
	systemProfile   = "/nix/var/nix/profiles/system"
	deployLogCap    = 32 << 10
	deployReasonCap = 4 << 10
	buildTimeout    = 2 * time.Hour
	switchTimeout   = 30 * time.Minute
	healthChecks    = 3
	// crashLoop is how many automatic restarts of the daemon since the switch
	// count as a crash loop: the health check gives up then instead of
	// waiting out its timeout.
	crashLoop = 3
)

// RunOptions are the `core-deploy` subcommand's arguments.
type RunOptions struct {
	ID     int64
	Kind   string
	Source string
	Host   string
	Socket string
	State  string
	// Reboot is an image update's: "", "now" or "HH:MM".
	Reboot string
}

// deployRun is one deploy in its own process.
type deployRun struct {
	o  RunOptions
	r  cmdrunner.Runner
	st deployStore
	d  CoreDeploy

	mu    sync.Mutex // guards d.Log and saved
	saved time.Time

	// Seams for tests.
	version       func(ctx context.Context) (string, error) // the daemon's deployed commit
	profile       func() string                             // what the system profile points at
	restarts      func(ctx context.Context) int             // the daemon unit's automatic restarts; -1 unknown
	restartsFrom  int                                       // restarts before the switch
	healthTimeout time.Duration
	healthEvery   time.Duration
	now           func() time.Time
}

// RunDeploy does the deploy the daemon started and records how it went. The
// returned error is for the unit's exit status only; the record has the
// result.
func RunDeploy(ctx context.Context, r cmdrunner.Runner, o RunOptions) error {
	x := &deployRun{
		o: o, r: r, st: deployStore{path: o.State},
		version:       func(ctx context.Context) (string, error) { return daemonCommit(ctx, o.Socket) },
		profile:       func() string { p, _ := os.Readlink(systemProfile); return p },
		restarts:      func(ctx context.Context) int { return daemonRestarts(ctx, r) },
		healthTimeout: 3 * time.Minute, healthEvery: 3 * time.Second, now: time.Now,
	}
	return x.run(ctx)
}

func (x *deployRun) run(ctx context.Context) error {
	x.d = CoreDeploy{Id: x.o.ID, Kind: x.o.Kind, Result: DeployRunning, StartedAt: x.now(), FailedUnits: []string{}}
	for _, d := range x.st.load() {
		if d.Id == x.o.ID {
			x.d = d
		}
	}
	before := x.failedUnits(ctx)
	result, reason := x.work(ctx)
	if ctx.Err() != nil && result != DeployHealthy {
		result, reason = DeployInterrupted, errDeployInterrupted
	}
	for _, u := range x.failedUnits(context.WithoutCancel(ctx)) {
		if !contains(before, u) {
			x.d.FailedUnits = append(x.d.FailedUnits, u)
		}
	}
	fin := x.now()
	x.d.Result, x.d.Reason, x.d.FinishedAt = result, reason, &fin
	if err := x.save(); err != nil {
		return err
	}
	if result != DeployHealthy {
		return fmt.Errorf("core %s: %s: %s", x.o.Kind, result, reason)
	}
	return nil
}

func (x *deployRun) work(ctx context.Context) (result, reason string) {
	switch x.o.Kind {
	case KindBuild, KindCoreUpdate, KindImageUpdate:
		return x.imageWork(ctx)
	}
	if x.o.Kind == KindRollback {
		x.phase("Switch")
		x.restartsFrom = x.restarts(ctx)
		if err := x.switchBack(ctx); err != nil {
			return DeployFailed, err.Error()
		}
		x.phase("Health check")
		if why := x.waitHealthy(ctx, ""); why != "" {
			return DeployFailed, why
		}
		return DeployHealthy, ""
	}

	x.phase("Fetch")
	fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	meta, err := flakeMetadata(fctx, x.r, x.o.Source)
	cancel()
	if err != nil {
		return DeployFailed, "main could not be fetched: " + errorBlocks(err.Error())
	}
	x.d.Rev = meta.Revision

	x.phase("Build")
	var out strings.Builder
	bctx, cancel := context.WithTimeout(ctx, buildTimeout)
	_, err = x.r.Run(bctx, cmdrunner.Cmd{Module: "sysupdates", Name: "nix", Args: []string{
		"--extra-experimental-features", "nix-command flakes", "build", "--no-link", "--print-build-logs", "--",
		meta.URL + "#nixosConfigurations." + x.o.Host + ".config.system.build.toplevel",
	}, OnLine: func(_, line string) { out.WriteString(line + "\n"); x.logLine(line) }})
	cancel()
	if err != nil {
		why := errorBlocks(out.String())
		if why == "" {
			why = err.Error()
		}
		return DeployBuildFailed, why
	}

	x.phase("Switch")
	prev := x.profile()
	x.restartsFrom = x.restarts(ctx)
	switchErr := x.sh(ctx, switchTimeout, "nixos-rebuild", "switch", "--flake", meta.URL+"#"+x.o.Host)
	if switchErr != nil {
		x.logf("the switch reported: %v", switchErr)
	}
	if x.profile() == prev && switchErr != nil {
		// Nothing was activated, so there is nothing to roll back.
		return DeployFailed, "the switch did not start: " + switchErr.Error()
	}

	x.phase("Health check")
	why := x.waitHealthy(ctx, meta.Revision)
	if why == "" {
		return DeployHealthy, ""
	}
	if ctx.Err() != nil {
		return DeployInterrupted, errDeployInterrupted
	}
	x.phase("Rolling back")
	x.logf("rolling back: %s", why)
	x.restartsFrom = x.restarts(ctx)
	if err := x.switchBack(ctx); err != nil {
		return DeployFailed, why + "; " + err.Error()
	}
	if again := x.waitHealthy(ctx, ""); again != "" {
		return DeployFailed, why + "; after the rollback: " + again
	}
	return DeployRolledBack, why
}

// daemonRestarts is how often systemd restarted the daemon by itself after
// it exited: a start that panics counts once per try. -1 when it cannot say.
func daemonRestarts(ctx context.Context, r cmdrunner.Runner) int {
	res, err := r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "systemctl", Args: []string{"show", "homelab-daemon.service", "-p", "NRestarts", "--value"}})
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		return -1
	}
	return n
}

// switchBack activates the generation before the current one. The command
// can fail on a unit that does not start while the host is back where it
// was, so what counts is that the system profile moved.
func (x *deployRun) switchBack(ctx context.Context) error {
	from := x.profile()
	// --no-reexec: without a flake to read, nixos-rebuild would first try to
	// build itself from <nixpkgs>, which this host does not have.
	err := x.sh(ctx, switchTimeout, "nixos-rebuild", "switch", "--rollback", "--no-reexec")
	if err != nil {
		x.logf("the switch back reported: %v", err)
	}
	if x.profile() == from {
		if err != nil {
			return fmt.Errorf("the switch back to the generation before did not happen: %w", err)
		}
		return errors.New("the switch back to the generation before did not happen")
	}
	return nil
}

// waitHealthy waits for the daemon to answer and Caddy to run; with rev, the
// daemon must report the system built from it. Healthy is three good checks
// in a row, so a daemon that starts and dies again does not pass. It returns
// "" when healthy, else what was wrong at the end.
func (x *deployRun) waitHealthy(ctx context.Context, rev string) string {
	deadline := x.now().Add(x.healthTimeout)
	good := 0
	for {
		why := x.health(ctx, rev)
		if why != "" && x.restartsFrom >= 0 {
			if n := x.restarts(ctx); n >= 0 && n-x.restartsFrom >= crashLoop {
				return fmt.Sprintf("the daemon keeps crashing (systemd restarted it %d times); %s", n-x.restartsFrom, why)
			}
		}
		if why == "" {
			if good++; good >= healthChecks {
				return ""
			}
			why = "the health check was interrupted"
		} else {
			good = 0
		}
		if !x.now().Before(deadline) || ctx.Err() != nil {
			return why
		}
		select {
		case <-ctx.Done():
		case <-time.After(x.healthEvery):
		}
	}
}

func (x *deployRun) health(ctx context.Context, rev string) string {
	commit, err := x.version(ctx)
	if err != nil {
		return "the daemon did not answer"
	}
	if rev != "" && (commit == "" || !strings.HasPrefix(rev, commit)) {
		return fmt.Sprintf("the daemon reports commit %q, not the deployed one", commit)
	}
	res, _ := x.r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "systemctl", Args: []string{"is-active", "caddy.service"}, ExpectExit: []int{3, 4}})
	if s := strings.TrimSpace(res.Stdout); s != "active" {
		return "Caddy is not running (" + s + ")"
	}
	return ""
}

// failedUnits lists the units systemd has as failed.
func (x *deployRun) failedUnits(ctx context.Context) []string {
	res, err := x.r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "systemctl", Args: []string{"list-units", "--failed", "--no-legend", "--plain"}})
	if err != nil {
		return nil
	}
	var units []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	sort.Strings(units)
	return units
}

func (x *deployRun) sh(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := x.r.Run(cctx, cmdrunner.Cmd{Module: "sysupdates", Name: name, Args: args, OnLine: func(_, line string) { x.logLine(line) }})
	return err
}

func (x *deployRun) phase(p string) {
	x.mu.Lock()
	x.d.Phase = p
	x.mu.Unlock()
	x.logf("── %s", p)
	if err := x.save(); err != nil {
		log.Warn("could not record the deploy", "error", err)
	}
}

func (x *deployRun) logf(format string, a ...any) { x.logLine(fmt.Sprintf(format, a...)) }

// logLine adds a line to the record's log, which keeps its end, and writes
// the record at most every two seconds so the dashboard can follow.
func (x *deployRun) logLine(line string) {
	fmt.Println(line) // the unit's journal has the whole output
	x.mu.Lock()
	x.d.Log += line + "\n"
	if over := len(x.d.Log) - deployLogCap; over > 0 {
		x.d.Log = x.d.Log[over:]
	}
	due := x.now().Sub(x.saved) > 2*time.Second
	x.mu.Unlock()
	if due {
		_ = x.save()
	}
}

func (x *deployRun) save() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.saved = x.now()
	return x.st.put(x.d)
}

// errorBlocks keeps nix's error blocks from build output: each `error:` line
// with the indented lines under it, which say where it broke. A line filter
// would keep the marker and drop the rest.
func errorBlocks(out string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "error:"), strings.HasPrefix(t, "error (ignored):"):
			in = true
			b.WriteString(line + "\n")
		case in && (line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			b.WriteString(line + "\n")
		default:
			in = false
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > deployReasonCap {
		s = s[:deployReasonCap] + "\n…"
	}
	return s
}

// daemonCommit asks the daemon for the commit of the system it runs in.
func daemonCommit(ctx context.Context, socket string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var v struct {
		Commit string `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Commit, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
