package stacks

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/httpx"
)

const (
	testSVG = `<svg xmlns="http://www.w3.org/2000/svg"/>`
	testPNG = "\x89PNG\r\n\x1a\n"
)

// iconSet stubs the dashboard-icons set: files maps a path such as
// /svg/jellyfin.svg to its body, fail makes every request answer 500, and
// hits counts the requests.
type iconSet struct {
	mu    sync.Mutex
	files map[string]string
	fail  bool
	hits  atomic.Int32
	delay time.Duration
}

func (s *iconSet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	s.mu.Lock()
	delay, fail, files := s.delay, s.fail, s.files
	s.mu.Unlock()
	time.Sleep(delay)
	if fail {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	body, ok := files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(body))
}

// iconService is a service with the v9 stack "players" approved, its icons
// fetched from a stubbed set and its commit's tree in a temp dir.
func iconService(t *testing.T, files map[string]string) (*Service, http.Handler, *iconSet, string, *atomic.Int32) {
	t.Helper()
	svc, fs, _ := newServiceOn(t)
	install(t, svc, fs, "aaaaaaa1", v9Manifest)
	set := &iconSet{files: files}
	srv := httptest.NewServer(set)
	t.Cleanup(srv.Close)
	tree := t.TempDir()
	write(t, filepath.Join(tree, "icons", "jellyfin.svg"), testSVG)
	var lookups atomic.Int32
	svc.icons.dir, svc.icons.base, svc.icons.client = filepath.Join(t.TempDir(), "icons"), srv.URL, srv.Client()
	svc.icons.tree = func(_ context.Context, url string) (string, error) {
		lookups.Add(1)
		if url != "github:o/r/aaaaaaa1" {
			t.Errorf("tree of %q, want the approved commit", url)
		}
		return tree, nil
	}
	op, _, _ := testAPIs(svc)
	return svc, op, set, tree, &lookups
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func getIcon(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stacks/"+path, nil))
	return w
}

func wantIcon(t *testing.T, w *httptest.ResponseRecorder, typ, body string) {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != typ || w.Body.String() != body {
		t.Fatalf("got %d %q %q, want 200 %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String(), typ, body)
	}
	h := w.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "private, max-age=86400" {
		t.Errorf("headers %v", h)
	}
	if csp := h.Get("Content-Security-Policy"); (typ == "image/svg+xml") != (csp == svgCSP) {
		t.Errorf("%s with Content-Security-Policy %q", typ, csp)
	}
}

func wantProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
	}
}

func TestIconSlugFetchedOnceAndKept(t *testing.T) {
	t.Parallel()
	svc, op, set, _, _ := iconService(t, map[string]string{"/svg/jellyfin.svg": testSVG})
	wantIcon(t, getIcon(op, "players/icon"), "image/svg+xml", testSVG)
	wantIcon(t, getIcon(op, "players/icon"), "image/svg+xml", testSVG)
	if n := set.hits.Load(); n != 1 {
		t.Errorf("%d downloads, want 1", n)
	}
	if b, err := os.ReadFile(filepath.Join(svc.icons.dir, "jellyfin.svg")); err != nil || string(b) != testSVG {
		t.Errorf("kept: %q %v", b, err)
	}
	// A restarted daemon serves it from the folder.
	fresh := newIconCache(svc.icons.dir, nil, svc.log)
	fresh.base = "http://127.0.0.1:1"
	if ic, err := fresh.slug("jellyfin"); err != nil || string(ic.Data) != testSVG {
		t.Errorf("from the folder: %+v %v", ic, err)
	}
}

func TestIconSlugFallsBackToPNG(t *testing.T) {
	t.Parallel()
	_, op, set, _, _ := iconService(t, map[string]string{"/png/jellyfin.png": testPNG})
	wantIcon(t, getIcon(op, "players/icon"), "image/png", testPNG)
	if n := set.hits.Load(); n != 2 {
		t.Errorf("%d requests, want the svg and the png", n)
	}
}

func TestIconSlugMissRemembered(t *testing.T) {
	t.Parallel()
	svc, op, set, _, _ := iconService(t, nil)
	now := time.Unix(1_800_000_000, 0)
	svc.icons.now = func() time.Time { return now }
	wantProblem(t, getIcon(op, "players/icon"), 404, "no_icon")
	wantProblem(t, getIcon(op, "players/icon"), 404, "no_icon")
	if n := set.hits.Load(); n != 2 {
		t.Errorf("%d requests, want 2 (svg, png) for both answers", n)
	}
	// After an hour the set is asked again, and now has it.
	set.mu.Lock()
	set.files = map[string]string{"/svg/jellyfin.svg": testSVG}
	set.mu.Unlock()
	now = now.Add(iconMissFor + time.Second)
	wantIcon(t, getIcon(op, "players/icon"), "image/svg+xml", testSVG)
}

func TestIconSlugFailureRetriedSoon(t *testing.T) {
	t.Parallel()
	svc, op, set, _, _ := iconService(t, map[string]string{"/svg/jellyfin.svg": testSVG})
	now := time.Unix(1_800_000_000, 0)
	svc.icons.now = func() time.Time { return now }
	set.mu.Lock()
	set.fail = true
	set.mu.Unlock()
	wantProblem(t, getIcon(op, "players/icon"), 502, "icon_fetch_failed")
	wantProblem(t, getIcon(op, "players/icon"), 502, "icon_fetch_failed")
	if n := set.hits.Load(); n != 1 {
		t.Errorf("%d requests while the failure is remembered, want 1", n)
	}
	set.mu.Lock()
	set.fail = false
	set.mu.Unlock()
	now = now.Add(iconErrFor + time.Second)
	wantIcon(t, getIcon(op, "players/icon"), "image/svg+xml", testSVG)
}

func TestIconSlugTooLarge(t *testing.T) {
	t.Parallel()
	_, op, _, _, _ := iconService(t, map[string]string{"/svg/jellyfin.svg": strings.Repeat("x", maxSlugIcon+1)})
	wantProblem(t, getIcon(op, "players/icon"), 502, "icon_fetch_failed")
}

func TestIconSlugDownloadedOnceForConcurrentCalls(t *testing.T) {
	t.Parallel()
	svc, _, set, _, _ := iconService(t, map[string]string{"/svg/jellyfin.svg": testSVG})
	set.mu.Lock()
	set.delay = 50 * time.Millisecond
	set.mu.Unlock()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ic, err := svc.icons.slug("jellyfin"); err != nil || string(ic.Data) != testSVG {
				t.Errorf("%+v %v", ic, err)
			}
		}()
	}
	wg.Wait()
	if n := set.hits.Load(); n != 1 {
		t.Errorf("%d downloads, want 1", n)
	}
}

func TestIconOfAService(t *testing.T) {
	t.Parallel()
	_, op, set, _, lookups := iconService(t, nil)

	// A file in the repo, read from the commit's tree, looked up once.
	wantIcon(t, getIcon(op, "players/icon?unit=jellyfin.service"), "image/svg+xml", testSVG)
	wantIcon(t, getIcon(op, "players/icon?unit=jellyfin.service"), "image/svg+xml", testSVG)
	if n := lookups.Load(); n != 1 {
		t.Errorf("%d store path lookups, want 1", n)
	}

	// An https URL is a redirect.
	w := getIcon(op, "players/icon?unit=plex.service")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://example.org/plex.svg" || w.Body.Len() != 0 {
		t.Errorf("got %d %v %q", w.Code, w.Header(), w.Body.String())
	}

	wantProblem(t, getIcon(op, "players/icon?unit=sonarr.service"), 404, "no_icon")
	wantProblem(t, getIcon(op, "nosuch/icon"), 404, "unknown_stack")
	if n := set.hits.Load(); n != 0 {
		t.Errorf("%d requests to the set", n)
	}
}

func TestIconNoneNamed(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	install(t, svc, fs, "aaaaaaa1", `{"version": 9, "name": "plain", "nixpkgs": "nixos-unstable"}`)
	op, _, _ := testAPIs(svc)
	wantProblem(t, getIcon(op, "plain/icon"), 404, "no_icon")
}

func TestIconUnapprovedReadsTheFetchedCommit(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	fs.set("bbbbbbb1", v9Manifest)
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	write(t, filepath.Join(tree, "icons", "jellyfin.svg"), testSVG)
	svc.icons.tree = func(_ context.Context, url string) (string, error) {
		if url != "github:o/r/bbbbbbb1" {
			t.Errorf("tree of %q, want the fetched commit", url)
		}
		return tree, nil
	}
	ic, err := svc.Icon(context.Background(), "players", "jellyfin.service")
	if err != nil || string(ic.Data) != testSVG {
		t.Errorf("%+v %v", ic, err)
	}
}

func TestIconFileRefusals(t *testing.T) {
	t.Parallel()
	tree, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(tree, "icons", "ok.png"), testPNG)
	write(t, filepath.Join(tree, "big.svg"), strings.Repeat("x", MaxIconFile+1))
	write(t, filepath.Join(outside, "secret.svg"), "secret")
	must(t, os.Symlink(filepath.Join(outside, "secret.svg"), filepath.Join(tree, "link.svg")))
	must(t, os.Symlink(outside, filepath.Join(tree, "dir")))
	must(t, os.Mkdir(filepath.Join(tree, "folder.svg"), 0o755))
	c := newIconCache("", func(context.Context, string) (string, error) { return tree, nil }, nil)

	for file, want := range map[string]string{
		"icons/ok.png":   "",
		"big.svg":        "invalid_icon",
		"link.svg":       "invalid_icon",
		"dir/secret.svg": "invalid_icon",
		"folder.svg":     "invalid_icon",
		"missing.svg":    "no_icon",
	} {
		ic, err := c.icon(context.Background(), Manifest{Version: 9, Icon: file}, "github:o/r/x", "")
		if want == "" {
			if err != nil || ic.Type != "image/png" || !bytes.Equal(ic.Data, []byte(testPNG)) {
				t.Errorf("%s: %+v %v", file, ic, err)
			}
			continue
		}
		var code string
		var p *httpx.Problem
		if errors.As(err, &p) {
			code = p.Code
		}
		if code != want || bytes.Contains(ic.Data, []byte("secret")) {
			t.Errorf("%s: %+v %v, want %s", file, ic, err, want)
		}
	}
}
