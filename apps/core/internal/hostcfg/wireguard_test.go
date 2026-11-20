package hostcfg

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestNextFreeAddress(t *testing.T) {
	t.Parallel()
	peers := func(addrs ...string) []Peer {
		var ps []Peer
		for _, a := range addrs {
			ps = append(ps, Peer{Address: a})
		}
		return ps
	}
	for _, c := range []struct {
		w    WireGuard
		want string
	}{
		{WireGuard{Address: "10.10.0.1/24"}, "10.10.0.2"},
		{WireGuard{Address: "10.10.0.1/24", Peers: peers("10.10.0.2/32", "10.10.0.4/32")}, "10.10.0.3"},
		{WireGuard{Address: "10.10.0.5/24", Peers: peers("10.10.0.1/32")}, "10.10.0.2"},
		// A /30 has two usable addresses: the host's and one device's.
		{WireGuard{Address: "10.10.0.1/30"}, "10.10.0.2"},
		{WireGuard{Address: "10.10.0.1/30", Peers: peers("10.10.0.2/32")}, ""},
	} {
		got, err := nextFreeAddress(c.w)
		if c.want == "" {
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%v: got %v, want a full subnet", c.w, got)
			}
			continue
		}
		if err != nil || got != netip.MustParseAddr(c.want) {
			t.Errorf("%v: got %v %v, want %s", c.w, got, err, c.want)
		}
	}
}

func TestParseTrace(t *testing.T) {
	t.Parallel()
	ip, err := parseTrace(strings.NewReader("fl=29f1\nh=1.1.1.1\nip=203.0.113.7\nts=1795121561.1\nvisit_scheme=https\n"))
	if err != nil || ip.String() != "203.0.113.7" {
		t.Fatalf("ip = %v, %v", ip, err)
	}
	if _, err := parseTrace(strings.NewReader("h=1.1.1.1\n")); err == nil {
		t.Error("a trace without ip= parsed")
	}
}

// Adding a device makes its keys, keeps only the public one, and answers
// with a config that holds the private one.
func TestAddWireGuardDevice(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	if _, err := s.AddWireGuardDevice(ctx, DeviceAdd{Name: "phone"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("added a device while the VPN is off: %v", err)
	}
	if _, err := s.SetWireGuard(ctx, WireGuard{Enabled: true, Address: "10.10.0.1/24"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWireGuardDevice(ctx, DeviceAdd{Name: "phone"}); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("added a device without an endpoint: %v", err)
	}
	if _, err := s.SetWireGuard(ctx, WireGuard{Enabled: true, Address: "10.10.0.1/24", Endpoint: "vpn.example.com:51820"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.AddWireGuardDevice(ctx, DeviceAdd{Name: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	ps := d.Settings.WireGuard.Peers
	if len(ps) != 1 || ps[0].Name != "phone" || ps[0].Address != "10.10.0.2/32" || !wgKeyRE.MatchString(ps[0].PublicKey) {
		t.Fatalf("peers = %+v", ps)
	}
	var private string
	for _, l := range strings.Split(d.Config, "\n") {
		if v, ok := strings.CutPrefix(l, "PrivateKey = "); ok {
			private = v
		}
	}
	if publicKey(private) != ps[0].PublicKey {
		t.Errorf("the config's private key is not the peer's:\n%s", d.Config)
	}
	for _, want := range []string{"Address = 10.10.0.2/32", "PublicKey = " + d.Settings.WireGuard.PublicKey, "Endpoint = vpn.example.com:51820", "AllowedIPs = 10.10.0.0/24"} {
		if !strings.Contains(d.Config, want) {
			t.Errorf("config lacks %q:\n%s", want, d.Config)
		}
	}
	if _, err := s.AddWireGuardDevice(ctx, DeviceAdd{Name: "phone"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a second device of the same name: %v", err)
	}
	d2, err := s.AddWireGuardDevice(ctx, DeviceAdd{Name: "laptop"})
	if err != nil || d2.Settings.WireGuard.Peers[1].Address != "10.10.0.3/32" {
		t.Errorf("second device: %+v %v", d2.Settings.WireGuard.Peers, err)
	}
}
