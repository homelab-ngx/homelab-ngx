package drafts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store/storetest"
)

func init() { stacks.SetPrivateDomain("example.test") }

// fakeStacks is the installed stacks; fetched records the Fetch calls.
type fakeStacks struct {
	list    []stacks.Stack
	fetched []string
}

func (f *fakeStacks) List(context.Context) ([]stacks.Stack, error) { return f.list, nil }
func (f *fakeStacks) Fetch(_ context.Context, name string) (stacks.Stack, error) {
	f.fetched = append(f.fetched, name)
	return stacks.Stack{Name: name}, nil
}

func newService(t *testing.T) (*Service, *fakeStacks) {
	t.Helper()
	st, _ := storetest.Open(t)
	fs := &fakeStacks{}
	s := New(filepath.Join(t.TempDir(), "drafts"), cmdrunner.OS{}, st, fs, stacks.GPU{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, fs
}

func code(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestCreateStartsFromTheTemplate(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	if d.Files != Template("site") || len(d.Rev) != 40 || d.Source != "git+file://"+filepath.Join(s.Dir(), "site") {
		t.Fatalf("draft = %+v", d)
	}
	if !strings.Contains(d.Files.Manifest, `"name": "site"`) {
		t.Fatalf("the template keeps its placeholder: %s", d.Files.Manifest)
	}
	if !stacks.ValidSource(d.Source) {
		t.Fatalf("%s is not a source the stacks accept", d.Source)
	}
	// The template passes its own check.
	c, err := s.Check(ctx, "site", nil)
	if err != nil || !c.OK || !c.Saved || c.Rev != d.Rev {
		t.Fatalf("check = %+v, %v", c, err)
	}
	if _, err := s.Create(ctx, "site"); code(err) != "draft_exists" {
		t.Fatalf("second create: %v", err)
	}
	if _, err := s.Create(ctx, "Bad_Name"); code(err) != "invalid_name" {
		t.Fatalf("bad name: %v", err)
	}
	l, err := s.List(ctx)
	if err != nil || len(l) != 1 || l[0].Name != "site" || l[0].Rev != d.Rev {
		t.Fatalf("list = %+v, %v", l, err)
	}
}

func TestCreateRefusesAnInstalledStacksName(t *testing.T) {
	s, fs := newService(t)
	fs.list = []stacks.Stack{{Name: "immich", Source: "github:o/immich"}}
	if _, err := s.Create(context.Background(), "immich"); code(err) != "stack_exists" {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveCommitsOnlyAChange(t *testing.T) {
	s, fs := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.Save(ctx, "site", DraftSave{Files: d.Files})
	if err != nil || same.Rev != d.Rev {
		t.Fatalf("an unchanged save made a commit: %v", err)
	}
	f := d.Files
	f.Flake = strings.Replace(f.Flake, "autoindex on;", "autoindex off;", 1)
	saved, err := s.Save(ctx, "site", DraftSave{Files: f, Message: "No index"})
	if err != nil || saved.Rev == d.Rev || saved.Files != f {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	msg, err := s.git(ctx, s.repo("site"), "log", "-1", "--format=%s %an")
	if err != nil || msg != "No index homelab dashboard" {
		t.Fatalf("commit = %q, %v", msg, err)
	}
	if len(fs.fetched) != 0 {
		t.Fatalf("fetched %v with nothing installed", fs.fetched)
	}

	// Installed, a save makes the stack read the new commit.
	fs.list = []stacks.Stack{{Name: "site", Source: d.Source}}
	f.Flake += "\n"
	if _, err := s.Save(ctx, "site", DraftSave{Files: f}); err != nil {
		t.Fatal(err)
	}
	if len(fs.fetched) != 1 || fs.fetched[0] != "site" {
		t.Fatalf("fetched = %v", fs.fetched)
	}
}

func TestCheckReportsManifestErrorsWithTheirField(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	f := d.Files
	f.Manifest = strings.Replace(f.Manifest, `"port": 8080 }]`, `"port": 0 }]`, 1)
	f.Flake = "{ outputs = _: { }; }"
	c, err := s.Check(ctx, "site", &f)
	if err != nil {
		t.Fatal(err)
	}
	if c.OK || c.Saved || len(c.Problems) != 2 {
		t.Fatalf("check = %+v", c)
	}
	if p := c.Problems[0]; p.File != stacks.ManifestFile || !strings.Contains(p.Message, "routes[0]") {
		t.Fatalf("manifest problem = %+v", p)
	}
	if p := c.Problems[1]; p.File != stacks.FlakeFile || !strings.Contains(p.Message, "nixosModules.default") {
		t.Fatalf("flake problem = %+v", p)
	}
	// The last check is kept on the draft.
	got, err := s.Get(ctx, "site")
	if err != nil || got.Check == nil || got.Check.OK {
		t.Fatalf("draft check = %+v, %v", got.Check, err)
	}
	// Unknown fields are refused, as at install.
	if p := CheckFiles("site", DraftFiles{Manifest: `{"version": 8, "nmae": "x"}`, Flake: "nixosModules"}); len(p) != 1 || !strings.Contains(p[0].Message, "nmae") {
		t.Fatalf("unknown field: %+v", p)
	}
}

func TestDeleteRefusesAnInstalledDraft(t *testing.T) {
	s, fs := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	fs.list = []stacks.Stack{{Name: "site", Source: d.Source + "?ref=HEAD"}}
	if err := s.Delete(ctx, "site"); code(err) != "draft_installed" {
		t.Fatalf("delete installed: %v", err)
	}
	fs.list = nil
	if err := s.Delete(ctx, "site"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "site"); code(err) != "unknown_draft" {
		t.Fatalf("get after delete: %v", err)
	}
	// The name is free again.
	if _, err := s.Create(ctx, "site"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildKeepsTheResultAndNixsError(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var got stacks.FileBuild
	s.build = func(_ context.Context, b stacks.FileBuild) (string, error) {
		got = b
		b.OnLine("evaluating…")
		<-release
		b.OnLine("\x1b[31;1merror:\x1b[0m attribute 'nginxx' missing")
		b.OnLine("       at «stack»/flake.nix:12:9:")
		b.OnLine("some later line")
		return "", errors.New("nix build failed (exit 1)")
	}
	running, err := s.Build(ctx, "site")
	if err != nil || running.Build == nil || running.Build.Result != BuildRunning {
		t.Fatalf("build = %+v, %v", running.Build, err)
	}
	if _, err := s.Build(ctx, "site"); code(err) != "build_running" {
		t.Fatalf("second build: %v", err)
	}
	if err := s.Delete(ctx, "site"); code(err) != "build_running" {
		t.Fatalf("delete while building: %v", err)
	}
	close(release)
	var b *DraftBuild
	for range 200 {
		v, err := s.Get(ctx, "site")
		if err != nil {
			t.Fatal(err)
		}
		if b = v.Build; b.Result != BuildRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.Result != BuildFailed || b.Rev != d.Rev || b.FinishedAt == nil {
		t.Fatalf("build = %+v", b)
	}
	if b.Error != "error: attribute 'nginxx' missing\n       at «stack»/flake.nix:12:9:" {
		t.Fatalf("error = %q", b.Error)
	}
	if !strings.Contains(b.Log, "evaluating…") {
		t.Fatalf("log = %q", b.Log)
	}
	if got.Name != "site" || got.Ref != d.Source+"?ref=main&rev="+d.Rev {
		t.Fatalf("built %+v", got)
	}

	// A build stored as running that no daemon runs was interrupted.
	s.storeBuild(ctx, "site", DraftBuild{Rev: d.Rev, Result: BuildRunning})
	v, err := s.Get(ctx, "site")
	if err != nil || v.Build.Result != BuildInterrupted {
		t.Fatalf("stale build = %+v, %v", v.Build, err)
	}
}

func TestNixErrorFallsBackToTheError(t *testing.T) {
	if got := nixError("copying\n", errors.New("copying the files: boom")); got != "copying the files: boom" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckParsesFlakeNix(t *testing.T) {
	if _, err := exec.LookPath("nix-instantiate"); err != nil {
		t.Skip("nix-instantiate is not installed")
	}
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	files := d.Files
	files.Flake = "{{{{\n  outputs !!= _: { nixosModules.default = { }; };\n}\n"
	c, err := s.Check(ctx, "site", &files)
	if err != nil {
		t.Fatal(err)
	}
	if c.OK || len(c.Problems) != 1 || c.Problems[0].File != "flake.nix" || !strings.Contains(c.Problems[0].Message, "syntax error") ||
		!strings.Contains(c.Problems[0].Message, "(line 1, column 2)") {
		t.Fatalf("check = %+v", c)
	}
	if c, _ := s.Check(ctx, "site", &d.Files); !c.OK {
		t.Fatalf("the template: %+v", c)
	}
}
