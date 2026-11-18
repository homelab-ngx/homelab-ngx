package stacks

import (
	"reflect"
	"strings"
	"testing"
)

const immichManifest = `{
  "version": 7,
  "name": "immich",
  "nixpkgs": "nixos-unstable",
  "routes": [{"host": "immich.example.com", "port": 2283, "max_body": "50GB"}],
  "variables": [{"name": "PHOTOS", "kind": "path", "description": "Photo library", "required": true, "default": "/pool/immich", "mount": "/pool/immich"}],
  "gpu": true,
  "egress": [{"to": "host", "port": 25}],
  "health": {"units": ["immich-server.service"], "http": {"port": 2283, "path": "/api/server/ping"}}
}`

// v1Manifest is immich at version 1, which still parses (stored
// generations carry it) but is no longer built.
const v1Manifest = `{
  "version": 1,
  "name": "immich",
  "routes": [{"host": "immich.example.com", "port": 2283, "max_body": "50GB"}],
  "storage": [{"host_path": "/pool/immich", "mount": "/pool/immich", "read_only": false}],
  "gpu": true,
  "egress": [{"to": "host", "port": 25}],
  "health": {"units": ["immich-server.service"], "http": {"port": 2283, "path": "/api/server/ping"}}
}`

func mustParse(t *testing.T, s string) Manifest {
	t.Helper()
	m, err := ParseManifest([]byte(s))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return m
}

func TestParseManifestAcceptsAFullManifestAndFillsDefaults(t *testing.T) {
	t.Parallel()
	m := mustParse(t, v1Manifest)
	if m.Egress[0].Proto != "tcp" {
		t.Errorf("proto default = %q, want tcp", m.Egress[0].Proto)
	}
	min := mustParse(t, `{"version":1,"name":"a"}`)
	if min.Routes == nil || min.Storage == nil || min.Egress == nil || min.Health.Units == nil {
		t.Errorf("absent lists must decode as empty, got %+v", min)
	}
}

func TestParseManifestNormalizesACIDRToItsNetwork(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":1,"name":"a","egress":[{"to":"192.168.1.7/24","port":80}]}`)
	if got := m.Egress[0].To; got != "192.168.1.0/24" {
		t.Errorf("to = %q, want 192.168.1.0/24", got)
	}
}

func TestParseManifestRejects(t *testing.T) {
	t.Parallel()
	base := func(extra string) string {
		return `{"version":1,"name":"a"` + extra + `}`
	}
	tests := []struct {
		name, in, want string
	}{
		{"unknown field", base(`,"vpn":true`), "unknown field"},
		{"trailing data", base("") + "{}", "trailing data"},
		{"future version", `{"version":9,"name":"a"}`, "version"},
		{"nixpkgs before version 7", `{"version":6,"name":"a","nixpkgs":"nixos-unstable"}`, "nixpkgs needs version 7"},
		{"version 7 without nixpkgs", `{"version":7,"name":"a"}`, "nixpkgs"},
		{"nixpkgs a branch name", `{"version":7,"name":"a","nixpkgs":"master"}`, "nixpkgs"},
		{"nixpkgs a short commit", `{"version":7,"name":"a","nixpkgs":"d4f25a3"}`, "nixpkgs"},
		{"env default in version 7", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"env","default":"1"}]}`, "the value the stack's flake sets"},
		{"env type unknown", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"env","type":"date"}]}`, "variables[0].type"},
		{"enum on a secret", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"secret","enum":["a"]}]}`, "only for an env variable"},
		{"enum value twice", `{"version":7,"name":"a","nixpkgs":"nixos-unstable","variables":[{"name":"X","kind":"env","enum":["a","a"]}]}`, "listed twice"},
		{"type before version 7", `{"version":6,"name":"a","variables":[{"name":"X","kind":"env","type":"number"}]}`, "need version 7"},
		{"secrets in version 3", `{"version":3,"name":"a","secrets":[{"name":"KEY"}]}`, "secrets need version 4"},
		{"secret name lower case", `{"version":4,"name":"a","secrets":[{"name":"key"}]}`, "secrets[0].name"},
		{"secret name with a dash", `{"version":4,"name":"a","secrets":[{"name":"MY-KEY"}]}`, "secrets[0].name"},
		{"secret name starting with a digit", `{"version":4,"name":"a","secrets":[{"name":"1KEY"}]}`, "secrets[0].name"},
		{"secret name empty", `{"version":4,"name":"a","secrets":[{"name":""}]}`, "secrets[0].name"},
		{"secret name a path", `{"version":4,"name":"a","secrets":[{"name":"../ENV"}]}`, "secrets[0].name"},
		{"secret name too long", `{"version":4,"name":"a","secrets":[{"name":"A` + strings.Repeat("B", 64) + `"}]}`, "secrets[0].name"},
		{"secret twice", `{"version":4,"name":"a","secrets":[{"name":"KEY"},{"name":"OTHER"},{"name":"KEY"}]}`, "secrets[2].name: \"KEY\" is listed twice"},
		{"secret with an unknown field", `{"version":4,"name":"a","secrets":[{"name":"KEY","value":"x"}]}`, "unknown field"},
		{"api in version 2", `{"version":2,"name":"a","api":["read"]}`, "api needs version 3"},
		{"api scope unknown", `{"version":3,"name":"a","api":["operator"]}`, "api[0]"},
		{"api scope twice", `{"version":3,"name":"a","api":["read","control","read"]}`, "api[2]: \"read\" is listed twice"},
		{"ports in version 1", base(`,"ports":[{"port":32400,"from":["192.168.1.0/24"]}]`), "need version 2"},
		{"containers in version 1", base(`,"containers":true`), "need version 2"},
		{"name too long for the interface", `{"version":1,"name":"abcdefghijklm"}`, "name"},
		{"name with upper case", `{"version":1,"name":"Immich"}`, "name"},
		{"a system field: the output name is fixed", `{"version":1,"name":"a","system":"a"}`, "unknown field"},
		{"route host with a scheme", base(`,"routes":[{"host":"https://x.example.com","port":80}]`), "routes[0].host"},
		{"route host twice", base(`,"routes":[{"host":"x.example.com","port":80},{"host":"x.example.com","port":81}]`), "listed twice"},
		{"route port zero", base(`,"routes":[{"host":"x.example.com","port":0}]`), "routes[0].port"},
		{"route max_body free text", base(`,"routes":[{"host":"x.example.com","port":80,"max_body":"50GB\n}"}]`), "max_body"},
		{"storage relative", base(`,"storage":[{"host_path":"pool/x","mount":"/x"}]`), "must be absolute"},
		{"storage parent step", base(`,"storage":[{"host_path":"/pool/x/../../etc","mount":"/x"}]`), "plain path"},
		{"storage trailing slash", base(`,"storage":[{"host_path":"/pool/x/","mount":"/x"}]`), "plain path"},
		{"storage with a bind option", base(`,"storage":[{"host_path":"/pool/x:noidmap","mount":"/x"}]`), "plain path"},
		{"storage whole pool", base(`,"storage":[{"host_path":"/pool","mount":"/x"}]`), "top-level"},
		{"storage host store", base(`,"storage":[{"host_path":"/nix/store","mount":"/x"}]`), "under /nix"},
		{"storage another stack's state", base(`,"storage":[{"host_path":"` + StacksStateDir + `/email","mount":"/x"}]`), "under " + StacksStateDir},
		{"storage mount over the store", base(`,"storage":[{"host_path":"/pool/x","mount":"/nix/store"}]`), "under /nix"},
		{"storage mount root", base(`,"storage":[{"host_path":"/pool/x","mount":"/"}]`), "mount point"},
		{"storage mount twice", base(`,"storage":[{"host_path":"/pool/x","mount":"/x"},{"host_path":"/pool/y","mount":"/x"}]`), "used twice"},
		{"egress hostname", base(`,"egress":[{"to":"router.lan","port":80}]`), "egress[0].to"},
		{"egress everything", base(`,"egress":[{"to":"0.0.0.0/0","port":80}]`), "every address"},
		{"egress proto", base(`,"egress":[{"to":"host","port":80,"proto":"icmp"}]`), "egress[0].proto"},
		{"port the host owns", `{"version":2,"name":"a","ports":[{"port":443,"from":["192.168.1.0/24"]}]}`, "belongs to the host"},
		{"port without clients", `{"version":2,"name":"a","ports":[{"port":32400}]}`, "ports[0].from"},
		{"port open to everyone", `{"version":2,"name":"a","ports":[{"port":32400,"from":["0.0.0.0/0"]}]}`, "every address"},
		{"port from IPv6", `{"version":2,"name":"a","ports":[{"port":32400,"from":["fd00::/64"]}]}`, "IPv4 CIDR"},
		{"port listed twice", `{"version":2,"name":"a","ports":[{"port":32400,"from":["192.168.1.0/24"]},{"port":32400,"to":1,"from":["192.168.1.0/24"]}]}`, "listed twice"},
		{"port target zero", `{"version":2,"name":"a","ports":[{"port":32400,"to":-1,"from":["192.168.1.0/24"]}]}`, "ports[0].to"},
		{"health unit without suffix", base(`,"health":{"units":["immich-server"]}`), "health.units[0]"},
		{"health units shown under one name", `{"version":1,"name":"media","health":{"units":["media-x.service","x.service"]}}`, "both shown as media-x.service"},
		{"health path relative", base(`,"health":{"http":{"port":80,"path":"ping"}}`), "health.http.path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestGrantsListsEveryRequest(t *testing.T) {
	t.Parallel()
	got := mustParse(t, v1Manifest).Grants()
	want := []Grant{
		{GrantRoute, "immich.example.com"},
		{GrantStorage, "/pool/immich at /pool/immich, read-write"},
		{GrantGPU, "/dev/dri"},
		{GrantEgress, "host port 25/tcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Grants = %v, want %v", got, want)
	}
}

func TestNewGrants(t *testing.T) {
	t.Parallel()
	approved := mustParse(t, v1Manifest)

	t.Run("nothing approved yet: everything is new", func(t *testing.T) {
		if got := NewGrants(Manifest{}, approved); len(got) != 4 {
			t.Errorf("got %v, want all four grants", got)
		}
	})
	t.Run("same grants with a new port, body limit and health check need no approval", func(t *testing.T) {
		next := mustParse(t, strings.NewReplacer(`"port": 2283, "max_body": "50GB"`, `"port": 3001`, `"immich-server.service"`, `"other.service"`).Replace(v1Manifest))
		if got := NewGrants(approved, next); len(got) != 0 {
			t.Errorf("got %v, want none", got)
		}
	})
	t.Run("dropping grants needs no approval", func(t *testing.T) {
		if got := NewGrants(approved, mustParse(t, `{"version":1,"name":"immich"}`)); len(got) != 0 {
			t.Errorf("got %v, want none", got)
		}
	})
	t.Run("read-write to read-only needs no approval, the reverse does", func(t *testing.T) {
		ro := mustParse(t, strings.Replace(v1Manifest, `"read_only": false`, `"read_only": true`, 1))
		if got := NewGrants(approved, ro); len(got) != 0 {
			t.Errorf("rw -> ro: got %v, want none", got)
		}
		got := NewGrants(ro, approved)
		if len(got) != 1 || got[0].Kind != GrantStorage {
			t.Errorf("ro -> rw: got %v, want the storage grant", got)
		}
	})
	t.Run("each added request is reported", func(t *testing.T) {
		next := mustParse(t, `{"version":1,"name":"immich",
			"routes":[{"host":"immich.example.com","port":2283},{"host":"photos.example.net","port":2283}],
			"storage":[{"host_path":"/pool/immich","mount":"/srv/photos"}],
			"gpu":true,
			"egress":[{"to":"host","port":25},{"to":"192.168.1.102/32","port":1883}]}`)
		got := NewGrants(approved, next)
		want := []Grant{
			{GrantRoute, "photos.example.net"},
			{GrantStorage, "/pool/immich at /srv/photos, read-write"},
			{GrantEgress, "192.168.1.102/32 port 1883/tcp"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("gpu added", func(t *testing.T) {
		noGPU := mustParse(t, strings.Replace(v1Manifest, `"gpu": true`, `"gpu": false`, 1))
		got := NewGrants(noGPU, approved)
		if len(got) != 1 || got[0].Kind != GrantGPU {
			t.Errorf("got %v, want the gpu grant", got)
		}
	})
}

const mediaManifest = `{
  "version": 7,
  "name": "media",
  "nixpkgs": "nixos-unstable",
  "ports": [
    {"port": 32400, "from": ["192.168.1.0/24"]},
    {"port": 8554, "from": ["192.168.1.102/32"]}
  ],
  "containers": true
}`

func TestVersionTwoPortsAndContainers(t *testing.T) {
	t.Parallel()
	m := mustParse(t, mediaManifest)
	if p := m.Ports[0]; p.To != 32400 || p.Proto != "tcp" {
		t.Errorf("defaults not filled: %+v", p)
	}
	if got := mustParse(t, `{"version":2,"name":"a","ports":[{"port":1,"from":["192.168.1.7/24"]}]}`).Ports[0].From[0]; got != "192.168.1.0/24" {
		t.Errorf("from = %q, want 192.168.1.0/24", got)
	}
	want := []Grant{
		{GrantPort, "host port 32400/tcp from 192.168.1.0/24 to stack port 32400"},
		{GrantPort, "host port 8554/tcp from 192.168.1.102/32 to stack port 8554"},
		{GrantContainers, "podman (system calls bpf, add_key, keyctl)"},
	}
	if got := m.Grants(); !reflect.DeepEqual(got, want) {
		t.Errorf("Grants = %v, want %v", got, want)
	}

	t.Run("a stored version 1 manifest still reads", func(t *testing.T) {
		v1 := mustParse(t, v1Manifest)
		if again := mustParse(t, encode(v1)); !reflect.DeepEqual(again, v1) {
			t.Errorf("round trip changed it: %+v", again)
		}
	})
	t.Run("narrower clients need no approval, wider ones and a new target do", func(t *testing.T) {
		narrower := mustParse(t, strings.Replace(mediaManifest, "192.168.1.0/24", "192.168.1.0/25", 1))
		if got := NewGrants(m, narrower); len(got) != 0 {
			t.Errorf("narrower: got %v, want none", got)
		}
		if got := NewGrants(narrower, m); len(got) != 1 || got[0].Kind != GrantPort {
			t.Errorf("wider: got %v, want the port grant", got)
		}
		moved := mustParse(t, strings.Replace(mediaManifest, `"port": 8554,`, `"port": 8554, "to": 9554,`, 1))
		if got := NewGrants(m, moved); len(got) != 1 || got[0].Detail != "host port 8554/tcp from 192.168.1.102/32 to stack port 9554" {
			t.Errorf("new target: got %v", got)
		}
	})
	t.Run("containers added", func(t *testing.T) {
		without := mustParse(t, strings.Replace(mediaManifest, `"containers": true`, `"containers": false`, 1))
		if got := NewGrants(without, m); len(got) != 1 || got[0].Kind != GrantContainers {
			t.Errorf("got %v, want the containers grant", got)
		}
	})
}

func TestVersionThreeAPI(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":3,"name":"ha","api":["read","control"]}`)
	want := []Grant{{GrantAPI, "read"}, {GrantAPI, "control"}}
	if got := m.Grants(); !reflect.DeepEqual(got, want) {
		t.Errorf("Grants = %v, want %v", got, want)
	}
	if !m.HasAPI(APIRead) || !m.HasAPI(APIControl) {
		t.Errorf("scopes not granted: %v", m.API)
	}

	t.Run("an empty list is no api and encodes as before", func(t *testing.T) {
		t.Parallel()
		empty := mustParse(t, `{"version":2,"name":"a","api":[]}`)
		if empty.API != nil || strings.Contains(encode(empty), "api") {
			t.Errorf("api = %#v, encoded %s", empty.API, encode(empty))
		}
	})
	t.Run("a new scope needs approval, dropping one does not", func(t *testing.T) {
		t.Parallel()
		read := mustParse(t, `{"version":3,"name":"ha","api":["read"]}`)
		got := NewGrants(read, m)
		if want := []Grant{{GrantAPI, "control"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("read -> read+control: got %v, want %v", got, want)
		}
		if got := NewGrants(m, read); len(got) != 0 {
			t.Errorf("read+control -> read: got %v, want none", got)
		}
		if got := NewGrants(Manifest{}, m); !reflect.DeepEqual(got, m.Grants()) {
			t.Errorf("nothing approved: got %v, want every scope", got)
		}
		if got := NewGrants(m, mustParse(t, `{"version":3,"name":"ha","api":["control","read"]}`)); len(got) != 0 {
			t.Errorf("reordered: got %v, want none", got)
		}
	})
}

func TestVersionFourSecrets(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":4,"name":"mail","secrets":[{"name":"IMAP_BRIDGE_PASSWORD","description":"Bridge password"},{"name":"MEILI_KEY_2"}]}`)
	if len(m.Secrets) != 2 || m.Secrets[0].Name != "IMAP_BRIDGE_PASSWORD" || m.Secrets[0].Description != "Bridge password" {
		t.Fatalf("secrets = %+v", m.Secrets)
	}
	// Internally a version 4 secret is a required secret variable.
	if v, ok := m.Variable("MEILI_KEY_2"); !ok || v.Kind != VarSecret || !v.Required || v.Default != nil {
		t.Errorf("MEILI_KEY_2 = %+v, %v", v, ok)
	}
	if _, ok := m.Variable("OTHER"); ok {
		t.Error("OTHER is declared")
	}
	// Secrets are not grants: adding one needs no approval.
	if g := m.Grants(); len(g) != 0 {
		t.Errorf("grants = %+v", g)
	}
	if got := NewGrants(mustParse(t, `{"version":3,"name":"mail"}`), m); len(got) != 0 {
		t.Errorf("new grants = %+v", got)
	}
	// Without secrets a version 4 manifest encodes as before.
	plain := mustParse(t, `{"version":4,"name":"mail","secrets":[]}`)
	if strings.Contains(encode(plain), "secrets") {
		t.Errorf("encoded = %s", encode(plain))
	}
	if !strings.Contains(encode(m), `"secrets":[{"name":"IMAP_BRIDGE_PASSWORD","description":"Bridge password"}`) {
		t.Errorf("encoded = %s", encode(m))
	}
}

func TestParseManifestVersion7(t *testing.T) {
	t.Parallel()
	m := mustParse(t, `{"version":7,"name":"ha","nixpkgs":"nixos-26.11","variables":[
		{"name":"LOG_LEVEL","kind":"env","description":"How much to log","enum":["debug","info"]},
		{"name":"PORT","kind":"env","type":"number"},
		{"name":"PASSWORD","kind":"secret","required":true}]}`)
	if m.Nixpkgs != "nixos-26.11" || m.Variables[0].Enum[1] != "info" || m.Variables[1].Type != TypeNumber {
		t.Errorf("got %+v", m)
	}
	for _, ok := range []string{"nixos-unstable", "nixos-26.05", strings.Repeat("a", 40)} {
		if !ValidNixpkgs(ok) {
			t.Errorf("%s refused", ok)
		}
	}
}
