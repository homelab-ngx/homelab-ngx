package stacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v9Manifest names an icon in each form: a slug for the stack, a URL and a
// repo file for its services.
const v9Manifest = `{
  "version": 9,
  "name": "players",
  "nixpkgs": "nixos-unstable",
  "icon": "jellyfin",
  "services": [
    {"unit": "plex.service", "icon": "https://example.org/plex.svg"},
    {"unit": "jellyfin.service", "icon": "icons/jellyfin.svg"}
  ],
  "health": {"units": ["jellyfin.service"]}
}`

func TestV9Parses(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(v9Manifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Icon != "jellyfin" || len(m.Services) != 2 || m.Services[1].Icon != "icons/jellyfin.svg" {
		t.Errorf("icons: %q %+v", m.Icon, m.Services)
	}
	for s, want := range map[string]string{
		"jellyfin": IconSlug, "https://example.org/a.svg": IconURL, "icons/a.svg": IconFile, "a.png": IconFile,
	} {
		if got := IconKind(s); got != want {
			t.Errorf("IconKind(%q) = %q, want %q", s, got, want)
		}
	}
	// Icons are no grant.
	if len(m.Grants()) != 0 {
		t.Errorf("grants: %+v", m.Grants())
	}
	// The API shape carries them.
	r := revision("abc", "github:o/r/abc", m, 0)
	if r.Manifest.Icon != "jellyfin" || len(r.Manifest.Services) != 2 || r.Manifest.Services[0].Unit != "plex.service" {
		t.Errorf("API shape: %+v", r.Manifest)
	}
}

func TestV9IconsRefused(t *testing.T) {
	t.Parallel()
	base := `{"version": 9, "name": "app", "nixpkgs": "nixos-unstable", `
	for in, want := range map[string]string{
		`"icon": "http://example.org/a.svg"}`:             "https://",
		`"icon": "https://"}`:                             "https URL",
		`"icon": "../a.svg"}`:                             "plain path",
		`"icon": "/a.svg"}`:                               "plain path",
		`"icon": "./a.svg"}`:                              "plain path",
		`"icon": "icons/a.gif"}`:                          ".svg, .png or .webp",
		`"icon": "Jellyfin"}`:                             "icon name",
		`"icon": "a b"}`:                                  "icon name",
		`"services": [{"unit": "plex", "icon": "plex"}]}`: "services[0].unit",
		`"services": [{"unit": "a.service", "icon": "a"}, {"unit": "a.service", "icon": "b"}]}`: "listed twice",
		`"services": [{"unit": "a.service"}]}`:                                                  "needs an icon",
		`"services": [{"unit": "a.service", "icon": "a", "x": 1}]}`:                             "unknown field",
	} {
		_, err := ParseManifest([]byte(base + in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want it to mention %q", in, err, want)
		}
	}
	// Version 8 has no icons.
	_, err := ParseManifest([]byte(`{"version": 8, "name": "app", "nixpkgs": "nixos-unstable", "icon": "app"}`))
	if err == nil || !strings.Contains(err.Error(), "need version 9") {
		t.Errorf("version 8 with an icon: %v", err)
	}
}

func TestCheckIconFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(name string, size int) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("icons/app.svg", 100)
	write("big.png", MaxIconFile+1)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.svg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.svg"), filepath.Join(root, "link.svg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	for icon, want := range map[string]string{
		"icons/app.svg":             "",
		"jellyfin":                  "", // not a file
		"https://example.org/a.svg": "",
		"icons/gone.svg":            "is not in the repo",
		"big.png":                   "larger than",
		"link.svg":                  "not a plain file",
		"dir/x.svg":                 "not a plain file",
		"icons/app.svg/x.svg":       "not a plain file",
	} {
		m := Manifest{Version: 9, Services: []ServiceMeta{{Unit: "a.service", Icon: icon}}}
		err := CheckIconFiles(root, m)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", icon, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err %v, want %q", icon, err, want)
		case want != "" && !strings.HasPrefix(err.Error(), "services[0].icon: "):
			t.Errorf("%s: %v does not name the field", icon, err)
		}
	}
}

func TestV9AddedFoldersHint(t *testing.T) {
	t.Parallel()
	m, err := ParseManifest([]byte(`{
  "version": 9, "name": "samba", "nixpkgs": "nixos-unstable",
  "added_folders": {"under": "/shares", "label": "Shares", "description": "Each is one share.", "read_only": false},
  "health": {"units": ["samba-smbd.service"]}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if h := m.AddedFolders; h == nil || h.Under != "/shares" || h.Label != "Shares" || h.ReadOnly {
		t.Errorf("hint: %+v", m.AddedFolders)
	}
	// A hint is no grant.
	if len(m.Grants()) != 0 {
		t.Errorf("grants: %+v", m.Grants())
	}
	r := revision("abc", "github:o/r/abc", m, 0)
	if h := r.Manifest.AddedFolders; h == nil || h.Under != "/shares" || h.Description != "Each is one share." {
		t.Errorf("API shape: %+v", r.Manifest.AddedFolders)
	}
}

func TestV9AddedFoldersRefused(t *testing.T) {
	t.Parallel()
	path := `"variables": [{"name": "DATA", "kind": "path", "required": true, "mount": "/data/media"}], `
	for in, want := range map[string]string{
		`{"version": 8, "added_folders": {"under": "/shares", "label": "Shares"}, `:      "needs version 9",
		`{"version": 9, "added_folders": {"under": "shares", "label": "Shares"}, `:       "added_folders.under",
		`{"version": 9, "added_folders": {"under": "/", "label": "Shares"}, `:            "added_folders.under",
		`{"version": 9, "added_folders": {"under": "/etc/x", "label": "Shares"}, `:       "the stack's own system",
		`{"version": 9, ` + path + `"added_folders": {"under": "/data", "label": "S"}, `: "clashes with its path DATA",
		`{"version": 9, "added_folders": {"under": "/shares", "label": " "}, `:           "needs a label",
		`{"version": 9, "added_folders": {"under": "/shares", "label": "a\nb"}, `:        "one line",
		`{"version": 9, "added_folders": {"under": "/shares", "label": "S", "x": 1}, `:   "unknown field",
	} {
		_, err := ParseManifest([]byte(in + `"name": "app", "nixpkgs": "nixos-unstable", "health": {"units": ["a.service"]}}`))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", in, err, want)
		}
	}
}
