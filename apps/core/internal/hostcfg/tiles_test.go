package hostcfg

import (
	"testing"
	"time"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTiles(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var h HostSettings
	h.Managed = ManagedImage
	got := tiles(h, TailscaleStatus{}, now)
	for k, want := range map[string]tilestatus.TileState{"clock": tilestatus.Warn, "mail": tilestatus.Off, "domain": tilestatus.Off, "lan": tilestatus.Off, "tailscale": tilestatus.Off, "wireguard": tilestatus.Off, "tunnel": tilestatus.Off, "ssh": tilestatus.Off} {
		if got[k].State != want || got[k].Count != nil {
			t.Errorf("empty host: %s = %+v, want %s", k, got[k], want)
		}
	}
	if fw := got["firewall"]; fw.State != tilestatus.None || fw.Count != nil {
		t.Errorf("no openings: firewall = %+v", fw)
	}

	h.TimeSync.Synced = true
	h.SMTP = SMTP{Enabled: true, Host: "smtp.example.com"}
	h.Domain.Domain = "home.example.com"
	h.Network.LAN = []string{"192.168.1.0/24"}
	h.Tailscale.Enabled = true
	h.WireGuard = WireGuard{Enabled: true, Peers: []Peer{{Name: "a"}, {Name: "b"}}}
	h.Tunnel = Tunnel{Enabled: true, CredentialsSet: true, Routes: []Route{{}}}
	h.Firewall.Openings = []Opening{{Port: 8080}, {Port: 8443}, {Port: 9000}}
	h.Host.SSHKeys = []string{"ssh-ed25519 AAAA"}
	got = tiles(h, TailscaleStatus{State: TailscaleNeedsLogin}, now)
	for _, k := range []string{"clock", "domain", "lan", "system"} {
		if s, ok := got[k]; ok {
			t.Errorf("%s = %+v, want no badge", k, s)
		}
	}
	check := func(k string, state tilestatus.TileState, n int) {
		t.Helper()
		s := got[k]
		if s.State != state || s.Count == nil || *s.Count != n {
			t.Errorf("%s = %+v, want %s %d", k, s, state, n)
		}
	}
	if got["tailscale"].State != tilestatus.Warn {
		t.Errorf("tailscale = %+v", got["tailscale"])
	}
	if got["mail"].State != tilestatus.OK {
		t.Errorf("mail = %+v", got["mail"])
	}
	check("wireguard", tilestatus.OK, 2)
	check("tunnel", tilestatus.OK, 1)
	check("firewall", tilestatus.None, 3)
	check("ssh", tilestatus.None, 1)

	// Enabled without credentials is not on yet.
	h.Tunnel.CredentialsSet = false
	if s := tiles(h, TailscaleStatus{State: TailscaleRunning}, now)["tunnel"]; s.State != tilestatus.Off {
		t.Errorf("tunnel without credentials = %+v", s)
	}
}

func TestTilesTailscale(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	soon := now.Add(5 * 24 * time.Hour)
	later := now.Add(30 * 24 * time.Hour)
	past := now.Add(-time.Hour)

	for _, c := range []struct {
		name    string
		enabled bool
		ts      TailscaleStatus
		want    tilestatus.TileState
		count   *int
	}{
		{"disabled", false, TailscaleStatus{State: TailscaleRunning, OnlinePeers: 4}, tilestatus.Off, nil},
		{"off", true, TailscaleStatus{State: TailscaleOff}, tilestatus.Off, nil},
		{"starting", true, TailscaleStatus{State: TailscaleStarting}, tilestatus.Info, nil},
		{"needs-login", true, TailscaleStatus{State: TailscaleNeedsLogin}, tilestatus.Warn, nil},
		{"needs-approval", true, TailscaleStatus{State: TailscaleNeedsApproval}, tilestatus.Warn, nil},
		{"stopped", true, TailscaleStatus{State: TailscaleStopped}, tilestatus.Warn, nil},
		{"expired", true, TailscaleStatus{State: TailscaleExpired}, tilestatus.Bad, nil},
		{"running-ok", true, TailscaleStatus{State: TailscaleRunning, KeyExpiry: &later, OnlinePeers: 3}, tilestatus.OK, intPtr(3)},
		{"expiring-soon", true, TailscaleStatus{State: TailscaleRunning, KeyExpiry: &soon, OnlinePeers: 2}, tilestatus.Warn, intPtr(2)},
		{"expired-timestamp", true, TailscaleStatus{State: TailscaleRunning, KeyExpiry: &past, OnlinePeers: 2}, tilestatus.Bad, intPtr(2)},
		{"pending-route", true, TailscaleStatus{State: TailscaleRunning, Routes: []TailscaleRoute{{Prefix: "192.168.1.0/24", Approved: false}}, OnlinePeers: 1}, tilestatus.Warn, intPtr(1)},
		{"pending-exit-node", true, TailscaleStatus{State: TailscaleRunning, ExitNode: TailscaleExitPending}, tilestatus.Warn, nil},
		{"health-warning", true, TailscaleStatus{State: TailscaleRunning, Health: []string{"relay issue"}, OnlinePeers: 4}, tilestatus.Warn, intPtr(4)},
	} {
		got := tailscaleTile(c.enabled, c.ts, now)
		if got.State != c.want || (got.Count == nil) != (c.count == nil) || (c.count != nil && *got.Count != *c.count) {
			t.Errorf("%s = %+v, want state=%s count=%v", c.name, got, c.want, c.count)
		}
	}
}

func intPtr(n int) *int { return &n }

func TestTilesApplyErrors(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	h := HostSettings{Managed: ManagedImage, TimeSync: TimeSync{Synced: true}}
	h.Tunnel = Tunnel{Enabled: true, CredentialsSet: true, Routes: []Route{{}, {}}}
	h.Applied.Errors = []string{"caddy: reload failed", "tunnel: no route", "etc: write failed", "something else", "ssh: x", "smtp: failed"}
	got := tiles(h, TailscaleStatus{}, now)
	for _, k := range []string{"domain", "tunnel", "system", "ssh", "mail"} {
		if got[k].State != tilestatus.Bad {
			t.Errorf("%s = %+v, want bad", k, got[k])
		}
	}
	if c := got["tunnel"].Count; c == nil || *c != 2 {
		t.Errorf("a failing tunnel keeps its count: %+v", got["tunnel"])
	}
	if got["firewall"].State == tilestatus.Bad {
		t.Error("firewall did not fail")
	}

	// A NixOS host's settings are not applied by the daemon.
	h.Managed = ManagedNixOS
	if s := tiles(h, TailscaleStatus{}, now)["system"]; s.State == tilestatus.Bad {
		t.Errorf("nixos host = %+v", s)
	}
}
