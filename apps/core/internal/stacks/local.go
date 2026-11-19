package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// A local folder source (git+file://) is the operator's own checkout, owned
// by their user. The daemon never runs git, or nix's git fetcher, as root in
// it: git refuses a repository another user owns, and a repo config such as
// core.fsmonitor would otherwise run as root. Both run as the folder's owner
// instead, and root builds only the store copy that fetch produces.

// localPath is the folder of a git+file source, or "".
func localPath(ref string) string {
	rest, ok := strings.CutPrefix(ref, "git+file://")
	if !ok {
		return ""
	}
	path, _, _ := strings.Cut(rest, "?")
	return path
}

// ownerOf is the user that owns path, or "" when that is root or unknown.
var ownerOf = func(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return ""
	}
	return u.Username
}

// committedRef makes a local folder source read its checked-out commit, not
// the working tree: without a ref, nix copies uncommitted changes too.
func committedRef(source string) string {
	if localPath(source) == "" || strings.Contains(source, "ref=") {
		return source
	}
	if strings.Contains(source, "?") {
		return source + "&ref=HEAD"
	}
	return source + "?ref=HEAD"
}

// prefetched is what `nix flake prefetch --json` prints.
type prefetched struct {
	Hash      string `json:"hash"`
	StorePath string `json:"storePath"`
	Locked    struct {
		Type  string `json:"type"`
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		URL   string `json:"url"`
		Ref   string `json:"ref"`
		Rev   string `json:"rev"`
	} `json:"locked"`
}

// prefetch copies ref into the store, as the folder's owner for a local
// source.
func prefetch(ctx context.Context, r cmdrunner.Runner, ref string) (prefetched, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	c := cmdrunner.Cmd{
		Module: "stacks", Name: "nix",
		Args: []string{"--extra-experimental-features", "nix-command flakes",
			"flake", "prefetch", "--json", "--refresh", "--", ref},
		Env: nixEnv(),
	}
	if p := localPath(ref); p != "" {
		c.AsUser = ownerOf(p)
	}
	res, err := r.Run(ctx, c)
	if err != nil {
		return prefetched{}, fmt.Errorf("nix flake prefetch: %w", err)
	}
	var out prefetched
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return prefetched{}, fmt.Errorf("nix flake prefetch: unreadable output: %w", err)
	}
	return out, nil
}

// buildRef is the flake reference the build reads. A local source owned by
// another user is fetched as that user first; root then builds the store
// copy, pinned by its hash.
func (s *Service) buildRef(ctx context.Context, ref string) (string, error) {
	p := localPath(ref)
	if p == "" || ownerOf(p) == "" {
		return ref, nil
	}
	f, err := prefetch(ctx, s.r, ref)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(f.StorePath, "/nix/store/") || f.Hash == "" {
		return "", errors.New("nix flake prefetch: no store path for the local source")
	}
	return "path:" + f.StorePath + "?narHash=" + url.QueryEscape(f.Hash), nil
}
