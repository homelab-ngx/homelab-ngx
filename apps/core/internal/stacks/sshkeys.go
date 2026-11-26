package stacks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"homelab-ngx.com/core/internal/httpx"
)

// HostSSHKeyName is the name of the host's default deploy key under
// Host.SSHKeysDir.
const HostSSHKeyName = "host"

const knownHostsFileName = "known_hosts"

var sshKeyNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// pinnedForgeHostKeys are the public SSH host keys for the three public forges
// supported with one-click deploy-key links, so the first fetch never stops on
// an interactive prompt or trusts a forged key on the wire.
const pinnedForgeHostKeys = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
gitlab.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBFSMqzJeV9rUzU4kWitGjeR4PWSa29SPqJ1fVkhtj3Hw9xjLVXVYrU9QlYWrOLXBpQ6KWjbjTDTdDkoohFzgbEY=
gitlab.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCsj2bNKTBSpIYDEGk9KxsGh3mySTRgMtXL583qmBpzeQ+jqCMRgBqB98u3z++J1sKlXHWfM9dyhSevkMwSbhoR8XIq/U0tCNyokEi/ueaBMCvbcTHhO7FcwzY92WK4Yt0aGROY5qX2UKSeOvuP4D6TPqKF1onrSzH9bx9XUf2lEdWT/ia1NEKjunUqu1xOB/StKDHMoX4/OKyIzuS0q/T1zOATthvasJFoPrAjkohTyaDUz2LN5JoH839hViyEG82yB+MjcFV5MU3N1l1QL3cVUCh93xSaua1N85qivl+siMkPGbO5xR/En4iEY6K2XPASUEMaieWVNTRCtJ4S8H+9
codeberg.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIVIC02vnjFyL+I4RHfvIGNtOgJMe769VTF1VR4EB3ZB
codeberg.org ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBL2pDxWr18SoiDJCGZ5LmxPygTlPu+cCKSkpqkvCyQzl5xmIMeKNdfdBpfbCGDPoZQghePzFZkKJNR/v9Win3Sc=
codeberg.org ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC8hZi7K1/2E2uBX8gwPRJAHvRAob+3Sn+y2hxiEhN0buv1igjYFTgFO2qQD8vLfU/HT/P/rqvEeTvaDfY1y/vcvQ8+YuUYyTwE2UaVU5aJv89y6PEZBYycaJCPdGIfZlLMmjilh/Sk8IWSEK6dQr+g686lu5cSWrFW60ixWpHpEVB26eRWin3lKYWSQGMwwKv4LwmW3ouqqs4Z4vsqRFqXJ/eCi3yhpT+nOjljXvZKiYTpYajqUC48IHAxTWugrKe1vXWOPxVXXMQEPsaIRc2hpK+v1LmfB7GnEGvF1UAKnEZbUuiD9PBEeD5a1MZQIzcoPWCrTxipEpuXQ5Tni4mN
`

type sshKeyInfo struct {
	Name        string
	KeyPath     string
	PublicKey   string
	Fingerprint string
}

// sshKeyStore manages the host's and per-stack Ed25519 SSH keys and a
// pinned known_hosts file in a root-only directory.
type sshKeyStore struct {
	dir string
	mu  sync.Mutex
}

func newSSHKeyStore(dir string) *sshKeyStore {
	if dir == "" {
		return nil
	}
	return &sshKeyStore{dir: dir}
}

func (s *sshKeyStore) ensureHostKey() (sshKeyInfo, error) {
	return s.ensureKey(HostSSHKeyName, "homelab-ngx")
}

func repoKeyName(owner, repo string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(owner + "/" + repo)))
	return "gh-" + hex.EncodeToString(sum[:])[:16]
}

func (s *sshKeyStore) ensureRepoKey(owner, repo string) (sshKeyInfo, error) {
	id := strings.ToLower(owner + "/" + repo)
	return s.ensureKey(repoKeyName(owner, repo), "homelab-ngx:"+id)
}

func (s *sshKeyStore) keyPaths(name string) (priv, pub string, err error) {
	if !sshKeyNameRE.MatchString(name) {
		return "", "", fmt.Errorf("invalid ssh key name %q", name)
	}
	return filepath.Join(s.dir, name), filepath.Join(s.dir, name+".pub"), nil
}

func (s *sshKeyStore) hasKey(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	priv, pub, err := s.keyPaths(name)
	if err != nil {
		return false
	}
	if _, err := os.Stat(priv); err != nil {
		return false
	}
	_, err = os.Stat(pub)
	return err == nil
}

func (s *sshKeyStore) readKey(name string) (sshKeyInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readKeyLocked(name)
}

func (s *sshKeyStore) readKeyLocked(name string) (sshKeyInfo, error) {
	priv, pub, err := s.keyPaths(name)
	if err != nil {
		return sshKeyInfo{}, err
	}
	if _, err := os.Stat(priv); err != nil {
		return sshKeyInfo{}, err
	}
	raw, err := os.ReadFile(pub)
	if err != nil {
		return sshKeyInfo{}, err
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return sshKeyInfo{}, fmt.Errorf("parsing public key %s: %w", pub, err)
	}
	return sshKeyInfo{
		Name:        name,
		KeyPath:     priv,
		PublicKey:   strings.TrimSpace(string(raw)),
		Fingerprint: ssh.FingerprintSHA256(pk),
	}, nil
}

func (s *sshKeyStore) ensureKey(name, comment string) (sshKeyInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, err := s.readKeyLocked(name); err == nil {
		return info, nil
	}
	privPath, pubPath, err := s.keyPaths(name)
	if err != nil {
		return sshKeyInfo{}, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return sshKeyInfo{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return sshKeyInfo{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return sshKeyInfo{}, err
	}
	privBytes := pem.EncodeToMemory(block)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return sshKeyInfo{}, err
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		pubLine += " " + comment
	}
	if err := writeAtomic(privPath, privBytes, 0o600); err != nil {
		return sshKeyInfo{}, err
	}
	if err := writeAtomic(pubPath, []byte(pubLine+"\n"), 0o644); err != nil {
		return sshKeyInfo{}, err
	}
	return sshKeyInfo{
		Name:        name,
		KeyPath:     privPath,
		PublicKey:   pubLine,
		Fingerprint: ssh.FingerprintSHA256(sshPub),
	}, nil
}

// ensureKnownHosts guarantees pinned public keys for known forges are present
// without discarding host keys learned earlier for self-hosted servers.
func (s *sshKeyStore) ensureKnownHosts() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, knownHostsFileName)
	existingBytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	existing := string(existingBytes)
	have := map[string]bool{}
	for _, line := range strings.Split(existing, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			have[t] = true
		}
	}
	var add []string
	for _, line := range strings.Split(strings.TrimSpace(pinnedForgeHostKeys), "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !have[t] {
			add = append(add, t)
		}
	}
	if len(add) == 0 && len(existingBytes) > 0 {
		return path, nil
	}
	var out bytes.Buffer
	out.Write(bytes.TrimRight(existingBytes, "\n"))
	if out.Len() > 0 {
		out.WriteByte('\n')
	}
	for _, line := range add {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := writeAtomic(path, out.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (s *sshKeyStore) sshCommand(keyName string) (string, error) {
	info, err := s.readKey(keyName)
	if err != nil {
		return "", err
	}
	kh, err := s.ensureKnownHosts()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"ssh -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=%s -o StrictHostKeyChecking=accept-new",
		shellQuote(info.KeyPath),
		shellQuote(kh),
	), nil
}

func (s *sshKeyStore) sshEnv(keyName string) ([]string, error) {
	cmd, err := s.sshCommand(keyName)
	if err != nil {
		return nil, err
	}
	return []string{"GIT_SSH_COMMAND=" + cmd}, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// sshFlakeRef converts a stack source to a git+ssh:// flake reference when
// SSH can reach it; local sources return "", false.
func sshFlakeRef(source string) (string, bool) {
	switch {
	case strings.HasPrefix(source, "github:"):
		parts := strings.SplitN(strings.TrimPrefix(source, "github:"), "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", false
		}
		repo := strings.TrimSuffix(parts[1], ".git")
		base := "git+ssh://git@github.com/" + parts[0] + "/" + repo + ".git"
		if len(parts) == 3 && parts[2] != "" {
			if commitRE.MatchString(parts[2]) {
				return base + "?rev=" + parts[2], true
			}
			return base + "?ref=" + parts[2], true
		}
		return base, true
	case strings.HasPrefix(source, "git+ssh://"):
		return source, true
	case strings.HasPrefix(source, "git+https://"):
		p, err := url.Parse(strings.TrimPrefix(source, "git+"))
		if err != nil || p.Host == "" {
			return "", false
		}
		p.Scheme = "ssh"
		if p.User == nil {
			p.User = url.User("git")
		}
		if strings.EqualFold(p.Hostname(), "github.com") && !strings.HasSuffix(p.Path, ".git") {
			p.Path += ".git"
		}
		return "git+" + p.String(), true
	}
	return "", false
}

// sshGitURL returns the plain ssh:// URL and ref for git fetch when a stack
// uses an SSH key.
func sshGitURL(source string) (u, ref string, ok bool) {
	sshRef, ok := sshFlakeRef(source)
	if !ok {
		return "", "", false
	}
	p, err := url.Parse(strings.TrimPrefix(sshRef, "git+"))
	if err != nil {
		return "", "", false
	}
	q := p.Query()
	ref = q.Get("ref")
	if ref == "" {
		ref = q.Get("rev")
	}
	if ref == "" {
		ref = "HEAD"
	}
	p.RawQuery = ""
	return p.String(), ref, true
}

// githubRepo extracts owner and repository name from a GitHub source.
func githubRepo(source string) (owner, repo string, ok bool) {
	switch {
	case strings.HasPrefix(source, "github:"):
		parts := strings.SplitN(strings.TrimPrefix(source, "github:"), "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], strings.TrimSuffix(parts[1], ".git"), true
	case strings.HasPrefix(source, "git+https://"), strings.HasPrefix(source, "git+ssh://"):
		p, err := url.Parse(strings.TrimPrefix(source, "git+"))
		if err != nil || !strings.EqualFold(p.Hostname(), "github.com") {
			return "", "", false
		}
		parts := strings.Split(strings.TrimPrefix(p.Path, "/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], strings.TrimSuffix(parts[1], ".git"), true
	}
	return "", "", false
}

// deployKeyURL returns the forge's deploy-keys page for source, or "" when
// the host is self-hosted or unknown.
func deployKeyURL(source string) string {
	if owner, repo, ok := githubRepo(source); ok {
		return fmt.Sprintf("https://github.com/%s/%s/settings/keys/new", owner, repo)
	}
	if !strings.HasPrefix(source, "git+https://") && !strings.HasPrefix(source, "git+ssh://") {
		return ""
	}
	p, err := url.Parse(strings.TrimPrefix(source, "git+"))
	if err != nil {
		return ""
	}
	slug := strings.TrimSuffix(strings.Trim(p.Path, "/"), ".git")
	if slug == "" {
		return ""
	}
	switch strings.ToLower(p.Hostname()) {
	case "gitlab.com":
		return fmt.Sprintf("https://gitlab.com/%s/-/settings/repository#js-deploy-keys-settings", slug)
	case "codeberg.org":
		return fmt.Sprintf("https://codeberg.org/%s/settings/keys", slug)
	}
	return ""
}

// repoDisplay returns a short repository name (host/path) for messages and UI.
func repoDisplay(source string) string {
	if strings.HasPrefix(source, "github:") {
		parts := strings.SplitN(strings.TrimPrefix(source, "github:"), "/", 3)
		if len(parts) >= 2 {
			return "github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
		}
	}
	if strings.HasPrefix(source, "git+https://") || strings.HasPrefix(source, "git+ssh://") {
		if p, err := url.Parse(strings.TrimPrefix(source, "git+")); err == nil && p.Hostname() != "" {
			return p.Hostname() + "/" + strings.TrimSuffix(strings.Trim(p.Path, "/"), ".git")
		}
	}
	return source
}

// isPrivateRepoError reports whether a fetch error looks like missing
// authorization or an invisible private repository rather than a network, DNS,
// or manifest error.
func isPrivateRepoError(err error) bool {
	if err == nil || errors.Is(err, ErrNoManifest) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == "invalid_manifest" {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, nonAuth := range []string{
		"could not resolve host",
		"couldn't resolve host",
		"name or service not known",
		"temporary failure in name resolution",
		"connection refused",
		"connection timed out",
		"operation timed out",
		"could not connect",
		"network is unreachable",
		"no route to host",
		"host key verification failed",
		"remote host identification has changed",
		"ssl certificate problem",
	} {
		if strings.Contains(msg, nonAuth) {
			return false
		}
	}
	for _, authSignal := range []string{
		"http error 401",
		"http error 403",
		"http error 404",
		"returned error: 401",
		"returned error: 403",
		"returned error: 404",
		"401 unauthorized",
		"403 forbidden",
		"404 not found",
		"permission denied",
		"repository not found",
		"could not read from remote repository",
		"authentication failed",
		"could not read username",
		"terminal prompts disabled",
		"access denied",
	} {
		if strings.Contains(msg, authSignal) {
			return true
		}
	}
	return strings.Contains(msg, "repository ") && strings.Contains(msg, "not found")
}

func (s *Service) sshEnvFor(sshKey string) []string {
	if sshKey == "" || s.sshKeys == nil {
		return nil
	}
	env, err := s.sshKeys.sshEnv(sshKey)
	if err != nil {
		s.log.Warn("stack ssh key unavailable", "key", sshKey, "error", err)
		return nil
	}
	return env
}

func (s *Service) nixEnvFor(sshKey string) []string {
	return append(nixEnv(), s.sshEnvFor(sshKey)...)
}

func (s *Service) hostKeyUsedByOtherGitHubRepo(ctx context.Context, owner, repo, excludeStack string) (bool, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.Name == excludeStack || r.SshKey != HostSSHKeyName {
			continue
		}
		o, rp, ok := githubRepo(r.Source)
		if ok && (!strings.EqualFold(o, owner) || !strings.EqualFold(rp, repo)) {
			return true, nil
		}
	}
	return false, nil
}

func privateRepoProblem(source string, key sshKeyInfo, hostKey bool) *httpx.Problem {
	repo := repoDisplay(source)
	url := deployKeyURL(source)
	detail := fmt.Sprintf("cannot access private repository %s; add this read-only deploy key (%s) and retry:\n%s",
		repo, key.Fingerprint, key.PublicKey)
	if url != "" {
		detail = fmt.Sprintf("cannot access private repository %s; add this read-only deploy key (%s) at %s and retry:\n%s",
			repo, key.Fingerprint, url, key.PublicKey)
	}
	return httpx.Forbidden("private_repo", "%s", detail).
		With("public_key", key.PublicKey).
		With("fingerprint", key.Fingerprint).
		With("deploy_key_url", url).
		With("host_key", hostKey).
		With("repo", repo)
}

// SSHKeys returns the host's deploy key and any per-stack keys in use for
// private stack repositories.
func (s *Service) SSHKeys(ctx context.Context) (StackSSHKeys, error) {
	if s.sshKeys == nil {
		return StackSSHKeys{}, httpx.Unavailable("ssh_keys_unavailable", "SSH keys are not configured on this host")
	}
	host, err := s.sshKeys.ensureHostKey()
	if err != nil {
		return StackSSHKeys{}, err
	}
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return StackSSHKeys{}, err
	}
	out := StackSSHKeys{
		Host: StackSSHKey{
			PublicKey:   host.PublicKey,
			Fingerprint: host.Fingerprint,
		},
		Stacks: []StackSSHKey{},
	}
	for _, r := range rows {
		if r.SshKey == "" || r.SshKey == HostSSHKeyName {
			continue
		}
		k, err := s.sshKeys.readKey(r.SshKey)
		if err != nil {
			s.log.Warn("stack ssh key not readable", "stack", r.Name, "key", r.SshKey, "error", err)
			continue
		}
		out.Stacks = append(out.Stacks, StackSSHKey{
			PublicKey:   k.PublicKey,
			Fingerprint: k.Fingerprint,
			Stack:       r.Name,
			Repo:        repoDisplay(r.Source),
		})
	}
	return out, nil
}
