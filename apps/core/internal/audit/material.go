package audit

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/backups"
	"homelab-ngx.com/core/internal/cloudflare"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/sysupdates"
)

// The sources an audit reads. Any may be nil; what it would give is then
// said to be not visible.
type (
	StackSource interface {
		List(ctx context.Context) ([]stacks.Stack, error)
		// SecretValues are the stacks' secret variable values, only to
		// scrub them out of the material.
		SecretValues(ctx context.Context) ([]string, error)
	}
	BackupSource interface {
		List(ctx context.Context) ([]backups.Backup, error)
		Destinations(ctx context.Context) ([]backups.Destination, error)
	}
	UpdateSource interface {
		ImageStatus(ctx context.Context) (sysupdates.ImageStatus, error)
	}
	ProblemSource interface {
		OpenProblems(ctx context.Context) ([]notifications.ProblemState, error)
	}
	SettingsSource interface {
		Get(ctx context.Context) (llm.AssistantSettings, error)
	}
)

// Sources are where an audit's material comes from.
type Sources struct {
	// Host is what the host's configuration says (config.json).
	Host config.Security
	// Facts, when set, gives Host, Tunnel and Domain at the time of the
	// audit instead: the host's settings change at run time.
	Facts func(ctx context.Context) (config.Security, config.CloudflareTunnel, string)
	// Tunnel is the Cloudflare tunnel's published hostnames.
	Tunnel config.CloudflareTunnel
	// Domain is the private domain stack routes are served under.
	Domain string
	// Probe checks how each published hostname answers from outside.
	Probe func(ctx context.Context, t config.CloudflareTunnel) []cloudflare.HostnameStatus
	// Runner runs ss for the listening sockets.
	Runner    cmdrunner.Runner
	Stacks    StackSource
	Backups   BackupSource
	Updates   UpdateSource
	Problems  ProblemSource
	Assistant SettingsSource
}

// Caps on the material.
const (
	areaChars  = 40_000
	minSecret  = 8
	notVisible = "(not visible to the daemon)"
)

// gathered is what an audit starts from: the overview every chunk gets and
// each area's own material, scrubbed.
type gathered struct {
	overview string
	areas    map[Area]string
}

// sources are the audit's sources with the host's facts as they are now.
func (s *Service) sources(ctx context.Context) Sources {
	src := s.src
	if src.Facts != nil {
		src.Host, src.Tunnel, src.Domain = src.Facts(ctx)
	}
	return src
}

// gather reads every area's material. An error of one source becomes a
// line of its material; the audit still runs.
func (s *Service) gather(ctx context.Context) gathered {
	src := s.sources(ctx)
	var stackList []stacks.Stack
	var stackErr error
	if src.Stacks != nil {
		stackList, stackErr = src.Stacks.List(ctx)
	}
	g := gathered{areas: map[Area]string{}}
	g.overview = overview(src, stackList)
	g.areas[AreaExposure] = s.exposure(ctx, stackList, stackErr)
	g.areas[AreaAccess] = s.access(ctx, stackList, stackErr)
	g.areas[AreaStacks] = stacksMaterial(stackList, stackErr, src.Stacks != nil)
	g.areas[AreaSecrets] = s.secretsAndBackups(ctx)
	g.areas[AreaUpdates] = s.updates(ctx, stackList, stackErr)

	known := s.knownSecrets(ctx)
	g.overview = scrub(g.overview, known)
	for a, m := range g.areas {
		if len(m) > areaChars {
			m = m[:areaChars] + "\n[material cut]\n"
		}
		g.areas[a] = scrub(m, known)
	}
	return g
}

// overview is the short picture of the whole host each chunk starts from.
func overview(src Sources, list []stacks.Stack) string {
	var b strings.Builder
	b.WriteString("# Host overview\n")
	if src.Domain != "" {
		fmt.Fprintf(&b, "Private domain (LAN and VPN only): %s\n", src.Domain)
	}
	public := slices.Clone(src.Tunnel.Hostnames)
	sort.Strings(public)
	if src.Tunnel.Enabled && len(public) > 0 {
		fmt.Fprintf(&b, "Published to the internet through the Cloudflare tunnel: %s\n", strings.Join(public, ", "))
	} else {
		b.WriteString("Published to the internet through the Cloudflare tunnel: none\n")
	}
	fw := src.Host.Firewall
	fmt.Fprintf(&b, "Firewall open on every interface: TCP %s; UDP %s\n", ints(fw.TCPPorts), ints(fw.UDPPorts))
	names := make([]string, 0, len(list))
	for _, st := range list {
		names = append(names, st.Name)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "Stacks: %s\n", strings.Join(names, ", "))
	} else {
		b.WriteString("Stacks: none\n")
	}
	return b.String()
}

func (s *Service) exposure(ctx context.Context, list []stacks.Stack, stackErr error) string {
	src := s.sources(ctx)
	h := src.Host
	var b strings.Builder

	b.WriteString("# Firewall\n")
	fmt.Fprintf(&b, "enabled: %t\n", h.Firewall.Enabled)
	fmt.Fprintf(&b, "open on every interface: TCP %s; UDP %s\n", ints(h.Firewall.TCPPorts), ints(h.Firewall.UDPPorts))
	for _, name := range sortedKeys(h.Firewall.Interfaces) {
		p := h.Firewall.Interfaces[name]
		fmt.Fprintf(&b, "open on %s only: TCP %s; UDP %s\n", name, ints(p.TCPPorts), ints(p.UDPPorts))
	}
	fmt.Fprintf(&b, "trusted interfaces (all traffic accepted): %s\n", list0(h.Firewall.TrustedInterfaces))
	fmt.Fprintf(&b, "stack interfaces (ve-*): trusted by the host firewall because each stack has its own nftables table "+
		"(homelab_stack_<name>), the only filter between it and the host. It lets the stack reach the internet and the host's "+
		"Caddy on TCP 443; the host's other ports, private ranges and IPv6 are refused unless an egress grant opens them, and "+
		"port %d only for a stack with an api grant. Nothing connects into a stack but the host and the clients its port grants name.\n", stacks.APIPort)
	if rules := firewallRules(h.Firewall.ExtraCommands); len(rules) > 0 {
		b.WriteString("extra rules:\n")
		for _, r := range rules {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}

	b.WriteString("\n# Listening sockets (ss, now)\n")
	b.WriteString(s.sockets(ctx))

	b.WriteString("\n# Cloudflare tunnel: hostnames reachable from the internet\n")
	if !src.Tunnel.Enabled {
		b.WriteString("the tunnel is off\n")
	} else {
		probes := map[string]cloudflare.HostnameStatus{}
		if src.Probe != nil {
			for _, p := range src.Probe(ctx, src.Tunnel) {
				probes[p.Hostname] = p
			}
		}
		hosts := slices.Clone(src.Tunnel.Hostnames)
		sort.Strings(hosts)
		for _, host := range hosts {
			fmt.Fprintf(&b, "%s:", host)
			if reason, ok := src.Tunnel.PublicHostnames[host]; ok {
				fmt.Fprintf(&b, " deliberately public, reason %q;", reason)
			} else {
				b.WriteString(" meant to be behind Cloudflare Access;")
			}
			if p, ok := probes[host]; ok {
				fmt.Fprintf(&b, " probed from outside: %s", p.Protection)
				if p.StatusCode != 0 {
					fmt.Fprintf(&b, " (HTTP %d)", p.StatusCode)
				}
				if p.Reason != "" {
					fmt.Fprintf(&b, ", %s", p.Reason)
				}
				if p.Err != "" {
					fmt.Fprintf(&b, ", error %s", p.Err)
				}
			} else {
				b.WriteString(" not probed")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n# Caddy sites of the host (outside the stacks)\n")
	if len(h.CaddyRoutes) == 0 {
		b.WriteString(notVisible + "\n")
	}
	for _, r := range h.CaddyRoutes {
		fmt.Fprintf(&b, "%s", r.Host)
		if len(r.Aliases) > 0 {
			fmt.Fprintf(&b, " (also %s)", strings.Join(r.Aliases, ", "))
		}
		fmt.Fprintf(&b, " -> %s", r.Upstream)
		if r.Static {
			b.WriteString(", serves the dashboard's files")
		}
		b.WriteString("\n")
		for _, d := range []struct{ label, text string }{{"directives", r.Directives}, {"proxy directives", r.ProxyDirectives}} {
			if strings.TrimSpace(d.text) == "" {
				continue
			}
			fmt.Fprintf(&b, "  %s:\n", d.label)
			for _, l := range strings.Split(strings.TrimSpace(d.text), "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					fmt.Fprintf(&b, "    %s\n", l)
				}
			}
		}
	}

	b.WriteString("\n# Stack routes and ports\n")
	switch {
	case stackErr != nil:
		fmt.Fprintf(&b, "the stacks could not be read: %v\n", stackErr)
	case len(list) == 0:
		b.WriteString("no stacks\n")
	}
	for _, st := range list {
		m := approved(st)
		if m == nil {
			fmt.Fprintf(&b, "%s: not approved yet\n", st.Name)
			continue
		}
		for _, r := range m.Routes {
			fmt.Fprintf(&b, "%s: route https://%s -> its port %d\n", st.Name, routeHost(r.Host, src.Domain), r.Port)
		}
		for _, p := range m.Ports {
			fmt.Fprintf(&b, "%s: host port %d/%s -> its port %d, from %s\n", st.Name, p.Port, p.Proto, p.To, list0(p.From))
		}
		if len(m.Routes) == 0 && len(m.Ports) == 0 {
			fmt.Fprintf(&b, "%s: no routes or ports\n", st.Name)
		}
	}

	b.WriteString("\n# WireGuard (inbound VPN)\n")
	if len(h.WireGuard) == 0 {
		b.WriteString("none\n")
	}
	for _, w := range h.WireGuard {
		fmt.Fprintf(&b, "%s listens on UDP %d; peers:", w.Interface, w.ListenPort)
		for _, p := range w.Peers {
			fmt.Fprintf(&b, " %s %s;", orDash(p.Name), strings.Join(p.AllowedIPs, ","))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n# Tailscale\n")
	ts := h.Tailscale
	if !ts.Enabled {
		b.WriteString("off\n")
	} else {
		fmt.Fprintf(&b, "on, UDP %d, firewall opened: %t, routing features: %s\n", ts.Port, ts.OpenFirewall, orDash(ts.RoutingFeatures))
		fmt.Fprintf(&b, "set flags: %s\n", list0(ts.SetFlags))
		if len(ts.UpFlags) > 0 {
			fmt.Fprintf(&b, "up flags: %s\n", strings.Join(ts.UpFlags, " "))
		}
	}
	notes(&b, h.Notes)
	return b.String()
}

func (s *Service) access(ctx context.Context, list []stacks.Stack, stackErr error) string {
	src := s.sources(ctx)
	h := src.Host
	var b strings.Builder

	b.WriteString("# SSH server\n")
	if !h.SSH.Enabled {
		b.WriteString("off\n")
	} else {
		fmt.Fprintf(&b, "on, ports %s, firewall opened: %t\n", ints(h.SSH.Ports), h.SSH.OpenFirewall)
		for _, k := range sortedKeys(h.SSH.Settings) {
			fmt.Fprintf(&b, "%s: %v\n", k, h.SSH.Settings[k])
		}
	}

	b.WriteString("\n# Login users\n")
	if len(h.Users) == 0 {
		b.WriteString(notVisible + "\n")
	}
	for _, u := range h.Users {
		fmt.Fprintf(&b, "%s: groups %s; %d SSH keys\n", u.Name, list0(u.Groups), u.SSHKeys)
	}
	fmt.Fprintf(&b, "sudo: on %t, wheel needs a password: %t\n", h.Sudo.Enabled, h.Sudo.WheelNeedsPassword)
	fmt.Fprintf(&b, "nix trusted users: %s\n", list0(h.NixTrustedUsers))

	b.WriteString("\n# Dashboard and daemon API\n")
	b.WriteString("The daemon's operator API is a unix socket, mode 0660, group homelab-daemon; Caddy proxies the dashboard's /api/* to it.\n")
	fmt.Fprintf(&b, "Stacks with an approved api grant reach the daemon API on port %d with their own token, only from their own address.\n", stacks.APIPort)
	b.WriteString("Their scopes: read (every GET) or control (start and stop as well).\n")
	switch {
	case stackErr != nil:
		fmt.Fprintf(&b, "the stacks could not be read: %v\n", stackErr)
	default:
		any := false
		for _, st := range list {
			if m := approved(st); m != nil && len(m.Api) > 0 {
				fmt.Fprintf(&b, "%s: api grant %s\n", st.Name, strings.Join(m.Api, ", "))
				any = true
			}
		}
		if !any {
			b.WriteString("no stack has an api grant\n")
		}
	}

	b.WriteString("\n# The assistant (where audit and Ask material is sent)\n")
	if src.Assistant == nil {
		b.WriteString(notVisible + "\n")
	} else if a, err := src.Assistant.Get(ctx); err != nil {
		fmt.Fprintf(&b, "could not be read: %v\n", err)
	} else {
		fmt.Fprintf(&b, "provider %s, address %s, model %s, audit model %s\n", a.Provider, orDash(a.BaseURL), orDash(a.Model), orDash(a.AuditModel))
	}
	notes(&b, h.Notes)
	return b.String()
}

func stacksMaterial(list []stacks.Stack, stackErr error, known bool) string {
	var b strings.Builder
	b.WriteString("# Stacks, as approved by the operator\n")
	switch {
	case !known:
		b.WriteString(notVisible + "\n")
	case stackErr != nil:
		fmt.Fprintf(&b, "the stacks could not be read: %v\n", stackErr)
	case len(list) == 0:
		b.WriteString("no stacks\n")
	}
	for _, st := range list {
		fmt.Fprintf(&b, "\n## %s\n", st.Name)
		fmt.Fprintf(&b, "source: %s", scrubURL(st.Source))
		if st.SourceDirty {
			b.WriteString(" (a local checkout with uncommitted changes)")
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "running: %t\n", st.Running)
		m := approved(st)
		if m == nil {
			b.WriteString("not approved yet\n")
			continue
		}
		fmt.Fprintf(&b, "approved commit %s at %s\n", short(st.Approved.Rev), day(st.Approved.At))
		fmt.Fprintf(&b, "gpu grant (binds /dev/dri): %t\n", m.Gpu)
		fmt.Fprintf(&b, "containers grant (nested containers; allows bpf, add_key, keyctl system calls): %t\n", m.Containers)
		for _, e := range m.Egress {
			fmt.Fprintf(&b, "egress to %s port %d/%s\n", e.To, e.Port, e.Proto)
		}
		if len(m.Egress) == 0 {
			b.WriteString("egress: only what the stack's network gives every stack (internet, not the LAN)\n")
		}
		for _, sto := range m.Storage {
			fmt.Fprintf(&b, "storage %s -> %s, read-only %t\n", sto.HostPath, sto.Mount, sto.ReadOnly)
		}
		if len(m.Api) > 0 {
			fmt.Fprintf(&b, "api grant: %s\n", strings.Join(m.Api, ", "))
		}
		for _, v := range st.Variables {
			if v.Builtin {
				continue
			}
			fmt.Fprintf(&b, "variable %s: %s", v.Name, v.Kind)
			if v.Kind == "path" && v.Mount != "" {
				fmt.Fprintf(&b, " bound at %s", v.Mount)
			}
			if v.InEnv {
				b.WriteString(", also in the environment file")
			}
			if !v.Set {
				b.WriteString(", not set")
			}
			b.WriteString("\n")
		}
		for _, g := range st.NewGrants {
			fmt.Fprintf(&b, "waiting for approval: %s grant, %s\n", g.Kind, g.Detail)
		}
	}
	return b.String()
}

func (s *Service) secretsAndBackups(ctx context.Context) string {
	src := s.sources(ctx)
	var b strings.Builder
	b.WriteString("# Backup destinations (no credentials)\n")
	if src.Backups == nil {
		b.WriteString(notVisible + "\n")
		return b.String()
	}
	if ds, err := src.Backups.Destinations(ctx); err != nil {
		fmt.Fprintf(&b, "could not be read: %v\n", err)
	} else {
		if len(ds) == 0 {
			b.WriteString("none\n")
		}
		for _, d := range ds {
			fmt.Fprintf(&b, "%s: %s", d.Name, d.Kind)
			switch {
			case d.S3 != nil:
				fmt.Fprintf(&b, ", endpoint %s, bucket %s", scrubURL(d.S3.Endpoint), d.S3.Bucket)
			case d.Restic != nil:
				fmt.Fprintf(&b, ", repository %s, credentials in %s", scrubURL(d.Restic.Repository), list0(d.Restic.EnvNames))
			case d.Rclone != nil:
				fmt.Fprintf(&b, ", rclone %s %s:%s", d.Rclone.Type, d.Rclone.Remote, d.Rclone.Path)
			}
			fmt.Fprintf(&b, "; deletes removed files: %t; restic password confirmed saved: %t; used by %s\n",
				d.DeleteRemoved, d.PasswordConfirmed, list0(d.UsedBy))
		}
	}
	b.WriteString("\n# Backups\n")
	if bs, err := src.Backups.List(ctx); err != nil {
		fmt.Fprintf(&b, "could not be read: %v\n", err)
	} else {
		if len(bs) == 0 {
			b.WriteString("none\n")
		}
		for _, bk := range bs {
			fmt.Fprintf(&b, "%s: enabled %t, schedule %s, %d sources, to %d destinations; last success %s; overdue %t",
				bk.Name, bk.Enabled, orDash(bk.Schedule), len(bk.Sources), len(bk.Destinations), dayPtr(bk.LastSuccess), bk.Overdue)
			if r := bk.LastRun; r != nil {
				fmt.Fprintf(&b, "; last run %s %s", day(r.StartedAt), r.Result)
				if r.Error != "" {
					fmt.Fprintf(&b, " (%s)", oneLine(r.Error))
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (s *Service) updates(ctx context.Context, list []stacks.Stack, stackErr error) string {
	src := s.sources(ctx)
	var b strings.Builder
	b.WriteString("# Core (the host image and the core extension)\n")
	if src.Updates == nil {
		b.WriteString(notVisible + "\n")
	} else if u, err := src.Updates.ImageStatus(ctx); err != nil {
		fmt.Fprintf(&b, "could not be read: %v\n", err)
	} else {
		fmt.Fprintf(&b, "image version %s, core version %s\n", orDash(u.Booted), orDash(u.Core.Active))
		switch r := u.Release; {
		case r.Error != "":
			fmt.Fprintf(&b, "the last release check failed: %s\n", r.Error)
		case r.Latest != "":
			fmt.Fprintf(&b, "newest release %s\n", r.Latest)
		}
		if u.Release.CheckedAt != nil {
			fmt.Fprintf(&b, "checked %s\n", day(*u.Release.CheckedAt))
		}
		if staged := append(append([]string{}, u.Available...), u.Core.Available...); len(staged) > 0 {
			fmt.Fprintf(&b, "downloaded and not installed: %s\n", strings.Join(staged, ", "))
		}
		fmt.Fprintf(&b, "a newer image is installed and waits for a reboot: %t\n", u.PendingReboot)
		if u.FellBackFrom != "" {
			fmt.Fprintf(&b, "image %s did not boot; the host is back on the version before\n", u.FellBackFrom)
		}
		fmt.Fprintf(&b, "installs releases by itself: %s\n", orDash(u.AutoInstallAt))
	}

	b.WriteString("\n# Stack updates\n")
	switch {
	case src.Stacks == nil:
		b.WriteString(notVisible + "\n")
	case stackErr != nil:
		fmt.Fprintf(&b, "the stacks could not be read: %v\n", stackErr)
	case len(list) == 0:
		b.WriteString("no stacks\n")
	}
	for _, st := range list {
		fmt.Fprintf(&b, "%s: %s", st.Name, st.UpdateState)
		if len(st.Generations) > 0 {
			fmt.Fprintf(&b, ", running generation built %s", day(st.Generations[0].BuiltAt))
		}
		if n := len(st.Commits); n > 0 {
			fmt.Fprintf(&b, ", %d newer commits, newest %s", n, day(st.Commits[0].At))
		}
		if st.RestartNeeded {
			b.WriteString(", restart needed")
		}
		b.WriteString("\n")
	}

	b.WriteString("\n# Open problems (notifications)\n")
	if src.Problems == nil {
		b.WriteString(notVisible + "\n")
	} else if ps, err := src.Problems.OpenProblems(ctx); err != nil {
		fmt.Fprintf(&b, "could not be read: %v\n", err)
	} else {
		if len(ps) == 0 {
			b.WriteString("none\n")
		}
		for _, p := range ps {
			fmt.Fprintf(&b, "%s: %s, open since %s\n", p.Key, oneLine(p.Title), day(p.OpenedAt))
		}
	}
	return b.String()
}

// sockets lists what listens now: protocol, address and process.
func (s *Service) sockets(ctx context.Context) string {
	if s.src.Runner == nil {
		return notVisible + "\n"
	}
	res, err := s.src.Runner.Run(ctx, cmdrunner.Cmd{Module: "audit", Name: "ss", Args: []string{"-Htlnup"}})
	if err != nil {
		return fmt.Sprintf("ss failed: %v\n", err)
	}
	return parseSS(res.Stdout)
}

// parseSS reads `ss -Htlnup`: one line a socket, the process name without
// its pid. Duplicate lines (a socket per thread or address family) go.
func parseSS(out string) string {
	seen := map[string]bool{}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		line := fmt.Sprintf("%s %s", f[0], f[4])
		if len(f) >= 7 {
			if i := strings.Index(f[6], `(("`); i >= 0 {
				name := f[6][i+3:]
				if j := strings.Index(name, `"`); j >= 0 {
					line += " " + name[:j]
				}
			}
		}
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "none\n"
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// firewallRules are the lines of the firewall's extra commands that open
// something, without NixOS's own boilerplate.
func firewallRules(cmds string) []string {
	var out []string
	for _, l := range strings.Split(cmds, "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "\\"))
		if l == "" || strings.HasPrefix(l, "#") || strings.Contains(l, "2>/dev/null") {
			continue
		}
		if strings.Contains(l, "ACCEPT") || strings.Contains(l, "nixos-fw-accept") || strings.Contains(l, "DNAT") ||
			strings.Contains(l, "MASQUERADE") || strings.Contains(l, "-i '") || strings.Contains(l, "-o ") {
			out = append(out, l)
		}
	}
	return out
}

// knownSecrets are the values to scrub: the stacks' secret variables.
func (s *Service) knownSecrets(ctx context.Context) []string {
	var out []string
	add := func(v string) {
		if v = strings.TrimSpace(v); len(v) >= minSecret {
			out = append(out, v)
		}
	}
	if s.src.Stacks != nil {
		vals, err := s.src.Stacks.SecretValues(ctx)
		if err != nil {
			s.log.Warn("audit: stack secrets could not be read for the scrub", "error", err)
		}
		for _, v := range vals {
			add(v)
		}
	}
	// Longest first, so a value that contains another goes whole.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// scrub replaces every known secret value in s.
func scrub(s string, known []string) string {
	for _, v := range known {
		s = strings.ReplaceAll(s, v, "[secret]")
	}
	return s
}

// scrubURL drops a user and password from a URL.
func scrubURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

func approved(st stacks.Stack) *stacks.StackManifest {
	if st.Approved == nil {
		return nil
	}
	return &st.Approved.Manifest
}

// routeHost is a route's full host name: a bare label is served under the
// private domain.
func routeHost(host, domain string) string {
	if strings.Contains(host, ".") || domain == "" {
		return host
	}
	return host + "." + domain
}

func notes(b *strings.Builder, ns []string) {
	if len(ns) == 0 {
		return
	}
	b.WriteString("\n# The operator's notes about the host\n")
	for _, n := range ns {
		fmt.Fprintf(b, "- %s\n", n)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func ints(v []int) string {
	if len(v) == 0 {
		return "none"
	}
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}

func list0(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	return strings.Join(v, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return orDash(rev)
}

func day(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02")
}

func dayPtr(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return day(*t)
}
