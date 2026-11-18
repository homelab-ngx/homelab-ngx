package stacks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/services"
	"homelab-ngx.com/core/internal/store"
)

func attempts(t *testing.T, svc *Service) []StackUpdate {
	t.Helper()
	l, err := svc.Updates(context.Background(), "immich", 0)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAttemptsRecordInstallAndUpdateWithTheirPackageChanges(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	h.On("nix", "--extra-experimental-features", "nix-command", "store", "diff-closures").
		Reply("immich: 2.3.0 → 2.4.0, +1.2 MiB\n")
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	finished(t, svc, svc.Update)

	l := attempts(t, svc)
	if len(l) != 2 {
		t.Fatalf("attempts = %+v", l)
	}
	up, in := l[0], l[1]
	if in.Kind != "install" || in.Result != "healthy" || in.ToGeneration != 1 || in.FromGeneration != 0 || in.FinishedAt == nil {
		t.Errorf("install = %+v", in)
	}
	if up.Kind != "update" || up.Result != "healthy" || up.Rev != "bbbbbbb2" || up.FromGeneration != 1 || up.ToGeneration != 2 {
		t.Errorf("update = %+v", up)
	}
	if len(up.Changes) != 1 || up.Changes[0].Name != "immich" || up.Changes[0].To != "2.4.0" {
		t.Errorf("changes = %+v", up.Changes)
	}
	one, err := svc.UpdateAttempt(context.Background(), up.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.Log, "building bbbbbbb2") || !strings.Contains(one.Log, "running generation 2") {
		t.Errorf("log:\n%s", one.Log)
	}
}

func TestAttemptsRecordARollbackAndItsReason(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	svc.healthTimeout = 0
	finished(t, svc, svc.Start)
	h.unhealthy["/nix/store/bbbbbbb2-system"] = true
	release(t, svc, fs, "bbbbbbb2", strings.Replace(immichManifest, `"port": 2283, "max_body": "50GB"`, `"port": 3001`, 1))
	finished(t, svc, svc.Update)

	up := attempts(t, svc)[0]
	if up.Result != "rolled_back" || !strings.Contains(up.Reason, "immich-server.service is failed") || up.ToGeneration != 2 {
		t.Errorf("attempt = %+v", up)
	}
}

func TestAttemptsRecordAFailedBuild(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	h.On("nix", "--extra-experimental-features", "nix-command flakes", "build").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		c.OnLine("stderr", "error: builder for '/nix/store/x.drv' failed")
		return fail(1, "")
	})
	finished(t, svc, svc.Update)

	up := attempts(t, svc)[0]
	if up.Result != "build_failed" || up.ToGeneration != 0 {
		t.Errorf("attempt = %+v", up)
	}
	one, _ := svc.UpdateAttempt(context.Background(), up.Id)
	if !strings.Contains(one.Log, "builder for '/nix/store/x.drv' failed") {
		t.Errorf("log:\n%s", one.Log)
	}
}

func TestADaemonRestartMarksAttemptsInterrupted(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	ctx := context.Background()
	if _, err := svc.st.Queries().InsertStackUpdate(ctx, store.InsertStackUpdateParams{Stack: "immich", Kind: "update", Now: 1}); err != nil {
		t.Fatal(err)
	}
	svc.StartWanted(ctx)
	if a := attempts(t, svc)[0]; a.Result != "interrupted" || a.FinishedAt == nil {
		t.Errorf("attempt = %+v", a)
	}
}

func TestRollbackToAChosenGeneration(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	finished(t, svc, svc.Start)
	release(t, svc, fs, "bbbbbbb2", immichManifest)
	finished(t, svc, svc.Update)
	release(t, svc, fs, "ccccccc3", immichManifest)
	finished(t, svc, svc.Update)

	ctx := context.Background()
	_, err := svc.Rollback(ctx, "immich", 3)
	wantCode(t, err, 409, "generation_unavailable")
	_, err = svc.Rollback(ctx, "immich", 9)
	wantCode(t, err, 409, "generation_unavailable")

	s := finished(t, svc, func(ctx context.Context, name string) (Stack, error) { return svc.Rollback(ctx, name, 1) })
	if s.Generation != 1 || h.current != 1 || s.Operation.Error != "" {
		t.Errorf("generation=%d profile=%d operation=%+v", s.Generation, h.current, s.Operation)
	}
	if a := attempts(t, svc)[0]; a.Kind != "rollback" || a.FromGeneration != 3 || a.ToGeneration != 1 || a.Rev != "aaaaaaa1" || a.Result != "healthy" {
		t.Errorf("attempt = %+v", a)
	}
}

func TestUpdateAllSkipsStacksThatWaitForApproval(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newRuntime(t)
	finished(t, svc, svc.Start)
	ctx := context.Background()

	// A commit that asks for a new grant is not approved, so nothing runs.
	fs.set("bbbbbbb2", strings.Replace(immichManifest, `{"to": "host", "port": 25}`, `{"to": "host", "port": 25}, {"to": "192.168.1.102/32", "port": 1883}`, 1))
	if _, err := svc.Fetch(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if s, _ := svc.Get(ctx, "immich"); s.UpdateState != NeedsApproval {
		t.Fatalf("update state = %s", s.UpdateState)
	}
	if q, err := svc.UpdateAll(ctx); err != nil || len(q) != 0 {
		t.Fatalf("queued = %v, %v", q, err)
	}

	release(t, svc, fs, "ccccccc3", immichManifest)
	if s, _ := svc.Get(ctx, "immich"); s.UpdateState != CommitsAvailable {
		t.Fatalf("update state = %s", s.UpdateState)
	}
	q, err := svc.UpdateAll(ctx)
	if err != nil || len(q) != 1 || q[0] != "immich" {
		t.Fatalf("queued = %v, %v", q, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, _ := svc.Get(ctx, "immich")
		if s.Generation == 2 && s.Operation != nil && s.Operation.FinishedAt != nil {
			if s.UpdateState != UpToDate {
				t.Errorf("update state after = %s", s.UpdateState)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("update all did not update immich: %+v", s.Operation)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUsageComesFromTheSampler(t *testing.T) {
	t.Parallel()
	svc, _, _ := newRuntime(t)
	finished(t, svc, svc.Start)
	ctx := context.Background()
	if got := svc.Measured(ctx); len(got) != 1 || got[0].Name != "immich" || !slices.Contains(got[0].Units, "immich-server.service") {
		t.Fatalf("measured = %+v", got)
	}
	svc.SetUsage(func(key string) *services.Usage {
		switch key {
		case "immich":
			return &services.Usage{CpuPercent: 50, MemoryBytes: 3000}
		case "immich/immich-server.service":
			return &services.Usage{MemoryBytes: 600}
		}
		return nil
	})
	if u := svc.usageOf("immich"); u == nil || u.MemoryBytes != 3000 || u.CpuPercent != 50 {
		t.Fatalf("stack usage = %+v", u)
	}
	for _, x := range svc.Units(ctx) {
		if x.Unit == "immich-server.service" && (x.Usage == nil || x.Usage.MemoryBytes != 600) {
			t.Errorf("unit usage = %+v", x.Usage)
		}
	}
}

func TestGitHistoryReadsALocalCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	commit := func(msg string) string {
		_ = os.WriteFile(filepath.Join(repo, "f"), []byte(msg), 0o644)
		run("add", "f")
		run("commit", "-q", "-m", msg)
		return run("rev-parse", "HEAD")
	}
	first := commit("first")
	commit("second")
	head := commit("third")

	// The test runs as the checkout's owner; impersonating it needs root.
	old := ownerOf
	ownerOf = func(string) string { return "" }
	t.Cleanup(func() { ownerOf = old })
	h := gitHistory{r: cmdrunner.OS{}}
	ctx := context.Background()
	got, dirty, err := h.Log(ctx, "x", "git+file://"+repo, first, head)
	if err != nil {
		t.Fatal(err)
	}
	if dirty || len(got) != 2 || got[0].Subject != "third" || got[1].Subject != "second" || got[0].Rev != head {
		t.Fatalf("commits = %+v dirty=%v", got, dirty)
	}
	_ = os.WriteFile(filepath.Join(repo, "f"), []byte("edited"), 0o644)
	if _, dirty, _ := h.Log(ctx, "x", "git+file://"+repo, head, head); !dirty {
		t.Error("an edited checkout is not dirty")
	}
	// An unknown since lists the last commits instead.
	if got, _, _ := h.Log(ctx, "x", "git+file://"+repo, strings.Repeat("0", 40), head); len(got) != 3 {
		t.Errorf("commits = %+v", got)
	}
}

func TestRemoteOfASource(t *testing.T) {
	t.Parallel()
	for src, want := range map[string][3]string{
		"github:example/stack-media":                     {"https://github.com/example/stack-media.git", "HEAD", ""},
		"github:example/stack-media/dev":                 {"https://github.com/example/stack-media.git", "dev", ""},
		"git+https://git.example/a/b.git?ref=main":       {"https://git.example/a/b.git", "main", ""},
		"git+file:///home/alice/repos/homelab-media":     {"", "", "/home/alice/repos/homelab-media"},
		"git+ssh://git@github.com/example/x.git?ref=dev": {"ssh://git@github.com/example/x.git", "dev", ""},
	} {
		u, ref, local, err := remote(src)
		if err != nil || [3]string{u, ref, local} != want {
			t.Errorf("remote(%q) = %q %q %q %v", src, u, ref, local, err)
		}
	}
}

func TestALocalSourceReadsItsCommittedTreeAsItsOwner(t *testing.T) {
	old := ownerOf
	ownerOf = func(string) string { return "alice" }
	t.Cleanup(func() { ownerOf = old })
	if got := committedRef("git+file:///home/alice/repos/x"); got != "git+file:///home/alice/repos/x?ref=HEAD" {
		t.Errorf("committedRef = %q", got)
	}
	if got := committedRef("git+file:///home/alice/repos/x?ref=dev"); got != "git+file:///home/alice/repos/x?ref=dev" {
		t.Errorf("committedRef with a ref = %q", got)
	}
	if got := committedRef("github:o/r"); got != "github:o/r" {
		t.Errorf("committedRef of a remote = %q", got)
	}

	h := newFakeHost()
	h.On("nix", "--extra-experimental-features", "nix-command flakes", "flake", "prefetch").
		Reply(`{"hash":"sha256-abc+/=","storePath":"/nix/store/xyz-source","locked":{"type":"git","rev":"r1"}}`)
	svc := &Service{r: h}
	ref, err := svc.buildRef(context.Background(), "git+file:///home/alice/repos/x?ref=HEAD&rev=r1")
	if err != nil || ref != "path:/nix/store/xyz-source?narHash=sha256-abc%2B%2F%3D" {
		t.Fatalf("buildRef = %q, %v", ref, err)
	}
	calls := h.CallsMatching("nix")
	if len(calls) != 1 || calls[0].AsUser != "alice" {
		t.Errorf("prefetch calls = %+v", calls)
	}
	if ref, _ := svc.buildRef(context.Background(), "github:o/r/r1"); ref != "github:o/r/r1" {
		t.Errorf("a remote is built as is: %q", ref)
	}
}
