package system

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/httpx"
)

type fakeCfg struct {
	snap *config.Snapshot
}

func (f *fakeCfg) Current() *config.Snapshot { return f.snap }

func testSnap() *config.Snapshot {
	return &config.Snapshot{
		Generation: 3,
		Nix:        &config.Nix{CoreServices: []string{"caddy.service"}},
	}
}

// serve registers s on a real operator API and answers one GET, decoding a
// 200 body into out.
func serve(t *testing.T, s *System, path string, out any) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code == 200 && out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v: %s", path, err, w.Body)
		}
	}
	return w.Code, w.Body.String()
}

// The version tests share commitFile and coreLink, so none runs in parallel.

// noCore points coreLink at nothing: a test run on an image host would read
// that host's own core extension.
func noCore(t *testing.T) {
	t.Helper()
	coreLink = filepath.Join(t.TempDir(), "homelab-core.raw")
	t.Cleanup(func() { coreLink = "/var/lib/extensions/homelab-core.raw" })
	freshStart(t)
}

// freshStart forgets the version the process started as, so the next
// Running call reads it again, as a daemon starting now would.
func freshStart(t *testing.T) {
	t.Helper()
	running = sync.OnceValues(BuildInfo)
	t.Cleanup(func() { running = sync.OnceValues(BuildInfo) })
}

func TestVersion(t *testing.T) {
	noCore(t)
	var v Version
	if serve(t, New(&fakeCfg{}, nil, nil), "/api/version", &v); v.Version != "dev" {
		t.Fatalf("version = %+v", v)
	}
}

// On the image host the version is the one in the name of the core extension
// in use, not a number built into the binary.
func TestVersionFromCoreExtensionInUse(t *testing.T) {
	dir := t.TempDir()
	coreLink = filepath.Join(dir, "homelab-core.raw")
	t.Cleanup(func() { coreLink = "/var/lib/extensions/homelab-core.raw" })
	freshStart(t)
	if err := os.Symlink("../extensions.d/homelab-core_0.2.5.raw", coreLink); err != nil {
		t.Fatal(err)
	}
	var v Version
	if serve(t, New(&fakeCfg{}, nil, nil), "/api/version", &v); v.Version != "0.2.5" {
		t.Fatalf("version = %q, want 0.2.5", v.Version)
	}
	// A link that names no version leaves the built-in one.
	if err := os.Remove(coreLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere.raw", coreLink); err != nil {
		t.Fatal(err)
	}
	if ver, _ := BuildInfo(); ver != "dev" {
		t.Fatalf("BuildInfo() version = %q, want dev", ver)
	}
}

// A core switched under a running daemon is not the daemon's version until
// it restarts: /version keeps what it started as, BuildInfo follows the link.
func TestVersionIsWhatTheDaemonStartedAs(t *testing.T) {
	dir := t.TempDir()
	coreLink = filepath.Join(dir, "homelab-core.raw")
	t.Cleanup(func() { coreLink = "/var/lib/extensions/homelab-core.raw" })
	freshStart(t)
	if err := os.Symlink("../extensions.d/homelab-core_0.3.1.raw", coreLink); err != nil {
		t.Fatal(err)
	}
	if v, _ := Running(); v != "0.3.1" {
		t.Fatalf("Running() = %q at start, want 0.3.1", v)
	}
	if err := os.Remove(coreLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../extensions.d/homelab-core_0.3.2.raw", coreLink); err != nil {
		t.Fatal(err)
	}
	var v Version
	if serve(t, New(&fakeCfg{}, nil, nil), "/api/version", &v); v.Version != "0.3.1" {
		t.Fatalf("/version = %q after the core switched, want 0.3.1 until a restart", v.Version)
	}
	if now, _ := BuildInfo(); now != "0.3.2" {
		t.Fatalf("BuildInfo() = %q, want the switched core 0.3.2", now)
	}
}

func TestVersionCommitFromDeployFile(t *testing.T) {
	noCore(t)
	commitFile = filepath.Join(t.TempDir(), "commit")
	t.Cleanup(func() { commitFile = "/etc/homelab-daemon/commit" })
	if err := os.WriteFile(commitFile, []byte("abc1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var v Version
	if serve(t, New(&fakeCfg{}, nil, nil), "/api/version", &v); v.Commit != "abc1234" {
		t.Fatalf("commit = %q, want abc1234", v.Commit)
	}
	if ver, c := BuildInfo(); ver != "dev" || c != "abc1234" {
		t.Fatalf("BuildInfo() = %q, %q, want dev, abc1234", ver, c)
	}
}

func TestGetConfigView(t *testing.T) {
	t.Parallel()
	var v config.ConfigView
	if code, body := serve(t, New(&fakeCfg{snap: testSnap()}, nil, nil), "/api/config", &v); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if v.Generation != 3 || v.CoreServices[0] != "caddy.service" || v.Pools == nil {
		t.Fatalf("view = %+v", v)
	}
}

func TestNotLoadedIs503(t *testing.T) {
	t.Parallel()
	s := New(&fakeCfg{}, nil, nil)
	for _, path := range []string{"/api/config", "/api/log-viewer-config", "/api/logs?unit=x"} {
		if code, body := serve(t, s, path, nil); code != 503 {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
}

func TestLogViewerConfigUnits(t *testing.T) {
	t.Parallel()
	snap := &config.Snapshot{
		Nix: &config.Nix{CoreServices: []string{"caddy.service"}},
	}
	stacks := fakeSources{
		fakeStacks: fakeStacks{{Name: "media-sonarr", Unit: "sonarr.service", JournalDir: "/j/media"}},
		backups:    []BackupUnit{{Name: "App data", Unit: "homelab-backup-3.service"}},
	}
	var got LogViewerConfig
	serve(t, New(&fakeCfg{snap: snap}, nil, stacks), "/api/log-viewer-config", &got)
	want := []LogViewerUnit{
		{Id: "caddy", Label: "Caddy", Group: LogViewerUnitGroupCore},
		{Id: "homelab-backup-3", Label: "App data", Group: LogViewerUnitGroupBackup},
		{Id: "media-sonarr", Label: "Media Sonarr", Group: LogViewerUnitGroupStack},
	}
	if !reflect.DeepEqual(got.Units, want) {
		t.Fatalf("units = %+v, want %+v", got.Units, want)
	}
}

func TestLogViewerUnitsLabelOrder(t *testing.T) {
	t.Parallel()
	got := logViewerUnits(&config.Snapshot{
		Nix: &config.Nix{CoreServices: []string{"pocket-id.service", "caddy.service", "homelab-daemon.service"}},
	}, []string{"homelab-cloudflared.service", "caddy.service"}, nil, nil)
	var ids []string
	for _, u := range got {
		ids = append(ids, u.ID)
	}
	if want := []string{"caddy", "homelab-cloudflared", "homelab-daemon", "pocket-id"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}
