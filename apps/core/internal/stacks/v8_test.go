package stacks

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// The host's private domain for every test in the package. Tests run in
// parallel, so it is set once and never changed.
func init() { SetPrivateDomain("example.com") }

// v8Manifest is a generic stack: a route label, an address variable its
// egress and a port grant name, and a media folder whose owner and group the
// stack takes.
const v8Manifest = `{
  "version": 8,
  "name": "players",
  "nixpkgs": "nixos-unstable",
  "routes": [{"host": "jellyfin", "port": 8096}],
  "gpu": true,
  "variables": [
    {"name": "MEDIA_DIR", "kind": "path", "default": "/pool/media", "mount": "/pool/media", "required": true,
     "owner": "jellyfin", "group": "media"},
    {"name": "MQTT_HOST", "kind": "address", "required": true, "description": "The broker."},
    {"name": "CAMERAS", "kind": "address"}
  ],
  "egress": [{"to": "$MQTT_HOST", "port": 1883}, {"to": "$CAMERAS", "port": 554}],
  "ports": [{"port": 32400, "from": ["lan", "$MQTT_HOST"]}, {"port": 8555, "proto": "udp", "from": ["lan"]}],
  "health": {"units": ["jellyfin.service"], "http": {"port": 8096, "path": "/health"}}
}`

func TestV8Parses(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(v8Manifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Routes[0].Host != "jellyfin.example.com" {
		t.Errorf("route label resolved to %q", m.Routes[0].Host)
	}
	var details []string
	for _, g := range m.Grants() {
		details = append(details, g.Kind+": "+g.Detail)
	}
	for _, want := range []string{
		"route: jellyfin.example.com",
		"egress: MQTT_HOST (the addresses you give) port 1883/tcp",
		"port: host port 32400/tcp from the host's LAN, MQTT_HOST (the addresses you give) to stack port 32400",
	} {
		if !slices.Contains(details, want) {
			t.Errorf("grants lack %q: %q", want, details)
		}
	}
	// The same references ask for nothing new; another variable does.
	if n := NewGrants(m, m); len(n) != 0 {
		t.Errorf("the same manifest asks for %+v", n)
	}
	next := m
	next.Ports = []Port{{Port: 32400, To: 32400, Proto: "tcp", From: []string{"lan", "$CAMERAS"}}}
	if n := NewGrants(m, next); len(n) != 1 || n[0].Kind != GrantPort {
		t.Errorf("a port for other clients asks for %+v", n)
	}
}

func TestV8Refuses(t *testing.T) {
	t.Parallel()
	base := func(extra string) string {
		return `{"version":8,"name":"a","nixpkgs":"nixos-unstable"` + extra + `}`
	}
	for _, tc := range []struct{ name, manifest, want string }{
		{"address before v8", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"address"}]}`, "needs version 8"},
		{"owner before v8", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"path","mount":"/x","owner":"a"}]}`, "need version 8"},
		{"label before v8", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","routes":[{"host":"photos","port":1}]}`, "hostname"},
		{"undeclared reference", base(`,"egress":[{"to":"$NOPE","port":1}]`), "not an address variable"},
		{"reference to an env variable", base(`,"variables":[{"name":"X","kind":"env"}],"egress":[{"to":"$X","port":1}]`), "not an address variable"},
		{"address with a default", base(`,"variables":[{"name":"X","kind":"address","default":"10.0.0.1"}]`), "no default"},
		{"owner on an env variable", base(`,"variables":[{"name":"X","kind":"env","owner":"a"}]`), "only for a path"},
		{"root owner", base(`,"variables":[{"name":"X","kind":"path","mount":"/x","owner":"root"}]`), "root"},
		{"bad group", base(`,"variables":[{"name":"X","kind":"path","mount":"/x","group":"Media"}]`), "not a group name"},
		{"builtin name", base(`,"variables":[{"name":"HOMELAB_DOMAIN","kind":"env"}]`), "filled in by the host"},
		{"lan as egress", base(`,"egress":[{"to":"lan","port":1}]`), "CIDR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.manifest))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseAddresses(t *testing.T) {
	t.Parallel()
	ps, value, err := ParseAddresses(" 192.168.1.102, 192.168.10.0/24 10.0.0.7/32 ")
	if err != nil {
		t.Fatal(err)
	}
	if value != "192.168.1.102,192.168.10.0/24,10.0.0.7/32" || len(ps) != 3 || ps[0].String() != "192.168.1.102/32" {
		t.Errorf("value=%q prefixes=%v", value, ps)
	}
	for _, bad := range []string{"", " , ", "0.0.0.0/0", "fd00::1", "mqtt.lan", "192.168.1.1:1883"} {
		if _, _, err := ParseAddresses(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestConcreteNet(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(v8Manifest))
	if err != nil {
		t.Fatal(err)
	}
	vars := []storedVar{{Name: "MQTT_HOST", Value: "192.168.1.102"}}
	egress, ports := concreteNet(m, vars, []string{"192.168.1.0/24"})
	// CAMERAS is unset: its egress opens nothing.
	if len(egress) != 1 || egress[0] != (Egress{To: "192.168.1.102/32", Port: 1883, Proto: "tcp"}) {
		t.Errorf("egress = %+v", egress)
	}
	if len(ports) != 2 || !slices.Equal(ports[0].From, []string{"192.168.1.0/24", "192.168.1.102/32"}) {
		t.Errorf("ports = %+v", ports)
	}
	// Without a LAN set, a port only for the LAN is dropped.
	if _, ports := concreteNet(m, vars, nil); len(ports) != 1 || ports[0].Port != 32400 {
		t.Errorf("ports without a LAN = %+v", ports)
	}
	rules, err := nftRuleset("players", "10.99.3.0/24", egress, ports, false)
	if err != nil || !strings.Contains(rules, "ip daddr 192.168.1.102/32 tcp dport 1883 accept") {
		t.Errorf("rules = %s, %v", rules, err)
	}
}

func TestBuildHostModule(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(v8Manifest))
	if err != nil {
		t.Fatal(err)
	}
	stat := func(string) (uint32, uint32, error) { return 990, 169, nil }
	gpu := GPU{Packages: []string{"intel-media-driver"}, LibvaDriver: "iHD"}
	h, ids, err := BuildHostModule(m, map[string]string{"MEDIA_DIR": "/pool/media"}, stat, gpu)
	if err != nil {
		t.Fatal(err)
	}
	if ids != "user jellyfin=990, group media=169" {
		t.Errorf("ids = %q", ids)
	}
	if !slices.Equal(h.TCP, []int{8096, 32400}) || !slices.Equal(h.UDP, []int{8555}) {
		t.Errorf("tcp=%v udp=%v", h.TCP, h.UDP)
	}
	files, err := Wrapper{Name: "players", Ref: "github:o/r/a", NixpkgsRev: strings.Repeat("d", 40), Host: h}.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`users.users."jellyfin".uid = lib.mkForce 990;`,
		`users.groups."media".gid = lib.mkForce 169;`,
		`networking.firewall.allowedTCPPorts = [ 8096 32400 ];`,
		`networking.firewall.allowedUDPPorts = [ 8555 ];`,
		`hardware.graphics.extraPackages = map (n: pkgs.${n}) [ "intel-media-driver" ];`,
		`environment.sessionVariables.LIBVA_DRIVER_NAME = "iHD";`,
		`nixpkgs.legacyPackages.x86_64-linux.${n}`,
	} {
		if !strings.Contains(files["flake.nix"], want) {
			t.Errorf("wrapper lacks %s:\n%s", want, files["flake.nix"])
		}
	}

	// A root-owned folder, and one owner taking two ids, are refused.
	if _, _, err := BuildHostModule(m, map[string]string{"MEDIA_DIR": "/pool/media"},
		func(string) (uint32, uint32, error) { return 0, 169, nil }, gpu); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("root-owned: %v", err)
	}
	two := m
	two.Variables = append(slices.Clone(m.Variables), Variable{Name: "MUSIC_DIR", Kind: VarPath, Mount: "/music", Group: "media"})
	gids := map[string]uint32{"/pool/media": 169, "/music": 170}
	if _, _, err := BuildHostModule(two, map[string]string{"MEDIA_DIR": "/pool/media", "MUSIC_DIR": "/music"},
		func(p string) (uint32, uint32, error) { return 990, gids[p], nil }, gpu); err == nil || !strings.Contains(err.Error(), "two ids") {
		t.Errorf("two gids: %v", err)
	}
	// Below version 8 nothing is added, so the wrapper stays as it was.
	if h, _, _ := BuildHostModule(Manifest{Version: 7}, nil, stat, gpu); h != nil {
		t.Errorf("a version 7 stack got %+v", h)
	}
}

// newV8Runtime is a service with the generic stack approved, its folder
// owned by owner (a pointer the test changes).
func newV8Runtime(t *testing.T) (*Service, *fakeHost, *sync.Mutex, *[2]uint32, *string) {
	t.Helper()
	svc, fs, h := newServiceOn(t)
	var mu sync.Mutex
	rules := new(string)
	h.On("systemctl", "--machine=players", "is-active").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.running {
			return fail(3, "failed\n")
		}
		return ok("active\n")
	})
	h.On("nft", "-f").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		b, _ := io.ReadAll(c.Stdin)
		mu.Lock()
		*rules = string(b)
		mu.Unlock()
		return ok("")
	})
	owner := &[2]uint32{990, 169}
	svc.statOwner = func(string) (uint32, uint32, error) {
		mu.Lock()
		defer mu.Unlock()
		return owner[0], owner[1], nil
	}
	svc.host.DashboardURL = "https://homelab.example.com"
	svc.host.LAN = []string{"192.168.1.0/24"}
	fs.set("aaaaaaa1", v8Manifest)
	ctx := context.Background()
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetVariable(ctx, "players", "MQTT_HOST", StackVariableSet{Value: "192.168.1.102 "}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "players", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	return svc, h, &mu, owner, rules
}

func TestV8StackGetsTheHostsValues(t *testing.T) {
	t.Parallel()
	svc, _, mu, _, loaded := newV8Runtime(t)
	ctx := context.Background()
	s := finishedOn(t, svc, "players", svc.Start)
	if !s.Running || s.Generations[0].NixpkgsRev == "" {
		t.Fatalf("stack = %+v", s)
	}

	env, err := os.ReadFile(filepath.Join(svc.host.VarsDir, "players", varsEnvName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MQTT_HOST=192.168.1.102\n", "HOMELAB_DOMAIN=example.com\n", "HOMELAB_DASHBOARD_URL=https://homelab.example.com\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env file lacks %q:\n%s", want, env)
		}
	}
	var builtins []string
	for _, v := range s.Variables {
		if v.Builtin {
			builtins = append(builtins, v.Name)
		}
		if v.Name == "MEDIA_DIR" && (v.Owner != "jellyfin" || v.Group != "media") {
			t.Errorf("MEDIA_DIR = %+v", v)
		}
	}
	if !slices.Equal(builtins, []string{BuiltinDomain, BuiltinDashboardURL}) {
		t.Errorf("builtins = %v", builtins)
	}
	mu.Lock()
	rules := *loaded
	mu.Unlock()
	if !strings.Contains(rules, "ip daddr 192.168.1.102/32 tcp dport 1883 accept") ||
		!strings.Contains(rules, "ip saddr { 192.168.1.0/24, 192.168.1.102/32 }") {
		t.Errorf("the firewall was not loaded with the stack's addresses:\n%s", rules)
	}
	flake, _ := os.ReadFile(filepath.Join(svc.host.BuildsDir, "players", "flake.nix"))
	if !strings.Contains(string(flake), `users.groups."media".gid = lib.mkForce 169;`) {
		t.Errorf("wrapper:\n%s", flake)
	}

	_, err = svc.SetVariable(ctx, "players", "MQTT_HOST", StackVariableSet{Value: "mqtt.lan"})
	wantCode(t, err, 422, "invalid_address")
	_, err = svc.SetVariable(ctx, "players", BuiltinDomain, StackVariableSet{Value: "x.y"})
	wantCode(t, err, 409, "builtin_variable")
}

func TestAChangedOwnerIsAnUpdate(t *testing.T) {
	t.Parallel()
	svc, h, mu, owner, _ := newV8Runtime(t)
	ctx := context.Background()
	finishedOn(t, svc, "players", svc.Start)

	mu.Lock()
	owner[1] = 170
	mu.Unlock()
	if _, err := svc.CheckUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := svc.Get(ctx, "players")
	if s.UpdateState != CommitsAvailable {
		t.Fatalf("state = %s", s.UpdateState)
	}
	h.mu.Lock()
	note := h.problems[updateScope+"players"]
	h.mu.Unlock()
	if !strings.Contains(note, "owners of its folders changed") {
		t.Errorf("notification = %q", note)
	}
	s = finishedOn(t, svc, "players", svc.Update)
	flake, _ := os.ReadFile(filepath.Join(svc.host.BuildsDir, "players", "flake.nix"))
	if s.UpdateState != UpToDate || !strings.Contains(string(flake), `users.groups."media".gid = lib.mkForce 170;`) {
		t.Errorf("after the update: state=%s\n%s", s.UpdateState, flake)
	}
}
