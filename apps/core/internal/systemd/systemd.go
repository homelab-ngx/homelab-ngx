// Package systemd wraps the systemctl/journalctl calls the daemon needs. All
// execs go through a cmdrunner.Runner so callers test against a fake.
package systemd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/logging"
)

var log = logging.Logger("systemd")

// CallTimeout bounds every systemctl/journalctl call so a hung systemd
// transaction (e.g. a dead D-Bus peer) cannot stall the monitor loop or an
// API request indefinitely.
const CallTimeout = 30 * time.Second

// Client runs systemd operations through a Runner.
type Client struct {
	r         cmdrunner.Runner
	opMu      sync.Map // unit -> *sync.Mutex
	pollEvery time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithPollInterval sets how often WaitActive polls (default 3s).
func WithPollInterval(d time.Duration) Option { return func(c *Client) { c.pollEvery = d } }

// New returns a Client that executes through r.
func New(r cmdrunner.Runner, opts ...Option) *Client {
	c := &Client{r: r, pollEvery: 3 * time.Second}
	for _, o := range opts {
		o(c)
	}
	return c
}

// lockUnit acquires the per-unit mutex that serialises start/stop/restart
// of the same unit (a monitor restart must not race an API stop).
func (c *Client) lockUnit(unit string) func() {
	v, _ := c.opMu.LoadOrStore(unit, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (c *Client) run(ctx context.Context, timeout time.Duration, name string, out cmdrunner.OutputMode, args ...string) (cmdrunner.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.r.Run(ctx, cmdrunner.Cmd{Module: "systemd", Name: name, Args: args, Output: out})
}

// isActive runs `systemctl is-active`, whose non-zero exit is an answer
// (3 = inactive, 4 = no such unit), not a failure worth a WARN.
func (c *Client) isActive(ctx context.Context, args ...string) (cmdrunner.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	return c.r.Run(ctx, cmdrunner.Cmd{
		Module: "systemd", Name: "systemctl", Args: append([]string{"is-active"}, args...),
		ExpectExit: []int{3, 4},
	})
}

func (c *Client) systemctl(ctx context.Context, args ...string) (cmdrunner.Result, error) {
	return c.run(ctx, CallTimeout, "systemctl", cmdrunner.OutputRaw, args...)
}

// Props batch-fetches properties for units with one `systemctl show` and
// returns unit -> property -> value. It returns nil when the call fails.
func (c *Client) Props(ctx context.Context, units []string, props ...string) map[string]map[string]string {
	return c.props(ctx, nil, units, props)
}

// PropsUnix is Props with `--timestamp=unix`, so timestamp properties read
// "@<seconds>" instead of a localised date.
func (c *Client) PropsUnix(ctx context.Context, units []string, props ...string) map[string]map[string]string {
	return c.props(ctx, []string{"--timestamp=unix"}, units, props)
}

func (c *Client) props(ctx context.Context, flags, units, props []string) map[string]map[string]string {
	if len(units) == 0 {
		return nil
	}
	args := append([]string{"show"}, flags...)
	args = append(args, units...)
	args = append(args, "--property="+strings.Join(props, ","))
	res, err := c.systemctl(ctx, args...)
	if err != nil {
		return nil
	}
	return parseShow(res.Stdout, len(units))
}

// parseShow parses multi-unit `systemctl show` output: blank-line-separated
// sections, each carrying an "Id=<unit>" line.
func parseShow(stdout string, n int) map[string]map[string]string {
	result := make(map[string]map[string]string, n)
	for _, section := range strings.Split(strings.TrimSpace(stdout), "\n\n") {
		if section == "" {
			continue
		}
		m := make(map[string]string)
		var id string
		for _, line := range strings.Split(section, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				m[k] = v
				if k == "Id" {
					id = v
				}
			}
		}
		if id != "" {
			result[id] = m
		}
	}
	return result
}

// IsActive reports whether a unit is currently active.
func (c *Client) IsActive(ctx context.Context, unit string) bool {
	_, err := c.isActive(ctx, "--quiet", unit)
	return err == nil
}

// ActiveMulti reports the active status of several units with one command,
// falling back to per-unit checks when the output does not line up.
func (c *Client) ActiveMulti(ctx context.Context, units []string) map[string]bool {
	res := make(map[string]bool, len(units))
	if len(units) == 0 {
		return res
	}
	// is-active exits non-zero when any unit is inactive; stdout is still valid.
	r, _ := c.isActive(ctx, units...)
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != len(units) {
		log.Warn("systemctl is-active multiple output length mismatch, falling back to individual checks", "expected", len(units), "got", len(lines))
		for _, u := range units {
			res[u] = c.IsActive(ctx, u)
		}
		return res
	}
	for i, u := range units {
		res[u] = strings.TrimSpace(lines[i]) == "active"
	}
	return res
}

// ActiveState returns the ActiveState property (active, inactive, failed,
// activating, deactivating, ...), or "" on error.
func (c *Client) ActiveState(ctx context.Context, unit string) string {
	res, err := c.systemctl(ctx, "show", unit, "--property=ActiveState", "--value")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// Start runs `systemctl start`, serialised per unit.
func (c *Client) Start(ctx context.Context, unit string) error {
	return c.StartTimeout(ctx, unit, CallTimeout)
}

// StartTimeout is Start with a caller-chosen timeout, for oneshot units whose
// start blocks for the duration of the job.
func (c *Client) StartTimeout(ctx context.Context, unit string, timeout time.Duration) error {
	defer c.lockUnit(unit)()
	res, err := c.run(ctx, timeout, "systemctl", cmdrunner.OutputCombined, "start", unit)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(res.Output))
	}
	return nil
}

// Stop runs `systemctl stop` and, if the unit ends up "failed" (common when an
// upstream unit lacks SuccessExitStatus= for the signal it exits with: Plex's
// SIGQUIT-then-coredump, podman containers exiting 143 on SIGTERM), clears
// that state so the post-stop view reads "inactive". A user-initiated stop is
// always a clean stop from our perspective.
func (c *Client) Stop(ctx context.Context, unit string) error {
	defer c.lockUnit(unit)()
	res, err := c.run(ctx, CallTimeout, "systemctl", cmdrunner.OutputCombined, "stop", unit)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(res.Output))
	}
	if c.ActiveState(ctx, unit) == "failed" {
		if _, rfErr := c.systemctl(ctx, "reset-failed", unit); rfErr != nil {
			log.Warn("reset-failed after stop failed", "unit", unit, "error", rfErr)
		} else {
			log.Info("cleared spurious failed state after stop", "unit", unit)
		}
	}
	return nil
}

// Restart runs `systemctl restart`, serialised per unit.
func (c *Client) Restart(ctx context.Context, unit string) error {
	defer c.lockUnit(unit)()
	res, err := c.run(ctx, CallTimeout, "systemctl", cmdrunner.OutputCombined, "restart", unit)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(res.Output))
	}
	return nil
}

// WaitActive polls until the unit is active, the timeout passes or ctx ends.
func (c *Client) WaitActive(ctx context.Context, unit string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if c.IsActive(ctx, unit) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s to become active", unit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.pollEvery):
		}
	}
}

// ExitedWithFailure reports whether the unit's last run ended non-zero.
func (c *Client) ExitedWithFailure(ctx context.Context, unit string) bool {
	res, err := c.systemctl(ctx, "show", unit, "--property=ExecMainStatus")
	if err != nil {
		return false
	}
	line := strings.TrimSpace(res.Stdout)
	return line != "ExecMainStatus=0" && line != "ExecMainStatus="
}

// JournalTail returns the last n journal lines of a unit. Output is returned
// even when journalctl exits non-zero.
func (c *Client) JournalTail(ctx context.Context, unit string, n int) string {
	res, _ := c.run(ctx, CallTimeout, "journalctl", cmdrunner.OutputCombined,
		"-u", unit, "-n", fmt.Sprint(n), "--no-pager", "-l")
	return res.Output
}

// Message IDs systemd logs when a unit's run ends: SD_MESSAGE_UNIT_SUCCESS, and
// SD_MESSAGE_UNIT_FAILURE_RESULT, which carries UNIT_RESULT.
const (
	msgUnitSuccess = "7ad2d189f7e94e70a38c781354912448"
	msgUnitFailure = "d9b373ed55a64feb8242e02dbe79a49c"
)

// LastRunOutcome returns how the unit's latest run ending at or after since
// ended: "success", or systemd's failure result ("exit-code", "signal", ...).
// found is false when no run ended since then. It reads the journal because
// systemd unloads an inactive unit, after which `systemctl show` reports no
// timestamps and a default Result.
func (c *Client) LastRunOutcome(ctx context.Context, unit string, since time.Time) (outcome string, found bool, err error) {
	res, err := c.run(ctx, CallTimeout, "journalctl", cmdrunner.OutputRaw,
		"--no-pager", "-q", "-o", "json", "-r", "-n", "1", "--since", fmt.Sprintf("@%d", since.Unix()),
		"UNIT="+unit, "MESSAGE_ID="+msgUnitSuccess, "MESSAGE_ID="+msgUnitFailure)
	if err != nil {
		return "", false, err
	}
	line := strings.TrimSpace(res.Stdout)
	if line == "" {
		return "", false, nil
	}
	var e struct {
		MessageID  string `json:"MESSAGE_ID"`
		UnitResult string `json:"UNIT_RESULT"`
	}
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return "", false, fmt.Errorf("parse journal entry for %s: %w", unit, err)
	}
	switch {
	case e.MessageID == msgUnitSuccess:
		return "success", true, nil
	case e.UnitResult != "":
		return e.UnitResult, true, nil
	}
	return "failed", true, nil
}

// ServiceUnits lists every loaded service unit name (active or not) with one
// systemctl call.
func (c *Client) ServiceUnits(ctx context.Context) ([]string, error) {
	res, err := c.systemctl(ctx, "list-units", "--type=service", "--all", "--plain", "--no-legend")
	if err != nil {
		return nil, err
	}
	var units []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	return units, nil
}
