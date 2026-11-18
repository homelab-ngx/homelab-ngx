package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// Fetched is one commit of a stack's source.
type Fetched struct {
	// Rev is the commit hash.
	Rev string
	// URL is the locked flake reference of that commit.
	URL string
	// Manifest is the raw homelab-stack.json of that commit.
	Manifest []byte
	// StorePath is the commit's tree in the store; empty in tests.
	StorePath string
	// NoFlake means the commit has no flake.nix at its root.
	NoFlake bool
}

// ErrNoManifest means the source has no homelab-stack.json.
var ErrNoManifest = errors.New("the repo has no " + ManifestFile)

// fetchTimeout bounds one fetch; a stack repo is small.
const fetchTimeout = 2 * time.Minute

// A source is a flake reference to a git repo. Local paths and tarballs are
// refused: a stack is pinned to a commit.
var sourceRE = regexp.MustCompile(`^(github:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_./-]+)?|git\+(ssh|https|file)://[A-Za-z0-9_.@:/~+-]+(\?ref=[A-Za-z0-9_./-]+)?)$`)

// ValidSource reports whether s is a source the daemon accepts.
func ValidSource(s string) bool { return sourceRE.MatchString(s) }

// NixSource fetches a stack's source with the host's nix.
type NixSource struct {
	r cmdrunner.Runner
	// readFile is os.ReadFile; a seam for tests, which have no store path.
	readFile func(string) ([]byte, error)
}

// NewNixSource returns the fetcher.
func NewNixSource(r cmdrunner.Runner) *NixSource {
	return &NixSource{r: r, readFile: os.ReadFile}
}

// Fetch resolves source to its newest commit, copies that commit into the
// store and reads the manifest from it. Nothing from the repo is evaluated.
func (n *NixSource) Fetch(ctx context.Context, source string) (Fetched, error) {
	out, err := prefetch(ctx, n.r, committedRef(source))
	if err != nil {
		return Fetched{}, err
	}
	l := out.Locked
	if l.Rev == "" || !strings.HasPrefix(out.StorePath, "/nix/store/") {
		return Fetched{}, errors.New("nix flake prefetch: the source did not resolve to a commit")
	}
	var url string
	switch l.Type {
	case "github":
		url = fmt.Sprintf("github:%s/%s/%s", l.Owner, l.Repo, l.Rev)
	case "git":
		// The branch stays in the reference: without it nix may not find a
		// commit that is not on the default branch.
		url = fmt.Sprintf("git+%s?rev=%s", l.URL, l.Rev)
		if l.Ref != "" {
			url = fmt.Sprintf("git+%s?ref=%s&rev=%s", l.URL, l.Ref, l.Rev)
		}
	default:
		return Fetched{}, fmt.Errorf("nix flake prefetch: unsupported source type %q", l.Type)
	}
	data, err := n.readFile(filepath.Join(out.StorePath, ManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Fetched{}, ErrNoManifest
	}
	if err != nil {
		return Fetched{}, err
	}
	_, err = n.readFile(filepath.Join(out.StorePath, FlakeFile))
	noFlake := errors.Is(err, fs.ErrNotExist)
	if err != nil && !noFlake {
		return Fetched{}, err
	}
	return Fetched{Rev: l.Rev, URL: url, Manifest: data, StorePath: out.StorePath, NoFlake: noFlake}, nil
}
