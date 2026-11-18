package sysupdates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// nixpkgsGit is where the NixOS release branches are listed.
const nixpkgsGit = "https://github.com/NixOS/nixpkgs.git"

// releaseRe is a NixOS release: YY.05 or YY.11.
var releaseRe = regexp.MustCompile(`^[0-9]{2}\.(05|11)$`)

// validRelease reports whether s names a NixOS release (YY.05 or YY.11).
func validRelease(s string) bool { return releaseRe.MatchString(s) }

// releaseOf is the NixOS release a branch names ("nixos-26.05" → "26.05");
// "" for anything else (nixos-unstable, nixos-26.05-small, a revision).
func releaseOf(ref string) string {
	r, ok := strings.CutPrefix(ref, "nixos-")
	if !ok || !validRelease(r) {
		return ""
	}
	return r
}

// released reports whether release r is out at t. A release branch exists
// about two weeks before the release, which lands at the end of its month
// (YY.05 → May, YY.11 → November); until then it is a pre-release.
func released(r string, t time.Time) bool {
	var yy, mm int
	if _, err := fmt.Sscanf(r, "%d.%d", &yy, &mm); err != nil {
		return false
	}
	// The first day of the following month (UTC).
	return !t.Before(time.Date(2000+yy, time.Month(mm)+1, 1, 0, 0, 0, 0, time.UTC))
}

// nextRelease picks the release to upgrade to from the release branches that
// exist: the newest one after current, or current itself when the base is
// pinned to a pre-release revision instead of following its branch.
func nextRelease(current string, pinned bool, branches []string) (string, bool) {
	best := ""
	for _, r := range branches {
		if (r > current || (pinned && r == current)) && r > best {
			best = r
		}
	}
	return best, best != ""
}

// nixosReleases lists the NixOS releases with a release branch in nixpkgs,
// oldest first.
func nixosReleases(ctx context.Context, r cmdrunner.Runner) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := r.Run(ctx, cmdrunner.Cmd{
		Module: "sysupdates", Name: "git", Args: []string{"ls-remote", "--heads", nixpkgsGit, "refs/heads/nixos-*"},
	})
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %w", nixpkgsGit, err)
	}
	var out []string
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		if _, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			if r := releaseOf(strings.TrimPrefix(name, "refs/heads/")); r != "" {
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// baseInput is the flake's `nixpkgs` input, the OS base, as flake.lock
// records it: the branch it follows, "" when it is pinned to a revision.
// ok is false when the lock cannot be read or has no such input.
func baseInput(repo string) (follows string, ok bool) {
	if repo == "" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(repo, "flake.lock"))
	if err != nil {
		return "", false
	}
	var lock struct {
		Root  string `json:"root"`
		Nodes map[string]struct {
			Inputs   map[string]json.RawMessage `json:"inputs"`
			Original struct {
				Ref string `json:"ref"`
			} `json:"original"`
		} `json:"nodes"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return "", false
	}
	var node string
	if json.Unmarshal(lock.Nodes[lock.Root].Inputs["nixpkgs"], &node) != nil {
		return "", false
	}
	n, found := lock.Nodes[node]
	return n.Original.Ref, found
}
