package cloudflare

import "context"

// Config is the tunnel to report on.
type Config struct {
	MetricsURL      string
	Hostnames       []string
	PublicHostnames map[string]string
}

// Report is the tunnel's state as the doctor sees it.
//
// Each source degrades independently and carries its own error: a dead
// tunnel must not hide the hostname probe.
type Report struct {
	Stats     TunnelStats
	StatsErr  string
	Hostnames []HostnameStatus
}

// Fetch assembles a Report: cloudflared's local metrics and an anonymous
// probe of each published hostname.
func Fetch(ctx context.Context, cfg Config) Report {
	var out Report
	if stats, err := FetchTunnelStats(ctx, nil, cfg.MetricsURL); err != nil {
		out.StatsErr = err.Error()
	} else {
		out.Stats = stats
	}
	out.Hostnames = ProbeHostnames(ctx, nil, cfg.Hostnames, cfg.PublicHostnames)
	return out
}
