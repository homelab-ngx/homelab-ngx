package stacks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/store"
)

// fetchInfo is what a fetch learns about the source beyond its manifest. It
// is stored with the fetched commit (stacks.fetched_info).
type fetchInfo struct {
	// Commits since the generation that ran at the fetch, newest first.
	Commits []commit `json:"commits"`
	// Dirty: a local checkout had uncommitted changes.
	Dirty bool `json:"dirty"`
}

type commit struct {
	Rev     string `json:"rev"`
	Subject string `json:"subject"`
	At      int64  `json:"at"` // unix seconds
}

// History reads a source's commits (gitHistory).
type History interface {
	// Log returns the commits up to rev, newest first: those after since
	// when since is set and known, else the last few. dirty reports
	// uncommitted changes in a local checkout.
	Log(ctx context.Context, name, source, since, rev string) (commits []commit, dirty bool, err error)
}

// learn gathers the fetch info of commit f of the stack's source. It is not
// fatal: a stack installs without its history.
func (s *Service) learn(ctx context.Context, name, source string, f Fetched, since string) fetchInfo {
	info := fetchInfo{Commits: []commit{}}
	if s.history != nil {
		c, dirty, err := s.history.Log(ctx, name, source, since, f.Rev)
		if err != nil {
			s.log.Warn("stack commits not read", "stack", name, "error", err)
		} else {
			info.Commits, info.Dirty = c, dirty
		}
	}
	return info
}

func (s *Service) storeInfo(ctx context.Context, name string, info fetchInfo) {
	b, _ := json.Marshal(info)
	if err := s.st.Queries().SetStackFetchedInfo(ctx, store.SetStackFetchedInfoParams{Name: name, Info: string(b)}); err != nil {
		s.log.Warn("stack fetch info not stored", "stack", name, "error", err)
	}
}

func readInfo(r store.Stack) fetchInfo {
	var info fetchInfo
	if r.FetchedInfo != "" {
		_ = json.Unmarshal([]byte(r.FetchedInfo), &info)
	}
	return info
}

// ── git history ─────────────────────────────────────────────────────────

// gitHistory reads commits with git: a local checkout in place, anything
// else through a blob-less bare mirror per stack.
type gitHistory struct {
	r          cmdrunner.Runner
	dir        string   // mirrors
	tokenFiles []string // where the host's GitHub token may be; the first that has one
}

// maxCommits bounds the commits listed per fetch.
const maxCommits = 50

func (h gitHistory) git(ctx context.Context, env []string, args ...string) (string, error) {
	return h.gitAs(ctx, "", env, args...)
}

// gitAs runs git as asUser; "" is the daemon's own user.
func (h gitHistory) gitAs(ctx context.Context, asUser string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	res, err := h.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "git", Args: args, AsUser: asUser,
		Env: append([]string{"GIT_TERMINAL_PROMPT=0"}, env...),
	})
	return res.Stdout, err
}

// remote is where git reads a source from: a URL and ref, or a local path.
func remote(source string) (u, ref, local string, err error) {
	switch {
	case strings.HasPrefix(source, "github:"):
		parts := strings.SplitN(strings.TrimPrefix(source, "github:"), "/", 3)
		if len(parts) < 2 {
			return "", "", "", fmt.Errorf("unreadable source %q", source)
		}
		ref = "HEAD"
		if len(parts) == 3 {
			ref = parts[2]
		}
		return "https://github.com/" + parts[0] + "/" + parts[1] + ".git", ref, "", nil
	case strings.HasPrefix(source, "git+"):
		p, err := url.Parse(strings.TrimPrefix(source, "git+"))
		if err != nil {
			return "", "", "", err
		}
		ref = p.Query().Get("ref")
		p.RawQuery = ""
		if p.Scheme == "file" {
			return "", "", p.Path, nil
		}
		if ref == "" {
			ref = "HEAD"
		}
		return p.String(), ref, "", nil
	}
	return "", "", "", fmt.Errorf("unreadable source %q", source)
}

func (h gitHistory) Log(ctx context.Context, name, source, since, rev string) ([]commit, bool, error) {
	u, ref, local, err := remote(source)
	if err != nil {
		return nil, false, err
	}
	repo, dirty, as := local, false, ""
	if local != "" {
		// The operator's checkout: git runs as its owner (see local.go).
		as = ownerOf(local)
		out, err := h.gitAs(ctx, as, nil, "-C", local, "status", "--porcelain")
		if err != nil {
			return nil, false, err
		}
		dirty = strings.TrimSpace(out) != ""
	} else {
		repo = filepath.Join(h.dir, name+".git")
		if _, err := os.Stat(repo); err != nil {
			if err := os.MkdirAll(h.dir, 0o700); err != nil {
				return nil, false, err
			}
			if _, err := h.git(ctx, nil, "init", "-q", "--bare", repo); err != nil {
				return nil, false, err
			}
		}
		if _, err := h.git(ctx, h.auth(u), "-C", repo, "fetch", "-q", "--filter=blob:none", "--no-tags",
			"--", u, "+"+ref+":refs/heads/stack"); err != nil {
			return nil, false, err
		}
	}
	args := []string{"-C", repo, "log", "-n", strconv.Itoa(maxCommits), "--format=%H%x1f%s%x1f%ct", rev}
	if since != "" && since != rev {
		if _, err := h.gitAs(ctx, as, nil, "-C", repo, "cat-file", "-e", since+"^{commit}"); err == nil {
			args = append(args, "^"+since)
		}
	} else if since == rev {
		return []commit{}, dirty, nil
	}
	out, err := h.gitAs(ctx, as, nil, args...)
	if err != nil {
		return nil, dirty, err
	}
	commits := []commit{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 3 {
			continue
		}
		at, _ := strconv.ParseInt(f[2], 10, 64)
		commits = append(commits, commit{Rev: f[0], Subject: f[1], At: at})
	}
	return commits, dirty, nil
}

// auth gives git the GitHub token through its environment: never argv, which
// any user on the host can read.
func (h gitHistory) auth(u string) []string {
	if !strings.HasPrefix(u, "https://github.com/") {
		return nil
	}
	tok := githubToken(h.tokenFiles)
	if tok == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
	}
}

func commitsView(info fetchInfo) []StackCommit {
	out := make([]StackCommit, 0, len(info.Commits))
	for _, c := range info.Commits {
		out = append(out, StackCommit{Rev: c.Rev, Subject: c.Subject, At: time.Unix(c.At, 0)})
	}
	return out
}

// GitHubTokenFiles is where a host keeps the token its stacks' private
// repos are fetched with: the image host's, then the one a NixOS host's
// secrets put in place.
var GitHubTokenFiles = []string{"/var/lib/homelab/github-token", "/run/secrets/nix/GITHUB_TOKEN"}

// githubToken is the token in the first of files that holds one.
func githubToken(files []string) string {
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if tok := strings.TrimSpace(string(raw)); err == nil && tok != "" {
			return tok
		}
	}
	return ""
}

// nixEnv gives nix the host's GitHub token for a private repo, through its
// environment: never argv, which any user on the host can read. Nix's own
// config on a NixOS host may name the same token; the image's cannot.
func nixEnv() []string {
	if tok := githubToken(GitHubTokenFiles); tok != "" {
		return []string{"NIX_CONFIG=access-tokens = github.com=" + tok}
	}
	return nil
}
