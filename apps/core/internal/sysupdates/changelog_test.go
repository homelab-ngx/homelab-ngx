package sysupdates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/httpx"
)

func TestParseChangelogSanitizes(t *testing.T) {
	t.Parallel()
	longText := strings.Repeat("a", maxChangeTextLen+20)
	longArea := strings.Repeat("b", maxChangeAreaLen+10)
	manyChanges := make([]ReleaseChange, 0, maxChangelogItems+3)
	for range maxChangelogItems + 3 {
		manyChanges = append(manyChanges, ReleaseChange{Area: "dashboard", Text: "change item"})
	}
	payload := struct {
		Releases []ReleaseNote `json:"releases"`
	}{
		Releases: []ReleaseNote{
			{Version: "0.4.2", Date: "2026-11-24", Changes: manyChanges, More: -3, Url: "javascript:alert(1)"},
			{Version: "bad-version", Date: "2026-11-25", Changes: []ReleaseChange{{Area: "host", Text: "ignored"}}},
			{Version: "0.4.6", Date: "2026-11-26", Changes: []ReleaseChange{
				{Area: longArea, Text: "  " + longText + "  "},
				{Area: "", Text: "   "},
				{Area: "Daemon", Text: "keeps valid text"},
			}, More: 2, Url: "https://github.com/homelab-ngx/homelab-ngx/releases/tag/v0.4.6"},
			{Version: "0.4.6", Date: "older-dup", Changes: []ReleaseChange{{Area: "cli", Text: "duplicate"}}},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseChangelog(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Releases) != 2 {
		t.Fatalf("got %d releases, want 2: %+v", len(got.Releases), got.Releases)
	}
	if got.Releases[0].Version != "0.4.6" || got.Releases[1].Version != "0.4.2" {
		t.Fatalf("order = %+v", got.Releases)
	}
	r0 := got.Releases[0]
	if len(r0.Changes) != 2 {
		t.Fatalf("r0 changes = %+v", r0.Changes)
	}
	if len([]rune(r0.Changes[0].Area)) != maxChangeAreaLen || r0.Changes[0].Area != strings.Repeat("b", maxChangeAreaLen) {
		t.Errorf("area = %q", r0.Changes[0].Area)
	}
	if len([]rune(r0.Changes[0].Text)) != maxChangeTextLen {
		t.Errorf("text len = %d, want %d", len([]rune(r0.Changes[0].Text)), maxChangeTextLen)
	}
	if r0.Changes[1].Area != "daemon" {
		t.Errorf("normalized area = %q, want daemon", r0.Changes[1].Area)
	}
	if r0.Url == "" {
		t.Error("valid https url dropped")
	}
	r1 := got.Releases[1]
	if r1.Url != "" {
		t.Errorf("non-https url kept: %q", r1.Url)
	}
	if len(r1.Changes) != maxChangelogItems || r1.More != 3 {
		t.Errorf("r1 len=%d more=%d, want %d and 3", len(r1.Changes), r1.More, maxChangelogItems)
	}
}

func TestChangelogFetchAndCache(t *testing.T) {
	root := imageHost(t, "0.4.5")
	useCore(t, "0.4.5")
	rel := newTestRelease(t, testKey(t), "0.4.6")
	body := `{"releases":[{"version":"0.4.6","date":"2026-11-26","changes":[{"area":"host","text":"add SMTP relay"}]},{"version":"0.4.5","date":"2026-11-26","changes":[{"area":"backups","text":"add backup preview"}]}]}`
	rel.mu.Lock()
	rel.files["changelog.json"] = []byte(body)
	rel.mu.Unlock()

	now := time.Unix(1700000000, 0)
	dir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Module{dir: dir, image: true, now: func() time.Time { return now }}
	c := m.CheckRelease(context.Background())
	if c.Error != "" || c.Latest != "0.4.6" {
		t.Fatalf("CheckRelease = %+v", c)
	}

	// A fresh daemon reads the cached changelog without calling the server again.
	m2 := &Module{dir: dir, image: true, now: func() time.Time { return now }}
	cl := m2.Changelog(context.Background())
	if len(cl.Releases) != 2 || cl.Releases[0].Version != "0.4.6" || cl.Releases[0].Changes[0].Text != "add SMTP relay" {
		t.Fatalf("cached changelog = %+v", cl)
	}

	// If the server later serves 404/bad JSON, CheckRelease keeps working and keeps the cache.
	rel.mu.Lock()
	rel.files["changelog.json"] = []byte("{not-json")
	rel.mu.Unlock()
	if c := m2.CheckRelease(context.Background()); c.Error != "" || c.Latest != "0.4.6" {
		t.Fatalf("CheckRelease after bad changelog = %+v", c)
	}
	cl2 := m2.Changelog(context.Background())
	if len(cl2.Releases) != 2 || cl2.Releases[0].Version != "0.4.6" {
		t.Fatalf("cache lost after bad fetch: %+v", cl2)
	}
}

func TestChangelogLazyFetchAndBackoff(t *testing.T) {
	root := imageHost(t, "0.4.6")
	useCore(t, "0.4.6")
	var hits atomic.Int32
	var ok atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/changelog.json" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		if !ok.Load() {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"releases":[{"version":"0.4.6","date":"2026-11-26","changes":[{"area":"daemon","text":"cached lazily"}]}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(releaseURLEnv, srv.URL)

	// Non-image hosts never touch the network.
	nonImg := &Module{dir: t.TempDir(), image: false, now: time.Now}
	if cl := nonImg.Changelog(context.Background()); cl.Releases == nil || len(cl.Releases) != 0 {
		t.Fatalf("non-image changelog = %+v", cl)
	}
	if hits.Load() != 0 {
		t.Fatalf("non-image host hit server %d times", hits.Load())
	}

	now := time.Unix(1700000000, 0)
	dir := filepath.Join(root, "state")
	_ = os.MkdirAll(dir, 0o755)
	m := &Module{dir: dir, image: true, now: func() time.Time { return now }}

	// First miss fetches once; second immediate call respects retry backoff.
	_ = m.Changelog(context.Background())
	_ = m.Changelog(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("hits before backoff = %d, want 1", hits.Load())
	}

	ok.Store(true)
	now = now.Add(changelogRetryEvery + time.Second)
	cl := m.Changelog(context.Background())
	if hits.Load() != 2 || len(cl.Releases) != 1 || cl.Releases[0].Version != "0.4.6" {
		t.Fatalf("after backoff: hits=%d cl=%+v", hits.Load(), cl)
	}
}

func TestHTTPGetChangelog(t *testing.T) {
	root := imageHost(t, "0.4.6")
	useCore(t, "0.4.6")
	dir := filepath.Join(root, "state")
	_ = os.MkdirAll(dir, 0o755)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/changelog.json" {
			_, _ = w.Write([]byte(`{"releases":[{"version":"0.4.6","date":"2026-11-26","changes":[{"area":"dashboard","text":"show release changelog"}]}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(releaseURLEnv, srv.URL)

	m := &Module{dir: dir, image: true, now: time.Now}
	mux := http.NewServeMux()
	NewHTTP(m).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/system-updates/changelog", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var out ReleaseChangelog
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Releases) != 1 || out.Releases[0].Version != "0.4.6" {
		t.Fatalf("body = %+v", out)
	}
}
