package stacks

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"
)

// The words an address variable's value may hold next to addresses and
// CIDRs. Each stands for one of the host's networks (Nets) and is resolved
// again when the host's networks change: a running stack's firewall at
// once, and the values its services read at its next restart.
const (
	WordLAN     = FromLAN
	WordVPN     = "vpn"
	WordTailnet = "tailnet"
)

// TailnetRange is the range Tailscale gives its devices' IPv4 addresses.
const TailnetRange = "100.64.0.0/10"

// Nets are the host's networks the address words stand for.
type Nets struct {
	// LAN are the host's LAN subnets.
	LAN []string
	// VPN is the WireGuard VPN's subnet, "" while the VPN is off.
	VPN string
	// Tailnet is whether the host is on a tailnet.
	Tailnet bool
}

// Word is the CIDRs the address word w stands for on this host, which is
// none while that network is off; ok is false when w is no word.
func (n Nets) Word(w string) (cidrs []string, ok bool) {
	switch w {
	case WordLAN:
		return slices.Clone(n.LAN), true
	case WordVPN:
		if n.VPN == "" {
			return nil, true
		}
		return []string{n.VPN}, true
	case WordTailnet:
		if !n.Tailnet {
			return nil, true
		}
		return []string{TailnetRange}, true
	}
	return nil, false
}

// why says why the word w stands for no network on this host: the setting
// that would give it one.
func (n Nets) why(w string) string {
	switch w {
	case WordLAN:
		return "the host has no LAN subnet (Settings › Network)"
	case WordVPN:
		return "the VPN is off"
	case WordTailnet:
		return "the host is not on a tailnet"
	}
	return ""
}

// ParseAddresses reads an address variable's value: IPv4 addresses, CIDRs
// and the words lan, vpn and tailnet, separated by commas or spaces. It
// returns the entries, an address as its /32 CIDR and a word as written,
// and the value as stored: the entries joined by commas.
func ParseAddresses(value string) ([]string, string, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(fields) == 0 {
		return nil, "", errors.New("give at least one IPv4 address, CIDR, lan, vpn or tailnet")
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, ok := (Nets{}).Word(f); ok {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
			continue
		}
		var p netip.Prefix
		if a, err := netip.ParseAddr(f); err == nil {
			p = netip.PrefixFrom(a, a.BitLen())
		} else if p, err = netip.ParsePrefix(f); err != nil {
			return nil, "", fmt.Errorf("%q is not an IPv4 address, a CIDR, lan, vpn or tailnet", f)
		}
		if !p.Addr().Is4() {
			return nil, "", fmt.Errorf("%q is not IPv4", f)
		}
		if p.Bits() == 0 {
			return nil, "", fmt.Errorf("%q covers every address", f)
		}
		out = append(out, p.Masked().String())
	}
	return out, strings.Join(fields, ","), nil
}

// Resolve is the CIDRs of an address variable's value on this host: its
// addresses and CIDRs, and what its words stand for. nil when the value
// does not parse.
func (n Nets) Resolve(value string) []string {
	entries, _, err := ParseAddresses(value)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		cidrs, ok := n.Word(e)
		if !ok {
			cidrs = []string{e}
		}
		for _, c := range cidrs {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}
