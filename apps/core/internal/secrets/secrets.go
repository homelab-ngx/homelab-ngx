// Package secrets manages the sops-nix secrets from the dashboard: presence
// and rotation metadata (never values), encrypted writes through `sops set`
// committed to the repo, the deploy-pending flag and the `nixos-rebuild switch` deploy.
//
// The canonical list of secrets is the registry JSON written at activation by
// the NixOS secrets module.
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

const (
	// DefaultRegistryPath is written at activation by the NixOS secrets module.
	DefaultRegistryPath = "/etc/homelab-secrets-registry.json"
	// DeployPendingPath is the tmpfs flag: set after a successful change,
	// cleared after a successful switch. It is meant to vanish on reboot.
	DeployPendingPath = "/run/homelab-daemon/secrets-pending"

	sopsBin    = "sops"
	ageKeyFile = "/var/lib/sops-age/keys.txt"
)

// repoOwner is the user and group that own the checkout: git runs as that
// user, and secrets.yaml is given back to them after sops rewrites it as
// root. A checkout owned by root (or one that cannot be read) gives "".
func (s *Service) repoOwner() (usr, group string) {
	st, err := os.Stat(s.repoPath)
	if err != nil {
		return "", ""
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid == 0 {
		return "", ""
	}
	usr, group = strconv.FormatUint(uint64(sys.Uid), 10), strconv.FormatUint(uint64(sys.Gid), 10)
	if u, err := user.LookupId(usr); err == nil {
		usr = u.Username
	}
	if g, err := user.LookupGroupId(group); err == nil {
		group = g.Name
	}
	return usr, group
}

// Entry is one registry row (the camelCase runPath is the registry's shape).
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Services    []string `json:"services"`
	RunPath     string   `json:"runPath"`
}

// RotationStore persists when each secret was last rotated through the API.
type RotationStore interface {
	Rotations(ctx context.Context) (map[string]time.Time, error)
	SetRotation(ctx context.Context, name string, at time.Time) error
}

var (
	// ErrNotFound means the secret is not in the registry.
	ErrNotFound = errors.New("secret not found")
)

// InvalidNameError is returned for a service or key that cannot be used as a
// sops path component.
type InvalidNameError struct{ Name string }

func (e *InvalidNameError) Error() string {
	return fmt.Sprintf("invalid secret name %q: service and key may only contain letters, digits, '_', '-' and '.'", e.Name)
}

// PreflightError means the repository is not in a state that allows a secret
// commit; nothing was modified.
type PreflightError struct{ Err error }

func (e *PreflightError) Error() string { return e.Err.Error() }
func (e *PreflightError) Unwrap() error { return e.Err }

var namePart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// GitResult describes the commit a secret change produced.
type GitResult struct {
	Branch      string
	Commit      string
	PushPending bool
	Warnings    []string
}

// Service is the secrets domain.
type Service struct {
	r            cmdrunner.Runner
	registryPath string
	repoPath     string
	st           RotationStore
	log          *slog.Logger

	writeMu sync.Mutex // serialises secret mutations

	regMu   sync.Mutex
	reg     []Entry
	regSize int64
	regMod  time.Time
	regOK   bool

	pendingPath string
	now         func() time.Time
}

// New wires a Service. repoPath is the flake root (see ResolveRepoPath); st may
// be nil (rotation times are then not recorded).
func New(r cmdrunner.Runner, registryPath, repoPath string, st RotationStore, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		r: r, registryPath: registryPath, repoPath: repoPath, st: st, log: log,
		pendingPath: DeployPendingPath, now: time.Now,
	}
}

// ResolveRepoPath returns the flake root directory from the HOMELAB_FLAKE
// value, e.g. "/home/alice/homelab" or "/path/to/flake#host"; "." when
// it is empty. The composition root passes the os.Getenv result, so importing
// this package has no side effects.
func ResolveRepoPath(flake string) string {
	p := flake
	if idx := strings.LastIndexByte(p, '#'); idx >= 0 {
		p = p[:idx]
	}
	if p == "" {
		return "."
	}
	return p
}

func (s *Service) secretsYAML() string {
	return filepath.Join(s.repoPath, "nixos/secrets/secrets.yaml")
}
func (s *Service) sopsConfig() string { return filepath.Join(s.repoPath, "nixos/.sops.yaml") }

// ---- registry ----

func (s *Service) loadRegistry() ([]Entry, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	info, err := os.Stat(s.registryPath)
	if err != nil {
		s.regOK = false
		if os.IsNotExist(err) {
			return nil, nil // registry not written yet (pre-first-deploy)
		}
		return nil, err
	}
	if s.regOK && info.Size() == s.regSize && info.ModTime().Equal(s.regMod) {
		return slices.Clone(s.reg), nil
	}
	data, err := os.ReadFile(s.registryPath)
	if err != nil {
		s.regOK = false
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		s.regOK = false
		return nil, fmt.Errorf("parse secrets registry: %w", err)
	}
	s.reg, s.regSize, s.regMod, s.regOK = entries, info.Size(), info.ModTime(), true
	return slices.Clone(entries), nil
}

// Resolver returns the function cmdrunner uses to inject registry secrets as
// environment variables: an exact "service/VAR" match first, then a match on
// the trailing "VAR" component. Wire it as cmdrunner.OS{Secrets: sec.Resolver()}.
func (s *Service) Resolver() cmdrunner.SecretResolver {
	return func(name string) string {
		entries, err := s.loadRegistry()
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if e.Name == name {
				return readKV(e, name)
			}
			if parts := strings.SplitN(e.Name, "/", 2); len(parts) == 2 && parts[1] == name {
				return readKV(e, name)
			}
		}
		return ""
	}
}

func readKV(e Entry, alias string) string {
	data, err := os.ReadFile(e.RunPath)
	if err != nil {
		return ""
	}
	kv := strings.TrimSpace(string(data))
	if !strings.Contains(kv, "=") {
		kv = alias + "=" + kv
	}
	return kv
}

// ---- reads ----

func (s *Service) status(e Entry, rot map[string]time.Time) SecretStatus {
	st := SecretStatus{
		Name: e.Name, Description: e.Description, RunPath: e.RunPath,
		Services: e.Services,
	}
	if st.Services == nil {
		st.Services = []string{}
	}
	if info, err := os.Stat(e.RunPath); err == nil && info.Size() > 0 {
		st.Present = true
		// Read only 3 bytes for the preview; never expose more.
		if f, err := os.Open(e.RunPath); err == nil {
			buf := make([]byte, 3)
			n, _ := f.Read(buf)
			f.Close()
			if n > 0 {
				st.Preview = string(buf[:n]) + "•••"
			}
		}
	}
	if t, ok := rot[e.Name]; ok {
		t = t.UTC()
		st.ModifiedAt = &t
	}
	return st
}

func (s *Service) rotations(ctx context.Context) map[string]time.Time {
	if s.st == nil {
		return nil
	}
	rot, err := s.st.Rotations(ctx)
	if err != nil {
		s.log.Warn("read secret rotation times failed", "error", err)
		return nil
	}
	return rot
}

// DeployPending reports whether a change is waiting for a deploy.
func (s *Service) DeployPending() bool {
	_, err := os.Stat(s.pendingPath)
	return err == nil
}

func (s *Service) setPending(pending bool) {
	if pending {
		_ = os.MkdirAll(filepath.Dir(s.pendingPath), 0o755)
		if err := os.WriteFile(s.pendingPath, []byte("1"), 0o600); err != nil {
			s.log.Warn("failed to set deploy-pending flag", "error", err)
		}
		return
	}
	_ = os.Remove(s.pendingPath)
}

// List returns every registered secret (metadata only) and the deploy flag.
func (s *Service) List(ctx context.Context) (SecretsList, error) {
	entries, err := s.loadRegistry()
	if err != nil {
		return SecretsList{}, fmt.Errorf("load secrets registry: %w", err)
	}
	rot := s.rotations(ctx)
	out := SecretsList{Secrets: make([]SecretStatus, 0, len(entries)), DeployPending: s.DeployPending()}
	for _, e := range entries {
		out.Secrets = append(out.Secrets, s.status(e, rot))
	}
	return out, nil
}

// Get returns the metadata of one secret.
func (s *Service) Get(ctx context.Context, name string) (SecretStatus, error) {
	entries, err := s.loadRegistry()
	if err != nil {
		return SecretStatus{}, fmt.Errorf("load secrets registry: %w", err)
	}
	for _, e := range entries {
		if e.Name == name {
			return s.status(e, s.rotations(ctx)), nil
		}
	}
	return SecretStatus{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

// ---- writes ----

func name(service, key string) (string, error) {
	n := service + "/" + key
	if !namePart.MatchString(service) || !namePart.MatchString(key) {
		return "", &InvalidNameError{Name: n}
	}
	return n, nil
}

// sopsSet updates one key of secrets.yaml with `sops set`; the value goes in
// through stdin as JSON, never argv.
func (s *Service) sopsSet(ctx context.Context, service, key, value string) error {
	var enc bytes.Buffer
	e := json.NewEncoder(&enc)
	e.SetEscapeHTML(false)
	if err := e.Encode(value); err != nil {
		return err
	}
	res, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "secrets", Name: sopsBin,
		Args:   []string{"set", "--value-stdin", s.secretsYAML(), fmt.Sprintf(`["%s"]["%s"]`, service, key)},
		Env:    []string{"SOPS_AGE_KEY_FILE=" + ageKeyFile, "SOPS_CONFIG=" + s.sopsConfig()},
		Stdin:  strings.NewReader(strings.TrimRight(enc.String(), "\n")),
		Output: cmdrunner.OutputCombined,
	})
	if err != nil {
		return fmt.Errorf("sops set failed: %w\noutput: %s", err, res.Output)
	}
	// sops runs as root and recreates the file: restore ownership to the repo owner.
	if usr, group := s.repoOwner(); usr != "" {
		_, _ = s.r.Run(ctx, cmdrunner.Cmd{Module: "secrets", Name: "chown", Args: []string{usr + ":" + group, s.secretsYAML()}})
	}
	return nil
}

// Unpushed reports whether the checkout has a commit to secrets.yaml that
// origin's main does not: a deploy from main would run without it.
func (s *Service) Unpushed(ctx context.Context) (bool, error) {
	out, err := s.gitRunner(ctx)("rev-list", "--count", "origin/main..HEAD", "--", "nixos/secrets/secrets.yaml")
	if err != nil {
		return false, fmt.Errorf("git rev-list: %w: %s", err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out) != "0", nil
}

func (s *Service) gitRunner(ctx context.Context) gitRunner {
	usr, _ := s.repoOwner()
	return func(args ...string) (string, error) {
		res, err := s.r.Run(ctx, cmdrunner.Cmd{
			Module: "secrets", Name: "git", Args: append([]string{"-C", s.repoPath}, args...),
			AsUser: usr, Output: cmdrunner.OutputCombined,
		})
		return res.Output, err
	}
}

// Set encrypts value into secrets.yaml, commits it and flags a pending deploy.
func (s *Service) Set(ctx context.Context, service, key, value string) (GitResult, error) {
	n, err := name(service, key)
	if err != nil {
		return GitResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	run := s.gitRunner(ctx)
	branch, err := s.preflight(run)
	if err != nil {
		return GitResult{}, &PreflightError{Err: err}
	}
	s.log.Info("rotating secret", "name", n)
	return s.writeLocked(ctx, service, key, value, rotateMessage(n), branch, run)
}

// writeLocked encrypts value into secrets.yaml, commits it with message,
// records the rotation time and flags a pending deploy. The caller holds
// writeMu and has passed preflight on branch.
func (s *Service) writeLocked(ctx context.Context, service, key, value, message, branch string, run gitRunner) (GitResult, error) {
	n := service + "/" + key
	if err := s.sopsSet(ctx, service, key, value); err != nil {
		return GitResult{}, err
	}
	return s.commitPrepared(message, branch, run, func() {
		if s.st != nil {
			if err := s.st.SetRotation(ctx, n, s.now()); err != nil {
				s.log.Warn("failed to record secret rotation", "name", n, "error", err)
			}
		}
		s.setPending(true)
	})
}

// Commit subjects of dashboard changes. The wipe subject is also the durable
// record that the operator wiped a name (see Service.wiped).
func rotateMessage(n string) string { return fmt.Sprintf("secrets: rotate %s via dashboard", n) }
func wipeMessage(n string) string   { return fmt.Sprintf("secrets: wipe %s via dashboard", n) }

// Delete wipes a secret (sets it to the empty string), commits and flags a
// pending deploy.
func (s *Service) Delete(ctx context.Context, service, key string) (GitResult, error) {
	n, err := name(service, key)
	if err != nil {
		return GitResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	run := s.gitRunner(ctx)
	branch, err := s.preflight(run)
	if err != nil {
		return GitResult{}, &PreflightError{Err: err}
	}
	s.log.Info("wiping secret", "name", n)
	if err := s.sopsSet(ctx, service, key, ""); err != nil {
		return GitResult{}, err
	}
	return s.commitPrepared(wipeMessage(n), branch, run, func() { s.setPending(true) })
}

// gitRunner runs one git command in the repo and returns its combined output.
type gitRunner func(args ...string) (string, error)

// preflight checks, before anything is modified, that the checkout allows a
// secret commit: a branch is checked out, nothing is staged, and secrets.yaml
// is clean. It returns the current branch.
func (s *Service) preflight(run gitRunner) (string, error) {
	branch, err := run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("determine current branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("secret changes require a checked-out branch; detached HEAD is not supported")
	}
	allStatus, err := run("status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("check staged repository state: %w", err)
	}
	for _, line := range strings.Split(allStatus, "\n") {
		if len(line) >= 2 && line[0] != ' ' && line[0] != '?' {
			return "", fmt.Errorf("repository has staged changes; commit or unstage them before changing a secret")
		}
	}
	status, err := run("status", "--porcelain", "--", s.secretsYAML())
	if err != nil {
		return "", fmt.Errorf("check secret repository state: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return "", fmt.Errorf("%s already has uncommitted changes", s.secretsYAML())
	}
	return branch, nil
}

// commit runs preflight then commitPrepared (used by tests and callers that
// have not preflighted yet).
func (s *Service) commit(message string, run gitRunner, onCommitted func()) (GitResult, error) {
	branch, err := s.preflight(run)
	if err != nil {
		return GitResult{}, err
	}
	return s.commitPrepared(message, branch, run, onCommitted)
}

// commitPrepared stages and commits secrets.yaml, runs onCommitted as soon as
// the local commit exists, then pushes. A missing remote or failed push is a
// warning (PushPending), never a failure of the already-committed change.
func (s *Service) commitPrepared(message, branch string, run gitRunner, onCommitted func()) (GitResult, error) {
	if output, err := run("add", s.secretsYAML()); err != nil {
		return GitResult{}, fmt.Errorf("git add failed: %w\noutput: %s", err, output)
	}
	if output, err := run("commit", "-m", message); err != nil {
		return GitResult{}, fmt.Errorf("git commit failed: %w\noutput: %s", err, output)
	}
	commit, err := run("rev-parse", "HEAD")
	if err != nil {
		return GitResult{}, fmt.Errorf("read secret commit: %w", err)
	}
	res := GitResult{Branch: branch, Commit: strings.TrimSpace(commit), Warnings: []string{}}
	onCommitted()
	remotes, _ := run("remote")
	if strings.TrimSpace(remotes) == "" {
		res.PushPending = true
		res.Warnings = []string{"secret committed locally; no Git remote is configured"}
		return res, nil
	}
	if output, err := run("push", "--set-upstream", "origin", "HEAD"); err != nil {
		s.log.Warn("secret committed locally but push failed", "branch", branch, "commit", res.Commit, "error", err)
		res.PushPending = true
		res.Warnings = []string{fmt.Sprintf("secret committed locally; remote push failed: %v (%s)", err, strings.TrimSpace(output))}
	}
	return res, nil
}
