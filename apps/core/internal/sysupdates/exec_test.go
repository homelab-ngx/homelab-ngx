package sysupdates

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/notifications"
)

// lsRemote is git ls-remote output with the nixos-25.11, -26.05 and -26.11
// release branches, and branches that name no release.
const lsRemote = "a\trefs/heads/nixos-25.11\nb\trefs/heads/nixos-26.05\nc\trefs/heads/nixos-26.05-small\n" +
	"d\trefs/heads/nixos-26.11\ne\trefs/heads/nixos-unstable\n"

// writeLock writes a flake.lock whose nixpkgs input is the original given
// (e.g. {"ref":"nixos-26.05"}, or {"rev":"…"} for a pin) into a new repo dir.
func writeLock(t *testing.T, original string) string {
	t.Helper()
	repo := t.TempDir()
	lock := `{"root":"root","version":7,"nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs_2"}},` +
		`"nixpkgs_2":{"original":` + original + `}}}`
	if err := os.WriteFile(filepath.Join(repo, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// newExecModule returns a Module executing through a Fake. The base nixpkgs
// follows nixos-26.05; nixos-26.05 and nixos-26.11 branches exist.
func newExecModule(t *testing.T) (*Module, *cmdrunnertest.Fake) {
	t.Helper()
	f := cmdrunnertest.NewFake()
	f.On("git", "ls-remote").Reply(lsRemote)
	m := New(f, t.TempDir(), writeLock(t, `{"owner":"NixOS","repo":"nixpkgs","ref":"nixos-26.05","type":"github"}`))
	m.now = func() time.Time { return time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC) }
	return m, f
}

func TestReleaseCheckFindsNewerRelease(t *testing.T) {
	t.Parallel()
	m, f := newExecModule(t)
	ctx := context.Background()
	if st := m.Status(context.Background()); st.Upgrade != nil || st.CheckedAt != nil {
		t.Fatalf("before the check: upgrade %+v checked %v", st.Upgrade, st.CheckedAt)
	}
	m.CheckReleases(ctx)
	st := m.Status(context.Background())
	if st.Release.Current != "26.05" || st.Release.Pinned || st.Release.Follows == nil || *st.Release.Follows != "nixos-26.05" {
		t.Fatalf("release = %+v", st.Release)
	}
	if st.Upgrade == nil || st.Upgrade.Release != "26.11" || !st.Upgrade.PreRelease || st.CheckedAt == nil {
		t.Fatalf("upgrade = %+v", st.Upgrade)
	}
	if c := f.CallsMatching("git", "ls-remote")[0]; c.Args[2] != nixpkgsGit {
		t.Fatalf("call = %+v", c)
	}
}

// A base on nixos-unstable has no release to move to, and a repo without a
// readable flake.lock has no known release at all.
func TestReleaseUnknownOrUnstable(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("git", "ls-remote").Reply(lsRemote)
	for name, repo := range map[string]string{
		"unstable": writeLock(t, `{"ref":"nixos-unstable"}`),
		"no lock":  t.TempDir(),
		"no repo":  "",
	} {
		m := New(f, t.TempDir(), repo)
		m.CheckReleases(context.Background())
		if st := m.Status(context.Background()); st.Release.Current != "" || st.Upgrade != nil {
			t.Errorf("%s: release %+v upgrade %+v", name, st.Release, st.Upgrade)
		}
	}
}

func TestBaseInput(t *testing.T) {
	t.Parallel()
	if f, ok := baseInput(writeLock(t, `{"ref":"nixos-26.11"}`)); !ok || f != "nixos-26.11" {
		t.Fatalf("follows = %q %v", f, ok)
	}
	if f, ok := baseInput(writeLock(t, `{"rev":"7fd4004d0511991fff3a0524856a9be09e28c5f0"}`)); !ok || f != "" {
		t.Fatalf("pinned: follows = %q %v", f, ok)
	}
}

func TestRebootRunsSystemctl(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		m, f := newExecModule(t)
		ran := make(chan time.Time, 1)
		f.On("systemctl", "reboot").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) { ran <- time.Now(); return cmdrunner.Result{}, nil })
		start := time.Now()
		m.Reboot()
		synctest.Wait()
		select {
		case <-ran:
			t.Fatal("reboot ran before the response could go out")
		default:
		}
		if at := <-ran; at.Sub(start) != rebootDelay {
			t.Fatalf("systemctl reboot ran after %v, want %v", at.Sub(start), rebootDelay)
		}
	})
}

func TestRecordGenerationDiffsClosures(t *testing.T) {
	t.Parallel()
	// Resolved: the record resolves the link, and macOS's temp folder is
	// under a symlink (/tmp is /private/tmp).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prevSys, sys := filepath.Join(dir, "sys-1"), filepath.Join(dir, "sys-2")
	for _, d := range []string{prevSys, sys} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "system-2-link")
	if err := os.Symlink(sys, link); err != nil {
		t.Fatal(err)
	}
	f := cmdrunnertest.NewFake()
	f.On("nix", "store", "diff-closures").Reply("jellyfin: 10.1 → 10.2\n")
	m := New(f, dir, "")

	g, err := m.recordGeneration(context.Background(), 2, link, Generation{Number: 1, System: prevSys}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Changes) != 1 || g.Changes[0].Name != "jellyfin" || g.Changes[0].Kind != "upgrade" {
		t.Fatalf("changes = %+v", g.Changes)
	}
	c := f.Calls()[0]
	if c.Module != "updates" || !slices.Equal(c.Args, []string{"store", "diff-closures", prevSys, sys}) {
		t.Fatalf("call = %+v", c)
	}

	bad := cmdrunnertest.NewFake()
	bad.On("nix", "store", "diff-closures").Fail("error: path is not valid")
	m = New(bad, dir, "")
	if _, err := m.recordGeneration(context.Background(), 2, link, Generation{Number: 1, System: prevSys}, true); err == nil {
		t.Fatal("diff-closures failure must fail the record")
	}
}

type fakeNotifier struct {
	scope   string
	current []notifications.Problem
}

func (f *fakeNotifier) Reconcile(_ context.Context, scope string, current []notifications.Problem) (notifications.Result, error) {
	f.scope, f.current = scope, current
	return notifications.Result{}, nil
}

func TestAReleasedUpgradeIsNotifiedAndAPreReleaseIsNot(t *testing.T) {
	t.Parallel()
	m, _ := newExecModule(t)
	n := &fakeNotifier{}
	m.SetNotifier(n)
	ctx := context.Background()
	m.CheckReleases(ctx)

	m.notifyRelease(ctx) // 26.11 is a pre-release on 2026-11-20
	if n.scope != releaseScope || len(n.current) != 0 {
		t.Fatalf("pre-release: scope %q current %+v", n.scope, n.current)
	}
	m.now = func() time.Time { return time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC) }
	m.notifyRelease(ctx)
	if len(n.current) != 1 || n.current[0].Key != "nixos-release:26.11" || n.current[0].Title != "NixOS 26.11 is available" {
		t.Fatalf("released: %+v", n.current)
	}
}
