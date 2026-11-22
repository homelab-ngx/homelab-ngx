package hostcfg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
)

const (
	wgInterface = "wg-homelab"
	// tunnelUnit runs cloudflared on the image (image/core).
	tunnelUnit = "homelab-cloudflared.service"
	// tunnelMetrics is cloudflared's local metrics and ready listener.
	tunnelMetrics = "127.0.0.1:20241"
	// stackRoutesDir holds the stacks' route snippets (internal/stacks).
	stackRoutesDir = "/var/lib/homelab-stack-routes"
)

func wgPort(w WireGuard) int {
	if w.ListenPort == 0 {
		return 51820
	}
	return w.ListenPort
}

// ImageApplier writes what the image host reads from the settings, under
// Root ("" is /), and has the host pick it up through Runner. Every part is
// rewritten on every apply; a part whose files did not change is not
// reloaded.
type ImageApplier struct {
	Runner cmdrunner.Runner
	Root   string

	// routed holds the tunnel routes whose DNS record was added since the
	// daemon started, as "<tunnel id> <hostname>".
	routed map[string]bool
}

func (a *ImageApplier) path(p string) string { return filepath.Join(a.Root, p) }

func (a *ImageApplier) run(ctx context.Context, name string, args ...string) error {
	_, err := a.Runner.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: name, Args: args})
	return err
}

// mkdir makes dir and its parents under /var/lib mode 0755 whatever the
// daemon's umask is (0027): the confext's directories are /etc's once
// merged, and networkd and Caddy read what is under /var/lib/homelab.
func (a *ImageApplier) mkdir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := a.path("/var/lib")
	for d := dir; strings.HasPrefix(d, base+"/"); d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// write puts content at path with mode and group (by name, "" for root),
// and reports whether it changed.
func (a *ImageApplier) write(path, content string, mode os.FileMode, group string) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, []byte(content)) {
		return false, nil
	}
	if err := a.mkdir(filepath.Dir(path)); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return false, err
	}
	if group != "" && a.Root == "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return false, fmt.Errorf("group %s: %w", group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(tmp, 0, gid); err != nil {
			return false, err
		}
	}
	return true, os.Rename(tmp, path)
}

// remove deletes path and reports whether it was there.
func remove(path string) (bool, error) {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Apply writes every part; a failing part does not stop the others.
func (a *ImageApplier) Apply(ctx context.Context, s Snapshot) []error {
	var errs []error
	for _, part := range []struct {
		name string
		fn   func(context.Context, Snapshot) error
	}{
		{"caddy", a.caddy},
		{"etc", a.etc},
		{"firewall", a.firewall},
		{"tunnel", a.tunnel},
		{"tailscale", a.tailscale},
		{"ssh", a.ssh},
	} {
		if err := part.fn(ctx, s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", part.name, err))
		}
	}
	return errs
}

// ── SSH ──────────────────────────────────────────────────────────────────

// SSHKeysFile is root's authorized keys from the settings; sshd reads it
// besides root's own file (the image's sshd_config.d/10-homelab.conf).
const SSHKeysFile = "/var/lib/homelab/ssh/authorized_keys"

func (a *ImageApplier) ssh(_ context.Context, s Snapshot) error {
	p := a.path(SSHKeysFile)
	if len(s.Host.SSHKeys) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	body := "# Written by homelab-daemon from the host settings; changes here are lost.\n" + strings.Join(s.Host.SSHKeys, "\n") + "\n"
	return writeIfChanged(p, body, 0o600)
}

// ── Caddy ────────────────────────────────────────────────────────────────

// CaddySite is the site file for the domain: a wildcard certificate by
// DNS-01 (or Caddy's local authority), the dashboard at its name and the
// stacks' routes. Empty without a domain.
func CaddySite(s Snapshot) string {
	d := s.Domain
	if d.Domain == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by homelab-daemon from the host settings; changes here are lost.\n")
	fmt.Fprintf(&b, "*.%s, %s {\n", d.Domain, d.Domain)
	tls := "\ttls"
	if d.ACMEEmail != "" {
		tls += " " + d.ACMEEmail
	}
	c := s.DNSCredentials
	switch d.DNSProvider {
	case DNSNone:
		b.WriteString("\ttls internal\n")
	case DNSCloudflare, DNSDuckDNS:
		fmt.Fprintf(&b, "%s {\n\t\tdns %s %s\n\t\tresolvers 1.1.1.1 9.9.9.9\n\t}\n", tls, d.DNSProvider, c["api_token"])
	case DNSPorkbun:
		fmt.Fprintf(&b, "%s {\n\t\tdns porkbun {\n\t\t\tapi_key %s\n\t\t\tapi_secret_key %s\n\t\t}\n\t\tresolvers 1.1.1.1 9.9.9.9\n\t}\n", tls, c["api_key"], c["api_secret_key"])
	case DNSRoute53:
		fmt.Fprintf(&b, "%s {\n\t\tdns route53 {\n\t\t\taccess_key_id %s\n\t\t\tsecret_access_key %s\n", tls, c["access_key_id"], c["secret_access_key"])
		if d.Region != "" {
			fmt.Fprintf(&b, "\t\t\tregion %s\n", d.Region)
		}
		b.WriteString("\t\t}\n\t\tresolvers 1.1.1.1 9.9.9.9\n\t}\n")
	}
	fmt.Fprintf(&b, "\t@dashboard host %s\n\thandle @dashboard {\n\t\timport homelab_dashboard\n\t}\n", DashboardHost(d))
	fmt.Fprintf(&b, "\timport %s/*.caddy\n", stackRoutesDir)
	b.WriteString("\thandle {\n\t\trespond 404\n\t}\n}\n")
	return b.String()
}

// CaddyHTTPRedirect is what the dashboard's plain-HTTP site (:80) does
// first once the host has a domain: send the browser to the dashboard's
// HTTPS name, so the password never crosses the network in the clear.
// Empty without a domain: :80 serves the dashboard itself.
func CaddyHTTPRedirect(s Snapshot) string {
	h := DashboardHost(s.Domain)
	if h == "" {
		return ""
	}
	return "# Written by homelab-daemon from the host settings; changes here are lost.\n" +
		"redir https://" + h + "{uri}\n"
}

func (a *ImageApplier) caddy(ctx context.Context, s Snapshot) error {
	p := a.path("/var/lib/homelab/caddy/site.caddy")
	var changed bool
	var err error
	if site := CaddySite(s); site != "" {
		// Credentials are in it: readable by Caddy only.
		changed, err = a.write(p, site, 0o640, "caddy")
	} else {
		changed, err = remove(p)
	}
	if err != nil {
		return err
	}
	// dashboard.caddy's :80 site imports this directory.
	r := a.path("/var/lib/homelab/caddy/http/redirect.caddy")
	var rchanged bool
	if redir := CaddyHTTPRedirect(s); redir != "" {
		rchanged, err = a.write(r, redir, 0o644, "")
	} else {
		rchanged, err = remove(r)
	}
	if err != nil || !(changed || rchanged) {
		return err
	}
	return a.run(ctx, "systemctl", "try-reload-or-restart", "caddy.service")
}

// ── /etc through a confext ───────────────────────────────────────────────

// EtcFiles are the files of the homelab-host configuration extension,
// relative to its root, and the symlinks (path → target).
func EtcFiles(s Snapshot) (files map[string]string, links map[string]string) {
	files = map[string]string{"etc/extension-release.d/extension-release.homelab-host": "ID=arch\n"}
	links = map[string]string{}
	agentEtc(files, links)
	if s.Host.Hostname != "" {
		files["etc/hostname"] = s.Host.Hostname + "\n"
	}
	if s.Host.Timezone != "" {
		links["etc/localtime"] = "../usr/share/zoneinfo/" + s.Host.Timezone
	}
	// A static address replaces the image's DHCP default (80-wired.network):
	// networkd takes the first file that matches an interface.
	if ip := s.Network.IPv4; ip.Mode == "static" {
		var n strings.Builder
		fmt.Fprintf(&n, "[Match]\nName=en* eth*\n\n[Network]\nAddress=%s\nGateway=%s\nMulticastDNS=yes\n", ip.Address, ip.Gateway)
		for _, d := range ip.DNS {
			fmt.Fprintf(&n, "DNS=%s\n", d)
		}
		files["etc/systemd/network/10-homelab-wired.network"] = n.String()
	}
	if w := s.WireGuard; w.Enabled && s.WireGuardKey != "" {
		var nd strings.Builder
		fmt.Fprintf(&nd, "[NetDev]\nName=%s\nKind=wireguard\n\n[WireGuard]\nPrivateKeyFile=/var/lib/homelab/wireguard/private.key\nListenPort=%d\n", wgInterface, wgPort(w))
		for _, p := range w.Peers {
			fmt.Fprintf(&nd, "\n# %s\n[WireGuardPeer]\nPublicKey=%s\nAllowedIPs=%s\n", p.Name, p.PublicKey, p.Address)
		}
		files["etc/systemd/network/50-"+wgInterface+".netdev"] = nd.String()
		files["etc/systemd/network/50-"+wgInterface+".network"] = fmt.Sprintf("[Match]\nName=%s\n\n[Network]\nAddress=%s\n", wgInterface, w.Address)
	}
	return files, links
}

func (a *ImageApplier) etc(ctx context.Context, s Snapshot) error {
	root := a.path("/var/lib/confexts/homelab-host")
	files, links := EtcFiles(s)
	changed := false
	// The WireGuard key file lives on /var, outside the confext.
	keyFile := a.path("/var/lib/homelab/wireguard/private.key")
	if s.WireGuard.Enabled && s.WireGuardKey != "" {
		c, err := a.write(keyFile, s.WireGuardKey+"\n", 0o640, "systemd-network")
		if err != nil {
			return err
		}
		changed = changed || c
	} else if _, err := remove(keyFile); err != nil {
		return err
	}
	for rel, content := range files {
		c, err := a.write(filepath.Join(root, rel), content, 0o644, "")
		if err != nil {
			return err
		}
		changed = changed || c
	}
	for rel, target := range links {
		p := filepath.Join(root, rel)
		if old, err := os.Readlink(p); err == nil && old == target {
			continue
		}
		_ = os.Remove(p)
		if err := a.mkdir(filepath.Dir(p)); err != nil {
			return err
		}
		if err := os.Symlink(target, p); err != nil {
			return err
		}
		changed = true
	}
	// Whatever the settings no longer ask for goes.
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if _, ok := files[rel]; ok {
			return nil
		}
		if _, ok := links[rel]; ok {
			return nil
		}
		changed = true
		return os.Remove(p)
	})
	if err != nil {
		return err
	}
	var errs []error
	if changed {
		// A directory extension whose files changed is not seen as changed
		// without --always-refresh.
		if err := a.run(ctx, "systemd-confext", "refresh", "--always-refresh=yes"); err != nil {
			errs = append(errs, err)
		}
		if !s.WireGuard.Enabled {
			// networkd leaves a removed netdev's device in place.
			_ = a.run(ctx, "ip", "link", "delete", wgInterface)
		}
		if err := a.run(ctx, "networkctl", "reload"); err != nil {
			errs = append(errs, err)
		}
	}
	// Every time: at boot the kernel's name is set from the image's
	// /etc/hostname before the confext is merged.
	if s.Host.Hostname != "" {
		if err := a.setHostname(s.Host.Hostname); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// setHostname sets the kernel's host name by writing it to /proc.
// hostnamectl --transient does not: hostnamed ignores a transient name while
// /etc/hostname holds a static one, and it cannot write the static one, as
// /etc is the read-only confext. hostnamed reads the static name from the
// merged /etc/hostname, and resolved's mDNS follows the kernel's name.
func (a *ImageApplier) setHostname(name string) error {
	p := a.path("/proc/sys/kernel/hostname")
	if old, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(old)) == name {
		return nil
	}
	if a.Root != "" {
		if err := a.mkdir(filepath.Dir(p)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
		return fmt.Errorf("setting the host name: %w", err)
	}
	return nil
}

// ── firewall ─────────────────────────────────────────────────────────────

// Ruleset is the host's nftables table: SSH, HTTP and HTTPS, ICMP, DHCP,
// the VPN's port, Tailscale's and the openings come in, and answers to the
// host's own traffic; the rest is dropped. The stacks' interfaces (ve-*)
// are let through: each stack's own table is its policy.
func Ruleset(s Snapshot) string {
	var b strings.Builder
	b.WriteString("# Written by homelab-daemon from the host settings.\n")
	b.WriteString("table inet homelab_host\ndelete table inet homelab_host\n")
	b.WriteString("table inet homelab_host {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n")
	for _, r := range []string{
		"ct state established,related accept",
		"ct state invalid drop",
		`iifname "lo" accept`,
		`iifname "ve-*" accept`,
		"meta l4proto { icmp, ipv6-icmp } accept",
		"udp sport 67 udp dport 68 accept",
		"udp dport 546 accept",
		// mDNS: the host answers as <hostname>.local on the LAN.
		"udp dport 5353 accept",
		"tcp dport { 22, 80, 443 } accept",
	} {
		fmt.Fprintf(&b, "\t\t%s\n", r)
	}
	if s.WireGuard.Enabled {
		fmt.Fprintf(&b, "\t\tudp dport %d accept\n", wgPort(s.WireGuard))
		fmt.Fprintf(&b, "\t\tiifname %q accept\n", wgInterface)
	}
	if s.Tailscale.Enabled {
		b.WriteString("\t\tudp dport 41641 accept\n\t\tiifname \"tailscale0\" accept\n")
	}
	openings := slices.Clone(s.Firewall.Openings)
	slices.SortFunc(openings, func(x, y Opening) int { return x.Port - y.Port })
	for _, o := range openings {
		from := ""
		if len(o.From) > 0 {
			from = "ip saddr { " + strings.Join(o.From, ", ") + " } "
		}
		fmt.Fprintf(&b, "\t\t%s%s dport %d accept\n", from, o.Proto, o.Port)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

func (a *ImageApplier) firewall(ctx context.Context, s Snapshot) error {
	p := a.path("/var/lib/homelab/nftables/host.nft")
	if _, err := a.write(p, Ruleset(s), 0o644, ""); err != nil {
		return err
	}
	// Loaded every time: the table is gone after a reboot.
	return a.run(ctx, "nft", "-f", p)
}

// ── Cloudflare tunnel ────────────────────────────────────────────────────

// TunnelConfig is cloudflared's config: each route to Caddy on 443 under
// its private name, everything else 404.
func TunnelConfig(s Snapshot, credentialsPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by homelab-daemon from the host settings.\ntunnel: %s\ncredentials-file: %s\nmetrics: %s\nno-autoupdate: true\ningress:\n", s.Tunnel.TunnelID, credentialsPath, tunnelMetrics)
	for _, r := range s.Tunnel.Routes {
		target := RouteTarget(r.Target, s.Domain.Domain)
		fmt.Fprintf(&b, "  - hostname: %s\n", r.Hostname)
		if r.Path != "" {
			fmt.Fprintf(&b, "    path: %q\n", r.Path)
		}
		fmt.Fprintf(&b, "    service: https://127.0.0.1:443\n    originRequest:\n      originServerName: %s\n      httpHostHeader: %s\n", target, target)
	}
	b.WriteString("  - service: http_status:404\n")
	return b.String()
}

func (a *ImageApplier) tunnel(ctx context.Context, s Snapshot) error {
	dir := a.path("/var/lib/homelab/cloudflared")
	creds := filepath.Join(dir, "credentials.json")
	cfg := filepath.Join(dir, "config.yml")
	cert := filepath.Join(dir, "cert.pem")
	if !s.Tunnel.Enabled || s.TunnelCredentials == "" {
		_, err1 := remove(cfg)
		_, err2 := remove(creds)
		_, err3 := remove(cert)
		_ = a.run(ctx, "systemctl", "disable", "--runtime", "--now", tunnelUnit)
		return errors.Join(err1, err2, err3)
	}
	c1, err := a.write(creds, s.TunnelCredentials+"\n", 0o600, "")
	if err != nil {
		return err
	}
	// The unit runs as a dynamic user and gets the file root keeps as a
	// credential (image/core: LoadCredential).
	c2, err := a.write(cfg, TunnelConfig(s, "/run/credentials/"+tunnelUnit+"/credentials.json"), 0o644, "")
	if err != nil {
		return err
	}
	if err := a.run(ctx, "systemctl", "enable", "--runtime", tunnelUnit); err != nil {
		return err
	}
	start := "start"
	if c1 || c2 {
		start = "restart"
	}
	if err := a.run(ctx, "systemctl", start, tunnelUnit); err != nil {
		return err
	}
	return a.routeDNS(ctx, s, cert)
}

// routeDNS points each route's name at the tunnel (a CNAME to
// <id>.cfargotunnel.com), with the certificate from the tunnel's login. A
// record of the same name is replaced. Each route is added once per daemon
// start; a tunnel whose credentials were pasted has no certificate, and its
// records are added at Cloudflare by hand.
func (a *ImageApplier) routeDNS(ctx context.Context, s Snapshot, cert string) error {
	if s.TunnelCert == "" {
		_, err := remove(cert)
		return err
	}
	if _, err := a.write(cert, s.TunnelCert, 0o600, ""); err != nil {
		return err
	}
	if a.routed == nil {
		a.routed = map[string]bool{}
	}
	var errs []error
	for _, r := range s.Tunnel.Routes {
		k := s.Tunnel.TunnelID + " " + r.Hostname
		if a.routed[k] {
			continue
		}
		if err := a.run(ctx, "cloudflared", "tunnel", "--origincert", cert, "route", "dns", "--overwrite-dns", s.Tunnel.TunnelID, r.Hostname); err != nil {
			errs = append(errs, fmt.Errorf("DNS record for %s (is it in the zone approved at the tunnel's login?): %s", r.Hostname, commandError(err)))
			continue
		}
		a.routed[k] = true
	}
	return errors.Join(errs...)
}

// ── Tailscale ────────────────────────────────────────────────────────────

func (a *ImageApplier) tailscale(ctx context.Context, s Snapshot) error {
	if !s.Tailscale.Enabled {
		_ = a.run(ctx, "systemctl", "disable", "--runtime", "--now", "tailscaled.service")
		return nil
	}
	if err := a.run(ctx, "systemctl", "enable", "--runtime", "--now", "tailscaled.service"); err != nil {
		return err
	}
	// Until the host is logged in (login.go) there is nothing to set; the
	// login applies the settings again once it is in.
	if readTailscale(ctx, a.Runner).State != TailscaleRunning {
		return nil
	}
	if err := a.forwarding(s.Tailscale.AdvertiseRoutes); err != nil {
		return err
	}
	args := []string{"set", "--advertise-routes=" + strings.Join(s.Tailscale.AdvertiseRoutes, ",")}
	if s.Host.Hostname != "" {
		args = append(args, "--hostname="+s.Host.Hostname)
	}
	if err := a.run(ctx, "tailscale", args...); err != nil {
		return fmt.Errorf("offering subnets: %s", commandError(err))
	}
	return nil
}

// forwarding turns on IP forwarding for each family the offered subnets are
// in, which Tailscale needs to route them through the host. It used to be on
// only by chance, while systemd-networkd managed a stack's link; a stack's
// start now turns on IPv4 forwarding alone (stacks/network.go).
func (a *ImageApplier) forwarding(routes []string) error {
	var v4, v6 bool
	for _, r := range routes {
		if p, err := netip.ParsePrefix(r); err == nil {
			v4, v6 = v4 || p.Addr().Is4(), v6 || !p.Addr().Is4()
		}
	}
	for _, f := range []struct {
		on   bool
		path string
	}{{v4, "/proc/sys/net/ipv4/ip_forward"}, {v6, "/proc/sys/net/ipv6/conf/all/forwarding"}} {
		if !f.on {
			continue
		}
		p := a.path(f.path)
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == "1" {
			continue
		}
		if a.Root != "" {
			if err := a.mkdir(filepath.Dir(p)); err != nil {
				return err
			}
		}
		if err := os.WriteFile(p, []byte("1"), 0o644); err != nil {
			return fmt.Errorf("turning on forwarding for the offered subnets: %w", err)
		}
	}
	return nil
}

// writeIfChanged writes body to p with mode unless p already holds it.
func writeIfChanged(p, body string, mode os.FileMode) error {
	if old, err := os.ReadFile(p); err == nil && string(old) == body {
		return os.Chmod(p, mode)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		return err
	}
	return os.Chmod(p, mode)
}
