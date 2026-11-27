package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cloudflare"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/storage"
)

// apiFor returns Deps whose daemon check talks to h.
func apiFor(t *testing.T, h http.Handler) Deps {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return Deps{Daemon: srv.Client(), DaemonURL: srv.URL + "/api/version"}
}

// deadAPI returns Deps whose daemon check dials a closed port.
func deadAPI(t *testing.T) Deps {
	t.Helper()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	return Deps{DaemonURL: "http://" + addr + "/api/version"}
}

func status(t *testing.T, c Check, st Status) Result {
	t.Helper()
	res := c.Run(context.Background())
	if res.Status != st {
		t.Fatalf("%s: status = %s (%s), want %s", c.Name(), res.Status, res.Detail, st)
	}
	return res
}

func byName(t *testing.T, d Deps, name string) Check {
	t.Helper()
	for _, c := range Checks(d) {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no check %q", name)
	return nil
}

func TestCheckSetIsTheSlimSeven(t *testing.T) {
	t.Parallel()
	got := strings.Join(Names(Checks(Deps{})), ",")
	want := "daemon,caddy,disk,systemd-units,smart,cloudflare-tunnel,db-snapshot"
	if got != want {
		t.Fatalf("checks = %s, want %s", got, want)
	}
}

// ── daemon ──

func TestDaemonCheck(t *testing.T) {
	t.Parallel()
	healthy := apiFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	status(t, byName(t, healthy, "daemon"), OK)

	broken := apiFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	status(t, byName(t, broken, "daemon"), Fail)

	res := status(t, byName(t, deadAPI(t), "daemon"), Fail)
	if !strings.Contains(res.Fix, "systemctl start homelab-daemon") {
		t.Fatalf("fix = %q", res.Fix)
	}
}

func TestDaemonCheckTimeoutIsUnknown(t *testing.T) {
	t.Parallel()
	slow := apiFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if res := byName(t, slow, "daemon").Run(ctx); res.Status != Unknown {
		t.Fatalf("result = %+v", res)
	}
}

// ── caddy ──

func TestCaddyCheck(t *testing.T) {
	t.Parallel()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer good.Close()
	status(t, byName(t, Deps{DashboardURL: good.URL}, "caddy"), OK)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer bad.Close()
	status(t, byName(t, Deps{DashboardURL: bad.URL}, "caddy"), Fail)
}

func TestCaddyCheckAsksSystemdWhenDashboardIsUnreachable(t *testing.T) {
	t.Parallel()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	url := "http://" + l.Addr().String()
	l.Close()

	inactive := cmdrunnertest.NewFake()
	inactive.On(systemctl, "is-active", "caddy.service").Fail("inactive")
	res := status(t, byName(t, Deps{DashboardURL: url, Runner: inactive}, "caddy"), Fail)
	if !strings.Contains(res.Detail, "inactive") {
		t.Fatalf("detail = %q", res.Detail)
	}

	active := cmdrunnertest.NewFake()
	active.On(systemctl, "is-active", "caddy.service").Reply("active\n")
	res = status(t, byName(t, Deps{DashboardURL: url, Runner: active}, "caddy"), Fail)
	if !strings.Contains(res.Detail, "active but") {
		t.Fatalf("detail = %q", res.Detail)
	}

	// systemctl itself failing to answer is not evidence about caddy.
	status(t, byName(t, Deps{DashboardURL: url, Runner: cmdrunnertest.NewFake()}, "caddy"), Unknown)
}

func TestCaddyCheckTakesTheDeclaredDashboardAddress(t *testing.T) {
	t.Parallel()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer good.Close()
	declared := func(url string) func() (*config.Nix, error) {
		return func() (*config.Nix, error) {
			n := &config.Nix{}
			n.Stacks.DashboardURL = url
			return n, nil
		}
	}
	res := status(t, byName(t, Deps{Nix: declared(good.URL)}, "caddy"), OK)
	if !strings.Contains(res.Detail, good.URL) {
		t.Fatalf("detail = %q", res.Detail)
	}

	// With no address declared, systemd's word decides.
	active := cmdrunnertest.NewFake()
	active.On(systemctl, "is-active", "caddy.service").Reply("active\n")
	status(t, byName(t, Deps{Nix: declared(""), Runner: active}, "caddy"), OK)
	status(t, byName(t, Deps{Nix: declared(""), Runner: cmdrunnertest.NewFake()}, "caddy"), Unknown)
}

// ── disk ──

func TestDiskCheck(t *testing.T) {
	t.Parallel()
	statfsOf := func(used map[string]uint64, errs map[string]error) func(string) (uint64, uint64, error) {
		return func(p string) (uint64, uint64, error) {
			if err := errs[p]; err != nil {
				return 0, 0, err
			}
			return 100 << 30, (100 << 30) - used[p]<<30, nil
		}
	}
	d := Deps{Mounts: []string{"/", "/pool"}}

	d.Statfs = statfsOf(map[string]uint64{"/": 40, "/pool": 89}, nil)
	status(t, byName(t, d, "disk"), OK)

	d.Statfs = statfsOf(map[string]uint64{"/": 40, "/pool": 91}, nil)
	res := status(t, byName(t, d, "disk"), Fail)
	if !strings.Contains(res.Detail, "/pool: 91.0%") {
		t.Fatalf("detail = %q", res.Detail)
	}

	d.Statfs = statfsOf(map[string]uint64{"/": 40}, map[string]error{"/pool": &os.PathError{Op: "statfs", Path: "/pool", Err: syscall.ENOENT}})
	status(t, byName(t, d, "disk"), Fail)

	d.Statfs = statfsOf(map[string]uint64{"/": 40}, map[string]error{"/pool": errors.New("EIO")})
	status(t, byName(t, d, "disk"), Unknown)
}

// ── systemd-units ──

func TestSystemdUnitsExcludesDaemonSupervisedUnits(t *testing.T) {
	t.Parallel()
	fake := cmdrunnertest.NewFake()
	fake.On(systemctl, "list-units", "--failed").Reply(
		"homelab-health-audit.service loaded failed failed Audit\n" +
			"tailscaled.service loaded failed failed Tailscale\n" +
			"mnt-nfs.mount loaded failed failed NFS\n")
	d := Deps{Runner: fake}
	res := status(t, byName(t, d, "systemd-units"), Fail)
	if !strings.Contains(res.Detail, "tailscaled") || !strings.Contains(res.Detail, "mnt-nfs.mount") ||
		strings.Contains(res.Detail, "audit") {
		t.Fatalf("detail = %q", res.Detail)
	}
	if !strings.Contains(res.Detail, "2 failed") {
		t.Fatalf("detail = %q", res.Detail)
	}
}

func TestSystemdUnitsOKWhenOnlySupervisedUnitsFailed(t *testing.T) {
	t.Parallel()
	fake := cmdrunnertest.NewFake()
	fake.On(systemctl, "list-units", "--failed").Reply("homelab-health-audit.service loaded failed failed Audit\n")
	status(t, byName(t, Deps{Runner: fake}, "systemd-units"), OK)
}

func TestSystemdUnitsUnknownWithoutSystemd(t *testing.T) {
	t.Parallel()
	status(t, byName(t, Deps{Runner: cmdrunnertest.NewFake()}, "systemd-units"), Unknown)
}

// ── smart ──

func scrutiny(t *testing.T, body string, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSmartCheck(t *testing.T) {
	t.Parallel()
	failing := scrutiny(t, `{"data":{"summary":{
		"a":{"device":{"device_name":"sda","model_name":"M1","device_status":0}},
		"b":{"device":{"device_name":"sdb","model_name":"M2","device_status":1}}}}}`, 200)
	res := status(t, byName(t, Deps{ScrutinyURL: failing}, "smart"), Fail)
	if !strings.Contains(res.Detail, "sdb (M2)") || strings.Contains(res.Detail, "sda") {
		t.Fatalf("detail = %q", res.Detail)
	}

	watched := scrutiny(t, `{"data":{"summary":{"a":{"device":{"device_name":"sdc","model_name":"M1","device_status":2}}}}}`, 200)
	res = status(t, byName(t, Deps{ScrutinyURL: watched}, "smart"), OK)
	if !strings.Contains(res.Detail, "watching sdc") {
		t.Fatalf("detail = %q", res.Detail)
	}
}

func TestSmartCheckIsUnknownNotFailedWhenScrutinyMisbehaves(t *testing.T) {
	t.Parallel()
	status(t, byName(t, Deps{ScrutinyURL: "http://127.0.0.1:1"}, "smart"), Unknown)
	status(t, byName(t, Deps{ScrutinyURL: scrutiny(t, "oops", 503)}, "smart"), Unknown)
	status(t, byName(t, Deps{ScrutinyURL: scrutiny(t, "{not json", 200)}, "smart"), Unknown)
}

func storageStatusServer(t *testing.T, st storage.StorageStatus, code int) Deps {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/storage" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(code)
		if code == http.StatusOK {
			_ = json.NewEncoder(w).Encode(st)
		}
	}))
	t.Cleanup(srv.Close)
	return Deps{
		Daemon:     srv.Client(),
		StorageURL: srv.URL + "/api/storage",
		Nix:        func() (*config.Nix, error) { return &config.Nix{}, nil },
	}
}

func TestSmartCheckFromDaemon(t *testing.T) {
	t.Parallel()

	// Healthy pool and unassigned disks -> OK.
	deps := storageStatusServer(t, storage.StorageStatus{
		Pools: []storage.Pool{{
			Name: "pool",
			Disks: []storage.Disk{
				{Name: "sda", Path: "/dev/sda", FriendlyName: "Samsung SSD", Health: &storage.DiskHealth{Passed: true}},
				{Name: "sdb", Path: "/dev/sdb", Model: "IronWolf", Health: &storage.DiskHealth{Passed: true, BadSectors: 2}},
			},
		}},
		Unassigned: []storage.Disk{
			{Name: "sdc", Path: "/dev/sdc", FriendlyName: "Spare HDD", Health: &storage.DiskHealth{Passed: true}},
			{Name: "vda", Path: "/dev/vda", Model: "VirtIO"},
		},
	}, http.StatusOK)
	res := status(t, byName(t, deps, "smart"), OK)
	if !strings.Contains(res.Detail, "3 disks healthy") || !strings.Contains(res.Detail, "1 without SMART") || !strings.Contains(res.Detail, "sdb (IronWolf) (2 bad sectors)") {
		t.Fatalf("healthy detail = %q", res.Detail)
	}

	// Failing pool disk -> Fail.
	failingPool := storageStatusServer(t, storage.StorageStatus{
		Pools: []storage.Pool{{
			Name: "pool",
			Disks: []storage.Disk{
				{Name: "sda", Path: "/dev/sda", FriendlyName: "SSD", Health: &storage.DiskHealth{Passed: true}},
				{Name: "sdb", Path: "/dev/sdb", FriendlyName: "IronWolf 4TB", Health: &storage.DiskHealth{Passed: false, BadSectors: 16}},
			},
		}},
	}, http.StatusOK)
	res = status(t, byName(t, failingPool, "smart"), Fail)
	if !strings.Contains(res.Detail, "sdb (IronWolf 4TB)") || strings.Contains(res.Detail, "sda") || !strings.Contains(res.Fix, "Storage") {
		t.Fatalf("failing pool = %+v", res)
	}

	// Failing unassigned disk -> Fail.
	failingSpare := storageStatusServer(t, storage.StorageStatus{
		Unassigned: []storage.Disk{
			{Name: "sdd", Path: "/dev/sdd", Model: "Old Disk", Health: &storage.DiskHealth{Passed: false}},
		},
	}, http.StatusOK)
	res = status(t, byName(t, failingSpare, "smart"), Fail)
	if !strings.Contains(res.Detail, "sdd (Old Disk)") {
		t.Fatalf("failing spare = %+v", res)
	}

	// No SMART readings (e.g. VM disks or before first background pass) -> Unknown.
	noReadings := storageStatusServer(t, storage.StorageStatus{
		Unassigned: []storage.Disk{{Name: "vda", Path: "/dev/vda"}},
	}, http.StatusOK)
	res = status(t, byName(t, noReadings, "smart"), Unknown)
	if !strings.Contains(res.Detail, "no SMART readings from the daemon") {
		t.Fatalf("no readings = %+v", res)
	}

	// Daemon unreachable or bad response -> Unknown.
	status(t, byName(t, Deps{StorageURL: "http://127.0.0.1:1", Nix: func() (*config.Nix, error) { return &config.Nix{}, nil }}, "smart"), Unknown)
	status(t, byName(t, storageStatusServer(t, storage.StorageStatus{}, http.StatusServiceUnavailable), "smart"), Unknown)

	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{bad"))
	}))
	t.Cleanup(badJSON.Close)
	status(t, byName(t, Deps{Daemon: badJSON.Client(), StorageURL: badJSON.URL, Nix: func() (*config.Nix, error) { return &config.Nix{}, nil }}, "smart"), Unknown)
}

func TestSmartDaemonAlertOpensAfterTwoFailures(t *testing.T) {
	t.Parallel()
	deps := storageStatusServer(t, storage.StorageStatus{
		Pools: []storage.Pool{{
			Disks: []storage.Disk{
				{Name: "sdb", Path: "/dev/sdb", FriendlyName: "WD Red", Health: &storage.DiskHealth{Passed: false}},
			},
		}},
	}, http.StatusOK)
	checks := []Check{byName(t, deps, "smart")}
	sink := &fakeSink{open: map[string]string{}}
	aud := newAuditor(sink, "smart")
	ctx := context.Background()

	first := RunDaily(ctx, DailyConfig{Checks: checks, Auditor: aud})
	if len(first.Outcome.Opened) != 0 {
		t.Fatalf("first failure opened alert early: %+v", first.Outcome)
	}
	second := RunDaily(ctx, DailyConfig{Checks: checks, Auditor: aud})
	if !reflect.DeepEqual(second.Outcome.Opened, []string{"audit:smart"}) {
		t.Fatalf("second failure opened = %v, want [audit:smart]", second.Outcome.Opened)
	}
	if p, ok := sink.open["audit:smart"]; !ok || !strings.Contains(p, "SMART") {
		t.Fatalf("open problem = %+v", sink.open)
	}
	var _ notifications.ProblemState
}

// ── cloudflare-tunnel ──

// tunnelDeps runs the check against a stubbed report and an enabled tunnel.
func tunnelDeps(rep cloudflare.Report) Deps {
	return Deps{
		Nix: func() (*config.Nix, error) {
			return &config.Nix{CloudflareTunnel: config.CloudflareTunnel{Enabled: true}}, nil
		},
		Tunnel: func(context.Context, config.CloudflareTunnel) cloudflare.Report { return rep },
	}
}

func TestCloudflareTunnelCheck(t *testing.T) {
	t.Parallel()
	healthy := cloudflare.Report{
		Stats: cloudflare.TunnelStats{Ready: true, ReadyConnections: 4},
		Hostnames: []cloudflare.HostnameStatus{
			{Hostname: "a.example.net", Protection: cloudflare.Guarded}, {Hostname: "b.example.net", Protection: cloudflare.Public},
		},
	}
	status(t, byName(t, tunnelDeps(healthy), "cloudflare-tunnel"), OK)

	open := healthy
	open.Hostnames = []cloudflare.HostnameStatus{{Hostname: "x.example.net", Protection: cloudflare.Open}}
	open.Stats.ReadyConnections = 1 // degraded too: the exposure must still be what is reported
	res := status(t, byName(t, tunnelDeps(open), "cloudflare-tunnel"), Fail)
	if !strings.Contains(res.Detail, "x.example.net") {
		t.Fatalf("detail = %q", res.Detail)
	}

	degraded := healthy
	degraded.Stats.ReadyConnections = 2
	res = status(t, byName(t, tunnelDeps(degraded), "cloudflare-tunnel"), Fail)
	if !strings.Contains(res.Detail, "2 of 4") {
		t.Fatalf("detail = %q", res.Detail)
	}

	status(t, byName(t, tunnelDeps(cloudflare.Report{StatsErr: "metrics down"}), "cloudflare-tunnel"), Fail)
}

func TestCloudflareTunnelCheckWithoutATunnel(t *testing.T) {
	t.Parallel()
	off := Deps{Nix: func() (*config.Nix, error) { return &config.Nix{}, nil }}
	status(t, byName(t, off, "cloudflare-tunnel"), OK)
	broken := Deps{Nix: func() (*config.Nix, error) { return nil, errors.New("no config") }}
	status(t, byName(t, broken, "cloudflare-tunnel"), Unknown)
}

func TestCloudflareTunnelCheckUsesHostTunnelOnImage(t *testing.T) {
	t.Parallel()
	var seen config.CloudflareTunnel
	d := Deps{
		Nix: func() (*config.Nix, error) { return &config.Nix{}, nil },
		HostTunnel: func(context.Context) (config.CloudflareTunnel, bool) {
			return config.CloudflareTunnel{
				Enabled:         true,
				Hostnames:       []string{"open.example.net"},
				PublicHostnames: map[string]string{},
				Connectors:      4,
			}, true
		},
		Tunnel: func(_ context.Context, cfg config.CloudflareTunnel) cloudflare.Report {
			seen = cfg
			return cloudflare.Report{
				Stats:     cloudflare.TunnelStats{Ready: true, ReadyConnections: 4},
				Hostnames: []cloudflare.HostnameStatus{{Hostname: "open.example.net", Protection: cloudflare.Open}},
			}
		},
	}
	res := status(t, byName(t, d, "cloudflare-tunnel"), Fail)
	if !seen.Enabled || !strings.Contains(res.Detail, "open.example.net") {
		t.Fatalf("seen=%+v detail=%q", seen, res.Detail)
	}
}

// ── db-snapshot ──

func TestDBSnapshotCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	d := Deps{Now: func() time.Time { return now }, Nix: func() (*config.Nix, error) {
		return &config.Nix{Paths: config.Paths{DBBackupDir: dir}}, nil
	}}
	snap := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	status(t, byName(t, d, "db-snapshot"), Fail) // empty directory

	snap("homelab-2026-10-10.db", 100*time.Hour)
	status(t, byName(t, d, "db-snapshot"), Fail) // only a stale one

	snap(".homelab-2026-10-14.db.tmp", time.Minute) // temp files do not count
	snap("other.db", time.Minute)
	status(t, byName(t, d, "db-snapshot"), Fail)

	snap("homelab-2026-10-14.db", 35*time.Hour)
	res := status(t, byName(t, d, "db-snapshot"), OK)
	if !strings.Contains(res.Detail, "homelab-2026-10-14.db") {
		t.Fatalf("detail = %q", res.Detail)
	}

	snap("homelab-2026-10-14.db", 37*time.Hour)
	status(t, byName(t, d, "db-snapshot"), Fail) // 36 h boundary
}

func TestDBSnapshotMissingDirectoryFails(t *testing.T) {
	t.Parallel()
	d := Deps{Nix: func() (*config.Nix, error) {
		return &config.Nix{Paths: config.Paths{DBBackupDir: filepath.Join(t.TempDir(), "nope")}}, nil
	}}
	status(t, byName(t, d, "db-snapshot"), Fail)
}
