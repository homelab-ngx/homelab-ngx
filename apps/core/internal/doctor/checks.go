package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/cloudflare"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/storage"
)

const (
	systemctl = "/run/current-system/sw/bin/systemctl"
	// defaultDaemonURL's host is a placeholder: the CLI's client dials the
	// socket whatever the host.
	defaultDaemonURL = "http://unix/api/version"
	// defaultStorageURL queries the daemon's cached disk and SMART state.
	defaultStorageURL = "http://unix/api/storage"
	// SnapshotMaxAge is how old the newest database snapshot may be.
	SnapshotMaxAge = 36 * time.Hour
	diskFullPct    = 90
	// defaultConnectors is cloudflared's default HA connection count.
	defaultConnectors = 4
)

// Deps are the outside world the checks look at. Zero values get defaults, so
// tests set only what they exercise.
type Deps struct {
	// Daemon reaches the daemon's API (over its unix socket in the CLI); the
	// daemon and SMART checks read DaemonURL and StorageURL through it.
	Daemon     *http.Client
	DaemonURL  string
	StorageURL string
	// Runner executes systemctl.
	Runner cmdrunner.Runner
	// HTTP fetches the dashboard and Scrutiny. Its own timeout is irrelevant:
	// requests carry the check's context.
	HTTP *http.Client
	// Nix loads the read-only nix config (backup units, snapshot directory,
	// the Cloudflare tunnel).
	Nix func() (*config.Nix, error)
	// Tunnel reads the Cloudflare tunnel's state: cloudflared's metrics and an
	// anonymous probe of each published hostname.
	Tunnel func(ctx context.Context, cfg config.CloudflareTunnel) cloudflare.Report
	// HostTunnel returns the image host's stored tunnel config (from SQLite)
	// when the read-only nix config has no tunnel enabled.
	HostTunnel func(ctx context.Context) (config.CloudflareTunnel, bool)

	// DashboardURL is the address the caddy check asks; empty takes the
	// host's declared dashboard address.
	DashboardURL string
	// ScrutinyURL is Scrutiny's summary API; empty falls back to the host's
	// configured one, and when both are empty the SMART check reads from the
	// daemon's storage API instead.
	ScrutinyURL string
	// Mounts are the filesystems the disk check inspects; empty takes /, the
	// cache directory and the pool.
	Mounts []string
	// Statfs returns total and free bytes of the filesystem at path.
	Statfs func(path string) (total, free uint64, err error)
	Now    func() time.Time
}

func (d *Deps) defaults() {
	if d.HTTP == nil {
		d.HTTP = &http.Client{}
	}
	if d.Daemon == nil {
		d.Daemon = d.HTTP
	}
	if d.DaemonURL == "" {
		d.DaemonURL = defaultDaemonURL
	}
	if d.StorageURL == "" {
		d.StorageURL = defaultStorageURL
	}
	if d.Statfs == nil {
		d.Statfs = statfs
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Nix == nil {
		d.Nix = func() (*config.Nix, error) { return config.LoadNix("/etc/homelab-daemon/config.json") }
	}
	if d.Tunnel == nil {
		d.Tunnel = func(ctx context.Context, cf config.CloudflareTunnel) cloudflare.Report {
			return cloudflare.Fetch(ctx, cloudflare.Config{
				MetricsURL: cf.MetricsURL, Hostnames: cf.Hostnames, PublicHostnames: cf.PublicHostnames,
			})
		}
	}
}

// Checks returns the full check set, in display order.
func Checks(d Deps) []Check {
	d.defaults()
	return []Check{
		fn{"daemon", d.daemon},
		fn{"caddy", d.caddy},
		fn{"disk", d.disk},
		fn{"systemd-units", d.systemdUnits},
		fn{"smart", d.smart},
		fn{"cloudflare-tunnel", d.cloudflareTunnel},
		fn{"db-snapshot", d.dbSnapshot},
	}
}

type fn struct {
	name string
	run  func(ctx context.Context) Result
}

func (f fn) Name() string                   { return f.name }
func (f fn) Run(ctx context.Context) Result { return f.run(ctx) }

func ok(format string, a ...any) Result {
	return Result{Status: OK, Detail: fmt.Sprintf(format, a...)}
}

func fail(fix, format string, a ...any) Result {
	return Result{Status: Fail, Detail: fmt.Sprintf(format, a...), Fix: fix}
}

func unknown(format string, a ...any) Result {
	return Result{Status: Unknown, Detail: fmt.Sprintf(format, a...)}
}

// unreachable reports whether err is a failure to dial the daemon socket
// (as opposed to the daemon answering badly or the context expiring).
func unreachable(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// ── daemon ───────────────────────────────────────────────────────────────────

func (d Deps) daemon(ctx context.Context) Result {
	code, err := d.get(ctx, d.Daemon, d.DaemonURL)
	switch {
	case err == nil && code == http.StatusOK:
		return ok("daemon answered GET /api/version")
	case err == nil:
		return fail("systemctl status homelab-daemon.service; journalctl -u homelab-daemon",
			"daemon answered HTTP %d", code)
	case ctx.Err() != nil:
		return unknown("daemon did not answer in time")
	case unreachable(err):
		return fail("sudo systemctl start homelab-daemon.service", "daemon socket unreachable: %v", err)
	default:
		return unknown("cannot query daemon: %v", err)
	}
}

// get sends a GET and returns the status, draining a bounded body.
func (Deps) get(ctx context.Context, c *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// ── caddy ────────────────────────────────────────────────────────────────────

func (d Deps) caddy(ctx context.Context) Result {
	const fix = "systemctl status caddy.service; verify DNS resolves the dashboard host to this machine"
	if d.DashboardURL == "" && d.Nix != nil {
		if nix, err := d.Nix(); err == nil {
			d.DashboardURL = nix.Stacks.DashboardURL
		}
	}
	if d.DashboardURL == "" {
		// No declared address to ask: systemd's word is all there is.
		res, _ := d.Runner.Run(ctx, cmdrunner.Cmd{Module: "doctor", Name: systemctl, Args: []string{"is-active", "caddy.service"}})
		switch state := strings.TrimSpace(res.Stdout); state {
		case "active":
			return ok("caddy.service is active; no dashboard address is declared")
		case "":
			return unknown("no dashboard address is declared and caddy.service state unknown")
		default:
			return fail("sudo systemctl start caddy.service", "caddy.service is %s", state)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.DashboardURL, nil)
	if err != nil {
		return unknown("bad dashboard URL: %v", err)
	}
	resp, err := d.HTTP.Do(req)
	if err == nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusOK {
			return ok("HTTP 200 at %s", d.DashboardURL)
		}
		return fail(fix, "HTTP %d at %s", resp.StatusCode, d.DashboardURL)
	}
	if ctx.Err() != nil {
		return unknown("dashboard did not answer in time")
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return unknown("cannot resolve the dashboard host: %v", err)
	}
	// The dashboard is unreachable; ask systemd whether Caddy is even running.
	res, serr := d.Runner.Run(ctx, cmdrunner.Cmd{Module: "doctor", Name: systemctl, Args: []string{"is-active", "caddy.service"}})
	switch state := strings.TrimSpace(res.Stdout); {
	case state == "active":
		return fail(fix, "caddy.service is active but %s is unreachable: %v", d.DashboardURL, err)
	case state != "" && serr != nil:
		return fail("sudo systemctl start caddy.service", "caddy.service is %s", state)
	default:
		return unknown("dashboard unreachable (%v) and caddy.service state unknown", err)
	}
}

// ── disk ─────────────────────────────────────────────────────────────────────

// mounts are the filesystems the disk check inspects: Mounts, or /, the
// cache directory and the pools (the directories under storage.MountRoot).
func (d Deps) mounts() []string {
	if len(d.Mounts) > 0 {
		return d.Mounts
	}
	out := []string{"/"}
	if nix, err := d.Nix(); err == nil {
		if dir := nix.Paths.Cache(); !slices.Contains(out, dir) {
			out = append(out, dir)
		}
	}
	pools, _ := os.ReadDir(storage.MountRoot)
	for _, e := range pools {
		if e.IsDir() {
			out = append(out, filepath.Join(storage.MountRoot, e.Name()))
		}
	}
	return out
}

func (d Deps) disk(ctx context.Context) Result {
	const fix = "prune container images, clear system logs, or clean pool restore trees"
	var details []string
	var full, missing bool
	for _, mnt := range d.mounts() {
		total, free, err := d.Statfs(mnt)
		switch {
		case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			details = append(details, fmt.Sprintf("%s: missing", mnt))
			missing = true
			continue
		case err != nil:
			return unknown("cannot stat %s: %v", mnt, err)
		}
		used := total - free
		var pct float64
		if total > 0 {
			pct = float64(used) / float64(total) * 100
		}
		details = append(details, fmt.Sprintf("%s: %.1f%% used (%s/%s)", mnt, pct, fmtBytes(used), fmtBytes(total)))
		if pct >= diskFullPct {
			full = true
		}
	}
	if full || missing {
		return fail(fix, "%s", strings.Join(details, "; "))
	}
	return ok("%s", strings.Join(details, "; "))
}

func statfs(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bfree * uint64(st.Bsize), nil
}

func fmtBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ── systemd-units ────────────────────────────────────────────────────────────

// ownUnits are the doctor's own jobs; a failed audit must not be reported by
// the audit itself.
var ownUnits = []string{"homelab-health-audit.service"}

func (d Deps) systemdUnits(ctx context.Context) Result {
	supervised := supervisedUnits()
	res, err := d.Runner.Run(ctx, cmdrunner.Cmd{Module: "doctor", Name: systemctl,
		Args: []string{"list-units", "--failed", "--plain", "--no-legend"}})
	if err != nil {
		return unknown("cannot query systemd: %v", err)
	}
	var failed []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || supervised[f[0]] {
			continue
		}
		failed = append(failed, strings.TrimSuffix(f[0], ".service"))
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return fail("systemctl status <unit>; journalctl -u <unit>",
			"%d failed unit(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return ok("no failed units outside the daemon's supervision")
}

// supervisedUnits is the doctor's own jobs. A backup run needs no entry: its
// transient unit is collected when it ends, so it never lingers as failed,
// and the daemon reports the run itself.
func supervisedUnits() map[string]bool {
	set := map[string]bool{}
	add := func(u string) {
		if u == "" {
			return
		}
		if !strings.Contains(u, ".") {
			u += ".service"
		}
		set[u] = true
	}
	for _, u := range ownUnits {
		add(u)
	}
	return set
}

// ── smart ────────────────────────────────────────────────────────────────────

func (d Deps) smart(ctx context.Context) Result {
	url := d.ScrutinyURL
	if url == "" {
		if nix, err := d.Nix(); err == nil {
			url = nix.Doctor.ScrutinyURL
		}
	}
	if url != "" {
		return d.smartFromScrutiny(ctx, url)
	}
	return d.smartFromDaemon(ctx)
}

func (d Deps) smartFromDaemon(ctx context.Context) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.StorageURL, nil)
	if err != nil {
		return unknown("bad storage URL: %v", err)
	}
	resp, err := d.Daemon.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return unknown("daemon did not answer in time; SMART status unknown")
		}
		return unknown("daemon unreachable; SMART status unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unknown("daemon storage endpoint returned HTTP %d; SMART status unknown", resp.StatusCode)
	}
	var st storage.StorageStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&st); err != nil {
		return unknown("daemon storage response unreadable; SMART status unknown")
	}

	seen := map[string]bool{}
	var disks []storage.Disk
	add := func(list []storage.Disk) {
		for _, dk := range list {
			k := dk.Path
			if k == "" {
				k = dk.Name
			}
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			disks = append(disks, dk)
		}
	}
	for _, p := range st.Pools {
		add(p.Disks)
	}
	add(st.Unassigned)

	var checked int
	var failing, warned []string
	for _, dk := range disks {
		if dk.Health == nil {
			continue
		}
		checked++
		name := dk.Name
		if name == "" {
			name = dk.Path
		}
		model := dk.FriendlyName
		if model == "" {
			model = dk.Model
		}
		label := name
		if model != "" {
			label = fmt.Sprintf("%s (%s)", name, model)
		}
		if !dk.Health.Passed {
			failing = append(failing, label)
		} else if dk.Health.BadSectors > 0 {
			warned = append(warned, fmt.Sprintf("%s (%d bad sectors)", label, dk.Health.BadSectors))
		}
	}
	sort.Strings(failing)
	sort.Strings(warned)
	if len(failing) > 0 {
		return fail("open Storage in the dashboard, check the failing disk, and plan a replacement",
			"failing disks: %s", strings.Join(failing, ", "))
	}
	if checked == 0 {
		return unknown("no SMART readings from the daemon (%s)", pluralDisk(len(disks)))
	}
	detail := fmt.Sprintf("%s healthy", pluralDisk(checked))
	if checked < len(disks) {
		detail += fmt.Sprintf(" (%d without SMART)", len(disks)-checked)
	}
	if len(warned) > 0 {
		detail += "; bad sectors on " + strings.Join(warned, ", ")
	}
	return ok("%s", detail)
}

func pluralDisk(n int) string {
	if n == 1 {
		return "1 disk"
	}
	return fmt.Sprintf("%d disks", n)
}

func (d Deps) smartFromScrutiny(ctx context.Context, url string) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return unknown("bad Scrutiny URL: %v", err)
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return unknown("Scrutiny unreachable; SMART status unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unknown("Scrutiny returned HTTP %d; SMART status unknown", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Summary map[string]struct {
				Device struct {
					Name   string `json:"device_name"`
					Model  string `json:"model_name"`
					Status int    `json:"device_status"`
				} `json:"device"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return unknown("Scrutiny response unreadable; SMART status unknown")
	}
	// device_status is a bitmask: 1 = the drive's own SMART thresholds
	// failed, 2 = Scrutiny's statistical failure-rate model. Only the first
	// is an alert; the model flags benign counters (e.g. Seagate command
	// timeouts) and would page for nothing.
	const smartFailed = 1
	var failing, watched []string
	for _, dev := range body.Data.Summary {
		label := fmt.Sprintf("%s (%s)", dev.Device.Name, dev.Device.Model)
		switch {
		case dev.Device.Status&smartFailed != 0:
			failing = append(failing, label)
		case dev.Device.Status != 0:
			watched = append(watched, label)
		}
	}
	sort.Strings(failing)
	sort.Strings(watched)
	if len(failing) > 0 {
		return fail("open Scrutiny, check the failing attributes, and plan a replacement",
			"failing disks: %s", strings.Join(failing, ", "))
	}
	detail := fmt.Sprintf("%d disks healthy", len(body.Data.Summary))
	if len(watched) > 0 {
		detail += "; Scrutiny is watching " + strings.Join(watched, ", ")
	}
	return ok("%s", detail)
}

// ── cloudflare-tunnel ────────────────────────────────────────────────────────

// cloudflareTunnel reports on the second path into the homelab.
//
// The unprotected-hostname case is the one worth waking up for: a service
// published through the tunnel with no Access policy is reachable by anyone on
// the internet, and nothing at the origin can tell.
func (d Deps) cloudflareTunnel(ctx context.Context) Result {
	nix, err := d.Nix()
	if err != nil {
		return unknown("cannot read the nix config: %v", err)
	}
	cfg := nix.CloudflareTunnel
	if !cfg.Enabled && d.HostTunnel != nil {
		if ht, ok := d.HostTunnel(ctx); ok {
			cfg = ht
		}
	}
	if !cfg.Enabled {
		return ok("cloudflare tunnel is not configured")
	}
	cf := d.Tunnel(ctx, cfg)
	status := "systemctl status " + cfg.Unit
	if cfg.Unit == "" {
		status = "systemctl list-units 'cloudflared*'"
	}
	want := cfg.Connectors
	if want <= 0 {
		want = defaultConnectors
	}
	if cf.StatsErr != "" {
		return fail(status+"; curl -s "+strings.TrimRight(cfg.MetricsURL, "/")+"/ready",
			"tunnel metrics unavailable: %s", cf.StatsErr)
	}

	// Checked before connector health: a service exposed with no policy is
	// worse than a degraded tunnel, and reporting only the latter would bury it.
	var open []string
	for _, h := range cf.Hostnames {
		if h.Protection == cloudflare.Open {
			open = append(open, h.Hostname)
		}
	}
	if len(open) > 0 {
		return fail("add an Access policy, or mark the route Open to anyone in Settings > Cloudflare Tunnel (or list under publicHostnames on NixOS) if it is anonymous on purpose",
			"published with no Access policy: %s", strings.Join(open, ", "))
	}
	if !cf.Stats.Ready || cf.Stats.ReadyConnections < want {
		return fail(status, "%d of %d connectors ready", cf.Stats.ReadyConnections, want)
	}

	return ok("%d connectors, %d hostname(s) checked, %d request(s)",
		cf.Stats.ReadyConnections, len(cf.Hostnames), cf.Stats.TotalRequests)
}

// ── db-snapshot ──────────────────────────────────────────────────────────────

// dbSnapshot checks that the daemon's nightly VACUUM INTO snapshot is fresh.
// It reads file mtimes only, so it works with the daemon down.
func (d Deps) dbSnapshot(ctx context.Context) Result {
	nix, err := d.Nix()
	if err != nil {
		return unknown("cannot read the nix config: %v", err)
	}
	dir := nix.Paths.DBBackupDir
	if dir == "" {
		dir = filepath.Join(nix.Paths.Cache(), "daemon", "backups")
	}
	const fix = "journalctl -u homelab-daemon | grep snapshot; the daemon snapshots nightly (db.snapshotSchedule, default 02:45)"
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fail(fix, "snapshot directory %s does not exist", dir)
	} else if err != nil {
		return unknown("cannot read %s: %v", dir, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "homelab-*.db"))
	if err != nil {
		return unknown("cannot list %s: %v", dir, err)
	}
	var newest time.Time
	var newestPath string
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.ModTime().After(newest) {
			newest, newestPath = fi.ModTime(), m
		}
	}
	if newestPath == "" {
		return fail(fix, "no homelab-*.db snapshot in %s", dir)
	}
	age := d.Now().Sub(newest)
	if age > SnapshotMaxAge {
		return fail(fix, "newest snapshot %s is %s old (limit %s)", filepath.Base(newestPath), age.Round(time.Minute), SnapshotMaxAge)
	}
	return ok("%s is %s old", filepath.Base(newestPath), age.Round(time.Minute))
}
