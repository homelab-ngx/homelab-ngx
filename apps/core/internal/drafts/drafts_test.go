package drafts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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
// tree is every stack's approved commit; moveErr fails SetSource.
type fakeStacks struct {
	list    []stacks.Stack
	fetched []string
	tree    string
	moveErr error
}

func (f *fakeStacks) ApprovedTree(_ context.Context, name string) (string, string, string, error) {
	for _, st := range f.list {
		if st.Name == name {
			return f.tree, st.Source, "abcdef1234567", nil
		}
	}
	return "", "", "", errors.New("no such stack")
}

func (f *fakeStacks) SetSource(_ context.Context, name, source string) (stacks.Stack, error) {
	if f.moveErr != nil {
		return stacks.Stack{}, f.moveErr
	}
	for i := range f.list {
		if f.list[i].Name == name {
			f.list[i].Source = source
		}
	}
	return stacks.Stack{Name: name, Source: source}, nil
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

// file is the text of the draft's file at p; "" when it has none.
func file(d Draft, p string) string {
	for _, f := range d.Files {
		if f.Path == p {
			return f.Content
		}
	}
	return ""
}

func paths(d Draft) []string {
	var out []string
	for _, f := range d.Files {
		out = append(out, f.Path)
	}
	return out
}

func write(p, content string) DraftChange { return DraftChange{Path: p, Content: &content} }

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
	tmpl := Template("site")
	if strings.Join(paths(d), " ") != "flake.nix homelab-stack.json" || file(d, "flake.nix") != tmpl["flake.nix"] ||
		len(d.Rev) != 40 || d.Source != "git+file://"+filepath.Join(s.Dir(), "site") {
		t.Fatalf("draft = %+v", d)
	}
	if !strings.Contains(file(d, "homelab-stack.json"), `"name": "site"`) {
		t.Fatalf("the template keeps its placeholder: %s", file(d, "homelab-stack.json"))
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
	same, err := s.Save(ctx, "site", DraftSave{Changes: []DraftChange{write("flake.nix", file(d, "flake.nix"))}})
	if err != nil || same.Rev != d.Rev {
		t.Fatalf("an unchanged save made a commit: %v", err)
	}
	flake := strings.Replace(file(d, "flake.nix"), "autoindex on;", "autoindex off;", 1)
	saved, err := s.Save(ctx, "site", DraftSave{Changes: []DraftChange{write("flake.nix", flake)}, Message: "No index"})
	if err != nil || saved.Rev == d.Rev || file(saved, "flake.nix") != flake {
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
	if _, err := s.Save(ctx, "site", DraftSave{Changes: []DraftChange{write("flake.nix", flake+"\n")}}); err != nil {
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
	changes := []DraftChange{
		write("homelab-stack.json", strings.Replace(file(d, "homelab-stack.json"), `"port": 8080 }]`, `"port": 0 }]`, 1)),
		write("flake.nix", "{ outputs = _: { }; }"),
	}
	c, err := s.Check(ctx, "site", &changes)
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
	if p := CheckFiles("site", `{"version": 8, "nmae": "x"}`, "nixosModules"); len(p) != 1 || !strings.Contains(p[0].Message, "nmae") {
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
	changes := []DraftChange{write("flake.nix", "{{{{\n  outputs !!= _: { nixosModules.default = { }; };\n}\n")}
	c, err := s.Check(ctx, "site", &changes)
	if err != nil {
		t.Fatal(err)
	}
	if c.OK || len(c.Problems) != 1 || c.Problems[0].File != "flake.nix" || !strings.Contains(c.Problems[0].Message, "syntax error") ||
		!strings.Contains(c.Problems[0].Message, "(line 1, column 2)") {
		t.Fatalf("check = %+v", c)
	}
	if c, _ := s.Check(ctx, "site", &[]DraftChange{}); !c.OK {
		t.Fatalf("the template: %+v", c)
	}
	// Another Nix file is parsed when it changed, under its own path.
	changes = []DraftChange{write("modules/web.nix", "{ x = ; }\n")}
	c, err = s.Check(ctx, "site", &changes)
	if err != nil || c.OK || len(c.Problems) != 1 || c.Problems[0].File != "modules/web.nix" {
		t.Fatalf("check = %+v, %v", c, err)
	}
	if _, err := s.Save(ctx, "site", DraftSave{Changes: changes}); err != nil {
		t.Fatal(err)
	}
	// A check of the saved files parses every Nix file.
	if c, _ := s.Check(ctx, "site", nil); c.OK || len(c.Problems) != 1 || c.Problems[0].File != "modules/web.nix" {
		t.Fatalf("saved check = %+v", c)
	}
	_ = d
}

func TestSaveAddsRenamesAndDeletesFiles(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	d, err := s.Create(ctx, "site")
	if err != nil {
		t.Fatal(err)
	}
	// A binary file in the repo is listed, kept and never sent.
	png := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
	if err := os.WriteFile(filepath.Join(s.repo("site"), "logo.png"), png, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := s.git(ctx, s.repo("site"), "add", "logo.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.commit(ctx, "site", "Logo"); err != nil {
		t.Fatal(err)
	}

	d, err = s.Save(ctx, "site", DraftSave{Changes: []DraftChange{
		write("modules/web/default.nix", "{ }\n"),
		write("README", "hello\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths(d), " "); got != "README flake.nix homelab-stack.json logo.png modules/web/default.nix" {
		t.Fatalf("files = %s", got)
	}
	for _, f := range d.Files {
		if f.Path == "logo.png" && (!f.Binary || f.Content != "" || f.Size != int64(len(png))) {
			t.Fatalf("logo = %+v", f)
		}
	}
	// A rename is a delete and a write; the emptied folder goes.
	d, err = s.Save(ctx, "site", DraftSave{Changes: []DraftChange{
		{Path: "modules/web/default.nix", Delete: true},
		write("web.nix", "{ }\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths(d), " "); got != "README flake.nix homelab-stack.json logo.png web.nix" {
		t.Fatalf("files = %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.repo("site"), "modules")); !os.IsNotExist(err) {
		t.Fatalf("modules/ is left: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.repo("site"), "logo.png")); string(b) != string(png) {
		t.Fatal("the binary file changed")
	}
	status, err := s.git(ctx, s.repo("site"), "status", "--porcelain")
	if err != nil || status != "" {
		t.Fatalf("the working tree is not committed: %q, %v", status, err)
	}

	// Refused changes leave the draft as it was.
	for _, tc := range []struct {
		change DraftChange
		code   string
	}{
		{DraftChange{Path: "flake.nix", Delete: true}, "required_file"},
		{DraftChange{Path: "nothing.nix", Delete: true}, "unknown_file"},
		{write("../escape", "x"), "invalid_path"},
		{write("/etc/x", "x"), "invalid_path"},
		{write("a//b", "x"), "invalid_path"},
		{write(".git/config", "x"), "invalid_path"},
		{write("sub/.git/hooks/x", "x"), "invalid_path"},
		{write("README/x", "x"), "path_taken"},
		{write("web.nix/x", "x"), "path_taken"},
		{DraftChange{Path: "README"}, "invalid_change"},
		{write(strings.Repeat("a", maxPath+1), "x"), "invalid_path"},
		{write("big", strings.Repeat("a", maxFile+1)), "file_too_large"},
	} {
		if _, err := s.Save(ctx, "site", DraftSave{Changes: []DraftChange{write("ok.nix", "{ }\n"), tc.change}}); code(err) != tc.code {
			t.Errorf("%s: err = %v, want %s", tc.change.Path, err, tc.code)
		}
	}
	after, err := s.Get(ctx, "site")
	if err != nil || after.Rev != d.Rev || len(after.Files) != len(d.Files) {
		t.Fatalf("a refused save changed the draft: %+v, %v", paths(after), err)
	}
}

func TestSaveRefusesTooManyFiles(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "site"); err != nil {
		t.Fatal(err)
	}
	var changes []DraftChange
	for i := range maxFiles {
		changes = append(changes, write(fmt.Sprintf("f/%d", i), "x"))
	}
	if _, err := s.Save(ctx, "site", DraftSave{Changes: changes}); code(err) != "too_many_files" {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateFromCopiesATree(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	src := t.TempDir()
	tmpl := Template("photos")
	for p, content := range map[string]string{
		"homelab-stack.json": tmpl["homelab-stack.json"], "flake.nix": tmpl["flake.nix"],
		"modules/app.nix": "{ }\n", ".git/HEAD": "ref: refs/heads/main\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(content), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := s.createFrom(ctx, "photos", src, "Copy of github:o/photos at abc", ""); err != nil {
		t.Fatal(err)
	}
	d, err := s.Get(ctx, "photos")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(paths(d), " "); got != "flake.nix homelab-stack.json modules/app.nix run.sh" {
		t.Fatalf("files = %s", got)
	}
	if fi, err := os.Stat(filepath.Join(s.repo("photos"), "run.sh")); err != nil || fi.Mode().Perm() != 0o750 {
		t.Fatalf("run.sh = %v, %v", fi.Mode(), err)
	}
	if msg, _ := s.git(ctx, s.repo("photos"), "log", "-1", "--format=%s"); msg != "Copy of github:o/photos at abc" {
		t.Fatalf("commit = %q", msg)
	}
	// A tree without the stack's files is not a stack.
	if err := s.createFrom(ctx, "empty", t.TempDir(), "x", ""); code(err) != "not_a_stack" {
		t.Fatalf("empty: %v", err)
	}
	if _, err := os.Stat(s.repo("empty")); !os.IsNotExist(err) {
		t.Fatalf("a failed copy left its repo: %v", err)
	}
}
