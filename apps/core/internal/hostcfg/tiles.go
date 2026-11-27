package hostcfg

import (
	"context"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/tilestatus"
)

const tailscaleExpiryWarnWindow = 14 * 24 * time.Hour

// Tiles reports the Settings tiles this package backs: clock, domain,
// lan, tailscale, wireguard, tunnel, firewall and ssh, and system when
// a part of the last apply failed that no other tile owns.
func (s *Service) Tiles(ctx context.Context) (map[string]tilestatus.TileStatus, error) {
	h, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	var ts TailscaleStatus
	if h.Tailscale.Enabled {
		ts = s.TailscaleStatus(ctx)
	}
	return tiles(h, ts, s.now()), nil
}

// applyTile is the tile each part of the apply belongs to (Apply's part
// names); etc and anything else are the system tile's.
var applyTile = map[string]string{"caddy": "domain", "firewall": "firewall", "tunnel": "tunnel", "tailscale": "tailscale", "ssh": "ssh", "smtp": "mail"}

func tiles(h HostSettings, ts TailscaleStatus, now time.Time) map[string]tilestatus.TileStatus {
	t := map[string]tilestatus.TileStatus{}
	if !h.TimeSync.Synced {
		t["clock"] = tilestatus.Of(tilestatus.Warn)
	}
	if h.SMTP.Enabled && h.SMTP.Host != "" {
		t["mail"] = tilestatus.Of(tilestatus.OK)
	} else {
		t["mail"] = tilestatus.Of(tilestatus.Off)
	}
	if h.Domain.Domain == "" {
		t["domain"] = tilestatus.Of(tilestatus.Off)
	}
	if len(h.Network.LAN) == 0 {
		t["lan"] = tilestatus.Of(tilestatus.Off)
	}
	t["tailscale"] = tailscaleTile(h.Tailscale.Enabled, ts, now)
	if h.WireGuard.Enabled {
		t["wireguard"] = counted(tilestatus.OK, len(h.WireGuard.Peers))
	} else {
		t["wireguard"] = tilestatus.Of(tilestatus.Off)
	}
	if h.Tunnel.Enabled && h.Tunnel.CredentialsSet {
		t["tunnel"] = counted(tilestatus.OK, len(h.Tunnel.Routes))
	} else {
		t["tunnel"] = tilestatus.Of(tilestatus.Off)
	}
	t["firewall"] = counted(tilestatus.None, len(h.Firewall.Openings))
	if n := len(h.Host.SSHKeys); n > 0 {
		t["ssh"] = counted(tilestatus.None, n)
	} else {
		t["ssh"] = tilestatus.Of(tilestatus.Off)
	}
	if h.Managed == ManagedNixOS {
		return t
	}
	for _, e := range h.Applied.Errors {
		part, _, _ := strings.Cut(e, ":")
		key, ok := applyTile[part]
		if !ok {
			key = "system"
		}
		st := t[key]
		st.State = tilestatus.Bad
		t[key] = st
	}
	return t
}

// tailscaleTile is the tone and online-peer count of Tailscale's state; turned off is off.
func tailscaleTile(enabled bool, ts TailscaleStatus, now time.Time) tilestatus.TileStatus {
	if !enabled {
		return tilestatus.Of(tilestatus.Off)
	}
	switch ts.State {
	case TailscaleRunning:
		if ts.KeyExpiry != nil && !ts.KeyExpiry.IsZero() && !ts.KeyExpiry.After(now) {
			return counted(tilestatus.Bad, ts.OnlinePeers)
		}
		warn := len(ts.Health) > 0 || ts.ExitNode == TailscaleExitPending
		if !warn && ts.KeyExpiry != nil && !ts.KeyExpiry.IsZero() && ts.KeyExpiry.Sub(now) <= tailscaleExpiryWarnWindow {
			warn = true
		}
		if !warn {
			for _, r := range ts.Routes {
				if !r.Approved {
					warn = true
					break
				}
			}
		}
		if warn {
			return counted(tilestatus.Warn, ts.OnlinePeers)
		}
		return counted(tilestatus.OK, ts.OnlinePeers)
	case TailscaleExpired:
		return tilestatus.Of(tilestatus.Bad)
	case TailscaleNeedsLogin, TailscaleNeedsApproval, TailscaleStopped:
		return tilestatus.Of(tilestatus.Warn)
	case TailscaleStarting:
		return tilestatus.Of(tilestatus.Info)
	}
	return tilestatus.Of(tilestatus.Off)
}

// counted carries n only when there is something to count.
func counted(s tilestatus.TileState, n int) tilestatus.TileStatus {
	if n == 0 {
		return tilestatus.Of(s)
	}
	return tilestatus.Counted(s, n)
}
