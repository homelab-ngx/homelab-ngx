package stacks

import (
	"slices"
	"testing"
)

func TestParseAddressesWords(t *testing.T) {
	t.Parallel()
	entries, value, err := ParseAddresses("lan, vpn tailnet 192.168.1.7 vpn")
	if err != nil {
		t.Fatal(err)
	}
	if value != "lan,vpn,tailnet,192.168.1.7,vpn" {
		t.Errorf("value = %q", value)
	}
	if !slices.Equal(entries, []string{"lan", "vpn", "tailnet", "192.168.1.7/32"}) {
		t.Errorf("entries = %v", entries)
	}
	for _, bad := range []string{"LAN", "wan", "lan/24"} {
		if _, _, err := ParseAddresses(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestNetsResolve(t *testing.T) {
	t.Parallel()
	on := Nets{LAN: []string{"192.168.1.0/24"}, VPN: "10.10.0.0/24", Tailnet: true}
	if got := on.Resolve("lan, vpn, tailnet, 192.168.1.7"); !slices.Equal(got, []string{"192.168.1.0/24", "10.10.0.0/24", TailnetRange, "192.168.1.7/32"}) {
		t.Errorf("on: %v", got)
	}
	// A word whose network is off stands for nothing.
	off := Nets{LAN: []string{"192.168.1.0/24"}}
	if got := off.Resolve("vpn, tailnet"); len(got) != 0 {
		t.Errorf("off: %v", got)
	}
	if got := off.Resolve("nonsense"); got != nil {
		t.Errorf("bad value: %v", got)
	}
}

func TestConcreteNetWords(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(v8Manifest))
	if err != nil {
		t.Fatal(err)
	}
	vars := []storedVar{{Name: "MQTT_HOST", Value: "vpn,tailnet"}}
	egress, ports := concreteNet(m, vars, Nets{LAN: []string{"192.168.1.0/24"}, VPN: "10.10.0.0/24", Tailnet: true})
	if len(egress) != 2 || egress[0].To != "10.10.0.0/24" || egress[1].To != TailnetRange {
		t.Errorf("egress = %+v", egress)
	}
	if !slices.Equal(ports[0].From, []string{"192.168.1.0/24", "10.10.0.0/24", TailnetRange}) {
		t.Errorf("ports = %+v", ports)
	}
	// With the VPN and Tailscale off, the variable opens nothing.
	egress, _ = concreteNet(m, vars, Nets{LAN: []string{"192.168.1.0/24"}})
	if len(egress) != 0 {
		t.Errorf("egress with both off = %+v", egress)
	}
}

func TestResolveAddressesDelivered(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(v8Manifest))
	if err != nil {
		t.Fatal(err)
	}
	vars := []storedVar{{Name: "MQTT_HOST", Value: "lan,192.168.5.5"}, {Name: "OTHER", Value: "lan"}}
	got := resolveAddresses(m, vars, Nets{LAN: []string{"192.168.1.0/24"}})
	// An address stays as stored, for a stack that reads it as a host.
	if got[0].Value != "192.168.1.0/24,192.168.5.5" {
		t.Errorf("address = %q", got[0].Value)
	}
	// A value that is no address variable is left as it is.
	if got[1].Value != "lan" || vars[0].Value != "lan,192.168.5.5" {
		t.Errorf("other = %q, input changed to %q", got[1].Value, vars[0].Value)
	}
}
