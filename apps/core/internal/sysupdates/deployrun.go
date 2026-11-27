package sysupdates

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

const (
	deployLogCap = 32 << 10
	healthChecks = 3
	// crashLoop is how many automatic restarts of the daemon since its restart
	// count as a crash loop: the health check gives up then instead of
	// waiting out its timeout.
	crashLoop = 3
)

// RunOptions are the `core-deploy` subcommand's arguments.
type RunOptions struct {
	ID     int64
	Kind   string
	Source string
	Socket string
	State  string
	// Reboot is an image update's or an install's: "", "now" or "HH:MM".
	Reboot string
	// Force rebuilds from main even when HEAD is already on this host.
	Force bool
}

// deployRun is one update in its own process.
type deployRun struct {
	o  RunOptions
	r  cmdrunner.Runner
	st deployStore
	d  CoreDeploy

	mu    sync.Mutex // guards d.Log and saved
	saved time.Time

	// Seams for tests.
	answers       func(ctx context.Context) error // the daemon answers on its socket
	restarts      func(ctx context.Context) int   // the daemon unit's automatic restarts; -1 unknown
	restartsFrom  int                             // restarts before the daemon was restarted
	healthTimeout time.Duration
	healthEvery   time.Duration
	now           func() time.Time
}

// RunDeploy does the update the daemon started and records how it went. The
// returned error is for the unit's exit status only; the record has the
// result.
func RunDeploy(ctx context.Context, r cmdrunner.Runner, o RunOptions) error {
	x := &deployRun{
		o: o, r: r, st: deployStore{path: o.State},
		answers:       func(ctx context.Context) error { return daemonAnswers(ctx, o.Socket) },
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
		if !slices.Contains(before, u) {
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

// waitHealthy waits for the daemon to answer and Caddy to run. Healthy is
// three good checks in a row, so a daemon that starts and dies again does
// not pass. It returns "" when healthy, else what was wrong at the end.
func (x *deployRun) waitHealthy(ctx context.Context) string {
	deadline := x.now().Add(x.healthTimeout)
	good := 0
	for {
		why := x.health(ctx)
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

func (x *deployRun) health(ctx context.Context) string {
	if err := x.answers(ctx); err != nil {
		return "the daemon did not answer"
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

func (x *deployRun) phase(p string) {
	x.mu.Lock()
	x.d.Phase = p
	x.mu.Unlock()
	x.logf("── %s", p)
	if err := x.save(); err != nil {
		log.Warn("could not record the update", "error", err)
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

// daemonAnswers asks the daemon for its version: any answer will do.
func daemonAnswers(ctx context.Context, socket string) error {
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/api/version", nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
