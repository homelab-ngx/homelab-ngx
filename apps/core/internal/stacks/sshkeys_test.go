package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

func TestSSHSourceRewriteAndForgeHelpers(t *testing.T) {
	t.Parallel()
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		source string
		sshRef string
		sshOK  bool
		gitURL string
		gitRef string
		owner  string
		repo   string
		ghOK   bool
		keyURL string
	}{
		{
			source: "github:alice/stack-one",
			sshRef: "git+ssh://git@github.com/alice/stack-one.git",
			sshOK:  true,
			gitURL: "ssh://git@github.com/alice/stack-one.git",
			gitRef: "HEAD",
			owner:  "alice",
			repo:   "stack-one",
			ghOK:   true,
			keyURL: "https://github.com/alice/stack-one/settings/keys/new",
		},
		{
			source: "github:alice/stack-one/feat/v2",
			sshRef: "git+ssh://git@github.com/alice/stack-one.git?ref=feat/v2",
			sshOK:  true,
			gitURL: "ssh://git@github.com/alice/stack-one.git",
			gitRef: "feat/v2",
			owner:  "alice",
			repo:   "stack-one",
			ghOK:   true,
			keyURL: "https://github.com/alice/stack-one/settings/keys/new",
		},
		{
			source: "github:alice/stack-one/" + commit,
			sshRef: "git+ssh://git@github.com/alice/stack-one.git?rev=" + commit,
			sshOK:  true,
			gitURL: "ssh://git@github.com/alice/stack-one.git",
			gitRef: commit,
			owner:  "alice",
			repo:   "stack-one",
			ghOK:   true,
			keyURL: "https://github.com/alice/stack-one/settings/keys/new",
		},
		{
			source: "git+https://github.com/alice/stack-one?ref=dev",
			sshRef: "git+ssh://git@github.com/alice/stack-one.git?ref=dev",
			sshOK:  true,
			gitURL: "ssh://git@github.com/alice/stack-one.git",
			gitRef: "dev",
			owner:  "alice",
			repo:   "stack-one",
			ghOK:   true,
			keyURL: "https://github.com/alice/stack-one/settings/keys/new",
		},
		{
			source: "git+https://gitlab.com/group/sub/stack-gl.git?ref=main",
			sshRef: "git+ssh://git@gitlab.com/group/sub/stack-gl.git?ref=main",
			sshOK:  true,
			gitURL: "ssh://git@gitlab.com/group/sub/stack-gl.git",
			gitRef: "main",
			keyURL: "https://gitlab.com/group/sub/stack-gl/-/settings/repository#js-deploy-keys-settings",
		},
		{
			source: "git+ssh://git@codeberg.org/alice/stack-cb.git",
			sshRef: "git+ssh://git@codeberg.org/alice/stack-cb.git",
			sshOK:  true,
			gitURL: "ssh://git@codeberg.org/alice/stack-cb.git",
			gitRef: "HEAD",
			keyURL: "https://codeberg.org/alice/stack-cb/settings/keys",
		},
		{
			source: "git+ssh://git@git.example.org/team/custom.git",
			sshRef: "git+ssh://git@git.example.org/team/custom.git",
			sshOK:  true,
			gitURL: "ssh://git@git.example.org/team/custom.git",
			gitRef: "HEAD",
			keyURL: "",
		},
		{
			source: "git+file:///srv/stacks/local",
			sshOK:  false,
		},
	} {
		gotRef, gotOK := sshFlakeRef(tc.source)
		if gotRef != tc.sshRef || gotOK != tc.sshOK {
			t.Errorf("sshFlakeRef(%q) = %q, %v; want %q, %v", tc.source, gotRef, gotOK, tc.sshRef, tc.sshOK)
		}
		if tc.sshOK {
			u, ref, ok := sshGitURL(tc.source)
			if !ok || u != tc.gitURL || ref != tc.gitRef {
				t.Errorf("sshGitURL(%q) = %q, %q, %v; want %q, %q, true", tc.source, u, ref, ok, tc.gitURL, tc.gitRef)
			}
		}
		o, r, ghOK := githubRepo(tc.source)
		if o != tc.owner || r != tc.repo || ghOK != tc.ghOK {
			t.Errorf("githubRepo(%q) = %q, %q, %v; want %q, %q, %v", tc.source, o, r, ghOK, tc.owner, tc.repo, tc.ghOK)
		}
		if gotURL := deployKeyURL(tc.source); gotURL != tc.keyURL {
			t.Errorf("deployKeyURL(%q) = %q; want %q", tc.source, gotURL, tc.keyURL)
		}
	}
}

func TestPrivateRepoErrorDetection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{ErrNoManifest, false},
		{context.DeadlineExceeded, false},
		{context.Canceled, false},
		{httpx.Unprocessable("invalid_manifest", "bad manifest"), false},
		{errors.New("error: unable to download 'https://api.github.com/repos/a/b/commits/HEAD': HTTP error 404"), true},
		{errors.New("error: unable to download 'https://gitlab.com/a/b': HTTP error 401"), true},
		{errors.New("HTTP error 403 Forbidden"), true},
		{errors.New("git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."), true},
		{errors.New("remote: Repository not found.\nfatal: repository 'https://github.com/a/b.git/' not found"), true},
		{errors.New("fatal: could not read Username for 'https://github.com': terminal prompts disabled"), true},
		{errors.New("fatal: unable to access 'https://nonexistent.invalid/a/b.git/': Could not resolve host: nonexistent.invalid"), false},
		{errors.New("ssh: Could not resolve hostname git.example.invalid: Name or service not known"), false},
		{errors.New("ssh: connect to host git.example.org port 22: Connection refused"), false},
		{errors.New("Host key verification failed.\nfatal: Could not read from remote repository."), false},
	} {
		if got := isPrivateRepoError(tc.err); got != tc.want {
			t.Errorf("isPrivateRepoError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestSSHKeyStoreGeneratesPersistsAndKeepsKnownHosts(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "ssh-keys")
	ks := newSSHKeyStore(dir)

	first, err := ks.ensureHostKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(first.Fingerprint, "SHA256:") {
		t.Fatalf("unexpected key info: %+v", first)
	}
	st, err := os.Stat(first.KeyPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, %v", st.Mode().Perm(), err)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(first.PublicKey))
	if err != nil || ssh.FingerprintSHA256(pk) != first.Fingerprint {
		t.Fatalf("public key did not parse cleanly: %v", err)
	}

	second, err := ks.ensureHostKey()
	if err != nil {
		t.Fatal(err)
	}
	if second.PublicKey != first.PublicKey || second.Fingerprint != first.Fingerprint {
		t.Fatalf("ensureHostKey regenerated key: %+v != %+v", second, first)
	}

	khPath, err := ks.ensureKnownHosts()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(khPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"github.com ssh-ed25519 ", "gitlab.com ssh-ed25519 ", "codeberg.org ssh-ed25519 "} {
		if !strings.Contains(string(raw), host) {
			t.Errorf("known_hosts missing %q", host)
		}
	}

	// Custom self-hosted host key learned later stays across ensureKnownHosts.
	custom := "git.example.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleCustomHostKey\n"
	if err := os.WriteFile(khPath, append(raw, custom...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.ensureKnownHosts(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(khPath)
	if !strings.Contains(string(after), custom) {
		t.Errorf("custom known_hosts line lost:\n%s", after)
	}
}

type fakeEnvSource struct {
	fn func(ctx context.Context, source string, env []string) (Fetched, error)
}

func (s *fakeEnvSource) Fetch(ctx context.Context, source string) (Fetched, error) {
	return s.fn(ctx, source, nil)
}

func (s *fakeEnvSource) FetchWithEnv(ctx context.Context, source string, env []string) (Fetched, error) {
	return s.fn(ctx, source, env)
}

func sshCmdFromEnv(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_SSH_COMMAND="); ok {
			return v
		}
	}
	return ""
}

func newSSHTestService(t *testing.T, src Source) (*Service, *fakeHost) {
	t.Helper()
	dir := t.TempDir()
	st, _ := storetest.Open(t)
	h := newFakeHost()
	svc := New(st, src, Host{
		Runner: h, StateDir: filepath.Join(dir, "state"), ProfilesDir: filepath.Join(dir, "profiles"), RoutesDir: filepath.Join(dir, "routes"),
		VarsDir: filepath.Join(dir, "vars"), AgeKeyFile: hostKey(t, dir), BuildsDir: filepath.Join(dir, "builds"),
		UnitsDir: filepath.Join(dir, "units"), MirrorsDir: filepath.Join(dir, "mirrors"), SSHKeysDir: filepath.Join(dir, "ssh-keys"),
	}, h, nil)
	svc.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	svc.timeZone = func() string { return "Europe/Stockholm" }
	svc.resolveNixpkgs = func(_ context.Context, spec string) (string, error) { return fakeNixpkgs(spec), nil }
	svc.nixpkgsRelease = func(context.Context, string) (NixpkgsRelease, error) {
		return NixpkgsRelease{Version: "26.11", Modified: time.Unix(1_790_000_000, 0)}, nil
	}
	svc.listChannels = func(context.Context) ([]string, error) { return []string{"nixos-unstable"}, nil }
	svc.probe = func(context.Context, string) error { return nil }
	svc.healthEvery = time.Millisecond
	svc.bootID = func() string { return "" }
	return svc, h
}

func TestAddPrivateGitHubReposUsesHostKeyThenPerStackKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Key paths granted on the simulated remote forge.
	granted := map[string]map[string]bool{
		"alice/stack-one": {},
		"alice/stack-two": {},
		"alice/account-3": {},
	}
	src := &fakeEnvSource{fn: func(_ context.Context, source string, env []string) (Fetched, error) {
		switch source {
		case "github:alice/public-app":
			return Fetched{Rev: "pub11111", URL: "github:alice/public-app/pub11111", Manifest: []byte(`{"version":7,"name":"public-app","nixpkgs":"nixos-unstable"}`)}, nil
		case "github:alice/stack-one", "github:alice/stack-two", "github:alice/account-3":
			return Fetched{}, errors.New("error: unable to download 'https://api.github.com/repos/" + strings.TrimPrefix(source, "github:") + "/commits/HEAD': HTTP error 404")
		}
		sshCmd := sshCmdFromEnv(env)
		for repo, keys := range granted {
			if source == "git+ssh://git@github.com/"+repo+".git" {
				for keyPath := range keys {
					if strings.Contains(sshCmd, keyPath) {
						name := strings.TrimPrefix(repo, "alice/")
						return Fetched{
							Rev:      name + "-rev1",
							URL:      source + "?ref=main&rev=" + name + "-rev1",
							Manifest: []byte(`{"version":7,"name":"` + name + `","nixpkgs":"nixos-unstable"}`),
						}, nil
					}
				}
				return Fetched{}, errors.New("git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.")
			}
		}
		return Fetched{}, errors.New("unexpected source: " + source)
	}}

	svc, h := newSSHTestService(t, src)

	// 1. Public repo succeeds immediately with no SSH key.
	pub, err := svc.Add(ctx, "github:alice/public-app", "")
	if err != nil {
		t.Fatalf("public add: %v", err)
	}
	if row, _ := svc.st.Queries().GetStack(ctx, pub.Name); row.SshKey != "" {
		t.Fatalf("public repo got ssh_key = %q", row.SshKey)
	}

	// 2. First private GitHub repo fails with private_repo and shows the host key.
	_, err = svc.Add(ctx, "github:alice/stack-one", "")
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != 403 || p.Code != "private_repo" {
		t.Fatalf("first private add = %v, want 403 private_repo", err)
	}
	if p.Ext["host_key"] != true || p.Ext["deploy_key_url"] != "https://github.com/alice/stack-one/settings/keys/new" {
		t.Fatalf("private_repo ext = %+v", p.Ext)
	}
	hostKeyPub, _ := p.Ext["public_key"].(string)
	if hostKeyPub == "" || p.Ext["fingerprint"] == "" {
		t.Fatalf("missing public_key or fingerprint in %+v", p.Ext)
	}

	// 3. Adding the host key to alice/stack-one lets retry succeed and installs with ssh_key = "host".
	hostInfo, err := svc.sshKeys.ensureHostKey()
	if err != nil {
		t.Fatal(err)
	}
	granted["alice/stack-one"][hostInfo.KeyPath] = true
	one, err := svc.Add(ctx, "github:alice/stack-one", "")
	if err != nil {
		t.Fatalf("retry private add 1: %v", err)
	}
	if one.Source != "github:alice/stack-one" {
		t.Errorf("source = %q, want github:alice/stack-one", one.Source)
	}
	if row, _ := svc.st.Queries().GetStack(ctx, "stack-one"); row.SshKey != HostSSHKeyName {
		t.Fatalf("stack-one ssh_key = %q, want %q", row.SshKey, HostSSHKeyName)
	}

	// 4. Second private GitHub repo gets its own dedicated key because host is already on stack-one.
	_, err = svc.Add(ctx, "github:alice/stack-two", "")
	var p2 *httpx.Problem
	if !errors.As(err, &p2) || p2.Status != 403 || p2.Code != "private_repo" {
		t.Fatalf("second private add = %v, want 403 private_repo", err)
	}
	if p2.Ext["host_key"] != false || p2.Ext["deploy_key_url"] != "https://github.com/alice/stack-two/settings/keys/new" {
		t.Fatalf("second private_repo ext = %+v", p2.Ext)
	}
	stackTwoPub, _ := p2.Ext["public_key"].(string)
	if stackTwoPub == "" || stackTwoPub == hostKeyPub {
		t.Fatalf("second repo key %q should differ from host key %q", stackTwoPub, hostKeyPub)
	}

	// 5. Retry after adding dedicated key installs stack-two with its own ssh_key.
	dedName := repoKeyName("alice", "stack-two")
	dedInfo, err := svc.sshKeys.readKey(dedName)
	if err != nil {
		t.Fatal(err)
	}
	granted["alice/stack-two"][dedInfo.KeyPath] = true
	two, err := svc.Add(ctx, "github:alice/stack-two", "")
	if err != nil {
		t.Fatalf("retry private add 2: %v", err)
	}
	if row, _ := svc.st.Queries().GetStack(ctx, two.Name); row.SshKey != dedName {
		t.Fatalf("stack-two ssh_key = %q, want %q", row.SshKey, dedName)
	}

	// 6. Account-level option: if host key works on a third repo, it uses host directly.
	granted["alice/account-3"][hostInfo.KeyPath] = true
	three, err := svc.Add(ctx, "github:alice/account-3", "")
	if err != nil {
		t.Fatalf("account-level key add: %v", err)
	}
	if row, _ := svc.st.Queries().GetStack(ctx, three.Name); row.SshKey != HostSSHKeyName {
		t.Fatalf("account-3 ssh_key = %q, want %q", row.SshKey, HostSSHKeyName)
	}

	// 7. SSHKeys lists the host key and the one dedicated per-stack key (not account-3).
	keysResp, err := svc.SSHKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if keysResp.Host.PublicKey != hostInfo.PublicKey || keysResp.Host.Fingerprint != hostInfo.Fingerprint {
		t.Errorf("host key = %+v, want %+v", keysResp.Host, hostInfo)
	}
	if len(keysResp.Stacks) != 1 || keysResp.Stacks[0].Stack != "stack-two" || keysResp.Stacks[0].PublicKey != dedInfo.PublicKey {
		t.Errorf("per-stack keys = %+v", keysResp.Stacks)
	}

	// 8. Fetch, buildLogged, and ApprovedTree pass GIT_SSH_COMMAND with the stack's key.
	if _, err := svc.Fetch(ctx, "stack-two"); err != nil {
		t.Fatalf("fetch stack-two: %v", err)
	}
	if _, err := svc.Approve(ctx, "stack-two", "stack-two-rev1"); err != nil {
		t.Fatal(err)
	}
	h.On("nix", "--extra-experimental-features", "nix-command flakes", "flake", "prefetch").
		Reply(`{"storePath":"/nix/store/stacktwo-tree"}`)
	if dir, _, _, err := svc.ApprovedTree(ctx, "stack-two"); err != nil || dir != "/nix/store/stacktwo-tree" {
		t.Fatalf("ApprovedTree = %q, %v", dir, err)
	}
	calls := h.CallsMatching("nix")
	lastCall := calls[len(calls)-1]
	if !strings.Contains(sshCmdFromEnv(lastCall.Env), dedInfo.KeyPath) {
		t.Errorf("ApprovedTree env = %v, want key %s", lastCall.Env, dedInfo.KeyPath)
	}
	if _, err := svc.buildLogged(ctx, &operation{}, dedName, "git+ssh://git@github.com/alice/stack-two.git?rev=r1", "#x"); err != nil {
		t.Fatal(err)
	}
	buildCalls := h.CallsMatching("nix")
	if cmd := sshCmdFromEnv(buildCalls[len(buildCalls)-1].Env); !strings.Contains(cmd, dedInfo.KeyPath) {
		t.Errorf("buildLogged GIT_SSH_COMMAND = %q, want %s", cmd, dedInfo.KeyPath)
	}

	// 9. Lock preserves ssh_key on CurrentLock and Restore.
	lock, err := svc.CurrentLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(lock.Stacks, func(e LockedStack) bool { return e.Name == "stack-two" })
	if idx < 0 || lock.Stacks[idx].SSHKey != dedName {
		t.Fatalf("lock entry for stack-two = %+v", lock.Stacks)
	}
	if _, err := svc.st.Queries().DeleteStack(ctx, "stack-two"); err != nil {
		t.Fatal(err)
	}
	want := false
	entry := lock.Stacks[idx]
	entry.WantRunning = false
	entry.Autostart = &want
	if _, err := svc.Restore(ctx, Lock{Version: LockVersion, Stacks: []LockedStack{entry}}); err != nil {
		t.Fatal(err)
	}
	if row, err := svc.st.Queries().GetStack(ctx, "stack-two"); err != nil || row.SshKey != dedName {
		t.Fatalf("restored stack-two ssh_key = %q, err=%v", row.SshKey, err)
	}

	// 10. gitHistory.LogWithKey passes GIT_SSH_COMMAND and the ssh:// URL.
	histCmds := cmdrunnertest.NewFake()
	histCmds.On("git", "init").Reply("")
	histCmds.On("git", "-C").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		if len(c.Args) > 2 && c.Args[2] == "fetch" {
			if !strings.Contains(sshCmdFromEnv(c.Env), dedInfo.KeyPath) {
				t.Errorf("git fetch env = %v, want key %s", c.Env, dedInfo.KeyPath)
			}
			if !slices.Contains(c.Args, "ssh://git@github.com/alice/stack-two.git") {
				t.Errorf("git fetch args = %v, want ssh:// url", c.Args)
			}
			return ok("")
		}
		if len(c.Args) > 2 && c.Args[2] == "log" {
			return ok("rev1\x1finit\x1f1700000000\n")
		}
		return ok("")
	})
	gh := gitHistory{r: histCmds, dir: t.TempDir(), keys: svc.sshKeys}
	commits, _, err := gh.LogWithKey(ctx, "stack-two", "github:alice/stack-two", dedName, "", "rev1")
	if err != nil || len(commits) != 1 || commits[0].Subject != "init" {
		t.Fatalf("LogWithKey = %+v, %v", commits, err)
	}
}

func TestAddTokenFallbackStillWorksWithoutSSH(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "github-token")
	if err := os.WriteFile(tokFile, []byte("ghp_testtoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := GitHubTokenFiles
	GitHubTokenFiles = []string{tokFile}
	t.Cleanup(func() { GitHubTokenFiles = saved })

	src := &fakeEnvSource{fn: func(_ context.Context, source string, env []string) (Fetched, error) {
		// Unauthenticated Fetch() still runs nixEnv() inside NixSource.
		if env != nil {
			t.Errorf("token fallback should not pass SSH env: %v", env)
		}
		if githubToken(GitHubTokenFiles) == "ghp_testtoken" && source == "github:alice/tok-repo" {
			return Fetched{Rev: "tok1", URL: "github:alice/tok-repo/tok1", Manifest: []byte(`{"version":7,"name":"tok-repo","nixpkgs":"nixos-unstable"}`)}, nil
		}
		return Fetched{}, errors.New("404 Not Found")
	}}
	svc, _ := newSSHTestService(t, src)
	s, err := svc.Add(context.Background(), "github:alice/tok-repo", "")
	if err != nil {
		t.Fatalf("token add: %v", err)
	}
	if row, _ := svc.st.Queries().GetStack(context.Background(), s.Name); row.SshKey != "" {
		t.Errorf("token stack got ssh_key = %q, want empty", row.SshKey)
	}
}
