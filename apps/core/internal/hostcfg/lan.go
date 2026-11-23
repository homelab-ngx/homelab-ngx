package hostcfg

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// lanOf is the /24 an IPv4 address is in: the LAN a fresh host assumes
// from its own address. A link-local address gives none.
func lanOf(addr string) (string, bool) {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		if p, perr := netip.ParsePrefix(addr); perr == nil {
			a, err = p.Addr(), nil
		}
	}
	if err != nil || !a.Is4() || a.IsLinkLocalUnicast() || a.IsLoopback() || a.IsUnspecified() {
		return "", false
	}
	return netip.PrefixFrom(a, 24).Masked().String(), true
}

// wiredAddrs are the IPv4 addresses of the host's wired interfaces now.
func wiredAddrs() []string {
	ifs, _ := net.Interfaces()
	var out []string
	for _, i := range wired(ifs) {
		out = append(out, i.Addresses...)
	}
	return out
}

// errNetworkSet: the network setting was written before the LAN could be
// inferred, so it is left as it is.
var errNetworkSet = errors.New("the network setting is already set")

// InferLAN gives a host whose network setting was never written a LAN:
// the /24 of its wired address, so a stack's port grant from lan opens to
// the home network from the start. DHCP may not have given an address yet
// when the daemon starts, so it looks again every few seconds for up to
// five minutes. A network setting saved from the dashboard, the CLI or the
// installer, even with no LAN in it, is never changed.
func (s *Service) InferLAN(ctx context.Context) {
	if s.managed != ManagedImage {
		return
	}
	deadline := s.now().Add(5 * time.Minute)
	for {
		done, err := s.inferLAN(ctx)
		if err != nil {
			s.log.Warn("the LAN was not set from the host's address", "error", err)
		}
		if done || err != nil || !s.now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.inferEvery):
		}
	}
}

// inferLAN is one try of InferLAN; done is false while the host has no
// address to infer from.
func (s *Service) inferLAN(ctx context.Context) (done bool, err error) {
	s.mu.Lock()
	rs, err := s.rows(ctx)
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	if _, ok := rs["network"]; ok {
		return true, nil
	}
	var lan string
	for _, a := range s.addrs() {
		if l, ok := lanOf(a); ok {
			lan = l
			break
		}
	}
	if lan == "" {
		return false, nil
	}
	_, err = s.change(ctx, func(rs map[string]row) error {
		if _, ok := rs["network"]; ok {
			return errNetworkSet
		}
		return s.save(ctx, "network", Network{LAN: []string{lan}, IPv4: IPv4{Mode: "dhcp"}}, nil)
	})
	if errors.Is(err, errNetworkSet) {
		return true, nil
	}
	if err == nil {
		s.log.Info("LAN set from the host's address", "lan", lan)
	}
	return err == nil, err
}
