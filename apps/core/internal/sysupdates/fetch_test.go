package sysupdates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"homelab-ngx.com/core/internal/notifications"
)

// testRelease is a release server: version v's update files, the
// installer, a SHA256SUMS over all of them signed with key, and /latest.
type testRelease struct {
	srv   *httptest.Server
	files map[string][]byte
	mu    sync.Mutex
	got   []string
}

func newTestRelease(t *testing.T, key *openpgp.Entity, v string) *testRelease {
	t.Helper()
	r := &testRelease{files: map[string][]byte{
		"latest":                     []byte(v + "\n"),
		v + "/homelab_" + v + ".efi": []byte("uki " + v),
		v + "/homelab_" + v + ".root.4f68bce3-e8cd-4db1-96e7-fbcaf984b709.raw":        []byte("root " + v),
		v + "/homelab_" + v + ".root-verity.2c7357ed-ebd2-46d9-aec1-23d437ec2bf5.raw": []byte("verity " + v),
		v + "/core/homelab-core_" + v + ".raw":                                        []byte("core " + v),
		v + "/homelab_" + v + ".raw":                                                  []byte("installer " + v),
	}}
	var sums strings.Builder
	for name, b := range r.files {
		if rel, ok := strings.CutPrefix(name, v+"/"); ok {
			h := sha256.Sum256(b)
			sums.WriteString(hex.EncodeToString(h[:]) + "  " + rel + "\n")
		}
	}
	r.sign(t, key, v, sums.String())
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/")
		r.mu.Lock()
		r.got = append(r.got, name)
		b, ok := r.files[name]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	t.Setenv(releaseURLEnv, r.srv.URL)
	return r
}

func (r *testRelease) sign(t *testing.T, key *openpgp.Entity, v, sums string) {
	t.Helper()
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, key, strings.NewReader(sums), nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[v+"/SHA256SUMS"] = []byte(sums)
	r.files[v+"/SHA256SUMS.gpg"] = sig.Bytes()
}

func (r *testRelease) fetched(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.got, name)
}

// testKey makes a signing key and points the host's release keyring at it.
func testKey(t *testing.T) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity("release test", "", "release@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	p := filepath.Join(t.TempDir(), "release.asc")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(releaseKeyringEnv, p)
	return e
}

func downloadRun(t *testing.T) *deployRun {
	t.Helper()
	return &deployRun{o: RunOptions{ID: 1, Kind: KindDownload}, st: deployStore{path: filepath.Join(t.TempDir(), deployFile)}, now: time.Now}
}

func staged(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(imageUpdates, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(imageUpdates, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func TestDownloadStagesASignedRelease(t *testing.T) {
	imageHost(t, "0.0.202611181531")
	rel := newTestRelease(t, testKey(t), "0.1.0")
	x := downloadRun(t)
	if res, why := x.download(context.Background()); res != DeployHealthy {
		t.Fatalf("download: %s: %s", res, why)
	}
	want := []string{
		"core/homelab-core_0.1.0.raw",
		"homelab_0.1.0.efi",
		"homelab_0.1.0.root-verity.2c7357ed-ebd2-46d9-aec1-23d437ec2bf5.raw",
		"homelab_0.1.0.root.4f68bce3-e8cd-4db1-96e7-fbcaf984b709.raw",
	}
	if got := staged(t); !slices.Equal(got, want) {
		t.Fatalf("staged %v, want %v", got, want)
	}
	if rel.fetched("0.1.0/homelab_0.1.0.raw") {
		t.Error("the installer was downloaded")
	}
	if x.d.Version != "0.1.0" || x.d.Phase != "Downloaded 0.1.0" {
		t.Errorf("record = %+v", x.d)
	}
	// Staged, it is not downloaded again.
	x = downloadRun(t)
	if res, _ := x.download(context.Background()); res != DeployHealthy || !strings.HasPrefix(x.d.Phase, "Up to date") {
		t.Fatalf("again: %s %q", res, x.d.Phase)
	}
}

func TestDownloadRefusesWhatTheKeyDidNotSign(t *testing.T) {
	ctx := context.Background()
	t.Run("another key", func(t *testing.T) {
		imageHost(t, "0.0.1")
		other, _ := openpgp.NewEntity("someone else", "", "x@example.com", nil)
		testKey(t)
		newTestRelease(t, other, "0.1.0")
		if res, why := downloadRun(t).download(ctx); res != DeployFailed || !strings.Contains(why, "not signed by the release key") {
			t.Fatalf("%s: %s", res, why)
		}
		if got := staged(t); len(got) != 0 {
			t.Fatalf("staged %v", got)
		}
	})
	t.Run("sums changed after signing", func(t *testing.T) {
		imageHost(t, "0.0.1")
		rel := newTestRelease(t, testKey(t), "0.1.0")
		rel.mu.Lock()
		rel.files["0.1.0/SHA256SUMS"] = append(rel.files["0.1.0/SHA256SUMS"], []byte("0000000000000000000000000000000000000000000000000000000000000000  homelab_0.1.0.efi\n")...)
		rel.mu.Unlock()
		if res, why := downloadRun(t).download(ctx); res != DeployFailed || !strings.Contains(why, "not signed") {
			t.Fatalf("%s: %s", res, why)
		}
	})
	t.Run("a file that does not match its sum", func(t *testing.T) {
		imageHost(t, "0.0.1")
		rel := newTestRelease(t, testKey(t), "0.1.0")
		rel.mu.Lock()
		rel.files["0.1.0/core/homelab-core_0.1.0.raw"] = []byte("tampered")
		rel.mu.Unlock()
		if res, why := downloadRun(t).download(ctx); res != DeployFailed || !strings.Contains(why, "does not match its sum") {
			t.Fatalf("%s: %s", res, why)
		}
		if got := staged(t); len(got) != 0 {
			t.Fatalf("staged %v", got)
		}
	})
}

func TestDownloadSkipsWhatIsNotNewer(t *testing.T) {
	// A build of main after 0.1.0 sorts above it.
	imageHost(t, "0.1.0.202611190130")
	useCore(t, "0.1.0.202611190130")
	rel := newTestRelease(t, testKey(t), "0.1.0")
	x := downloadRun(t)
	if res, _ := x.download(context.Background()); res != DeployHealthy || !strings.HasPrefix(x.d.Phase, "Up to date") {
		t.Fatalf("%s %q", res, x.d.Phase)
	}
	if rel.fetched("0.1.0/SHA256SUMS") {
		t.Error("fetched the sums of an older release")
	}
}

func TestDownloadWithoutAServer(t *testing.T) {
	imageHost(t, "0.0.1")
	t.Setenv(releaseURLEnv, "http://127.0.0.1:1")
	if res, why := downloadRun(t).download(context.Background()); res != DeployFailed || !strings.Contains(why, "release server") {
		t.Fatalf("%s: %s", res, why)
	}
}

func TestTheEmbeddedReleaseKeyReads(t *testing.T) {
	t.Setenv(releaseKeyringEnv, "")
	keys, err := releaseKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys %d: %v", len(keys), err)
	}
	if fp := strings.ToUpper(hex.EncodeToString(keys[0].PrimaryKey.Fingerprint)); fp != "74BE38EB9E1F070C2AF87E4481D74B7E90B6ADB0" {
		t.Fatalf("primary %s", fp)
	}
}

type countingNotifier struct{ calls [][]notifications.Problem }

func (c *countingNotifier) Reconcile(_ context.Context, scope string, current []notifications.Problem) (notifications.Result, error) {
	c.calls = append(c.calls, current)
	return notifications.Result{}, nil
}

func TestAStagedReleaseIsNotifiedOnce(t *testing.T) {
	root := imageHost(t, "0.0.1")
	newTestRelease(t, testKey(t), "0.1.0")
	n := &countingNotifier{}
	m := &Module{file: filepath.Join(root, "state", "x.json"), image: true, now: time.Now, notify: n}
	_ = os.MkdirAll(filepath.Join(root, "state"), 0o755)
	ctx := context.Background()

	if c := m.CheckRelease(ctx); c.Latest != "0.1.0" || c.Error != "" || c.CheckedAt == nil {
		t.Fatalf("check = %+v", c)
	}
	m.notifyImageRelease(ctx) // not downloaded yet
	if len(n.calls) != 0 {
		t.Fatalf("notified before the download: %+v", n.calls)
	}
	if res, why := downloadRun(t).download(ctx); res != DeployHealthy {
		t.Fatalf("download: %s", why)
	}
	m.notifyImageRelease(ctx)
	m.notifyImageRelease(ctx)
	// A new daemon (after a core update) remembers it told.
	m2 := &Module{file: m.file, image: true, now: time.Now, notify: n}
	m2.notifyImageRelease(ctx)
	if len(n.calls) != 1 || len(n.calls[0]) != 1 || n.calls[0][0].Key != "image-release:0.1.0" {
		t.Fatalf("calls = %+v", n.calls)
	}
	// Once the host runs it, the notification closes.
	_ = os.WriteFile(osRelease, []byte("IMAGE_ID=homelab\nIMAGE_VERSION=0.1.0\n"), 0o644)
	m2.notifyImageRelease(ctx)
	m2.notifyImageRelease(ctx)
	if len(n.calls) != 2 || len(n.calls[1]) != 0 {
		t.Fatalf("calls = %+v", n.calls)
	}
}

func TestReleaseFilesNeedsTheWholeUpdate(t *testing.T) {
	t.Parallel()
	sums := strings.Repeat("a", 64) + "  homelab_1.0.0.efi\n" + strings.Repeat("b", 64) + " *core/homelab-core_1.0.0.raw\n"
	if _, err := releaseFiles([]byte(sums), "1.0.0"); err == nil {
		t.Fatal("no root: want an error")
	}
	sums += strings.Repeat("c", 64) + "  homelab_1.0.0.root.ab-12.raw\n" + strings.Repeat("d", 64) + "  homelab_1.0.0.root-verity.cd-34.raw\n" +
		strings.Repeat("e", 64) + "  ../../etc/passwd\n" + strings.Repeat("f", 64) + "  homelab_1.0.0.raw\n"
	files, err := releaseFiles([]byte(sums), "1.0.0")
	if err != nil || len(files) != 4 {
		t.Fatalf("files %v: %v", files, err)
	}
}
