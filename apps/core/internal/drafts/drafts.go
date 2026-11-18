// Package drafts keeps the stacks written in the dashboard. Each draft is a
// git repo the daemon owns under its state directory, holding the two files
// of a stack repo. A save is a commit; check and build read the files the way
// an install does; and an install is an ordinary stack whose source is the
// repo (git+file://), so updates, rollback and the lock work unchanged.
package drafts

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store"
)

// Stacks is what drafts needs of the installed stacks: which were installed
// from a draft, and reading a draft's new commit into them.
type Stacks interface {
	List(ctx context.Context) ([]stacks.Stack, error)
	Fetch(ctx context.Context, name string) (stacks.Stack, error)
}

// branch is the one branch of a draft's repo.
const branch = "main"

// defaultMessage is a save's commit message when the operator gives none.
const defaultMessage = "Edit in dashboard"

//go:embed template/homelab-stack.json
var templateManifest string

//go:embed template/flake.nix
var templateFlake string

// Template is the files a new draft named name starts from.
func Template(name string) DraftFiles {
	return DraftFiles{
		Manifest: strings.ReplaceAll(templateManifest, "NAME", name),
		Flake:    strings.ReplaceAll(templateFlake, "NAME", name),
	}
}

// Service owns the drafts.
type Service struct {
	dir    string
	r      cmdrunner.Runner
	st     *store.Store
	stacks Stacks
	gpu    stacks.GPU
	log    *slog.Logger
	now    func() time.Time
	// build runs a build; a seam for tests.
	build func(context.Context, stacks.FileBuild) (string, error)
	// assistant gives the language model; nil leaves the assistant off.
	assistant Assistant

	// lspStart starts a language server (nil: nixd in a transient unit);
	// lspSlots bounds the ones running.
	lspStart lspStarter
	lspSlots chan struct{}

	// mu guards builds and revs, and serializes the writes to the repos.
	mu     sync.Mutex
	builds map[string]*running
	revs   map[string]cachedRev
}

// running is a build in flight.
type running struct {
	view DraftBuild
	log  capLog
}

// New returns the service. dir holds one repo per draft; gpu is the host's
// graphics stack, which a build with the GPU grant uses.
func New(dir string, r cmdrunner.Runner, st *store.Store, s Stacks, gpu stacks.GPU, log *slog.Logger) *Service {
	return &Service{
		dir: dir, r: r, st: st, stacks: s, gpu: gpu, log: log, now: time.Now,
		build:    func(ctx context.Context, b stacks.FileBuild) (string, error) { return b.Run(ctx) },
		builds:   map[string]*running{},
		revs:     map[string]cachedRev{},
		lspSlots: make(chan struct{}, lspSessions),
	}
}

// Dir is the folder that holds the drafts' repos.
func (s *Service) Dir() string { return s.dir }

func (s *Service) repo(name string) string { return filepath.Join(s.dir, name) }

// source is the draft's repo as a stack source.
func (s *Service) source(name string) string { return "git+file://" + s.repo(name) }

func unknown(name string) error {
	return httpx.NotFound("unknown_draft", "there is no draft named %q", name)
}

func (s *Service) row(ctx context.Context, name string) (store.StackDraft, error) {
	if !stacks.ValidName(name) {
		return store.StackDraft{}, unknown(name)
	}
	r, err := s.st.Queries().GetDraft(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.StackDraft{}, unknown(name)
	}
	return r, err
}

// List returns every draft, by name.
func (s *Service) List(ctx context.Context) ([]DraftSummary, error) {
	rows, err := s.st.Queries().ListDrafts(ctx)
	if err != nil {
		return nil, err
	}
	installed := s.installed(ctx)
	out := make([]DraftSummary, 0, len(rows))
	for _, r := range rows {
		rev, at, err := s.head(ctx, r.Name)
		if err != nil {
			s.log.Warn("draft repo not read", "draft", r.Name, "error", err)
		}
		out = append(out, DraftSummary{Name: r.Name, Rev: rev, SavedAt: at, InstalledAs: orEmpty(installed[r.Name])})
	}
	return out, nil
}

// Get returns one draft with its saved files.
func (s *Service) Get(ctx context.Context, name string) (Draft, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Draft{}, err
	}
	return s.view(ctx, r)
}

func (s *Service) view(ctx context.Context, r store.StackDraft) (Draft, error) {
	files, err := s.read(r.Name)
	if err != nil {
		return Draft{}, err
	}
	rev, at, err := s.head(ctx, r.Name)
	if err != nil {
		return Draft{}, err
	}
	d := Draft{
		Name: r.Name, Source: s.source(r.Name), Rev: rev, SavedAt: at,
		CreatedAt: time.UnixMilli(r.CreatedAt), Files: files,
		InstalledAs: orEmpty(s.installed(ctx)[r.Name]),
	}
	if r.CheckResult != "" {
		var c DraftCheck
		if json.Unmarshal([]byte(r.CheckResult), &c) == nil {
			d.Check = &c
		}
	}
	s.mu.Lock()
	b := s.builds[r.Name]
	if b != nil {
		v := b.view
		v.Log = b.log.String()
		d.Build = &v
	} else if fresh, err := s.row(ctx, r.Name); err == nil {
		// r may predate a build that finished since: runBuild stores the
		// result before it drops the build under this lock, so a row read
		// here is never a finished build still marked running.
		r = fresh
	}
	s.mu.Unlock()
	if b == nil && r.BuildResult != "" {
		var v DraftBuild
		if json.Unmarshal([]byte(r.BuildResult), &v) == nil {
			if v.Result == BuildRunning {
				// Stored as running, and no build runs: the daemon stopped
				// during it.
				v.Result = BuildInterrupted
			}
			d.Build = &v
		}
	}
	return d, nil
}

// installed maps each draft to the stacks installed from it.
func (s *Service) installed(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	if s.stacks == nil {
		return out
	}
	list, err := s.stacks.List(ctx)
	if err != nil {
		s.log.Warn("stacks not listed", "error", err)
		return out
	}
	prefix := "git+file://" + s.dir + "/"
	for _, st := range list {
		src, _, _ := strings.Cut(st.Source, "?")
		if name, ok := strings.CutPrefix(src, prefix); ok && stacks.ValidName(name) {
			out[name] = append(out[name], st.Name)
		}
	}
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Create starts a draft from the template, as its first commit.
func (s *Service) Create(ctx context.Context, name string) (Draft, error) {
	if !stacks.ValidName(name) {
		return Draft{}, httpx.BadRequest("invalid_name",
			"%q is not a stack name: use 1-12 characters of a-z, 0-9 and '-', starting with a letter", name)
	}
	if err := s.create(ctx, name); err != nil {
		return Draft{}, err
	}
	s.log.Info("draft created", "draft", name)
	return s.Get(ctx, name)
}

func (s *Service) create(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.st.Queries().GetDraft(ctx, name); err == nil {
		return httpx.Conflict("draft_exists", "a draft named %q already exists", name)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if s.stacks != nil {
		if list, err := s.stacks.List(ctx); err == nil {
			for _, st := range list {
				if st.Name == name {
					return httpx.Conflict("stack_exists", "a stack named %q is already installed; pick another name", name)
				}
			}
		}
	}
	dir := s.repo(name)
	if _, err := os.Stat(dir); err == nil {
		return httpx.Conflict("draft_exists", "%s is already there; remove it or pick another name", dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	err := func() error {
		if _, err := s.git(ctx, dir, "init", "--quiet", "--initial-branch="+branch); err != nil {
			return err
		}
		if err := s.write(name, Template(name)); err != nil {
			return err
		}
		_, err := s.commit(ctx, name, "Start from the template")
		return err
	}()
	if err == nil {
		err = s.st.Queries().InsertDraft(ctx, store.InsertDraftParams{Name: name, CreatedAt: s.now().UnixMilli()})
	}
	if err != nil {
		_ = os.RemoveAll(dir)
	}
	return err
}

// Save writes the files and commits them. Files that did not change make
// no commit. A stack installed from the draft reads the new commit at once,
// so it shows as an update (approved by itself when it asks for nothing new).
func (s *Service) Save(ctx context.Context, name string, in DraftSave) (Draft, error) {
	if _, err := s.row(ctx, name); err != nil {
		return Draft{}, err
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = defaultMessage
	}
	s.mu.Lock()
	if err := s.write(name, in.Files); err != nil {
		s.mu.Unlock()
		return Draft{}, err
	}
	changed, err := s.commit(ctx, name, msg)
	s.mu.Unlock()
	if err != nil {
		return Draft{}, err
	}
	if changed {
		s.log.Info("draft saved", "draft", name)
		for _, st := range s.installed(ctx)[name] {
			if _, err := s.stacks.Fetch(ctx, st); err != nil {
				s.log.Warn("stack did not read the saved draft", "draft", name, "stack", st, "error", err)
			}
		}
	}
	return s.Get(ctx, name)
}

// Delete removes a draft and its repo. A draft a stack is installed from is
// kept: the stack's updates read it.
func (s *Service) Delete(ctx context.Context, name string) error {
	if _, err := s.row(ctx, name); err != nil {
		return err
	}
	if in := s.installed(ctx)[name]; len(in) > 0 {
		return httpx.Conflict("draft_installed",
			"the stack %s is installed from this draft; remove the stack first", strings.Join(in, ", "))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.builds[name] != nil {
		return httpx.Conflict("build_running", "a build of %q is running; wait for it to end", name)
	}
	if err := os.RemoveAll(s.repo(name)); err != nil {
		return err
	}
	if err := s.st.Queries().DeleteDraft(ctx, name); err != nil {
		return err
	}
	s.log.Info("draft deleted", "draft", name)
	return nil
}

// write puts the files in the draft's working tree.
func (s *Service) write(name string, f DraftFiles) error {
	for file, content := range map[string]string{stacks.ManifestFile: f.Manifest, stacks.FlakeFile: f.Flake} {
		if err := os.WriteFile(filepath.Join(s.repo(name), file), []byte(content), 0o640); err != nil {
			return err
		}
	}
	return nil
}

// read returns the draft's files as saved.
func (s *Service) read(name string) (DraftFiles, error) {
	m, err := os.ReadFile(filepath.Join(s.repo(name), stacks.ManifestFile))
	if err != nil {
		return DraftFiles{}, fmt.Errorf("reading the draft %s: %w", name, err)
	}
	f, err := os.ReadFile(filepath.Join(s.repo(name), stacks.FlakeFile))
	if err != nil {
		return DraftFiles{}, fmt.Errorf("reading the draft %s: %w", name, err)
	}
	return DraftFiles{Manifest: string(m), Flake: string(f)}, nil
}

// commit commits the working tree; false when nothing changed.
func (s *Service) commit(ctx context.Context, name, msg string) (bool, error) {
	dir := s.repo(name)
	if _, err := s.git(ctx, dir, "add", "--all"); err != nil {
		return false, err
	}
	res, err := s.r.Run(ctx, gitCmd(dir, []string{"diff", "--cached", "--quiet"}, 1))
	if err == nil && res.ExitCode == 0 {
		return false, nil
	}
	var ce *cmdrunner.Error
	if err != nil && !(errors.As(err, &ce) && ce.Result.ExitCode == 1) {
		return false, err
	}
	if _, err := s.git(ctx, dir, "commit", "--quiet", "--no-verify", "--message", msg); err != nil {
		return false, err
	}
	return true, nil
}

// head is the draft's last commit and its time.
func (s *Service) head(ctx context.Context, name string) (string, time.Time, error) {
	out, err := s.git(ctx, s.repo(name), "log", "-1", "--format=%H %ct")
	if err != nil {
		return "", time.Time{}, err
	}
	rev, ts, _ := strings.Cut(out, " ")
	sec, _ := strconv.ParseInt(ts, 10, 64)
	return rev, time.Unix(sec, 0), nil
}

// gitEnv keeps the host's and root's git configuration out of the drafts'
// repos and names the daemon as the author.
var gitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_AUTHOR_NAME=homelab dashboard", "GIT_AUTHOR_EMAIL=homelab-daemon@localhost",
	"GIT_COMMITTER_NAME=homelab dashboard", "GIT_COMMITTER_EMAIL=homelab-daemon@localhost",
}

func gitCmd(dir string, args []string, expect ...int) cmdrunner.Cmd {
	return cmdrunner.Cmd{
		Module: "drafts", Name: "git", Args: append([]string{"-C", dir}, args...), Env: gitEnv, ExpectExit: expect,
	}
}

func (s *Service) git(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := s.r.Run(ctx, gitCmd(dir, args))
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(res.Stdout), nil
}
