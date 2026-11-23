package stacks

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// v10Manifest has a single address variable, the one device the stack
// talks to, and a list one for the clients it lets in.
func v10Manifest(version int, single bool) string {
	s := ""
	if single {
		s = `, "single": true`
	}
	return `{
  "version": ` + map[int]string{9: "9", 10: "10"}[version] + `,
  "name": "relay",
  "nixpkgs": "nixos-unstable",
  "variables": [
    {"name": "DEVICE", "kind": "address", "required": true` + s + `},
    {"name": "CLIENTS", "kind": "address"}
  ],
  "egress": [{"to": "$DEVICE", "port": 8788}],
  "ports": [{"port": 1883, "from": ["$CLIENTS"]}]
}`
}

func TestV10Refuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, manifest, want string }{
		{"single before v10", v10Manifest(9, true), "needs version 10"},
		{"single on an env variable", `{"version":10,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"env","single":true}]}`, "only for an address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.manifest))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	m, err := ParseManifest([]byte(v10Manifest(10, true)))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := m.Variable("DEVICE"); !d.Single {
		t.Errorf("DEVICE = %+v", d)
	}
}

func TestParseSingleAddress(t *testing.T) {
	t.Parallel()
	if v, err := ParseSingleAddress(" 192.168.1.20 "); err != nil || v != "192.168.1.20" {
		t.Errorf("got %q, %v", v, err)
	}
	for _, bad := range []string{"", "lan", "192.168.1.0/24", "192.168.1.20/32", "192.168.1.20,10.0.0.1", "lan,192.168.1.102", "fd00::1", "host.lan"} {
		if _, err := ParseSingleAddress(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestSingleAddressVariable(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	svc.host.LAN = []string{"192.168.1.0/24"}
	fs.set("aaaaaaa1", v10Manifest(10, true))
	ctx := context.Background()
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	_, err := svc.SetVariable(ctx, "relay", "DEVICE", StackVariableSet{Value: "lan,192.168.1.102"})
	wantCode(t, err, 422, "invalid_address")
	if _, err := svc.SetVariable(ctx, "relay", "DEVICE", StackVariableSet{Value: " 192.168.1.102"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVariable(ctx, "relay", "CLIENTS", StackVariableSet{Value: "lan,10.0.0.7"}); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Get(ctx, "relay")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range s.Variables {
		switch v.Name {
		case "DEVICE":
			if !v.Single || v.Networks != nil || v.Value != "192.168.1.102" {
				t.Errorf("DEVICE = %+v", v)
			}
		case "CLIENTS":
			n := v.Networks
			if v.Single || n == nil || !slices.Equal(n.Lan, []string{"192.168.1.0/24"}) || n.Vpn == nil || len(n.Vpn) != 0 || len(n.Tailnet) != 0 {
				t.Errorf("CLIENTS = %+v networks %+v", v, n)
			}
		}
	}
}

// A commit that makes a variable single while it holds a list waits for a
// new value: an update is refused at once, naming the variable.
func TestASingleVariableHoldingAListBlocks(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	fs.set("aaaaaaa1", v10Manifest(9, false))
	ctx := context.Background()
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVariable(ctx, "relay", "DEVICE", StackVariableSet{Value: "lan,192.168.1.102"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "relay", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	fs.set("bbbbbbb2", v10Manifest(10, true))
	if _, err := svc.Fetch(ctx, "relay"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "relay", "bbbbbbb2"); err != nil {
		t.Fatal(err)
	}
	s, _ := svc.Get(ctx, "relay")
	if s.VariablesReady {
		t.Error("variables ready with a list in a single variable")
	}
	_, err := svc.Start(ctx, "relay")
	wantCode(t, err, 409, "invalid_address")
	if err == nil || !strings.Contains(err.Error(), "DEVICE takes one IPv4 address") {
		t.Errorf("err = %v", err)
	}
	if _, err := svc.SetVariable(ctx, "relay", "DEVICE", StackVariableSet{Value: "192.168.1.102"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := svc.Get(ctx, "relay"); !s.VariablesReady {
		t.Error("variables not ready after a single address")
	}
}
