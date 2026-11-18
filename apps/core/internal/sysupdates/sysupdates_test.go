package sysupdates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/httpx"
)

func TestParseDiffClosures(t *testing.T) {
	t.Parallel()
	out := "AGENTS.md: ∅ → ε\n" +
		"SDL2_mixer: ∅ → 2.8.2, \x1b[31;1m415.5 KiB\x1b[0m\n" +
		"aardvark-dns: \x1b[31;1m34.5 KiB\x1b[0m\n" +
		"abseil-cpp: 20210324.2, 20260107.1 → 20260817.0, \x1b[32;1m-4.5 MiB\x1b[0m\n" +
		"jellyfin: 12.2 → 12.1\n" +
		"old-thing: 1.0 → ∅, -3.0 KiB\n"
	got := ParseDiffClosures(out)
	want := []UpdateChange{
		{Name: "SDL2_mixer", From: "", To: "2.8.2", Kind: "added"},
		{Name: "abseil-cpp", From: "20210324.2, 20260107.1", To: "20260817.0", Kind: "upgrade"},
		{Name: "jellyfin", From: "12.2", To: "12.1", Kind: "downgrade"},
		{Name: "old-thing", From: "1.0", To: "", Kind: "removed"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"1.9", "1.10", -1},
		{"7.2.9-1.39.6", "7.2.3-1.39.2", 1},
		{"1.0", "1.0", 0},
		{"1.0", "1.0.1", -1},
		// Image versions: builds before the first release are 0.0.<commit
		// time>, so every release sorts after them.
		{"0.0.202611180849", "0.0.202611181200", -1},
		{"0.1.0", "0.0.202611180849", 1},
		{"0.9.0", "0.10.0", -1},
		// A build of main after a release is <release>.<commit time>: above
		// the release it follows, below the next one.
		{"0.1.0.202611190130", "0.1.0", 1},
		{"0.1.0.202611190130", "0.1.1", -1},
		{"0.1.0.202611190130", "0.1.0.202611190200", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestBuildEventsClassifiesChangesAndRollbacks(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)
	gens := []Generation{
		{Number: 1, Time: t0.Add(-48 * time.Hour), System: "/s/a", Baseline: true},
		{Number: 2, Time: t0.Add(5 * time.Minute), System: "/s/b", Tiers: map[string]string{"plexmediaserver": "service", "jellyfin-web": "service", "ripgrep": "installed", "vector": "installed", "linux": "kernel"}, Changes: []UpdateChange{
			{Name: "plexmediaserver", From: "1.0", To: "1.1", Kind: "upgrade"},
			{Name: "jellyfin-web", From: "1", To: "2", Kind: "upgrade"},
			{Name: "ripgrep", From: "1", To: "2", Kind: "upgrade"},
			{Name: "glibc", From: "2.40", To: "2.41", Kind: "upgrade"},
			{Name: "vector", From: "0.58", To: "0.59", Kind: "upgrade"},
			{Name: "linux", From: "6.12", To: "6.13", Kind: "upgrade"},
		}},
		{Number: 3, Time: t0.Add(8 * time.Minute), System: "/s/a"},
	}
	evs := buildEvents(gens, "/s/a")
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	byID := map[string]UpdateEvent{}
	for _, e := range evs {
		byID[e.Id] = e
	}
	g2 := byID["gen-2"]
	if g2.Source != "manual" {
		t.Errorf("gen-2 source = %q, want manual", g2.Source)
	}
	tiers := map[string]string{}
	for _, c := range g2.Changes {
		tiers[c.Name] = c.Tier
	}
	want := map[string]string{"plexmediaserver": "service", "jellyfin-web": "service", "ripgrep": "installed", "vector": "installed", "linux": "kernel"}
	for k, v := range want {
		if tiers[k] != v {
			t.Errorf("%s = %s, want %s", k, tiers[k], v)
		}
	}
	// glibc is a dependency: counted, not listed.
	if _, ok := tiers["glibc"]; ok || g2.Dependencies != 1 || g2.TotalChanges != 5 {
		t.Errorf("gen-2 dependencies = %d, total %d, glibc listed %v", g2.Dependencies, g2.TotalChanges, ok)
	}
	if g3 := byID["gen-3"]; g3.Source != "rollback" || !g3.Current {
		t.Errorf("gen-3 = source %q current %v", g3.Source, g3.Current)
	}
	if evs[0].Id != "gen-3" {
		t.Errorf("newest event = %q, want gen-3", evs[0].Id)
	}

	tl := paginate(evs, Query{Days: 7, Loc: time.UTC})
	if tl.TotalEvents != 3 {
		t.Errorf("default TotalEvents = %d, want 3", tl.TotalEvents)
	}
	tl = paginate(evs, Query{Days: 7, Loc: time.UTC, Source: "rollback"})
	if tl.TotalEvents != 1 {
		t.Errorf("rollback source: %+v", tl)
	}
	tl = paginate(evs, Query{Days: 7, Loc: time.UTC, Q: "PLEX"})
	if tl.TotalEvents != 1 || len(tl.Days[0].Events[0].Changes) != 1 {
		t.Errorf("search: %+v", tl)
	}
	tl = paginate(evs, Query{Days: 1, Page: 2, Loc: time.UTC})
	if tl.Pages != 2 || tl.Page != 2 || tl.Days[0].Date != "2026-10-10" {
		t.Errorf("paging: pages %d page %d days %+v", tl.Pages, tl.Page, tl.Days)
	}
}

func TestDrvName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"jellyfin-12.2":                     "jellyfin",
		"plexmediaserver-1.43.4.10903-e552": "plexmediaserver",
		"immich-machine-learning-2.1.0":     "immich-machine-learning",
		"python3-3.14.7":                    "python3",
		"unit-script-foo-start":             "unit-script-foo-start",
	} {
		if got := drvName(in); got != want {
			t.Errorf("drvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSystemTiers(t *testing.T) {
	t.Parallel()
	sys := t.TempDir()
	h := func(name string) string { return "/nix/store/" + strings.Repeat("a", 32) + "-" + name }
	units := filepath.Join(sys, "etc/systemd/system")
	script := filepath.Join(sys, "aaaa-unit-script-redis-start")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(units, 0o755))
	must(os.WriteFile(filepath.Join(units, "jellyfin.service"),
		[]byte("[Service]\nExecStart="+h("jellyfin-12.2")+"/bin/jellyfin --flag\n"), 0o644))
	// A generated script: its shebang shell and core tools don't count.
	must(os.WriteFile(script, []byte("#!"+h("bash-5.3")+"/bin/bash\n"+h("coreutils-9.1")+"/bin/mkdir -p x\nexec "+h("redis-8.1")+"/bin/redis-server\n"), 0o755))
	must(os.WriteFile(filepath.Join(units, "redis.service"),
		[]byte("ExecStart=@"+script+"\n"), 0o644))
	must(os.Symlink(h("linux-6.12.1")+"/bzImage", filepath.Join(sys, "kernel")))
	// Installed: what sw/bin links to, script helpers left out.
	bin := filepath.Join(sys, "sw/bin")
	must(os.MkdirAll(bin, 0o755))
	must(os.Symlink(h("vector-0.59")+"/bin/vector", filepath.Join(bin, "vector")))
	must(os.Symlink(h("git-2.51.0")+"/bin/git", filepath.Join(bin, "git")))
	must(os.Symlink(h("git-2.51.0")+"/bin/git-shell", filepath.Join(bin, "git-shell")))
	must(os.Symlink(h("coreutils-9.1")+"/bin/ls", filepath.Join(bin, "ls")))

	got := systemTiers(sys)
	for name, want := range map[string]string{"jellyfin": "service", "redis": "service", "linux": "kernel", "vector": "installed", "git": "installed"} {
		if got[name] != want {
			t.Errorf("tier %s = %q, want %q (all: %v)", name, got[name], want, got)
		}
	}
	for _, name := range []string{"coreutils", "bash"} {
		if _, ok := got[name]; ok {
			t.Errorf("script helper %s has a tier", name)
		}
	}
}

func TestHTTPTimelineValidatesFilters(t *testing.T) {
	t.Parallel()
	m := New(nil, t.TempDir(), "")
	profilesFixture(t, m) // no generations; never the host's profiles
	mux := http.NewServeMux()
	NewHTTP(m).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "t", "1")})
	for path, want := range map[string]int{
		"/api/system-updates/timeline":                        http.StatusOK,
		"/api/system-updates/timeline?source=rollback":        http.StatusOK,
		"/api/system-updates/timeline?source=medium":          http.StatusUnprocessableEntity,
		"/api/system-updates/timeline?days=61":                http.StatusUnprocessableEntity,
		"/api/system-updates/timeline?page=0":                 http.StatusUnprocessableEntity,
		"/api/system-updates/timeline?q=linux&page=2&days=60": http.StatusOK,
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Errorf("%s: %d %s, want %d", path, w.Code, w.Body, want)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/system-updates/timeline", nil))
	var tl UpdateTimeline
	if err := json.Unmarshal(w.Body.Bytes(), &tl); err != nil || tl.Page != 1 || tl.Days == nil {
		t.Fatalf("defaults: %+v %v", tl, err)
	}
}

// profilesFixture points m at a temp profiles directory holding a
// system-<n>-link per given n, each to its own system directory.
func profilesFixture(t *testing.T, m *Module, gens ...int) string {
	t.Helper()
	m.profiles = filepath.Join(t.TempDir(), "profiles")
	if err := os.Mkdir(m.profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range gens {
		addGeneration(t, m.profiles, n)
	}
	return m.profiles
}

func addGeneration(t *testing.T, profiles string, n int) {
	t.Helper()
	sys := filepath.Join(filepath.Dir(profiles), fmt.Sprintf("sys-%d", n))
	if err := os.Mkdir(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sys, filepath.Join(profiles, fmt.Sprintf("system-%d-link", n))); err != nil {
		t.Fatal(err)
	}
}

func setMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func timelineGenerations(m *Module) []int {
	var gens []int
	for _, d := range m.Timeline(context.Background(), Query{Days: 365}).Days {
		for _, ev := range d.Events {
			if ev.Generation != 0 {
				gens = append(gens, ev.Generation)
			}
		}
	}
	slices.Sort(gens)
	return gens
}

func TestTimelineRescansOnlyWhenProfilesChange(t *testing.T) {
	t.Parallel()
	m, f := newExecModule(t)
	f.On("nix", "store", "diff-closures").Reply("jellyfin: 10.1 → 10.2\n")
	profiles := profilesFixture(t, m, 1)
	t0 := time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)
	setMtime(t, profiles, t0)
	if got := timelineGenerations(m); !slices.Equal(got, []int{1}) {
		t.Fatalf("generations = %v, want [1]", got)
	}

	// A new link with the directory mtime put back: no rescan.
	addGeneration(t, profiles, 2)
	setMtime(t, profiles, t0)
	if got := timelineGenerations(m); !slices.Equal(got, []int{1}) {
		t.Fatalf("unchanged directory rescanned: generations = %v", got)
	}

	setMtime(t, profiles, t0.Add(time.Second))
	if got := timelineGenerations(m); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("changed directory not rescanned: generations = %v", got)
	}
}

func TestScanRetriesFailedGenerationWithoutDirectoryChange(t *testing.T) {
	t.Parallel()
	m, f := newExecModule(t)
	f.On("nix", "store", "diff-closures").Fail("error: out of memory").Reply("jellyfin: 10.1 → 10.2\n")
	profiles := profilesFixture(t, m, 1, 2)
	setMtime(t, profiles, time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC))

	m.Scan(context.Background())
	if got := timelineGenerations(m); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("failed generation not retried: generations = %v", got)
	}
	if n := len(f.CallsMatching("nix", "store", "diff-closures")); n != 2 {
		t.Fatalf("diff-closures ran %d times, want 2 (fail, retry)", n)
	}
}

func TestNextRelease(t *testing.T) {
	t.Parallel()
	branches := []string{"25.11", "26.05", "26.11"}
	for _, c := range []struct {
		current string
		pinned  bool
		want    string
	}{
		{"26.05", false, "26.11"},
		{"26.11", false, ""},
		{"26.11", true, "26.11"}, // a pre-release snapshot moves onto its branch
		{"25.11", false, "26.11"},
	} {
		got, ok := nextRelease(c.current, c.pinned, branches)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("nextRelease(%s, %v) = %q, %v; want %q", c.current, c.pinned, got, ok, c.want)
		}
	}
}

func TestReleased(t *testing.T) {
	t.Parallel()
	at := func(s string) time.Time { tt, _ := time.Parse(time.DateOnly, s); return tt }
	for _, c := range []struct {
		r, at string
		want  bool
	}{
		{"26.11", "2026-11-20", false},
		{"26.11", "2026-12-01", true},
		{"26.05", "2026-05-31", false},
		{"26.05", "2026-06-01", true},
		{"bogus", "2030-01-01", false},
	} {
		if got := released(c.r, at(c.at)); got != c.want {
			t.Errorf("released(%s, %s) = %v", c.r, c.at, got)
		}
	}
	if releaseOf("nixos-26.05") != "26.05" || releaseOf("nixos-26.05-small") != "" || releaseOf("nixos-unstable") != "" {
		t.Error("releaseOf")
	}
}
