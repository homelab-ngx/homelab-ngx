package hostcfg

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ── WireGuard devices ────────────────────────────────────────────────────

// DeviceAdd names a device the host makes keys for.
type DeviceAdd struct {
	Name string `json:"name" minLength:"1" maxLength:"64" doc:"The device's name, such as phone."`
}

// AddedDevice is a device whose keys the host made.
type AddedDevice struct {
	Settings HostSettings `json:"settings"`
	Config   string       `json:"config" doc:"The device's WireGuard config, with its private key. Shown this once: the host keeps only the public key."`
}

// AddWireGuardDevice makes a key pair for a new device, gives it the next
// free address in the VPN and adds it as a peer. Only the answer holds the
// device's private key, in its config.
func (s *Service) AddWireGuardDevice(ctx context.Context, in DeviceAdd) (AddedDevice, error) {
	if s.managed == ManagedNixOS {
		return AddedDevice{}, ErrManaged
	}
	cur, err := s.Get(ctx)
	if err != nil {
		return AddedDevice{}, err
	}
	w := cur.WireGuard
	if !w.Enabled || w.PublicKey == "" {
		return AddedDevice{}, invalid("the VPN is off: turn it on and apply first")
	}
	endpoint := Endpoint(cur)
	if endpoint == "" {
		return AddedDevice{}, invalid("endpoint: say how devices reach the host from outside first, such as vpn.example.com:%d", wgPort(w))
	}
	addr, err := nextFreeAddress(w)
	if err != nil {
		return AddedDevice{}, err
	}
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return AddedDevice{}, err
	}
	peer := Peer{Name: strings.TrimSpace(in.Name), PublicKey: base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), Address: addr.String() + "/32"}
	w.Peers = append(w.Peers, peer)
	st, err := s.SetWireGuard(ctx, w)
	if err != nil {
		return AddedDevice{}, err
	}
	return AddedDevice{Settings: st, Config: DeviceConfig(st.WireGuard, endpoint, peer, base64.StdEncoding.EncodeToString(k.Bytes()))}, nil
}

// Endpoint is how devices reach the host's VPN: the one set, else the
// dynamic DNS name with the VPN's port.
func Endpoint(st HostSettings) string {
	if st.WireGuard.Endpoint != "" {
		return st.WireGuard.Endpoint
	}
	if n := DDNSName(st); n != "" && st.DDNS.Enabled {
		return fmt.Sprintf("%s:%d", n, wgPort(st.WireGuard))
	}
	return ""
}

// nextFreeAddress is the lowest address in the VPN's subnet that is not the
// host's, a peer's, the network's or its broadcast address.
func nextFreeAddress(w WireGuard) (netip.Addr, error) {
	p, err := netip.ParsePrefix(w.Address)
	if err != nil {
		return netip.Addr{}, invalid("address: the host's address and the VPN's subnet, such as 10.10.0.1/24")
	}
	used := map[netip.Addr]bool{p.Addr(): true}
	for _, peer := range w.Peers {
		if a, err := netip.ParsePrefix(peer.Address); err == nil {
			used[a.Addr()] = true
		}
	}
	net := p.Masked()
	for a := net.Addr().Next(); net.Contains(a.Next()); a = a.Next() {
		if !used[a] {
			return a, nil
		}
	}
	return netip.Addr{}, invalid("the VPN's subnet %s has no free address left", net)
}

// DeviceConfig is a device's wg-quick config: its key and address, and the
// host as its one peer, reached at endpoint, for the VPN's subnet.
func DeviceConfig(w WireGuard, endpoint string, peer Peer, privateKey string) string {
	net := w.Address
	if p, err := netip.ParsePrefix(w.Address); err == nil {
		net = p.Masked().String()
	}
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = 25\n",
		privateKey, peer.Address, w.PublicKey, endpoint, net)
}

// ── the home's public address ────────────────────────────────────────────

// traceURL answers with the address a request came from (ip=), on IPv4.
const traceURL = "https://1.1.1.1/cdn-cgi/trace"

// PublicAddress is the home's address on the internet, as Cloudflare sees it.
type PublicAddress struct {
	IP string `json:"ip" doc:"The home's public IPv4 address. Behind carrier-grade NAT it is the carrier's, and a port forwarded at home is not reached through it."`
}

// PublicIP asks url (a Cloudflare trace) for the address the request came from.
func PublicIP(ctx context.Context, c *http.Client, url string) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	res, err := c.Do(req)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("asking %s for the public address: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("asking %s for the public address: %s", url, res.Status)
	}
	return parseTrace(io.LimitReader(res.Body, 64<<10))
}

func parseTrace(r io.Reader) (netip.Addr, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "ip="); ok {
			return netip.ParseAddr(strings.TrimSpace(v))
		}
	}
	return netip.Addr{}, fmt.Errorf("the trace has no ip= line")
}
