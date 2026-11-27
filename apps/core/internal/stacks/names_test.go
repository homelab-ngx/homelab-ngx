package stacks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddUnderAChosenName(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)

	s, err := svc.Add(ctx, src, "photos")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "photos" || s.Fetched.Manifest.Name != "immich" {
		t.Errorf("got name %q from manifest %q", s.Name, s.Fetched.Manifest.Name)
	}
	// The same repo again, under the name it suggests.
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Add(ctx, src, "photos")
	wantCode(t, err, 409, "stack_exists")
	for _, bad := range []string{"Photos", "1photos", "a-name-of-thirty-three-characters", "pho tos"} {
		_, err = svc.Add(ctx, src, bad)
		wantCode(t, err, 400, "invalid_name")
	}

	// A later commit is read for the installed name, not the manifest's.
	fs.set("bbbbbbb2", immichManifest)
	if s, err = svc.Fetch(ctx, "photos"); err != nil || s.Fetched.Rev != "bbbbbbb2" {
		t.Fatalf("fetch = %+v, %v", s.Fetched, err)
	}
}

func TestALongNameIsCutOnlyForItsInterface(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"immich":        "ve-immich",
		"twelve-chars":  "ve-twelve-chars",
		"plex-jellyfin": "ve-plex-jellyfi",
	} {
		if got := HostIface(name); got != want {
			t.Errorf("HostIface(%q) = %q, want %q", name, got, want)
		}
	}

	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	long := "a-name-of-thirty-two-characters1"
	if s, err := svc.Add(ctx, src, long); err != nil || s.Name != long {
		t.Fatalf("add %q = %+v, %v", long, s, err)
	}
	if _, err := svc.Add(ctx, src, "plex-jellyfin"); err != nil {
		t.Fatal(err)
	}
	// The same first 12 characters, a different 13th: the same interface.
	_, err := svc.Add(ctx, src, "plex-jellyfix")
	wantCode(t, err, 409, "iface_taken")
	if err == nil || !strings.Contains(err.Error(), "ve-plex-jellyfi") {
		t.Errorf("the refusal does not name the interface: %v", err)
	}
	// A 12-character name is its own interface, and so it clashes too.
	_, err = svc.Add(ctx, src, "plex-jellyfi")
	wantCode(t, err, 409, "iface_taken")
	if _, err := svc.Add(ctx, src, "plex-jellyf"); err != nil {
		t.Fatal(err)
	}
}

func TestAChosenNameMustKeepHealthUnitsApart(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	// As "immich" these are immich-x-a and immich-a; as "x" both are x-a.
	fs.set("aaaaaaa1", `{"version":7,"name":"immich","nixpkgs":"nixos-unstable","health":{"units":["x-a.service","a.service"]}}`)
	_, err := svc.Add(context.Background(), src, "x")
	wantCode(t, err, 422, "invalid_manifest")
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
}

func TestLabelIsSetClearedAndLocked(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "immich", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}

	s, err := svc.SetLabel(ctx, "immich", "  Family photos ")
	if err != nil || s.Label != "Family photos" || s.Name != "immich" {
		t.Fatalf("set = %q/%q, %v", s.Name, s.Label, err)
	}
	if l := readLockFile(t, svc); len(l.Stacks) != 1 || l.Stacks[0].Label != "Family photos" {
		t.Errorf("lock = %+v", l)
	}

	_, err = svc.SetLabel(ctx, "immich", strings.Repeat("ö", maxLabel+1))
	wantCode(t, err, 400, "invalid_label")
	_, err = svc.SetLabel(ctx, "immich", "a\nb")
	wantCode(t, err, 400, "invalid_label")
	_, err = svc.SetLabel(ctx, "nope", "x")
	wantCode(t, err, 404, "unknown_stack")
	if s, _ := svc.Get(ctx, "immich"); s.Label != "Family photos" {
		t.Errorf("a refused label changed it to %q", s.Label)
	}

	if s, err = svc.SetLabel(ctx, "immich", ""); err != nil || s.Label != "" {
		t.Fatalf("clear = %q, %v", s.Label, err)
	}
	if l := readLockFile(t, svc); l.Stacks[0].Label != "" {
		t.Errorf("lock after clearing = %+v", l.Stacks[0])
	}
}

func TestRestoreUnderANameItsManifestDoesNotSuggest(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	res, err := svc.Restore(ctx, Lock{Version: LockVersion, Stacks: []LockedStack{{
		Name: "photos", Label: "Photos", Source: src, Rev: "aaaaaaa1", URL: "github:o/r/aaaaaaa1",
		Manifest: json.RawMessage(immichManifest),
	}}}, "")
	if err != nil || len(res.Restored) != 1 {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	s, err := svc.Get(ctx, "photos")
	if err != nil || s.Label != "Photos" || s.Approved == nil || s.Approved.Manifest.Name != "immich" {
		t.Errorf("restored = %+v, %v", s, err)
	}
}

func TestBuildNamesTheSystemAfterTheInstalledName(t *testing.T) {
	t.Parallel()
	svc, fs, h := newServiceOn(t)
	h.On("systemctl", "--machine=photos", "is-active").Reply("active\n")
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, "photos"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "photos", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	o := svc.ops["photos"]
	svc.mu.Unlock()
	select {
	case <-o.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("start did not finish; phase %q", o.phase)
	}
	builds := h.CallsMatching("nix", "--extra-experimental-features", "nix-command flakes", "build")
	if len(builds) != 1 {
		t.Fatalf("builds = %+v", builds)
	}
	if got := builds[0].Args[len(builds[0].Args)-1]; got != "path:"+filepath.Join(svc.host.BuildsDir, "photos")+"#"+WrapperSystem {
		t.Errorf("built %s", got)
	}
	if flake, _ := os.ReadFile(filepath.Join(svc.host.BuildsDir, "photos", "flake.nix")); !strings.Contains(string(flake), `networking.hostName = "photos";`) {
		t.Errorf("wrapper flake:\n%s", flake)
	}
	if s, _ := svc.Get(ctx, "photos"); !s.Running || s.Generation != 1 {
		t.Errorf("running=%v generation=%d", s.Running, s.Generation)
	}
}
