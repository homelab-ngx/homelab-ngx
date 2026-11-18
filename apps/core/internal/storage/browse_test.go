package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type folderList []string

func (f folderList) HostFolders(context.Context) ([]string, error) { return f, nil }

// browseHarness has no pools; its browse roots are <tmp>/pool/media and
// <tmp>/data, next to <tmp>/pool/private and <tmp>/secret.
func browseHarness(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	h.fake.On("lsblk").Reply(`{"blockdevices":[]}`)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"pool/media/films/old", "pool/media/music", "pool/private", "data", "secret"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "pool/media/a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.svc.UseStackFolders(folderList{filepath.Join(tmp, "pool/media"), filepath.Join(tmp, "data"), filepath.Join(tmp, "gone")})
	return h, tmp
}

func names(l Listing) []string {
	out := []string{}
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return out
}

func TestBrowseAboveRootsShowsOnlyTheWayDown(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()

	l, err := h.svc.Browse(ctx, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l); len(got) != 2 || got[0] != "data" || got[1] != "pool" || l.Root != "" {
		t.Fatalf("above roots: %v root %q, want [data pool] and no root", got, l.Root)
	}
	l, err = h.svc.Browse(ctx, filepath.Join(tmp, "pool"))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l); len(got) != 1 || got[0] != "media" {
		t.Fatalf("/pool: %v, want [media]", got)
	}
	if _, err := h.svc.Browse(ctx, "/"); err != nil {
		t.Fatalf("/: %v", err)
	}
}

func TestBrowseRefusesOutsideRoots(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	media := filepath.Join(tmp, "pool/media")
	if err := os.Symlink(filepath.Join(tmp, "secret"), filepath.Join(media, "out")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(tmp, "secret"),
		filepath.Join(tmp, "pool/private"),
		filepath.Join(tmp, "nothing"),
		media + "/../private",
		filepath.Join(media, "out"),
		"pool/media",
		"",
	} {
		if _, err := h.svc.Browse(ctx, p); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("Browse(%q) = %v, want ErrOutsideRoots", p, err)
		}
		if _, err := h.svc.BrowseUsage(ctx, p); !errors.Is(err, ErrOutsideRoots) {
			t.Errorf("BrowseUsage(%q) = %v, want ErrOutsideRoots", p, err)
		}
	}
	for _, p := range []string{filepath.Join(media, "nope"), filepath.Join(media, "a.txt")} {
		if _, err := h.svc.Browse(ctx, p); !errors.Is(err, ErrNoSuchFolder) {
			t.Errorf("Browse(%q) = %v, want ErrNoSuchFolder", p, err)
		}
	}
}

func TestBrowseListsARoot(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	media := filepath.Join(tmp, "pool/media")
	if err := os.Symlink(filepath.Join(tmp, "secret"), filepath.Join(media, "out")); err != nil {
		t.Fatal(err)
	}
	l, err := h.svc.Browse(context.Background(), media+"/films/..")
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != media || l.Root != media || l.Total != 4 || l.Truncated {
		t.Fatalf("listing = %+v", l)
	}
	types := map[string]string{}
	for _, e := range l.Entries {
		types[e.Name] = e.Type
		if e.Owner == "" || e.Mode == "" || e.ModifiedAt.IsZero() {
			t.Errorf("entry %+v lacks stat fields", e)
		}
		if e.Name == "a.txt" && e.Size != 5 {
			t.Errorf("a.txt size = %d", e.Size)
		}
	}
	if types["films"] != "dir" || types["a.txt"] != "file" || types["out"] != "symlink" {
		t.Fatalf("types = %v", types)
	}
}

func TestBrowseTruncates(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	data := filepath.Join(tmp, "data")
	for i := range browseLimit + 3 {
		if err := os.WriteFile(filepath.Join(data, "f"+time.Duration(i).String()), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := h.svc.Browse(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Truncated || l.Total != browseLimit+3 || len(l.Entries) != browseLimit {
		t.Fatalf("total %d, entries %d, truncated %v", l.Total, len(l.Entries), l.Truncated)
	}
}

func TestBrowseUsage(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	ctx := context.Background()
	media := filepath.Join(tmp, "pool/media")
	now := time.Now()
	h.svc.now = func() time.Time { return now }
	h.svc.onBcachefs = func(p string) bool { return p == media }
	h.fake.On("bcdu", "--json", "-d", "1000", media).Reply(`{"folders":[` +
		`{"path":"` + media + `","total":90,"exclusive":90,"shared":0},` +
		`{"path":"` + media + `/films","total":70,"exclusive":50,"shared":20},` +
		`{"path":"` + media + `/films/old","total":30,"exclusive":10,"shared":20},` +
		`{"path":"` + media + `/music","total":40,"exclusive":20,"shared":20}]}`)

	got, err := h.svc.BrowseUsage(ctx, media)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Folders) != 2 || got.Folders["films"].Exclusive != 50 || got.Folders["music"].Total != 40 {
		t.Fatalf("usage = %+v", got)
	}
	// A folder below is served from the same run.
	got, err = h.svc.BrowseUsage(ctx, filepath.Join(media, "films"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 1 || got.Folders["old"].Shared != 20 || !got.MeasuredAt.Equal(now) {
		t.Fatalf("usage below = %+v", got)
	}
	if n := len(h.fake.CallsMatching("bcdu")); n != 1 {
		t.Fatalf("bcdu ran %d times, want 1", n)
	}
	// An old run is measured again.
	now = now.Add(treeTTL)
	if _, err := h.svc.BrowseUsage(ctx, media); err != nil {
		t.Fatal(err)
	}
	if n := len(h.fake.CallsMatching("bcdu")); n != 2 {
		t.Fatalf("bcdu ran %d times after expiry, want 2", n)
	}

	// Off bcachefs and above a root there are no figures, and no run.
	for p, reason := range map[string]string{filepath.Join(tmp, "data"): "not_bcachefs", tmp: ""} {
		got, err := h.svc.BrowseUsage(ctx, p)
		if err != nil || got.Available || got.Reason != reason {
			t.Errorf("BrowseUsage(%q) = %+v, %v; want unavailable, reason %q", p, got, err, reason)
		}
	}
	if n := len(h.fake.CallsMatching("bcdu")); n != 2 {
		t.Fatalf("bcdu ran %d times, want still 2", n)
	}
}

func TestBrowseUsageFailedRun(t *testing.T) {
	t.Parallel()
	h, tmp := browseHarness(t)
	h.svc.onBcachefs = func(string) bool { return true }
	data := filepath.Join(tmp, "data")
	h.fake.On("bcdu", "--json", "-d", "1000", data).Fail("not root")
	got, err := h.svc.BrowseUsage(context.Background(), data)
	if err != nil || got.Available || got.Reason != "failed" {
		t.Fatalf("usage = %+v, %v; want unavailable, failed", got, err)
	}
}
