package hostcfg

import (
	"testing"

	"homelab-ngx.com/core/internal/tilestatus"
)

func TestTiles(t *testing.T) {
	var h HostSettings
	h.Managed = ManagedImage
	got := tiles(h, "")
	for k, want := range map[string]tilestatus.TileState{"clock": tilestatus.Warn, "domain": tilestatus.Off, "lan": tilestatus.Off, "tailscale": tilestatus.Off, "wireguard": tilestatus.Off, "tunnel": tilestatus.Off, "ssh": tilestatus.Off} {
		if got[k].State != want || got[k].Count != nil {
			t.Errorf("empty host: %s = %+v, want %s", k, got[k], want)
		}
	}
	if fw := got["firewall"]; fw.State != tilestatus.None || fw.Count != nil {
		t.Errorf("no openings: firewall = %+v", fw)
	}

	h.TimeSync.Synced = true
	h.Domain.Domain = "home.example.com"
	h.Network.LAN = []string{"192.168.1.0/24"}
	h.Tailscale.Enabled = true
	h.WireGuard = WireGuard{Enabled: true, Peers: []Peer{{Name: "a"}, {Name: "b"}}}
	h.Tunnel = Tunnel{Enabled: true, CredentialsSet: true, Routes: []Route{{}}}
	h.Firewall.Openings = []Opening{{Port: 8080}, {Port: 8443}, {Port: 9000}}
	h.Host.SSHKeys = []string{"ssh-ed25519 AAAA"}
	got = tiles(h, TailscaleNeedsLogin)
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
	check("wireguard", tilestatus.OK, 2)
	check("tunnel", tilestatus.OK, 1)
	check("firewall", tilestatus.None, 3)
	check("ssh", tilestatus.None, 1)

	// Enabled without credentials is not on yet.
	h.Tunnel.CredentialsSet = false
	if s := tiles(h, TailscaleRunning)["tunnel"]; s.State != tilestatus.Off {
		t.Errorf("tunnel without credentials = %+v", s)
	}
}

func TestTilesTailscale(t *testing.T) {
	for ts, want := range map[TailscaleState]tilestatus.TileState{
		TailscaleRunning: tilestatus.OK, TailscaleNeedsLogin: tilestatus.Warn, TailscaleNeedsApproval: tilestatus.Warn,
		TailscaleStarting: tilestatus.Info, TailscaleOff: tilestatus.Off,
	} {
		if got := tailscaleTile(true, ts); got != want {
			t.Errorf("%s = %s, want %s", ts, got, want)
		}
	}
	if got := tailscaleTile(false, TailscaleRunning); got != tilestatus.Off {
		t.Errorf("turned off = %s", got)
	}
}

func TestTilesApplyErrors(t *testing.T) {
	h := HostSettings{Managed: ManagedImage, TimeSync: TimeSync{Synced: true}}
	h.Tunnel = Tunnel{Enabled: true, CredentialsSet: true, Routes: []Route{{}, {}}}
	h.Applied.Errors = []string{"caddy: reload failed", "tunnel: no route", "etc: write failed", "something else", "ssh: x"}
	got := tiles(h, "")
	for _, k := range []string{"domain", "tunnel", "system", "ssh"} {
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
	if s := tiles(h, "")["system"]; s.State == tilestatus.Bad {
		t.Errorf("nixos host = %+v", s)
	}
}
