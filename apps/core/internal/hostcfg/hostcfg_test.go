package hostcfg

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

const peerKey = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="

var creds = `{"AccountTag":"acc","TunnelID":"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0","TunnelSecret":"c2VjcmV0"}`

// recorder is an applier that keeps the snapshots it was given.
type recorder struct{ got []Snapshot }

func (r *recorder) Apply(_ context.Context, s Snapshot) []error {
	r.got = append(r.got, s)
	return nil
}

func newService(t *testing.T) (*Service, *store.Store, *recorder) {
	t.Helper()
	st, _ := storetest.Open(t)
	id, _ := age.GenerateX25519Identity()
	key := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(key, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s := New(st, key, ManagedImage, nil, rec, slog.New(slog.DiscardHandler))
	tz := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tz, "Europe"), 0o755)
	_ = os.WriteFile(filepath.Join(tz, "Europe", "Stockholm"), []byte("TZif"), 0o644)
	s.tzdir = tz
	return s, st, rec
}

func TestDomainCredentialsStaySealed(t *testing.T) {
	t.Parallel()
	s, st, rec := newService(t)
	ctx := context.Background()
	got, err := s.SetDomain(ctx, DomainSet{Domain: "Home.Example.com", DNSProvider: DNSCloudflare, DNSCredentials: map[string]string{"api_token": "cf-secret"}})
	if err != nil || got.Domain.Domain != "home.example.com" || !got.Domain.DNSCredentialsSet {
		t.Fatalf("set: %+v %v", got.Domain, err)
	}
	rows, _ := st.Queries().ListHostSettings(ctx)
	for _, r := range rows {
		if strings.Contains(r.Value, "cf-secret") || strings.Contains(string(r.Secret), "cf-secret") {
			t.Fatal("the token is stored in the clear")
		}
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "cf-secret") {
		t.Fatal("the token is in the API answer")
	}
	if last := rec.got[len(rec.got)-1]; last.DNSCredentials["api_token"] != "cf-secret" {
		t.Fatalf("the applier did not get the token: %+v", last.DNSCredentials)
	}
	// Absent credentials keep them; another provider needs its own.
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com", DashboardLabel: "lab", DNSProvider: DNSCloudflare}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com", DNSProvider: DNSPorkbun}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("porkbun without credentials: %v", err)
	}
	v, _ := s.Values(ctx)
	if v.Domain != "home.example.com" || v.DashboardURL != "https://lab.home.example.com" {
		t.Fatalf("values = %+v", v)
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()
	s, _, _ := newService(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"hostname":      second(s.SetHost(ctx, Host{Hostname: "two words"})),
		"timezone":      second(s.SetHost(ctx, Host{Timezone: "Mars/Olympus"})),
		"domain":        second(s.SetDomain(ctx, DomainSet{Domain: "nodot"})),
		"lan":           second(s.SetNetwork(ctx, Network{LAN: []string{"fd00::/64"}})),
		"opening port":  second(s.SetFirewall(ctx, Firewall{Openings: []Opening{{Port: 0, Proto: "tcp"}}})),
		"opening twice": second(s.SetFirewall(ctx, Firewall{Openings: []Opening{{Port: 8080, Proto: "tcp"}, {Port: 8080, Proto: "tcp"}}})),
		"wg address":    second(s.SetWireGuard(ctx, WireGuard{Enabled: true, Address: "10.10.0.1"})),
		"wg peer key":   second(s.SetWireGuard(ctx, WireGuard{Enabled: true, Address: "10.10.0.1/24", Peers: []Peer{{Name: "phone", PublicKey: "nope", Address: "10.10.0.2"}}})),
		"wg peer out":   second(s.SetWireGuard(ctx, WireGuard{Enabled: true, Address: "10.10.0.1/24", Peers: []Peer{{Name: "phone", PublicKey: peerKey, Address: "10.20.0.2"}}})),
		"wg peer host":  second(s.SetWireGuard(ctx, WireGuard{Enabled: true, Address: "10.10.0.1/24", Peers: []Peer{{Name: "phone", PublicKey: peerKey, Address: "10.10.0.1"}}})),
		"tunnel creds":  second(s.SetTunnel(ctx, TunnelSet{Enabled: true})),
		"tunnel bad":    second(s.SetTunnel(ctx, TunnelSet{Credentials: ptr(`{"TunnelID":"x"}`)})),
		"route target":  second(s.SetTunnel(ctx, TunnelSet{Credentials: &creds, Routes: []Route{{Hostname: "photos.example.net", Target: "a b"}}})),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
	if _, err := s.SetHost(ctx, Host{Hostname: "Box", Timezone: "Europe/Stockholm"}); err != nil {
		t.Fatal(err)
	}
}

func second[T any](_ T, err error) error { return err }
func ptr(s string) *string               { return &s }

// The server key is made once and kept; peers are normalised.
func TestWireGuardKeyKept(t *testing.T) {
	t.Parallel()
	s, _, rec := newService(t)
	ctx := context.Background()
	in := WireGuard{Enabled: true, Address: "10.10.0.1/24", Endpoint: "vpn.example.com:51820", Peers: []Peer{{Name: "phone", PublicKey: peerKey, Address: "10.10.0.2"}}}
	a, err := s.SetWireGuard(ctx, in)
	if err != nil || a.WireGuard.PublicKey == "" || a.WireGuard.Peers[0].Address != "10.10.0.2/32" {
		t.Fatalf("set: %+v %v", a.WireGuard, err)
	}
	b, _ := s.SetWireGuard(ctx, in)
	if b.WireGuard.PublicKey != a.WireGuard.PublicKey {
		t.Fatal("the server key changed")
	}
	if k := rec.got[len(rec.got)-1].WireGuardKey; k == "" || publicKey(k) != a.WireGuard.PublicKey {
		t.Fatal("the applier did not get the matching private key")
	}
	v, _ := s.Values(ctx)
	if len(v.Security.WireGuard) != 1 || v.Security.WireGuard[0].Peers[0].Name != "phone" {
		t.Fatalf("security facts = %+v", v.Security.WireGuard)
	}
}

func TestNixOSIsReadOnly(t *testing.T) {
	t.Parallel()
	st, _ := storetest.Open(t)
	nix := &config.Nix{Stacks: config.Stacks{Domain: "home.example.com", DashboardURL: "https://homelab.home.example.com", LAN: []string{"192.168.1.0/24"}}}
	nix.Security.Firewall.TCPPorts = []int{8080}
	nix.Security.Tailscale = config.Tailscale{
		Enabled:  true,
		UpFlags:  []string{"--advertise-routes=192.168.1.0/24,10.0.0.0/24", "--ssh"},
		SetFlags: []string{"--advertise-exit-node", "--accept-dns=false", "--login-server", "https://hs.example.net"},
	}
	s := New(st, "", ManagedNixOS, nix, nil, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	got, err := s.Get(ctx)
	if err != nil || got.Managed != ManagedNixOS || got.Domain.Domain != "home.example.com" || got.Domain.DashboardLabel != "homelab" || got.Firewall.Openings[0].Port != 8080 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if !got.Tailscale.Enabled || !slices.Equal(got.Tailscale.AdvertiseRoutes, []string{"192.168.1.0/24", "10.0.0.0/24"}) ||
		!got.Tailscale.ExitNode || got.Tailscale.UseDNS() || !got.Tailscale.SSH || got.Tailscale.LoginServer != "https://hs.example.net" {
		t.Fatalf("nixos tailscale = %+v", got.Tailscale)
	}
	if _, err := s.SetHost(ctx, Host{Hostname: "x"}); !errors.Is(err, ErrManaged) {
		t.Fatalf("set on nixos: %v", err)
	}
}

func snapshotOf(t *testing.T) Snapshot {
	t.Helper()
	s := Snapshot{DNSCredentials: map[string]string{"api_token": "tok"}, TunnelCredentials: creds, WireGuardKey: "aGVsbG8="}
	s.Host = Host{Hostname: "box", Timezone: "Europe/Stockholm"}
	s.Domain = Domain{Domain: "home.example.com", DNSProvider: DNSCloudflare, ACMEEmail: "ops@example.com"}
	s.Firewall.Openings = []Opening{{Port: 8443, Proto: "tcp", From: []string{"192.168.1.0/24"}}}
	s.WireGuard = WireGuard{Enabled: true, Address: "10.10.0.1/24", Peers: []Peer{{Name: "phone", PublicKey: peerKey, Address: "10.10.0.2/32"}}}
	s.Tunnel = Tunnel{Enabled: true, TunnelID: tunnelID(creds), Routes: []Route{{Hostname: "photos.example.net", Target: "photos"}, {Hostname: "books.example.net", Target: "calibre.home.example.com", Path: "^/opds"}}}
	s.Tailscale = Tailscale{Enabled: true, AdvertiseRoutes: []string{"192.168.1.0/24"}}
	return s
}

func TestRendered(t *testing.T) {
	t.Parallel()
	s := snapshotOf(t)
	site := CaddySite(s)
	for _, want := range []string{"*.home.example.com, home.example.com {", "tls ops@example.com {", "dns cloudflare tok", "@dashboard host homelab.home.example.com", "import homelab_dashboard", "import /var/lib/homelab-stack-routes/*.caddy"} {
		if !strings.Contains(site, want) {
			t.Errorf("site lacks %q:\n%s", want, site)
		}
	}
	if CaddySite(Snapshot{}) != "" {
		t.Error("a site without a domain")
	}
	if r := CaddyHTTPRedirect(s); !strings.Contains(r, "redir https://homelab.home.example.com{uri}\n") {
		t.Errorf("plain HTTP redirect = %q", r)
	}
	if r := CaddyHTTPRedirect(Snapshot{}); r != "" {
		t.Errorf("a redirect without a domain: %q", r)
	}
	rules := Ruleset(s)
	for _, want := range []string{"policy drop;", "tcp dport { 22, 80, 443 } accept", "udp dport 51820 accept", `iifname "ve-*" accept`, "ip saddr { 192.168.1.0/24 } tcp dport 8443 accept", "udp dport 41641 accept"} {
		if !strings.Contains(rules, want) {
			t.Errorf("ruleset lacks %q:\n%s", want, rules)
		}
	}
	cfg := TunnelConfig(s, "/c.json")
	for _, want := range []string{"tunnel: 0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", "hostname: photos.example.net", "originServerName: photos.home.example.com", `path: "^/opds"`, "httpHostHeader: calibre.home.example.com", "service: http_status:404"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("tunnel config lacks %q:\n%s", want, cfg)
		}
	}
	files, links := EtcFiles(s)
	if files["etc/hostname"] != "box\n" || links["etc/localtime"] != "../usr/share/zoneinfo/Europe/Stockholm" ||
		!strings.Contains(files["etc/systemd/network/50-wg-homelab.netdev"], "PublicKey="+peerKey) ||
		!strings.Contains(files["etc/systemd/network/50-wg-homelab.network"], "Address=10.10.0.1/24") {
		t.Errorf("etc files = %v %v", files, links)
	}
	if want := "https://" + DashboardHost(s.Domain) + "\n"; s.Domain.Domain == "" || files[DashboardFile] != want {
		t.Errorf("%s = %q, want %q", DashboardFile, files[DashboardFile], want)
	}
}

// Apply writes every part under its root, reloads what changed, and removes
// what the settings no longer ask for.
func TestImageApplier(t *testing.T) {
	// The daemon's umask: directories must still come out 0755.
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)
	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	for _, p := range [][]string{{"systemctl"}, {"systemd-confext"}, {"networkctl"}, {"nft"}, {"tailscale"}, {"ip"}} {
		f.On(p...).Reply("")
	}
	a := &ImageApplier{Runner: f, Root: root}
	ctx := context.Background()
	s := snapshotOf(t)
	if errs := a.Apply(ctx, s); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, p := range []string{"/var/lib/homelab/caddy/site.caddy", "/var/lib/confexts/homelab-host/etc/hostname", "/var/lib/homelab/nftables/host.nft",
		"/var/lib/homelab/cloudflared/config.yml", "/var/lib/homelab/cloudflared/credentials.json", "/var/lib/homelab/wireguard/private.key"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, d := range []string{"/var/lib/confexts", "/var/lib/confexts/homelab-host/etc/systemd/network", "/var/lib/homelab/wireguard"} {
		if st, _ := os.Stat(filepath.Join(root, d)); st == nil || st.Mode().Perm() != 0o755 {
			t.Errorf("%s is not 0755", d)
		}
	}
	if st, _ := os.Stat(filepath.Join(root, "/var/lib/homelab/caddy/site.caddy")); st.Mode().Perm() != 0o640 {
		t.Errorf("site.caddy mode %v", st.Mode().Perm())
	}
	if len(f.CallsMatching("systemctl", "try-reload-or-restart", "caddy.service")) != 1 || len(f.CallsMatching("systemd-confext", "refresh")) != 1 ||
		len(f.CallsMatching("systemctl", "restart", tunnelUnit)) != 1 || len(f.CallsMatching("nft", "-f")) != 1 {
		t.Fatalf("calls = %+v", f.Calls())
	}
	// Again with nothing changed: no reloads, but the firewall and the host
	// name every time.
	before := len(f.Calls())
	a.Apply(ctx, s)
	again := f.Calls()[before:]
	for _, c := range again {
		if c.Name == "systemd-confext" || (c.Name == "systemctl" && c.Args[0] == "try-reload-or-restart") || (c.Name == "systemctl" && c.Args[0] == "restart") {
			t.Errorf("reloaded with nothing changed: %v", c)
		}
	}
	if len(f.CallsMatching("nft", "-f")) != 2 {
		t.Error("the firewall was not applied again")
	}
	// The kernel's name is the setting's, and is set again after a boot
	// put the image's name back.
	kernel := filepath.Join(root, "/proc/sys/kernel/hostname")
	if b, _ := os.ReadFile(kernel); string(b) != s.Host.Hostname {
		t.Errorf("kernel host name = %q, want %q", b, s.Host.Hostname)
	}
	if err := os.WriteFile(kernel, []byte("homelab"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Apply(ctx, s)
	if b, _ := os.ReadFile(kernel); string(b) != s.Host.Hostname {
		t.Errorf("kernel host name after a boot = %q, want %q", b, s.Host.Hostname)
	}
	// Domain, VPN and tunnel off: their files go.
	s.Domain, s.WireGuard, s.Tunnel = Domain{}, WireGuard{}, Tunnel{}
	a.Apply(ctx, s)
	for _, p := range []string{"/var/lib/homelab/caddy/site.caddy", "/var/lib/confexts/homelab-host/etc/systemd/network/50-wg-homelab.netdev",
		"/var/lib/homelab/cloudflared/credentials.json", "/var/lib/homelab/wireguard/private.key"} {
		if _, err := os.Stat(filepath.Join(root, p)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there", p)
		}
	}
	if len(f.CallsMatching("systemctl", "disable", "--runtime", "--now", tunnelUnit)) != 1 {
		t.Error("the tunnel was not stopped")
	}
}

// The coding agents' system locations point at the core's guidance whatever
// the settings, and a second apply keeps them.
func TestAgentEtc(t *testing.T) {
	files, links := EtcFiles(Snapshot{})
	// No domain, no dashboard file: the greeting falls back to .local.
	if f, ok := files[DashboardFile]; ok {
		t.Errorf("%s without a domain = %q", DashboardFile, f)
	}
	for p, want := range map[string]string{
		"etc/claude-code/CLAUDE.md":              "/usr/share/homelab/agent/brief.md",
		"etc/claude-code/.claude/skills/homelab": "/usr/share/homelab/agent/skills/homelab",
		"etc/codex/skills/homelab":               "/usr/share/homelab/agent/skills/homelab",
	} {
		if links[p] != want {
			t.Errorf("link %s = %q, want %q", p, links[p], want)
		}
	}
	var oc struct{ Instructions, Skills []string }
	if err := json.Unmarshal([]byte(files["etc/opencode/opencode.json"]), &oc); err != nil ||
		!slices.Equal(oc.Instructions, []string{"/usr/share/homelab/agent/brief.md"}) ||
		!slices.Equal(oc.Skills, []string{"/usr/share/homelab/agent/skills"}) {
		t.Errorf("opencode.json = %q (%v)", files["etc/opencode/opencode.json"], err)
	}
	if c := files["etc/codex/config.toml"]; !strings.HasPrefix(strings.SplitN(c, "\n", 2)[1], `developer_instructions = """`) ||
		!strings.Contains(c, "/usr/share/homelab/agent/brief.md") {
		t.Errorf("codex config.toml = %q", c)
	}

	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	for _, p := range [][]string{{"systemd-confext"}, {"networkctl"}, {"ip"}} {
		f.On(p...).Reply("")
	}
	a := &ImageApplier{Runner: f, Root: root}
	ext := filepath.Join(root, "/var/lib/confexts/homelab-host")
	for range 2 {
		if err := a.etc(context.Background(), Snapshot{}); err != nil {
			t.Fatal(err)
		}
		if got, err := os.Readlink(filepath.Join(ext, "etc/codex/skills/homelab")); err != nil || got != "/usr/share/homelab/agent/skills/homelab" {
			t.Fatalf("codex skill link = %q (%v)", got, err)
		}
		if _, err := os.Stat(filepath.Join(ext, "etc/opencode/opencode.json")); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.CallsMatching("systemd-confext", "refresh")); n != 1 {
		t.Errorf("confext refreshed %d times, want once", n)
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "os-release")
	_ = os.WriteFile(p, []byte("ID=arch\nIMAGE_ID=homelab\nIMAGE_VERSION=3\n"), 0o644)
	if Detect(p) != ManagedImage {
		t.Error("image not detected")
	}
	_ = os.WriteFile(p, []byte("ID=nixos\n"), 0o644)
	if Detect(p) != ManagedNixOS || Detect(p+"x") != ManagedNixOS {
		t.Error("nixos not detected")
	}
}

func TestAdoptsTheInstallersSettingsOnce(t *testing.T) {
	t.Parallel()
	s, _, rec := newService(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "install.json")
	if s.AdoptInstall(ctx, file) {
		t.Fatal("adopted without the file")
	}
	_ = os.WriteFile(file, []byte(`{"hostname":"Box","timezone":"Europe/Stockholm"}`), 0o644)
	if !s.AdoptInstall(ctx, file) {
		t.Fatal("the installer's settings were not adopted")
	}
	if got, _ := s.Get(ctx); got.Host.Hostname != "box" || got.Host.Timezone != "Europe/Stockholm" || len(rec.got) != 1 {
		t.Fatalf("host = %+v, applied %d times", got.Host, len(rec.got))
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the file is still there")
	}
	// A name set on the dashboard wins over a file that turns up again.
	_ = os.WriteFile(file, []byte(`{"hostname":"other"}`), 0o644)
	if s.AdoptInstall(ctx, file) {
		t.Fatal("adopted again over the host's own setting")
	}
}

func TestStaticIPv4(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in IPv4
		ok bool
	}{
		{IPv4{}, true},
		{IPv4{Mode: "static", Address: "192.168.1.20/24", Gateway: "192.168.1.1", DNS: []string{"1.1.1.1", " "}}, true},
		{IPv4{Mode: "static", Address: "192.168.1.20", Gateway: "192.168.1.1"}, false},     // no prefix
		{IPv4{Mode: "static", Address: "192.168.1.0/24", Gateway: "192.168.1.1"}, false},   // the network itself
		{IPv4{Mode: "static", Address: "192.168.1.20/24", Gateway: "10.0.0.1"}, false},     // gateway elsewhere
		{IPv4{Mode: "static", Address: "192.168.1.20/24", Gateway: "192.168.1.20"}, false}, // gateway is the host
		{IPv4{Mode: "manual"}, false},
	} {
		if _, err := CheckIPv4(c.in); (err == nil) != c.ok {
			t.Errorf("%+v: err %v", c.in, err)
		}
	}
	s, _, rec := newService(t)
	ctx := context.Background()
	if _, err := s.SetNetwork(ctx, Network{IPv4: IPv4{Mode: "static", Address: "192.168.1.20/24", Gateway: "192.168.1.1", DNS: []string{"1.1.1.1"}}}); err != nil {
		t.Fatal(err)
	}
	files, _ := EtcFiles(rec.got[len(rec.got)-1])
	want := "[Match]\nName=en* eth*\n\n[Network]\nAddress=192.168.1.20/24\nGateway=192.168.1.1\nMulticastDNS=yes\nDNS=1.1.1.1\n"
	if got := files["etc/systemd/network/10-homelab-wired.network"]; got != want {
		t.Errorf("network file = %q", got)
	}
	if _, err := s.SetNetwork(ctx, Network{}); err != nil {
		t.Fatal(err)
	}
	if files, _ := EtcFiles(rec.got[len(rec.got)-1]); files["etc/systemd/network/10-homelab-wired.network"] != "" {
		t.Error("DHCP still has a static file")
	}
	if got, _ := s.Get(ctx); got.Network.IPv4.Mode != "dhcp" {
		t.Errorf("mode = %q", got.Network.IPv4.Mode)
	}
}

func TestSSHKeys(t *testing.T) {
	t.Parallel()
	const ed = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICPRatDu9E/56fcXpfkt3EDvquMMSCc3uOMCAVaLKQNU me@laptop"
	if _, err := CheckSSHKeys([]string{"ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAICPRatDu9E/56fcXpfkt3EDvquMMSCc3uOMCAVaLKQNU"}); err == nil {
		t.Error("an ed25519 blob passed as ssh-rsa")
	}
	if _, err := CheckSSHKeys([]string{"not a key"}); err == nil {
		t.Error("junk passed")
	}
	s, _, rec := newService(t)
	ctx := context.Background()
	if _, err := s.SetSSHKeys(ctx, SSHKeys{Keys: []string{ed, "  " + ed + "  ", ""}}); err != nil {
		t.Fatal(err)
	}
	// Setting the name and zone keeps the keys.
	got, err := s.SetHost(ctx, Host{Hostname: "box"})
	if err != nil || len(got.Host.SSHKeys) != 1 || got.Host.SSHKeys[0] != ed || got.Host.Hostname != "box" {
		t.Fatalf("host = %+v %v", got.Host, err)
	}
	a := &ImageApplier{Root: t.TempDir()}
	if err := a.ssh(ctx, rec.got[len(rec.got)-1]); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.Root, SSHKeysFile)
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), ed) {
		t.Errorf("authorized_keys = %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if _, err := s.SetSSHKeys(ctx, SSHKeys{Keys: nil}); err != nil {
		t.Fatal(err)
	}
	if err := a.ssh(ctx, rec.got[len(rec.got)-1]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("the keys file stayed after every key was removed")
	}
}

func TestWired(t *testing.T) {
	mac := net.HardwareAddr{0xd8, 0x43, 0xae, 0, 0x11, 0x22}
	got := wired([]net.Interface{
		{Name: "lo"},
		{Name: "enp5s0", HardwareAddr: mac},
		{Name: "ve-drafts", HardwareAddr: mac},
		{Name: "wg-homelab"},
		{Name: "eth0", HardwareAddr: mac},
	})
	want := []Interface{{Name: "enp5s0", MAC: "d8:43:ae:00:11:22", Addresses: []string{}, Subnets: []string{}}, {Name: "eth0", MAC: "d8:43:ae:00:11:22", Addresses: []string{}, Subnets: []string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wired = %v, want %v", got, want)
	}
}

// A folder of zones is not a zone.
func TestTimezoneFolderIsRefused(t *testing.T) {
	s, _, _ := newService(t)
	for _, tz := range []string{"Europe", "Europe/Oslo"} {
		if _, err := s.SetHost(context.Background(), Host{Hostname: "box", Timezone: tz}); !errors.Is(err, ErrInvalid) {
			t.Errorf("time zone %q: %v", tz, err)
		}
	}
	if _, err := s.SetHost(context.Background(), Host{Hostname: "box", Timezone: "Europe/Stockholm"}); err != nil {
		t.Errorf("a zone file: %v", err)
	}
}

func TestTimeSettings(t *testing.T) {
	s, _, rec := newService(t)
	ctx := context.Background()
	for _, bad := range []Time{{Clock: "am"}, {NTP: []string{"not a host"}}, {NTP: []string{"a", "b", "c", "d", "e"}}} {
		if _, err := s.SetTime(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetTime(%+v) = %v, want invalid", bad, err)
		}
	}
	st, err := s.SetTime(ctx, Time{Clock: Clock24h, NTP: []string{" Router.Lan. ", "192.168.1.1", "router.lan", ""}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Time.Clock != Clock24h || !reflect.DeepEqual(st.Time.NTP, []string{"router.lan", "192.168.1.1"}) {
		t.Errorf("time = %+v", st.Time)
	}
	files, _ := EtcFiles(rec.got[len(rec.got)-1])
	if f := files[timesyncdFile]; f != "[Time]\nNTP=router.lan 192.168.1.1\n" {
		t.Errorf("timesyncd = %q", f)
	}
	st, _ = s.SetTime(ctx, Time{})
	files, _ = EtcFiles(rec.got[len(rec.got)-1])
	if _, ok := files[timesyncdFile]; ok || st.Time.NTP == nil {
		t.Errorf("no servers: file kept or ntp nil (%+v)", st.Time)
	}
}

func TestConsoleKeyboard(t *testing.T) {
	s, _, rec := newService(t)
	ctx := context.Background()
	dir := t.TempDir()
	s.xkbRules, s.kbdMap = filepath.Join(dir, "base.lst"), filepath.Join(dir, "kbd-model-map")
	_ = os.WriteFile(s.xkbRules, []byte("! model\n  pc105  Generic 105-key PC\n\n! layout\n  us              English (US)\n  se              Swedish\n  ara             Arabic\n\n! variant\n  nodeadkeys      se: Swedish (no dead keys)\n"), 0o644)
	_ = os.WriteFile(s.kbdMap, []byte("# Generated\nsv-latin1\tse\tpc105\t-\tterminate:ctrl_alt_bksp\nse-nodead\tse\tpc105\tnodeadkeys\t-\n"), 0o644)
	got := s.KeyboardLayouts().Layouts
	if len(got) != 3 || got[0].Name != "ara" || got[2] != (KeyboardLayout{"se", "Swedish"}) {
		t.Errorf("layouts = %+v", got)
	}
	if _, err := s.SetConsole(ctx, Console{Keyboard: "nodeadkeys"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a variant was taken: %v", err)
	}
	st, err := s.SetConsole(ctx, Console{Keyboard: "se"})
	if err != nil || st.Console.Keyboard != "se" {
		t.Fatal(st.Console, err)
	}
	files, _ := EtcFiles(rec.got[len(rec.got)-1])
	if !strings.HasSuffix(files[kmsconFile], "\nxkb-layout=se\n") || files[vconsoleFile] != vconsoleBase+"KEYMAP=sv-latin1\n" {
		t.Errorf("kmscon %q, vconsole %q", files[kmsconFile], files[vconsoleFile])
	}
	// No keymap for the layout: kmscon only.
	_, _ = s.SetConsole(ctx, Console{Keyboard: "ara"})
	files, _ = EtcFiles(rec.got[len(rec.got)-1])
	if _, ok := files[vconsoleFile]; ok || files[kmsconFile] == "" {
		t.Errorf("ara: %v", files)
	}
	_, _ = s.SetConsole(ctx, Console{})
	files, _ = EtcFiles(rec.got[len(rec.got)-1])
	if _, ok := files[kmsconFile]; ok {
		t.Error("no layout: the image's kmscon.conf is shadowed")
	}
}

// The settings' console files start from the image's; they must not drift.
func TestConsoleBasesMatchTheImage(t *testing.T) {
	for name, want := range map[string]string{"etc/kmscon/kmscon.conf": kmsconBase, "etc/vconsole.conf": vconsoleBase} {
		b, err := os.ReadFile(filepath.Join("../../../../image/base/mkosi.extra", name))
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("no image tree beside the daemon")
		}
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != want {
			t.Errorf("%s changed in the image: copy it into general.go", name)
		}
	}
}

func TestGeneralReload(t *testing.T) {
	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	for _, p := range [][]string{{"systemctl"}, {"systemd-confext"}, {"networkctl"}, {"nft"}, {"tailscale"}, {"ip"}} {
		f.On(p...).Reply("")
	}
	a := &ImageApplier{Runner: f, Root: root}
	ctx := context.Background()
	s := snapshotOf(t)
	a.Apply(ctx, s)
	if len(f.CallsMatching("systemctl", "try-restart")) != 0 {
		t.Fatalf("restarted with nothing set: %+v", f.Calls())
	}
	s.Time.NTP, s.Console.Keyboard, s.Keymap = []string{"router.lan"}, "se", "sv-latin1"
	a.Apply(ctx, s)
	for _, u := range []string{"systemd-timesyncd.service", "systemd-vconsole-setup.service", consoleUnit} {
		if len(f.CallsMatching("systemctl", "try-restart", u)) != 1 {
			t.Errorf("%s not restarted: %+v", u, f.Calls())
		}
	}
	// Taken off again: the files go and the services restart on the image's.
	s.Time.NTP, s.Console.Keyboard, s.Keymap = nil, "", ""
	a.Apply(ctx, s)
	if len(f.CallsMatching("systemctl", "try-restart", consoleUnit)) != 2 || len(f.CallsMatching("systemctl", "try-restart", "systemd-timesyncd.service")) != 2 {
		t.Errorf("not restarted after removal: %+v", f.Calls())
	}
	if _, err := os.Stat(filepath.Join(root, "/var/lib/confexts/homelab-host", kmsconFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("kmscon.conf left in the confext")
	}
}

func TestReadTimeSync(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("timedatectl", "show", "--property=NTPSynchronized").Reply("yes\n")
	f.On("timedatectl", "show-timesync").Reply("0.arch.pool.ntp.org\n")
	if got := readTimeSync(context.Background(), f); got != (TimeSync{true, "0.arch.pool.ntp.org"}) {
		t.Errorf("synced: %+v", got)
	}
	f = cmdrunnertest.NewFake()
	f.On("timedatectl").Reply("no\n")
	if got := readTimeSync(context.Background(), f); got != (TimeSync{}) {
		t.Errorf("not synced: %+v", got)
	}
}

func TestLANOf(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"192.0.2.37": "192.0.2.0/24", "10.1.2.3/16": "10.1.2.0/24", "169.254.1.1": "", "127.0.0.1": "", "fd00::1": "", "x": "",
	} {
		if got, _ := lanOf(in); got != want {
			t.Errorf("lanOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAFreshHostTakesItsLANFromItsAddress(t *testing.T) {
	t.Parallel()
	s, _, rec := newService(t)
	ctx := context.Background()
	var mu sync.Mutex
	addrs := []string{}
	s.addrs = func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(addrs) }
	s.inferEvery = time.Millisecond
	// DHCP has no address yet: the first tries wait, and a link-local
	// address is no LAN.
	done := make(chan struct{})
	go func() { s.InferLAN(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	addrs = []string{"169.254.3.4", "192.168.1.20"}
	mu.Unlock()
	<-done
	got, _ := s.Get(ctx)
	if !slices.Equal(got.Network.LAN, []string{"192.168.1.0/24"}) || got.Network.IPv4.Mode != "dhcp" || len(rec.got) != 1 {
		t.Fatalf("network = %+v, applied %d times", got.Network, len(rec.got))
	}
	// A LAN the operator cleared stays cleared.
	if _, err := s.SetNetwork(ctx, Network{}); err != nil {
		t.Fatal(err)
	}
	s.InferLAN(ctx)
	if got, _ := s.Get(ctx); len(got.Network.LAN) != 0 {
		t.Errorf("LAN set again: %v", got.Network.LAN)
	}
}

func TestAStaticInstallTakesItsLANFromItsAddress(t *testing.T) {
	t.Parallel()
	s, _, _ := newService(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "install.json")
	_ = os.WriteFile(file, []byte(`{"hostname":"box","ipv4":{"mode":"static","address":"10.0.5.9/16","gateway":"10.0.0.1"}}`), 0o644)
	if !s.AdoptInstall(ctx, file) {
		t.Fatal("not adopted")
	}
	if got, _ := s.Get(ctx); !slices.Equal(got.Network.LAN, []string{"10.0.5.0/24"}) {
		t.Errorf("LAN = %v", got.Network.LAN)
	}
}

func TestTunnelRefusesDashboard(t *testing.T) {
	t.Parallel()
	s, _, _ := newService(t)
	ctx := context.Background()

	// Without a domain set, the default dashboard label ("homelab") is still refused.
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Credentials: &creds,
		Routes:      []Route{{Hostname: "dash.example.net", Target: "homelab"}},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tunnel targeting default dashboard without domain: %v, want invalid", err)
	}

	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"homelab", "homelab.home.example.com", "HOMELAB.HOME.EXAMPLE.COM"} {
		if _, err := s.SetTunnel(ctx, TunnelSet{
			Credentials: &creds,
			Routes:      []Route{{Hostname: "dash.example.net", Target: target}},
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetTunnel(%q) = %v, want invalid", target, err)
		}
	}
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Credentials: &creds,
		Routes: []Route{
			{Hostname: "photos.example.net", Target: "photos"},
			{Hostname: "immich.example.net", Target: "photos.home.example.com"},
		},
	}); err != nil {
		t.Fatalf("SetTunnel valid routes: %v", err)
	}

	// Custom dashboard_label blocks that label and its full host name, while allowing "homelab".
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com", DashboardLabel: "lab"}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"lab", "lab.home.example.com"} {
		if _, err := s.SetTunnel(ctx, TunnelSet{
			Credentials: &creds,
			Routes:      []Route{{Hostname: "dash.example.net", Target: target}},
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetTunnel(%q) with label lab = %v, want invalid", target, err)
		}
	}
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Credentials: &creds,
		Routes:      []Route{{Hostname: "other.example.net", Target: "homelab"}},
	}); err != nil {
		t.Fatalf("SetTunnel(homelab) with label lab: %v", err)
	}

	// Changing dashboard_label to collide with an existing tunnel route is refused.
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Credentials: &creds,
		Routes:      []Route{{Hostname: "svc.example.net", Target: "panel"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com", DashboardLabel: "panel"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("SetDomain colliding with label route = %v, want invalid", err)
	}
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Credentials: &creds,
		Routes:      []Route{{Hostname: "svc.example.net", Target: "panel.home.example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com", DashboardLabel: "panel"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("SetDomain colliding with FQDN route = %v, want invalid", err)
	}

	// Defense in depth: TunnelConfig drops any dashboard route already in a snapshot.
	var snap Snapshot
	snap.Domain = Domain{Domain: "home.example.com"}
	snap.Tunnel = Tunnel{
		Enabled:  true,
		TunnelID: tunnelID(creds),
		Routes: []Route{
			{Hostname: "dash1.example.net", Target: "homelab"},
			{Hostname: "dash2.example.net", Target: "homelab.home.example.com"},
			{Hostname: "photos.example.net", Target: "photos"},
		},
	}
	cfg := TunnelConfig(snap, "/c.json")
	for _, forbidden := range []string{"dash1.example.net", "dash2.example.net", "homelab.home.example.com"} {
		if strings.Contains(cfg, forbidden) {
			t.Errorf("TunnelConfig kept dashboard route %q:\n%s", forbidden, cfg)
		}
	}
	if !strings.Contains(cfg, "photos.example.net") {
		t.Errorf("TunnelConfig dropped non-dashboard route:\n%s", cfg)
	}
}
