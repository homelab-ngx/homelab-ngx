package hostcfg

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/store"
)

// Errors the API maps to statuses.
var (
	// ErrInvalid is a setting that cannot be stored.
	ErrInvalid = errors.New("invalid setting")
	// ErrManaged is a change on a host whose settings belong to NixOS.
	ErrManaged = errors.New("this host's settings come from its NixOS configuration")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Values are what the rest of the daemon reads from the settings: the
// stacks (domain, dashboard address, LAN), the doctor and the security
// audit (tunnel, firewall, VPN).
type Values struct {
	Domain       string
	DashboardURL string
	LAN          []string
	Tunnel       config.CloudflareTunnel
	Security     config.Security
}

// Applier writes what the host reads from a snapshot of the settings and
// returns what failed. The image's is the real one (apply.go).
type Applier interface {
	Apply(ctx context.Context, s Snapshot) []error
}

// Snapshot is the settings with their secrets, for the applier.
type Snapshot struct {
	HostSettings
	DNSCredentials map[string]string
	// TunnelCredentials is the credentials file's JSON.
	TunnelCredentials string
	// WireGuardKey is the server's private key (base64).
	WireGuardKey string
}

// Service keeps the settings and applies them.
type Service struct {
	st      *store.Store
	keyFile string
	managed Managed
	nix     *config.Nix
	apply   Applier
	log     *slog.Logger
	now     func() time.Time
	// TZDir is where time zones are checked; a variable for tests.
	tzdir string

	mu       sync.Mutex // serialises changes and applies
	applied  Applied
	onChange []func(Values)
}

// New keeps the settings in st, sealing secrets with keyFile. A host whose
// managed is ManagedNixOS shows nix and refuses changes; apply may be nil
// there.
func New(st *store.Store, keyFile string, managed Managed, nix *config.Nix, apply Applier, log *slog.Logger) *Service {
	return &Service{st: st, keyFile: keyFile, managed: managed, nix: nix, apply: apply, log: log, now: time.Now, tzdir: "/usr/share/zoneinfo"}
}

// Detect tells an image host from a NixOS one by its os-release.
func Detect(osRelease string) Managed {
	b, err := os.ReadFile(osRelease)
	if err == nil && regexp.MustCompile(`(?m)^IMAGE_ID="?homelab"?$`).Match(b) {
		return ManagedImage
	}
	return ManagedNixOS
}

// OnChange registers fn, called with the new values after every change and
// once at Start.
func (s *Service) OnChange(fn func(Values)) { s.onChange = append(s.onChange, fn) }

// ── storage ──────────────────────────────────────────────────────────────

type row struct {
	value  json.RawMessage
	secret []byte
}

func (s *Service) rows(ctx context.Context) (map[string]row, error) {
	rs, err := s.st.Queries().ListHostSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]row{}
	for _, r := range rs {
		out[r.Section] = row{json.RawMessage(r.Value), r.Secret}
	}
	return out, nil
}

func (s *Service) save(ctx context.Context, section string, value any, secret any) error {
	v, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var sealed []byte
	if secret != nil {
		plain, err := json.Marshal(secret)
		if err != nil {
			return err
		}
		if sealed, err = agefile.Seal(s.keyFile, plain); err != nil {
			return err
		}
	}
	return s.st.Queries().UpsertHostSetting(ctx, store.UpsertHostSettingParams{
		Section: section, Value: string(v), Secret: sealed, UpdatedAt: s.now().UnixMilli(),
	})
}

func (s *Service) open(r row, into any) error {
	if len(r.secret) == 0 {
		return nil
	}
	plain, err := agefile.Open(s.keyFile, r.secret)
	if err != nil {
		return fmt.Errorf("the stored secret does not open: %w", err)
	}
	return json.Unmarshal(plain, into)
}

type domainSecret struct {
	Credentials map[string]string `json:"credentials,omitempty"`
}
type tunnelSecret struct {
	Credentials string `json:"credentials,omitempty"`
}
type wgSecret struct {
	PrivateKey string `json:"private_key,omitempty"`
}

// snapshot reads every section with its secrets.
func (s *Service) snapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	snap.Managed = ManagedImage
	rs, err := s.rows(ctx)
	if err != nil {
		return snap, err
	}
	dec := func(section string, into any) {
		if r, ok := rs[section]; ok {
			_ = json.Unmarshal(r.value, into)
		}
	}
	dec("host", &snap.Host)
	dec("domain", &snap.Domain)
	dec("network", &snap.Network)
	if snap.Network.IPv4.Mode == "" {
		snap.Network.IPv4.Mode = "dhcp"
	}
	dec("firewall", &snap.Firewall)
	dec("wireguard", &snap.WireGuard)
	dec("tunnel", &snap.Tunnel)
	dec("tailscale", &snap.Tailscale)

	var ds domainSecret
	if err := s.open(rs["domain"], &ds); err != nil {
		return snap, fmt.Errorf("domain: %w", err)
	}
	snap.DNSCredentials = ds.Credentials
	snap.Domain.DNSCredentialsSet = len(ds.Credentials) > 0
	var ts tunnelSecret
	if err := s.open(rs["tunnel"], &ts); err != nil {
		return snap, fmt.Errorf("tunnel: %w", err)
	}
	snap.TunnelCredentials = ts.Credentials
	snap.Tunnel.CredentialsSet = ts.Credentials != ""
	snap.Tunnel.TunnelID = tunnelID(ts.Credentials)
	var ws wgSecret
	if err := s.open(rs["wireguard"], &ws); err != nil {
		return snap, fmt.Errorf("wireguard: %w", err)
	}
	snap.WireGuardKey = ws.PrivateKey
	snap.WireGuard.PublicKey = publicKey(ws.PrivateKey)
	snap.Network.LAN = nonNil(snap.Network.LAN)
	snap.Firewall.Openings = nonNil(snap.Firewall.Openings)
	snap.WireGuard.Peers = nonNil(snap.WireGuard.Peers)
	snap.Tunnel.Routes = nonNil(snap.Tunnel.Routes)
	snap.Tailscale.AdvertiseRoutes = nonNil(snap.Tailscale.AdvertiseRoutes)
	return snap, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ── reading ──────────────────────────────────────────────────────────────

// Get returns the settings, no secrets.
func (s *Service) Get(ctx context.Context) (HostSettings, error) {
	if s.managed == ManagedNixOS {
		return s.fromNix(), nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return HostSettings{}, err
	}
	s.mu.Lock()
	snap.Applied = s.applied
	s.mu.Unlock()
	snap.Applied.Errors = nonNil(snap.Applied.Errors)
	return snap.HostSettings, nil
}

// Values returns what the rest of the daemon reads.
func (s *Service) Values(ctx context.Context) (Values, error) {
	if s.managed == ManagedNixOS {
		n := s.nix
		return Values{Domain: n.Stacks.Domain, DashboardURL: n.Stacks.DashboardURL, LAN: n.Stacks.LAN, Tunnel: n.CloudflareTunnel, Security: n.Security}, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Values{}, err
	}
	return valuesOf(snap.HostSettings), nil
}

// DashboardHost is the dashboard's host name under the domain.
func DashboardHost(d Domain) string {
	if d.Domain == "" {
		return ""
	}
	label := d.DashboardLabel
	if label == "" {
		label = "homelab"
	}
	return label + "." + d.Domain
}

// RouteTarget is the private host name a tunnel route serves: a label under
// the domain, or a full name as given.
func RouteTarget(target, domain string) string {
	if strings.Contains(target, ".") || domain == "" {
		return target
	}
	return target + "." + domain
}

func valuesOf(st HostSettings) Values {
	v := Values{Domain: st.Domain.Domain, LAN: slices.Clone(st.Network.LAN)}
	if h := DashboardHost(st.Domain); h != "" {
		v.DashboardURL = "https://" + h
	}
	v.Tunnel = config.CloudflareTunnel{
		Enabled: st.Tunnel.Enabled, MetricsURL: "http://" + tunnelMetrics, Unit: tunnelUnit, Connectors: 4,
		PublicHostnames: map[string]string{},
	}
	for _, r := range st.Tunnel.Routes {
		v.Tunnel.Hostnames = append(v.Tunnel.Hostnames, r.Hostname)
	}
	fw := config.Firewall{Enabled: true, TCPPorts: []int{22, 80, 443}}
	for _, o := range st.Firewall.Openings {
		if o.Proto == "udp" {
			fw.UDPPorts = append(fw.UDPPorts, o.Port)
		} else {
			fw.TCPPorts = append(fw.TCPPorts, o.Port)
		}
	}
	sec := config.Security{
		Firewall: fw,
		SSH:      config.SSH{Enabled: true, Ports: []int{22}, OpenFirewall: true, Settings: map[string]any{"PasswordAuthentication": false, "PermitRootLogin": "prohibit-password"}},
		Notes:    []string{"An image host: settings are kept by the daemon and applied at run time.", "The dashboard has no login; only the LAN, the VPN and the tailnet reach it."},
	}
	if st.WireGuard.Enabled {
		fw.UDPPorts = append(fw.UDPPorts, wgPort(st.WireGuard))
		sec.Firewall = fw
		wg := config.WireGuard{Interface: wgInterface, ListenPort: wgPort(st.WireGuard)}
		for _, p := range st.WireGuard.Peers {
			wg.Peers = append(wg.Peers, config.WireGuardPeer{Name: p.Name, AllowedIPs: []string{p.Address}})
		}
		sec.WireGuard = []config.WireGuard{wg}
	}
	sec.Tailscale = config.Tailscale{Enabled: st.Tailscale.Enabled, Port: 41641}
	v.Security = sec
	return v
}

// fromNix is a NixOS host's settings as far as its config says them.
func (s *Service) fromNix() HostSettings {
	n := s.nix
	st := HostSettings{Managed: ManagedNixOS, Applied: Applied{Errors: []string{}}}
	st.Domain.Domain = n.Stacks.Domain
	if u, err := url.Parse(n.Stacks.DashboardURL); err == nil && u.Host != "" {
		st.Domain.DashboardLabel = strings.TrimSuffix(u.Host, "."+n.Stacks.Domain)
	}
	st.Network.LAN = nonNil(slices.Clone(n.Stacks.LAN))
	st.Firewall.Openings = []Opening{}
	for _, p := range n.Security.Firewall.TCPPorts {
		st.Firewall.Openings = append(st.Firewall.Openings, Opening{Port: p, Proto: "tcp", From: []string{}})
	}
	for _, p := range n.Security.Firewall.UDPPorts {
		st.Firewall.Openings = append(st.Firewall.Openings, Opening{Port: p, Proto: "udp", From: []string{}})
	}
	st.WireGuard.Peers = []Peer{}
	if len(n.Security.WireGuard) > 0 {
		wg := n.Security.WireGuard[0]
		st.WireGuard.Enabled, st.WireGuard.ListenPort = true, wg.ListenPort
		for _, p := range wg.Peers {
			addr := ""
			if len(p.AllowedIPs) > 0 {
				addr = p.AllowedIPs[0]
			}
			st.WireGuard.Peers = append(st.WireGuard.Peers, Peer{Name: p.Name, Address: addr})
		}
	}
	st.Tunnel.Enabled = n.CloudflareTunnel.Enabled
	st.Tunnel.Routes = []Route{}
	for _, h := range n.CloudflareTunnel.Hostnames {
		st.Tunnel.Routes = append(st.Tunnel.Routes, Route{Hostname: h})
	}
	st.Tailscale.Enabled = n.Security.Tailscale.Enabled
	st.Tailscale.AdvertiseRoutes = []string{}
	return st
}

// ── changing ─────────────────────────────────────────────────────────────

var (
	labelRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	domainRE   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	tzRE       = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,2}$`)
	wgKeyRE    = regexp.MustCompile(`^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw480]=$`)
	emailRE    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	tunnelIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func (s *Service) change(ctx context.Context, fn func(rs map[string]row) error) (HostSettings, error) {
	if s.managed == ManagedNixOS {
		return HostSettings{}, ErrManaged
	}
	s.mu.Lock()
	rs, err := s.rows(ctx)
	if err == nil {
		err = fn(rs)
	}
	s.mu.Unlock()
	if err != nil {
		return HostSettings{}, err
	}
	s.Apply(ctx)
	return s.Get(ctx)
}

// SetHost changes the host name and time zone.
func (s *Service) SetHost(ctx context.Context, in Host) (HostSettings, error) {
	in.Hostname = strings.ToLower(strings.TrimSpace(in.Hostname))
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.Hostname != "" && !labelRE.MatchString(in.Hostname) {
		return HostSettings{}, invalid("hostname: one label of letters, digits and -")
	}
	if in.Timezone != "" {
		if !tzRE.MatchString(in.Timezone) {
			return HostSettings{}, invalid("timezone: an IANA zone such as Europe/Stockholm")
		}
		if _, err := os.Stat(filepath.Join(s.tzdir, in.Timezone)); err != nil {
			return HostSettings{}, invalid("timezone: %s is not a known zone", in.Timezone)
		}
	}
	return s.change(ctx, func(rs map[string]row) error {
		in.SSHKeys = storedHost(rs).SSHKeys
		return s.save(ctx, "host", in, nil)
	})
}

// SetSSHKeys changes root's authorized SSH keys.
func (s *Service) SetSSHKeys(ctx context.Context, in SSHKeys) (HostSettings, error) {
	keys, err := CheckSSHKeys(in.Keys)
	if err != nil {
		return HostSettings{}, err
	}
	return s.change(ctx, func(rs map[string]row) error {
		h := storedHost(rs)
		h.SSHKeys = keys
		return s.save(ctx, "host", h, nil)
	})
}

func storedHost(rs map[string]row) Host {
	var h Host
	if r, ok := rs["host"]; ok {
		_ = json.Unmarshal(r.value, &h)
	}
	return h
}

// CheckSSHKeys validates public keys in OpenSSH's authorized_keys format
// (options are not taken) and returns them tidied, duplicates dropped.
func CheckSSHKeys(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for i, k := range in {
		k = strings.Join(strings.Fields(k), " ")
		if k == "" {
			continue
		}
		f := strings.SplitN(k, " ", 3)
		if len(f) < 2 || !sshKeyType(f[0], f[1]) {
			return nil, invalid("ssh_keys[%d]: not a public key such as ssh-ed25519 AAAA… user@host", i)
		}
		if !seen[f[1]] {
			seen[f[1]] = true
			out = append(out, k)
		}
	}
	return nonNil(out), nil
}

// sshKeyType reports whether blob (base64) is a key of type t: its wire
// form starts with the type's name.
func sshKeyType(t, blob string) bool {
	switch {
	case t == "ssh-ed25519", t == "ssh-rsa", strings.HasPrefix(t, "ecdsa-sha2-"), strings.HasPrefix(t, "sk-"):
	default:
		return false
	}
	b, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(b) < 4 {
		return false
	}
	n := int(binary.BigEndian.Uint32(b))
	return n == len(t) && len(b) >= 4+n && string(b[4:4+n]) == t
}

// SetDomain changes the domain and its certificate.
func (s *Service) SetDomain(ctx context.Context, in DomainSet) (HostSettings, error) {
	d := Domain{
		Domain: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Domain)), "."), DashboardLabel: strings.ToLower(strings.TrimSpace(in.DashboardLabel)),
		ACMEEmail: strings.TrimSpace(in.ACMEEmail), DNSProvider: in.DNSProvider, Region: strings.TrimSpace(in.Region),
	}
	switch {
	case d.Domain != "" && !domainRE.MatchString(d.Domain):
		return HostSettings{}, invalid("domain: a domain name such as home.example.com")
	case d.DashboardLabel != "" && !labelRE.MatchString(d.DashboardLabel):
		return HostSettings{}, invalid("dashboard_label: one label of letters, digits and -")
	case d.ACMEEmail != "" && !emailRE.MatchString(d.ACMEEmail):
		return HostSettings{}, invalid("acme_email: an email address")
	}
	fields, known := dnsFields[d.DNSProvider]
	if d.DNSProvider != DNSNone && !known {
		return HostSettings{}, invalid("dns_provider: cloudflare, porkbun, duckdns, route53 or empty")
	}
	if d.DNSProvider != DNSRoute53 {
		d.Region = ""
	}
	return s.change(ctx, func(rs map[string]row) error {
		var old Domain
		_ = json.Unmarshal(rs["domain"].value, &old)
		var sec domainSecret
		if in.DNSCredentials != nil {
			sec.Credentials = map[string]string{}
			for _, f := range fields {
				v := strings.TrimSpace(in.DNSCredentials[f])
				if v == "" || strings.ContainsAny(v, " \t\r\n{}\"") {
					return invalid("dns_credentials: %s %s is missing or has spaces, braces or quotes", d.DNSProvider, f)
				}
				sec.Credentials[f] = v
			}
			for k := range in.DNSCredentials {
				if !slices.Contains(fields, k) {
					return invalid("dns_credentials: %s takes no %s", d.DNSProvider, k)
				}
			}
		} else if old.DNSProvider == d.DNSProvider {
			if err := s.open(rs["domain"], &sec); err != nil {
				return err
			}
		}
		if d.DNSProvider != DNSNone && len(sec.Credentials) == 0 {
			return invalid("dns_credentials: %s needs %s", d.DNSProvider, strings.Join(fields, " and "))
		}
		if d.DNSProvider == DNSNone {
			sec = domainSecret{}
		}
		return s.save(ctx, "domain", d, sec)
	})
}

// CheckIPv4 validates an IPv4 setting and returns it tidied: DHCP keeps
// nothing else; static needs an address with its prefix and a gateway in
// that subnet, and takes up to three DNS servers.
func CheckIPv4(in IPv4) (IPv4, error) {
	switch in.Mode {
	case "", "dhcp":
		return IPv4{Mode: "dhcp"}, nil
	case "static":
	default:
		return IPv4{}, invalid("ipv4.mode: dhcp or static")
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(in.Address))
	if err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 30 || p.Addr() == p.Masked().Addr() {
		return IPv4{}, invalid("ipv4.address: %q is not a host address with its prefix, such as 192.168.1.20/24", in.Address)
	}
	gw, err := netip.ParseAddr(strings.TrimSpace(in.Gateway))
	if err != nil || !gw.Is4() || !p.Masked().Contains(gw) || gw == p.Addr() {
		return IPv4{}, invalid("ipv4.gateway: %q is not another address in %s", in.Gateway, p.Masked())
	}
	var dns []string
	for _, d := range in.DNS {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		a, err := netip.ParseAddr(d)
		if err != nil || !a.Is4() {
			return IPv4{}, invalid("ipv4.dns: %q is not an IPv4 address", d)
		}
		dns = append(dns, a.String())
	}
	if len(dns) > 3 {
		return IPv4{}, invalid("ipv4.dns: at most three servers")
	}
	return IPv4{Mode: "static", Address: p.String(), Gateway: gw.String(), DNS: dns}, nil
}

func ipv4Subnet(field, c string) (string, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(c))
	if err != nil || !p.Addr().Is4() || p.Bits() == 0 {
		return "", invalid("%s: %q is not an IPv4 subnet such as 192.168.1.0/24", field, c)
	}
	return p.Masked().String(), nil
}

// SetNetwork changes the LAN.
func (s *Service) SetNetwork(ctx context.Context, in Network) (HostSettings, error) {
	ip, err := CheckIPv4(in.IPv4)
	if err != nil {
		return HostSettings{}, err
	}
	var lan []string
	for _, c := range in.LAN {
		p, err := ipv4Subnet("lan", c)
		if err != nil {
			return HostSettings{}, err
		}
		lan = append(lan, p)
	}
	return s.change(ctx, func(map[string]row) error { return s.save(ctx, "network", Network{LAN: nonNil(lan), IPv4: ip}, nil) })
}

// SetFirewall changes the openings.
func (s *Service) SetFirewall(ctx context.Context, in Firewall) (HostSettings, error) {
	seen := map[string]bool{}
	var out []Opening
	for i, o := range in.Openings {
		if o.Port < 1 || o.Port > 65535 || (o.Proto != "tcp" && o.Proto != "udp") {
			return HostSettings{}, invalid("openings[%d]: a port 1-65535 over tcp or udp", i)
		}
		k := fmt.Sprintf("%d/%s", o.Port, o.Proto)
		if seen[k] {
			return HostSettings{}, invalid("openings[%d]: %s is listed twice", i, k)
		}
		seen[k] = true
		var from []string
		for _, c := range o.From {
			p, err := ipv4Subnet(fmt.Sprintf("openings[%d].from", i), c)
			if err != nil {
				return HostSettings{}, err
			}
			from = append(from, p)
		}
		out = append(out, Opening{Port: o.Port, Proto: o.Proto, From: nonNil(from), Note: strings.TrimSpace(o.Note)})
	}
	return s.change(ctx, func(map[string]row) error { return s.save(ctx, "firewall", Firewall{Openings: nonNil(out)}, nil) })
}

// SetWireGuard changes the VPN. The server's key is made on first enable
// and kept.
func (s *Service) SetWireGuard(ctx context.Context, in WireGuard) (HostSettings, error) {
	in.PublicKey = ""
	if in.ListenPort < 0 || in.ListenPort > 65535 {
		return HostSettings{}, invalid("listen_port: 1-65535, or 0 for 51820")
	}
	var net netip.Prefix
	if in.Enabled || in.Address != "" {
		var err error
		if net, err = netip.ParsePrefix(strings.TrimSpace(in.Address)); err != nil || !net.Addr().Is4() || net.Bits() > 30 || net.Bits() < 8 {
			return HostSettings{}, invalid("address: the host's address and the VPN's subnet, such as 10.10.0.1/24")
		}
		in.Address = net.String()
	}
	if in.Endpoint = strings.TrimSpace(in.Endpoint); in.Endpoint != "" && strings.ContainsAny(in.Endpoint, " /") {
		return HostSettings{}, invalid("endpoint: host:port, such as vpn.example.com:51820")
	}
	names, keys, addrs := map[string]bool{}, map[string]bool{}, map[netip.Addr]bool{net.Addr(): true}
	for i, p := range in.Peers {
		p.Name, p.PublicKey = strings.TrimSpace(p.Name), strings.TrimSpace(p.PublicKey)
		a, err := netip.ParsePrefix(strings.TrimSpace(p.Address))
		if err != nil && !strings.Contains(p.Address, "/") {
			if ad, err2 := netip.ParseAddr(strings.TrimSpace(p.Address)); err2 == nil {
				a, err = netip.PrefixFrom(ad, 32), nil
			}
		}
		switch {
		case p.Name == "" || names[p.Name]:
			return HostSettings{}, invalid("peers[%d].name: needed, once", i)
		case !wgKeyRE.MatchString(p.PublicKey) || keys[p.PublicKey]:
			return HostSettings{}, invalid("peers[%d].public_key: a WireGuard public key, once", i)
		case err != nil || a.Bits() != 32 || !net.Contains(a.Addr()) || addrs[a.Addr()]:
			return HostSettings{}, invalid("peers[%d].address: one free address in %s", i, net.Masked())
		}
		names[p.Name], keys[p.PublicKey], addrs[a.Addr()] = true, true, true
		p.Address = a.String()
		in.Peers[i] = p
	}
	in.Peers = nonNil(in.Peers)
	return s.change(ctx, func(rs map[string]row) error {
		var sec wgSecret
		if err := s.open(rs["wireguard"], &sec); err != nil {
			return err
		}
		if sec.PrivateKey == "" && in.Enabled {
			k, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			sec.PrivateKey = base64.StdEncoding.EncodeToString(k.Bytes())
		}
		var secret any
		if sec.PrivateKey != "" {
			secret = sec
		}
		return s.save(ctx, "wireguard", in, secret)
	})
}

// publicKey is the WireGuard public key of a private one, "" when there is
// none or it does not parse.
func publicKey(private string) string {
	b, err := base64.StdEncoding.DecodeString(private)
	if err != nil || len(b) != 32 {
		return ""
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

// tunnelID reads TunnelID from a credentials file.
func tunnelID(creds string) string {
	var c struct{ TunnelID string }
	if json.Unmarshal([]byte(creds), &c) != nil {
		return ""
	}
	return c.TunnelID
}

// SetTunnel changes the tunnel.
func (s *Service) SetTunnel(ctx context.Context, in TunnelSet) (HostSettings, error) {
	seen := map[string]bool{}
	for i, r := range in.Routes {
		r.Hostname = strings.ToLower(strings.TrimSpace(r.Hostname))
		r.Target = strings.ToLower(strings.TrimSpace(r.Target))
		r.Path = strings.TrimSpace(r.Path)
		if !domainRE.MatchString(r.Hostname) {
			return HostSettings{}, invalid("routes[%d].hostname: a public host name such as photos.example.net", i)
		}
		if !labelRE.MatchString(r.Target) && !domainRE.MatchString(r.Target) {
			return HostSettings{}, invalid("routes[%d].target: a name under the private domain or a full host name", i)
		}
		if r.Path != "" {
			if _, err := regexp.Compile(r.Path); err != nil || strings.ContainsAny(r.Path, "\"\n") {
				return HostSettings{}, invalid("routes[%d].path: a regular expression", i)
			}
		}
		k := r.Hostname + " " + r.Path
		if seen[k] {
			return HostSettings{}, invalid("routes[%d]: listed twice", i)
		}
		seen[k] = true
		in.Routes[i] = r
	}
	return s.change(ctx, func(rs map[string]row) error {
		var sec tunnelSecret
		if in.Credentials != nil {
			c := strings.TrimSpace(*in.Credentials)
			var parsed struct{ AccountTag, TunnelID, TunnelSecret string }
			if c != "" && (json.Unmarshal([]byte(c), &parsed) != nil || parsed.AccountTag == "" || parsed.TunnelSecret == "" || !tunnelIDRE.MatchString(parsed.TunnelID)) {
				return invalid("credentials: the JSON file cloudflared tunnel create writes (AccountTag, TunnelID, TunnelSecret)")
			}
			sec.Credentials = c
		} else if err := s.open(rs["tunnel"], &sec); err != nil {
			return err
		}
		if in.Enabled && sec.Credentials == "" {
			return invalid("credentials: the tunnel needs its credentials file to run")
		}
		t := Tunnel{Enabled: in.Enabled, Routes: nonNil(in.Routes)}
		return s.save(ctx, "tunnel", t, sec)
	})
}

// SetTailscale changes Tailscale.
func (s *Service) SetTailscale(ctx context.Context, in Tailscale) (HostSettings, error) {
	var routes []string
	for _, c := range in.AdvertiseRoutes {
		p, err := ipv4Subnet("advertise_routes", c)
		if err != nil {
			return HostSettings{}, err
		}
		routes = append(routes, p)
	}
	return s.change(ctx, func(map[string]row) error {
		return s.save(ctx, "tailscale", Tailscale{Enabled: in.Enabled, AdvertiseRoutes: nonNil(routes)}, nil)
	})
}

// ── applying ─────────────────────────────────────────────────────────────

// Apply writes what the host reads from the current settings and tells the
// listeners the new values. On NixOS it only tells them (once, at start).
func (s *Service) Apply(ctx context.Context) {
	vals, err := s.Values(ctx)
	if err != nil {
		s.log.Error("host settings unreadable", "error", err)
		return
	}
	if s.managed == ManagedImage && s.apply != nil {
		s.mu.Lock()
		snap, err := s.snapshot(ctx)
		var errs []error
		if err != nil {
			errs = []error{err}
		} else {
			errs = s.apply.Apply(ctx, snap)
		}
		now := s.now()
		s.applied = Applied{At: &now, Errors: []string{}}
		for _, e := range errs {
			s.applied.Errors = append(s.applied.Errors, e.Error())
			s.log.Error("host setting not applied", "error", e)
		}
		s.mu.Unlock()
	}
	for _, fn := range s.onChange {
		fn(vals)
	}
}

// AdoptInstall takes the host name, time zone, SSH keys and IPv4 setting the
// installer left in path (JSON: hostname, timezone, ssh_keys, ipv4) into the
// settings at the host's first start,
// applies them and removes the file. It reports whether it did: once the
// host section exists, the file is ignored.
func (s *Service) AdoptInstall(ctx context.Context, path string) bool {
	if s.managed != ManagedImage {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s.mu.Lock()
	rs, err := s.rows(ctx)
	s.mu.Unlock()
	if err != nil {
		return false
	}
	if _, ok := rs["host"]; ok {
		_ = os.Remove(path)
		return false
	}
	var in struct {
		Host
		IPv4 IPv4 `json:"ipv4"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		s.log.Warn("the installer's settings are unreadable", "error", err)
		return false
	}
	if in.IPv4.Mode == "static" {
		if _, err := s.SetNetwork(ctx, Network{IPv4: in.IPv4}); err != nil {
			s.log.Warn("the installer's network was not taken", "error", err)
		}
	}
	h := in.Host
	if _, err := s.SetHost(ctx, h); err != nil {
		s.log.Warn("the installer's host name and time zone were not taken", "error", err)
		return false
	}
	if len(h.SSHKeys) > 0 {
		if _, err := s.SetSSHKeys(ctx, SSHKeys{Keys: h.SSHKeys}); err != nil {
			s.log.Warn("the installer's SSH keys were not taken", "error", err)
		}
	}
	_ = os.Remove(path)
	s.log.Info("host name and time zone from the installer", "hostname", h.Hostname, "timezone", h.Timezone)
	return true
}
