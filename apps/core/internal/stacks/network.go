package stacks

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// Every stack gets one /24 out of 10.99.0.0/16: the host is .1 on the veth
// `ve-<stack>`, the stack is .2 on `host0`.
const subnetCount = 256

func subnetAt(i int) string { return fmt.Sprintf("10.99.%d.0/24", i) }

// allocSubnet returns the lowest subnet no stack has.
func allocSubnet(used []string) (string, error) {
	taken := map[string]bool{}
	for _, u := range used {
		taken[u] = true
	}
	for i := 0; i < subnetCount; i++ {
		if s := subnetAt(i); !taken[s] {
			return s, nil
		}
	}
	return "", errors.New("every stack subnet is in use")
}

// addrs returns the host's and the stack's address in subnet.
func addrs(subnet string) (host, stack netip.Addr, err error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil || !p.Addr().Is4() || p.Bits() != 24 {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("stored subnet %q is not an IPv4 /24", subnet)
	}
	host = p.Masked().Addr().Next()
	return host, host.Next(), nil
}

// stackAddr is the stack's address, or "" before a subnet is allocated.
func stackAddr(subnet string) string {
	if _, a, err := addrs(subnet); err == nil {
		return a.String()
	}
	return ""
}

func hostIface(name string) string { return "ve-" + name }

// A stack has no say over its address: this file is bound into the stack
// where its systemd-networkd reads it ahead of the built-in host0 default.
// The resolvers are public ones because the stack cannot reach the LAN's.
func networkFile(subnet string) (string, error) {
	host, stack, err := addrs(subnet)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`[Match]
Name=host0

[Network]
Address=%s/24
Gateway=%s
DNS=1.1.1.1
DNS=9.9.9.9
LinkLocalAddressing=no
IPv6AcceptRA=no
`, stack, host), nil
}

// blockedNets are where a stack may not go without an egress grant: private
// ranges (the LAN, the VPN peers, the other stacks), the Tailscale range and
// link-local.
var blockedNets = []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"}

func nftTable(name string) string { return "homelab_stack_" + strings.ReplaceAll(name, "-", "_") }

// nftRuleset is the whole of a stack's firewall as one nft script, replacing
// any earlier version in a single transaction. A stack reaches the internet
// and Caddy on the host's port 443; everything else on the host, the private
// ranges and IPv6 are refused unless an egress grant opens them. Nothing but
// the host opens a connection into the stack, except the clients a port
// grant names, whose connections to that port on any of the host's own
// addresses are forwarded to the stack with their source kept.
//
// With api set (an approved `api` grant), the stack may also reach the
// stack listener on APIPort, where its token decides the rest.
//
// The host's own firewall trusts `ve-*` interfaces, so these rules are the
// only filter between a stack and the host. They are loaded before the
// interface gets its address.
func nftRuleset(name, subnet string, egress []Egress, ports []Port, api bool) (string, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		return "", fmt.Errorf("stored subnet %q: %w", subnet, err)
	}
	_, stack, err := addrs(subnet)
	if err != nil {
		return "", err
	}
	table, ifc := nftTable(name), hostIface(name)
	var toHost, toNet []string
	for _, e := range egress {
		port := fmt.Sprintf("%s dport %d accept", e.Proto, e.Port)
		if e.To == EgressHost {
			toHost = append(toHost, port)
			continue
		}
		c, err := netip.ParsePrefix(e.To)
		if err != nil {
			return "", fmt.Errorf("egress grant %q: %w", e.To, err)
		}
		fam := "ip"
		if c.Addr().Is6() {
			fam = "ip6"
		}
		// A granted range may hold one of the host's own addresses, which
		// arrives on the input hook rather than the forward hook.
		rule := fmt.Sprintf("%s daddr %s %s", fam, c, port)
		toHost = append(toHost, rule)
		toNet = append(toNet, rule)
	}

	var b strings.Builder
	line := func(depth int, format string, a ...any) {
		b.WriteString(strings.Repeat("\t", depth))
		fmt.Fprintf(&b, format, a...)
		b.WriteByte('\n')
	}
	// Declaring the table first makes the delete succeed on the first load.
	line(0, "table inet %s", table)
	line(0, "delete table inet %s", table)
	line(0, "table inet %s {", table)
	line(1, "chain input {")
	line(2, "type filter hook input priority filter - 10; policy accept;")
	line(2, "iifname != %q return", ifc)
	line(2, "ct state established,related accept")
	line(2, "tcp dport 443 accept")
	if api {
		line(2, "tcp dport %d accept", APIPort)
	}
	for _, r := range toHost {
		line(2, "%s", r)
	}
	line(2, "reject with icmpx admin-prohibited")
	line(1, "}")
	line(1, "chain forward {")
	line(2, "type filter hook forward priority filter - 10; policy accept;")
	line(2, "oifname %q ct state established,related accept", ifc)
	for _, f := range ports {
		line(2, "oifname %q ip saddr { %s } ip daddr %s %s dport %d ct status dnat accept",
			ifc, strings.Join(f.From, ", "), stack, f.Proto, f.To)
	}
	line(2, "oifname %q drop", ifc)
	line(2, "iifname != %q return", ifc)
	line(2, "ct state established,related accept")
	for _, r := range toNet {
		line(2, "%s", r)
	}
	line(2, "ip daddr { %s } reject with icmpx admin-prohibited", strings.Join(blockedNets, ", "))
	line(2, "meta nfproto ipv6 reject with icmpx admin-prohibited")
	line(2, "accept")
	line(1, "}")
	if len(ports) > 0 {
		line(1, "chain prerouting {")
		line(2, "type nat hook prerouting priority dstnat; policy accept;")
		for _, f := range ports {
			line(2, "ip saddr { %s } fib daddr type local %s dport %d dnat ip to %s:%d",
				strings.Join(f.From, ", "), f.Proto, f.Port, stack, f.To)
		}
		line(1, "}")
	}
	line(1, "chain postrouting {")
	line(2, "type nat hook postrouting priority srcnat; policy accept;")
	line(2, "ip saddr %s oifname != %q masquerade", p.Masked(), ifc)
	line(1, "}")
	line(0, "}")
	return b.String(), nil
}

// applyRules loads the stack's firewall for generation manifest m. The
// stack listener is open only while both m and the approved manifest grant
// an API scope.
func (s *Service) applyRules(ctx context.Context, name, subnet string, m Manifest) error {
	api := len(m.API) > 0
	if api {
		r, err := s.row(ctx, name)
		if err != nil {
			return err
		}
		approved, ok, err := approvedManifest(r)
		api = err == nil && ok && len(approved.API) > 0
	}
	egress, ports := m.Egress, m.Ports
	if m.Version >= 8 {
		s.varsMu.Lock()
		vars, err := s.loadVars(name)
		s.varsMu.Unlock()
		if err != nil {
			return err
		}
		egress, ports = concreteNet(m, vars, s.hostLAN())
	}
	rules, err := nftRuleset(name, subnet, egress, ports, api)
	if err != nil {
		return err
	}
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "nft", Args: []string{"-f", "-"}, Stdin: strings.NewReader(rules),
	}); err != nil {
		return fmt.Errorf("loading the firewall rules: %w", err)
	}
	return nil
}

// concreteNet is m's egress and port grants with every address variable
// ($NAME) replaced by the addresses stored for it and "lan" by the host's LAN
// subnets. A grant whose variable has no valid value opens nothing, and a
// port grant left with no clients is dropped.
func concreteNet(m Manifest, vars []storedVar, lan []string) ([]Egress, []Port) {
	addrs := func(ref string) []string {
		if ref == FromLAN {
			return lan
		}
		n, ok := varRef(ref)
		if !ok {
			return []string{ref}
		}
		d, ok := m.Variable(n)
		if !ok || d.Kind != VarAddress {
			return nil
		}
		value, src := resolve(d, vars)
		if src == SourceUnset {
			return nil
		}
		ps, _, err := ParseAddresses(value)
		if err != nil {
			return nil
		}
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.String()
		}
		return out
	}
	egress := []Egress{}
	for _, e := range m.Egress {
		if e.To == EgressHost {
			egress = append(egress, e)
			continue
		}
		for _, a := range addrs(e.To) {
			c := Egress{To: a, Port: e.Port, Proto: e.Proto}
			if !slices.Contains(egress, c) {
				egress = append(egress, c)
			}
		}
	}
	ports := []Port{}
	for _, p := range m.Ports {
		var from []string
		for _, c := range p.From {
			for _, a := range addrs(c) {
				if !slices.Contains(from, a) {
					from = append(from, a)
				}
			}
		}
		if len(from) > 0 {
			p.From = from
			ports = append(ports, p)
		}
	}
	return egress, ports
}

// linkUp gives the host side of the stack's veth its address. nspawn creates
// the interface and nothing on the host configures it.
func (s *Service) linkUp(ctx context.Context, name, subnet string) error {
	host, _, err := addrs(subnet)
	if err != nil {
		return err
	}
	ifc := hostIface(name)
	for _, args := range [][]string{
		{"address", "replace", host.String() + "/24", "dev", ifc},
		{"link", "set", ifc, "up"},
	} {
		if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "ip", Args: args}); err != nil {
			return fmt.Errorf("configuring %s: %w", ifc, err)
		}
	}
	return nil
}

// removeRules deletes the stack's firewall; `destroy` accepts a table that
// is not loaded.
func (s *Service) removeRules(ctx context.Context, name string) error {
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "nft", Args: []string{"destroy", "table", "inet", nftTable(name)},
	}); err != nil {
		return fmt.Errorf("removing the firewall rules: %w", err)
	}
	return nil
}
