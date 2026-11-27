package hostcfg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/cloudflare"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/store"
)

const (
	tunnelProbeEvery  = 5 * time.Minute
	tunnelProbeSettle = 2 * time.Second
	tunnelMetricsWait = 1500 * time.Millisecond
	tunnelStartGrace  = 15 * time.Second
	defaultWantConns  = 4
	maxRecentErrors   = 5
)

// TunnelState is the live state of cloudflared on the host.
type TunnelState string

const (
	TunnelOff       TunnelState = "off"
	TunnelStarting  TunnelState = "starting"
	TunnelConnected TunnelState = "connected"
	TunnelDegraded  TunnelState = "degraded"
	TunnelDown      TunnelState = "down"
)

// Schema documents TunnelState as its enum.
func (TunnelState) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"off", "starting", "connected", "degraded", "down"}}
}

// RouteDNSState is whether the host added a tunnel route's DNS CNAME record.
type RouteDNSState string

const (
	RouteDNSAdded   RouteDNSState = "added"
	RouteDNSFailed  RouteDNSState = "failed"
	RouteDNSManual  RouteDNSState = "manual"
	RouteDNSPending RouteDNSState = "pending"
)

// Schema documents RouteDNSState as its enum.
func (RouteDNSState) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"added", "failed", "manual", "pending"}}
}

// RouteProtection is what an anonymous visitor gets from a published hostname.
type RouteProtection string

const (
	RouteProtectionChecking    RouteProtection = "checking"
	RouteProtectionProtected   RouteProtection = "protected"
	RouteProtectionPublic      RouteProtection = "public"
	RouteProtectionOpen        RouteProtection = "open"
	RouteProtectionNotServed   RouteProtection = "not-served"
	RouteProtectionUnreachable RouteProtection = "unreachable"
)

// Schema documents RouteProtection as its enum.
func (RouteProtection) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"checking", "protected", "public", "open", "not-served", "unreachable"}}
}

// RouteStatus is the live DNS and exposure state of one tunnel route.
type RouteStatus struct {
	Hostname   string          `json:"hostname"`
	Path       string          `json:"path,omitempty"`
	Target     string          `json:"target"`
	Public     bool            `json:"public"`
	DNS        RouteDNSState   `json:"dns" doc:"added: CNAME written by the host. failed: route dns failed. manual: credentials pasted without a zone cert. pending: not written yet."`
	DNSError   string          `json:"dns_error,omitempty" doc:"Why route dns failed, when dns is failed."`
	Protection RouteProtection `json:"protection" doc:"What an anonymous HTTP request receives at this hostname."`
	StatusCode int             `json:"status_code,omitempty" doc:"HTTP status code from the last probe."`
	CheckedAt  *time.Time      `json:"checked_at,omitempty" doc:"When the hostname was last probed."`
	NoTarget   bool            `json:"no_target,omitempty" doc:"The route's target matches no installed stack's approved route."`
}

// TunnelStatus is cloudflared's live health and each route's DNS and exposure state.
type TunnelStatus struct {
	State        TunnelState   `json:"state" doc:"Live connector health: off, starting, connected, degraded or down."`
	Connections  int           `json:"connections" doc:"Ready HA connections to Cloudflare."`
	Want         int           `json:"want" doc:"Expected HA connections (normally 4)."`
	Colos        []string      `json:"colos" doc:"Cloudflare edge locations the tunnel is connected to."`
	ConnectorID  string        `json:"connector_id,omitempty"`
	Version      string        `json:"version,omitempty" doc:"cloudflared version from its metrics."`
	StartedAt    *time.Time    `json:"started_at,omitempty" doc:"When the cloudflared unit started."`
	Zone         string        `json:"zone,omitempty" doc:"The Cloudflare zone approved at login."`
	Error        string        `json:"error,omitempty" doc:"Why the tunnel is down or degraded."`
	RecentErrors []string      `json:"recent_errors" doc:"Up to five recent cloudflared error lines when degraded or down."`
	Routes       []RouteStatus `json:"routes"`
}

// tunnelDNSReader is implemented by ImageApplier so Service can read per-route
// DNS sync outcomes without coupling tests to ImageApplier.
type tunnelDNSReader interface {
	TunnelRouteDNS(tunnelID, hostname string) (RouteDNSState, string, bool)
}

type probeEntry struct {
	protection cloudflare.Protection
	statusCode int
	err        string
	at         time.Time
}

type tunnelProbeLoop struct {
	mu     sync.Mutex
	probes map[string]probeEntry // lowercased hostname -> last probe
	kick   chan bool             // true = wait settle delay before probing
	settle time.Duration
	every  time.Duration

	// Seams for tests.
	fetchStats func(ctx context.Context, baseURL string) (cloudflare.TunnelStats, error)
	probeHosts func(ctx context.Context, hostnames []string, allow map[string]string) []cloudflare.HostnameStatus
}

func newTunnelProbeLoop() *tunnelProbeLoop {
	return &tunnelProbeLoop{
		probes: map[string]probeEntry{},
		kick:   make(chan bool, 1),
		settle: tunnelProbeSettle,
		every:  tunnelProbeEvery,
		fetchStats: func(ctx context.Context, baseURL string) (cloudflare.TunnelStats, error) {
			c := &http.Client{Timeout: tunnelMetricsWait}
			return cloudflare.FetchTunnelStats(ctx, c, baseURL)
		},
		probeHosts: func(ctx context.Context, hostnames []string, allow map[string]string) []cloudflare.HostnameStatus {
			return cloudflare.ProbeHostnames(ctx, nil, hostnames, allow)
		},
	}
}

func (s *Service) kickTunnelProbe(settle bool) {
	select {
	case s.tunnelProbe.kick <- settle:
	default:
	}
}

func (s *Service) clearTunnelProbe() {
	s.tunnelProbe.mu.Lock()
	s.tunnelProbe.probes = map[string]probeEntry{}
	s.tunnelProbe.mu.Unlock()
	s.zoneMu.Lock()
	s.zoneCheckedFor = ""
	s.zoneMu.Unlock()
}

// RunTunnelProbe periodically probes each configured tunnel route from outside
// so GET /host-settings/tunnel/status and tiles() stay fast and non-blocking.
func (s *Service) RunTunnelProbe(ctx context.Context) {
	every := s.tunnelProbe.every
	if every <= 0 {
		every = tunnelProbeEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.backfillTunnelZone(ctx)
		s.probeAllTunnelRoutes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case settle := <-s.tunnelProbe.kick:
			if settle && s.tunnelProbe.settle > 0 {
				timer := time.NewTimer(s.tunnelProbe.settle)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}
}

func (s *Service) probeAllTunnelRoutes(ctx context.Context) {
	v, err := s.Values(ctx)
	if err != nil || !v.Tunnel.Enabled || len(v.Tunnel.Hostnames) == 0 {
		return
	}
	hosts := uniqueHosts(v.Tunnel.Hostnames)
	results := s.tunnelProbe.probeHosts(ctx, hosts, v.Tunnel.PublicHostnames)
	now := s.now()
	s.tunnelProbe.mu.Lock()
	defer s.tunnelProbe.mu.Unlock()
	keep := map[string]bool{}
	for _, h := range hosts {
		keep[strings.ToLower(h)] = true
	}
	for k := range s.tunnelProbe.probes {
		if !keep[k] {
			delete(s.tunnelProbe.probes, k)
		}
	}
	for _, r := range results {
		s.tunnelProbe.probes[strings.ToLower(r.Hostname)] = probeEntry{
			protection: r.Protection, statusCode: r.StatusCode, err: r.Err, at: now,
		}
	}
}

func uniqueHosts(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, h := range in {
		k := strings.ToLower(strings.TrimSpace(h))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// backfillTunnelZone tries once per tunnel ID to resolve Tunnel.Zone from a
// stored cert.pem on hosts that connected before zone capture existed.
func (s *Service) backfillTunnelZone(ctx context.Context) {
	if s.managed != ManagedImage {
		return
	}
	snap, err := s.snapshot(ctx)
	if err != nil || !snap.Tunnel.DNSManaged || snap.Tunnel.Zone != "" || snap.TunnelCert == "" {
		return
	}
	key := snap.Tunnel.TunnelID
	if key == "" {
		return
	}
	s.zoneMu.Lock()
	already := s.zoneCheckedFor == key
	s.zoneCheckedFor = key
	s.zoneMu.Unlock()
	if already {
		return
	}
	zone := s.resolveZone(ctx, snap.TunnelCert)
	if zone == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, err := s.rows(ctx)
	if err != nil {
		return
	}
	var t Tunnel
	if r, ok := rs["tunnel"]; ok {
		_ = json.Unmarshal(r.value, &t)
	}
	var sec tunnelSecret
	if err := s.open(rs["tunnel"], &sec); err != nil || sec.Cert == "" || tunnelID(sec.Credentials) != key {
		return
	}
	if t.Zone == "" {
		t.Zone = zone
		t.Routes = nonNil(t.Routes)
		_ = s.save(ctx, "tunnel", t, sec)
	}
}

// parseCertPEM extracts zoneID and apiToken from cloudflared's cert.pem.
func parseCertPEM(cert string) (zoneID, apiToken string, ok bool) {
	rest := []byte(cert)
	for len(rest) > 0 {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "ARGO TUNNEL TOKEN" {
			continue
		}
		raw := blk.Bytes
		var tok struct {
			ZoneID   string `json:"zoneID"`
			APIToken string `json:"apiToken"`
		}
		if json.Unmarshal(raw, &tok) != nil {
			// Some tools base64-encode JSON before PEM encoding.
			if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err == nil {
				_ = json.Unmarshal(dec, &tok)
			}
		}
		if tok.ZoneID != "" && tok.APIToken != "" {
			return tok.ZoneID, tok.APIToken, true
		}
	}
	return "", "", false
}

// resolveZone asks Cloudflare for the zone name matching cert.pem's zoneID.
// Best-effort: returns "" if the cert lacks zoneID/apiToken or the token cannot
// read the zone, so login is never blocked.
func (s *Service) resolveZone(ctx context.Context, cert string) string {
	zoneID, apiToken, ok := parseCertPEM(cert)
	if !ok {
		return ""
	}
	base := s.cfAPIBase
	if base == "" {
		base = "https://api.cloudflare.com"
	}
	client := s.cfHTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u := strings.TrimRight(base, "/") + "/client/v4/zones/" + url.PathEscape(zoneID)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		Success bool `json:"success"`
		Result  struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) != nil || !body.Success {
		return ""
	}
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(body.Result.Name)), ".")
	if !domainRE.MatchString(name) {
		return ""
	}
	return name
}

func inZone(hostname, zone string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	z := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	return z != "" && (h == z || strings.HasSuffix(h, "."+z))
}

// TunnelStatus reads local cloudflared metrics, systemd unit state, per-route
// DNS outcomes, and cached exposure probes. Never blocks on outbound network.
func (s *Service) TunnelStatus(ctx context.Context) TunnelStatus {
	st := TunnelStatus{
		State:        TunnelOff,
		Want:         defaultWantConns,
		Colos:        []string{},
		RecentErrors: []string{},
		Routes:       []RouteStatus{},
	}
	hs, err := s.Get(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	t := hs.Tunnel
	st.Zone = t.Zone
	unit := tunnelUnit
	metricsURL := "http://" + tunnelMetrics
	if s.managed == ManagedNixOS && s.nix != nil {
		if s.nix.CloudflareTunnel.Unit != "" {
			unit = s.nix.CloudflareTunnel.Unit
		}
		if s.nix.CloudflareTunnel.MetricsURL != "" {
			metricsURL = s.nix.CloudflareTunnel.MetricsURL
		}
		if s.nix.CloudflareTunnel.Connectors > 0 {
			st.Want = s.nix.CloudflareTunnel.Connectors
		}
	}

	active := t.Enabled && (s.managed == ManagedNixOS || t.CredentialsSet)
	if active {
		uState, startedAt := s.readTunnelUnit(ctx, unit)
		st.StartedAt = startedAt
		mctx, cancel := context.WithTimeout(ctx, tunnelMetricsWait)
		stats, statsErr := s.tunnelProbe.fetchStats(mctx, metricsURL)
		cancel()

		justStarted := startedAt != nil && s.now().Sub(*startedAt) < tunnelStartGrace
		switch {
		case uState == "activating":
			st.State = TunnelStarting
		case statsErr != nil:
			if uState == "active" && justStarted {
				st.State = TunnelStarting
			} else {
				st.State = TunnelDown
				if uState != "" && uState != "active" {
					st.Error = fmt.Sprintf("%s is %s", unit, uState)
				} else {
					st.Error = statsErr.Error()
				}
			}
		default:
			st.Connections = stats.ReadyConnections
			st.Colos = nonNil(stats.EdgeLocations)
			st.ConnectorID = stats.ConnectorID
			st.Version = stats.Version
			switch {
			case stats.Ready && stats.ReadyConnections >= st.Want:
				st.State = TunnelConnected
			case stats.ReadyConnections == 0:
				if justStarted {
					st.State = TunnelStarting
				} else {
					st.State = TunnelDown
					st.Error = fmt.Sprintf("0 of %d connections ready", st.Want)
				}
			default:
				st.State = TunnelDegraded
				st.Error = fmt.Sprintf("%d of %d connections ready", stats.ReadyConnections, st.Want)
			}
		}
		if st.State == TunnelDown || st.State == TunnelDegraded {
			st.RecentErrors = s.recentTunnelErrors(ctx, unit)
		}
	}

	var knownTargets map[string]bool
	if s.stackRoutes != nil && hs.Domain.Domain != "" {
		knownTargets = map[string]bool{}
		for _, host := range s.stackRoutes(ctx) {
			knownTargets[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))] = true
		}
	}
	var dnsReader tunnelDNSReader
	if r, ok := s.apply.(tunnelDNSReader); ok {
		dnsReader = r
	}

	s.tunnelProbe.mu.Lock()
	defer s.tunnelProbe.mu.Unlock()
	for _, r := range t.Routes {
		rs := RouteStatus{
			Hostname:   r.Hostname,
			Path:       r.Path,
			Target:     r.Target,
			Public:     r.Public,
			DNS:        RouteDNSPending,
			Protection: RouteProtectionChecking,
		}
		if !t.DNSManaged {
			rs.DNS = RouteDNSManual
		} else if dnsReader != nil && t.TunnelID != "" {
			if ds, derr, ok := dnsReader.TunnelRouteDNS(t.TunnelID, r.Hostname); ok {
				rs.DNS = ds
				rs.DNSError = derr
			}
		}
		if active {
			if p, ok := s.tunnelProbe.probes[strings.ToLower(r.Hostname)]; ok {
				at := p.at
				rs.CheckedAt = &at
				rs.StatusCode = p.statusCode
				rs.Protection = mapProtection(p.protection, r.Public)
			}
		}
		if knownTargets != nil {
			target := strings.ToLower(RouteTarget(r.Target, hs.Domain.Domain))
			if target != "" && !knownTargets[target] {
				rs.NoTarget = true
			}
		}
		st.Routes = append(st.Routes, rs)
	}
	return st
}

func mapProtection(p cloudflare.Protection, public bool) RouteProtection {
	switch p {
	case cloudflare.Guarded:
		return RouteProtectionProtected
	case cloudflare.NotServed:
		return RouteProtectionNotServed
	case cloudflare.Open, cloudflare.Public:
		// Reflect changes to Route.Public immediately without waiting for the next probe.
		if public {
			return RouteProtectionPublic
		}
		return RouteProtectionOpen
	case cloudflare.Unknown:
		return RouteProtectionUnreachable
	default:
		return RouteProtectionChecking
	}
}

func (s *Service) readTunnelUnit(ctx context.Context, unit string) (activeState string, startedAt *time.Time) {
	if s.run == nil || unit == "" {
		return "", nil
	}
	rctx, cancel := context.WithTimeout(ctx, tunnelMetricsWait)
	defer cancel()
	res, err := s.run.Run(rctx, cmdrunner.Cmd{
		Module: "hostcfg",
		Name:   "systemctl",
		Args:   []string{"show", "--timestamp=unix", "--property=ActiveState,ActiveEnterTimestamp", unit},
		Output: cmdrunner.OutputRaw,
	})
	if err != nil {
		return "", nil
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			activeState = v
		case "ActiveEnterTimestamp":
			if sec, err := strconv.ParseInt(strings.TrimPrefix(v, "@"), 10, 64); err == nil && sec > 0 {
				ts := time.Unix(sec, 0).UTC()
				startedAt = &ts
			}
		}
	}
	if activeState != "active" && activeState != "activating" {
		startedAt = nil
	}
	return activeState, startedAt
}

func (s *Service) recentTunnelErrors(ctx context.Context, unit string) []string {
	if s.run == nil || unit == "" {
		return []string{}
	}
	jctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	res, err := s.run.Run(jctx, cmdrunner.Cmd{
		Module: "hostcfg",
		Name:   "journalctl",
		Args:   []string{"-u", unit, "-n", "60", "--no-pager", "-o", "cat"},
		Output: cmdrunner.OutputRaw,
	})
	if err != nil {
		return []string{}
	}
	var errs []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		if strings.Contains(low, "level=error") || strings.Contains(l, " ERR ") || strings.HasPrefix(l, "ERR ") {
			errs = append(errs, l)
		}
	}
	if len(errs) > maxRecentErrors {
		errs = errs[len(errs)-maxRecentErrors:]
	}
	return nonNil(errs)
}

// TunnelRestart restarts cloudflared and returns the fresh status.
func (s *Service) TunnelRestart(ctx context.Context) (TunnelStatus, error) {
	if s.managed == ManagedNixOS {
		return TunnelStatus{}, ErrManaged
	}
	hs, err := s.Get(ctx)
	if err != nil {
		return TunnelStatus{}, err
	}
	if !hs.Tunnel.Enabled || !hs.Tunnel.CredentialsSet {
		return TunnelStatus{}, invalid("the tunnel is not on")
	}
	if s.run == nil {
		return TunnelStatus{}, fmt.Errorf("%w: no command runner", ErrUpstream)
	}
	if _, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "systemctl", Args: []string{"restart", tunnelUnit}}); err != nil {
		return TunnelStatus{}, fmt.Errorf("%w: restarting %s: %s", ErrUpstream, tunnelUnit, commandError(err))
	}
	s.kickTunnelProbe(true)
	return s.TunnelStatus(ctx), nil
}

// RecheckTunnelRoute probes one configured tunnel hostname now and updates the cache.
func (s *Service) RecheckTunnelRoute(ctx context.Context, hostname string) (TunnelStatus, error) {
	host := strings.ToLower(strings.TrimSpace(hostname))
	v, err := s.Values(ctx)
	if err != nil {
		return TunnelStatus{}, err
	}
	if !v.Tunnel.Enabled {
		return TunnelStatus{}, invalid("the tunnel is not on")
	}
	if !slices.ContainsFunc(v.Tunnel.Hostnames, func(h string) bool { return strings.EqualFold(h, host) }) {
		return TunnelStatus{}, invalid("no tunnel route for %s", hostname)
	}
	results := s.tunnelProbe.probeHosts(ctx, []string{host}, v.Tunnel.PublicHostnames)
	now := s.now()
	s.tunnelProbe.mu.Lock()
	for _, r := range results {
		s.tunnelProbe.probes[strings.ToLower(r.Hostname)] = probeEntry{
			protection: r.Protection, statusCode: r.StatusCode, err: r.Err, at: now,
		}
	}
	s.tunnelProbe.mu.Unlock()
	return s.TunnelStatus(ctx), nil
}

// TunnelFromStore reads the stored image-host tunnel configuration directly
// from SQLite without needing the daemon or age key. Used by `homelab doctor`
// so tunnel checks work even while the daemon is down.
func TunnelFromStore(ctx context.Context, st *store.Store) (config.CloudflareTunnel, bool) {
	if st == nil {
		return config.CloudflareTunnel{}, false
	}
	rows, err := st.Queries().ListHostSettings(ctx)
	if err != nil {
		return config.CloudflareTunnel{}, false
	}
	for _, r := range rows {
		if r.Section != "tunnel" {
			continue
		}
		var t Tunnel
		if json.Unmarshal([]byte(r.Value), &t) != nil {
			return config.CloudflareTunnel{}, false
		}
		if !t.Enabled || len(r.Secret) == 0 {
			return config.CloudflareTunnel{}, false
		}
		cf := config.CloudflareTunnel{
			Enabled:         true,
			MetricsURL:      "http://" + tunnelMetrics,
			Unit:            tunnelUnit,
			Connectors:      defaultWantConns,
			PublicHostnames: map[string]string{},
		}
		for _, rt := range t.Routes {
			cf.Hostnames = append(cf.Hostnames, rt.Hostname)
			if rt.Public {
				cf.PublicHostnames[rt.Hostname] = "marked open to anyone in host settings"
			}
		}
		return cf, true
	}
	return config.CloudflareTunnel{}, false
}
