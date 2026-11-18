package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

// fakeSource serves one commit, or one error.
type fakeSource struct {
	f   Fetched
	err error
}

func (s *fakeSource) Fetch(context.Context, string) (Fetched, error) { return s.f, s.err }

func (s *fakeSource) set(rev, manifest string) {
	s.f, s.err = Fetched{Rev: rev, URL: "github:o/r/" + rev, Manifest: []byte(manifest)}, nil
}

func newService(t *testing.T) (*Service, *fakeSource) {
	t.Helper()
	svc, src, _ := newServiceOn(t)
	return svc, src
}

// newServiceOn returns a service on a fake host, with its directories under
// the test's temp dir.
func newServiceOn(t *testing.T) (*Service, *fakeSource, *fakeHost) {
	t.Helper()
	dir := t.TempDir()
	st, _ := storetest.Open(t)
	src, h := &fakeSource{}, newFakeHost()
	svc := New(st, src, Host{
		Runner: h, StateDir: filepath.Join(dir, "state"), ProfilesDir: filepath.Join(dir, "profiles"), RoutesDir: filepath.Join(dir, "routes"),
		VarsDir: filepath.Join(dir, "vars"), AgeKeyFile: hostKey(t, dir), BuildsDir: filepath.Join(dir, "builds"),
	}, h, nil)
	svc.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	svc.timeZone = func() string { return "Europe/Stockholm" }
	svc.resolveNixpkgs = func(_ context.Context, spec string) (string, error) { return fakeNixpkgs(spec), nil }
	svc.nixpkgsRelease = func(context.Context, string) (NixpkgsRelease, error) {
		return NixpkgsRelease{Version: "26.11", Modified: time.Unix(1_790_000_000, 0)}, nil
	}
	svc.listChannels = func(context.Context) ([]string, error) {
		return []string{"nixos-unstable", "nixos-26.11", "nixos-26.05"}, nil
	}
	svc.probe = func(context.Context, string) error { return nil }
	svc.healthEvery = time.Millisecond
	return svc, src, h
}

func wantCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != status || p.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
}

const src = "github:example/stack-immich"

func TestAddStoresTheStackUnapprovedWithEveryGrantWaiting(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	fs.set("aaaaaaa1", immichManifest)
	s, err := svc.Add(context.Background(), src, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "immich" || s.Approved != nil || len(s.NewGrants) != 4 || s.Fetched.Rev != "aaaaaaa1" {
		t.Errorf("got %+v", s)
	}
	if s.Fetched.Manifest.Health.Http == nil || s.Fetched.Manifest.Routes[0].Host != "immich.example.com" {
		t.Errorf("manifest did not reach the API shape: %+v", s.Fetched.Manifest)
	}
	_, err = svc.Add(context.Background(), src, "")
	wantCode(t, err, 409, "stack_exists")
}

func TestAddRefusals(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()

	for _, bad := range []string{"path:/tmp/x", "/tmp/x", "--help", "github:only-owner", "git+ssh://host/x?rev=1&dir=y", "https://example.com/x.tar.gz"} {
		_, err := svc.Add(ctx, bad, "")
		wantCode(t, err, 400, "invalid_source")
	}

	fs.err = errors.New("exit 1: repository not found")
	_, err := svc.Add(ctx, src, "")
	wantCode(t, err, 502, "fetch_failed")

	fs.f, fs.err = Fetched{}, ErrNoManifest
	_, err = svc.Add(ctx, src, "")
	wantCode(t, err, 422, "invalid_manifest")

	fs.set("aaaaaaa1", `{"version":1,"name":"immich","storage":[{"host_path":"/nix/store","mount":"/x"}]}`)
	_, err = svc.Add(ctx, src, "")
	wantCode(t, err, 422, "invalid_manifest")

	if l, _ := svc.List(ctx); len(l) != 0 {
		t.Errorf("a refused add left a stack behind: %+v", l)
	}
}

func TestApproveOnlyTheCommitThatWasShown(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}

	// The repo moves on, with one more grant, before the operator approves.
	fs.set("bbbbbbb2", strings.Replace(immichManifest, `"egress": [`, `"egress": [{"to":"192.168.1.1/32","port":80},`, 1))
	if _, err := svc.Fetch(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Approve(ctx, "immich", "aaaaaaa1")
	wantCode(t, err, 409, "stale_rev")

	s, err := svc.Approve(ctx, "immich", "bbbbbbb2")
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved == nil || s.Approved.Rev != "bbbbbbb2" || len(s.NewGrants) != 0 || len(s.Approved.Manifest.Egress) != 2 {
		t.Errorf("got %+v", s)
	}

	_, err = svc.Approve(ctx, "nope", "x")
	wantCode(t, err, 404, "unknown_stack")
}

func TestFetchApprovesACommitThatAsksForNothingNew(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}

	// Before the first approval nothing is approved by a fetch.
	fs.set("bbbbbbb2", immichManifest)
	s, err := svc.Fetch(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved != nil || len(s.NewGrants) != 4 {
		t.Fatalf("a fetch approved a stack nobody had approved: %+v", s)
	}
	if _, err := svc.Approve(ctx, "immich", "bbbbbbb2"); err != nil {
		t.Fatal(err)
	}

	// Same grants, new commit: approved at once.
	fs.set("ccccccc3", strings.Replace(immichManifest, `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	s, err = svc.Fetch(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved.Rev != "ccccccc3" || s.Approved.Manifest.Routes[0].Port != 3001 || len(s.NewGrants) != 0 {
		t.Errorf("got %+v", s)
	}

	// One more grant: the fetched commit waits and the approved one stays.
	fs.set("ddddddd4", strings.Replace(immichManifest, `"egress": [`, `"egress": [{"to":"192.168.1.1/32","port":80},`, 1))
	s, err = svc.Fetch(ctx, "immich")
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved.Rev != "ccccccc3" || s.Fetched.Rev != "ddddddd4" || len(s.NewGrants) != 1 || s.NewGrants[0].Detail != "192.168.1.1/32 port 80/tcp" {
		t.Errorf("got approved=%s fetched=%s new=%v", s.Approved.Rev, s.Fetched.Rev, s.NewGrants)
	}
}

func TestARepoThatRenamesItselfKeepsItsInstalledName(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	// The manifest's name only suggests a name at install and names the
	// flake's system; the stack stays "immich".
	fs.set("bbbbbbb2", strings.Replace(immichManifest, `"name": "immich"`, `"name": "photos"`, 1))
	s, err := svc.Fetch(ctx, "immich")
	if err != nil || s.Name != "immich" || s.Fetched.Manifest.Name != "photos" {
		t.Errorf("fetch = %q from %q, %v", s.Name, s.Fetched.Manifest.Name, err)
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", immichManifest)
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	wantCode(t, svc.Remove(ctx, "immich"), 404, "unknown_stack")
	_, err := svc.Get(ctx, "immich")
	wantCode(t, err, 404, "unknown_stack")
}

func TestNixSourceReadsTheManifestOfTheLockedCommit(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("nix").Reply(`{"hash":"sha256-x","locked":{"owner":"example","repo":"stack-immich","rev":"abc123","type":"github"},"storePath":"/nix/store/xxx-source"}`)
	n := NewNixSource(f)
	n.readFile = func(p string) ([]byte, error) {
		if p != "/nix/store/xxx-source/homelab-stack.json" && p != "/nix/store/xxx-source/flake.nix" {
			t.Errorf("read %s", p)
		}
		return []byte("{}"), nil
	}
	got, err := n.Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != "abc123" || got.URL != "github:example/stack-immich/abc123" || string(got.Manifest) != "{}" {
		t.Errorf("got %+v", got)
	}
	args := f.Calls()[0].Args
	if args[len(args)-2] != "--" || args[len(args)-1] != src {
		t.Errorf("the source must follow --: %v", args)
	}

	git := cmdrunnertest.NewFake()
	git.On("nix").Reply(`{"locked":{"ref":"main","rev":"def456","type":"git","url":"ssh://git@github.com/example/stack-immich"},"storePath":"/nix/store/yyy-source"}`)
	n = NewNixSource(git)
	n.readFile = func(string) ([]byte, error) { return []byte("{}"), nil }
	got, err = n.Fetch(context.Background(), "git+ssh://git@github.com/example/stack-immich")
	if err != nil || got.URL != "git+ssh://git@github.com/example/stack-immich?ref=main&rev=def456" {
		t.Errorf("git source: %+v, %v", got, err)
	}
	n.readFile = func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	if _, err := n.Fetch(context.Background(), "git+ssh://git@github.com/example/stack-immich"); !errors.Is(err, ErrNoManifest) {
		t.Errorf("err = %v, want ErrNoManifest", err)
	}

	unlocked := cmdrunnertest.NewFake()
	unlocked.On("nix").Reply(`{"locked":{"type":"path","path":"/tmp/x"},"storePath":"/nix/store/zzz-source"}`)
	if _, err := NewNixSource(unlocked).Fetch(context.Background(), src); err == nil {
		t.Error("a source without a commit was accepted")
	}
}

func TestApproveRefusesAHostPortAnotherStackForwards(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", mediaManifest)
	if _, err := svc.Add(ctx, "github:example/stack-media", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, "media", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	fs.set("bbbbbbb1", `{"version":7,"name":"tv","nixpkgs":"nixos-unstable","ports":[{"port":32400,"from":["192.168.1.5/32"]}]}`)
	if _, err := svc.Add(ctx, "github:example/stack-tv", ""); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Approve(ctx, "tv", "bbbbbbb1")
	wantCode(t, err, 409, "port_taken")
}

// fakeNixpkgs is what a channel resolves to in tests: its name padded to a
// commit; a commit is itself.
func fakeNixpkgs(spec string) string {
	if commitRE.MatchString(spec) {
		return spec
	}
	return (strings.Repeat("0", 40) + fmt.Sprintf("%x", spec))[len(fmt.Sprintf("%x", spec)):]
}
