package stacks

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// nixpkgsGit is where a channel is looked up.
const nixpkgsGit = "https://github.com/NixOS/nixpkgs.git"

// ResolveNixpkgs is the commit spec names: a commit is itself, a channel
// (nixos-unstable, nixos-YY.MM) is the newest commit of its branch, read
// with git ls-remote so nothing is downloaded.
func ResolveNixpkgs(ctx context.Context, r cmdrunner.Runner, spec string) (string, error) {
	if commitRE.MatchString(spec) {
		return spec, nil
	}
	if !channelRE.MatchString(spec) {
		return "", fmt.Errorf("%q is not a nixpkgs channel or commit", spec)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ref := "refs/heads/" + spec
	res, err := r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "git", Args: []string{"ls-remote", "--heads", nixpkgsGit, ref},
	})
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", spec, err)
	}
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		rev, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name == ref && commitRE.MatchString(rev) {
			return rev, nil
		}
	}
	return "", fmt.Errorf("nixpkgs has no branch %s", spec)
}

// pending reports whether the stack's running generation g is not what an
// update would build: the approved commit, for a version 7 stack the
// nixpkgs commit its manifest resolves to now, and for a version 8 stack the
// ids its folders' owners have now.
func pending(r store.Stack, g store.StackGeneration) bool {
	return g.Rev != r.ApprovedRev || (r.NixpkgsRev != "" && g.NixpkgsRev != r.NixpkgsRev) || g.HostIds != r.HostIds
}

// refreshNixpkgs resolves the approved manifest's nixpkgs and stores the
// commit, so a channel that moved shows as an update. A stack approved at a
// manifest older than version 7 has none. A failed lookup keeps the commit
// it had: the next check tries again.
func (s *Service) refreshNixpkgs(ctx context.Context, name string) {
	r, err := s.row(ctx, name)
	if err != nil {
		return
	}
	m, ok, err := approvedManifest(r)
	if err != nil {
		return
	}
	want := ""
	if ok && m.Version >= 7 {
		spec := nixpkgsSpec(r, m)
		if want, err = s.resolveNixpkgs(ctx, spec); err != nil {
			s.log.Warn("stack nixpkgs not looked up", "stack", name, "nixpkgs", spec, "error", err)
			return
		}
	}
	if want == r.NixpkgsRev {
		return
	}
	if err := s.st.Queries().SetStackNixpkgs(ctx, store.SetStackNixpkgsParams{Name: name, NixpkgsRev: want}); err != nil {
		s.log.Warn("stack nixpkgs not stored", "stack", name, "error", err)
		return
	}
	if want != "" {
		s.log.Info("stack nixpkgs resolved", "stack", name, "nixpkgs", nixpkgsSpec(r, m), "rev", want)
	}
}

// nixpkgsSpec is the nixpkgs a version 7 stack is built with: the
// operator's choice, else the one its approved manifest m recommends.
func nixpkgsSpec(r store.Stack, m Manifest) string {
	if r.Nixpkgs != "" {
		return r.Nixpkgs
	}
	return m.Nixpkgs
}

// nixpkgsFor is the nixpkgs commit a build of r's approved manifest m uses:
// the stored one, else its nixpkgs looked up now and stored.
func (s *Service) nixpkgsFor(ctx context.Context, r store.Stack, m Manifest) (string, error) {
	if r.NixpkgsRev != "" {
		return r.NixpkgsRev, nil
	}
	rev, err := s.resolveNixpkgs(ctx, nixpkgsSpec(r, m))
	if err != nil {
		return "", fmt.Errorf("building: %w", err)
	}
	if err := s.st.Queries().SetStackNixpkgs(ctx, store.SetStackNixpkgsParams{Name: r.Name, NixpkgsRev: rev}); err != nil {
		return "", err
	}
	return rev, nil
}

// buildWrapped builds a version 7 stack: its approved commit's module on the
// daemon's base module, with nixpkgs at nixpkgsRev.
// It returns the system and the host ids it was built with.
func (s *Service) buildWrapped(ctx context.Context, o *operation, r store.Stack, m Manifest, nixpkgsRev string) (system, ids string, err error) {
	if s.host.BuildsDir == "" {
		return "", "", errors.New("building: no directory for stack builds is configured")
	}
	ref, err := s.buildRef(ctx, r.ApprovedUrl)
	if err != nil {
		return "", "", fmt.Errorf("building: %w", err)
	}
	host, ids, err := s.hostModule(r.Name, m)
	if err != nil {
		return "", "", fmt.Errorf("building: %w", err)
	}
	dir := filepath.Join(s.host.BuildsDir, r.Name)
	w := Wrapper{Name: r.Name, Ref: ref, NixpkgsRev: nixpkgsRev, TimeZone: s.timeZone(), Host: host}
	if err := w.Write(dir); err != nil {
		return "", "", fmt.Errorf("building: %w", err)
	}
	system, err = s.buildLogged(ctx, o, "path:"+dir, "#"+WrapperSystem, "--no-write-lock-file")
	return system, ids, err
}

// refreshHostIDs stores the ids the approved manifest's folder owners have
// now, so a changed owner or path shows as an update. A folder it cannot
// read keeps the ids stored: the build says why.
func (s *Service) refreshHostIDs(ctx context.Context, name string) {
	r, err := s.row(ctx, name)
	if err != nil {
		return
	}
	m, ok, err := approvedManifest(r)
	if err != nil || !ok {
		return
	}
	_, ids, err := s.hostModule(name, m)
	if err != nil {
		s.log.Warn("stack folder owners not read", "stack", name, "error", err)
		return
	}
	if ids != r.HostIds {
		if err := s.st.Queries().SetStackHostIDs(ctx, store.SetStackHostIDsParams{Name: name, HostIds: ids}); err != nil {
			s.log.Warn("stack folder owners not stored", "stack", name, "error", err)
		}
	}
}

// SetNixpkgs chooses the nixpkgs a version 7 stack is built with: a channel
// or a commit, or "" for the one its manifest recommends. It is looked up
// at once, so a choice that names nothing is refused; the stack is not
// rebuilt until it is updated.
//
// A choice older than the nixpkgs the stack runs (see olderNixpkgs) is
// refused with nixpkgs_downgrade unless allowDowngrade is set: a newer
// version may already have upgraded the stack's data, such as a database.
func (s *Service) SetNixpkgs(ctx context.Context, name, spec string, allowDowngrade bool) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	m, ok, err := approvedManifest(r)
	if err != nil {
		return Stack{}, err
	}
	if !ok || m.Version < 7 {
		return Stack{}, httpx.Conflict("needs_version_7",
			"stack %q builds with the nixpkgs its repo pins; choosing one needs manifest version 7", name)
	}
	if spec != "" && !ValidNixpkgs(spec) {
		return Stack{}, httpx.BadRequest("invalid_nixpkgs",
			"%q is not nixos-unstable, nixos-YY.MM or a 40-character NixOS/nixpkgs commit", spec)
	}
	r.Nixpkgs = spec
	rev, err := s.resolveNixpkgs(ctx, nixpkgsSpec(r, m))
	if err != nil {
		return Stack{}, httpx.BadGateway("nixpkgs_lookup_failed", "%v", err)
	}
	if !allowDowngrade {
		if err := s.refuseDowngrade(ctx, r, rev); err != nil {
			return Stack{}, err
		}
	}
	if err := s.st.Queries().SetStackNixpkgsChoice(ctx, store.SetStackNixpkgsChoiceParams{
		Name: name, Nixpkgs: spec, NixpkgsRev: rev,
	}); err != nil {
		return Stack{}, err
	}
	s.log.Info("stack nixpkgs chosen", "stack", name, "nixpkgs", nixpkgsSpec(r, m), "rev", rev)
	s.writeLock(ctx)
	return s.Get(ctx, name)
}

// NixpkgsRelease is what a nixpkgs commit is: its NixOS release
// (lib/.version, such as 26.11; unstable carries the next release) and when
// it was made.
type NixpkgsRelease struct {
	Version  string
	Modified time.Time
}

// String is the release and day, as an operator reads it.
func (n NixpkgsRelease) String() string {
	return "NixOS " + n.Version + " of " + n.Modified.UTC().Format("2006-01-02")
}

// ReadNixpkgsRelease fetches the nixpkgs commit rev (nix keeps it, and the
// build that follows reads the same copy) and reads its release and date.
func ReadNixpkgsRelease(ctx context.Context, r cmdrunner.Runner, rev string) (NixpkgsRelease, error) {
	if !commitRE.MatchString(rev) {
		return NixpkgsRelease{}, fmt.Errorf("%q is not a nixpkgs commit", rev)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	res, err := r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "nix",
		Args: []string{"--extra-experimental-features", "nix-command flakes",
			"flake", "metadata", "--json", "--", "github:NixOS/nixpkgs/" + rev},
	})
	if err != nil {
		return NixpkgsRelease{}, fmt.Errorf("reading nixpkgs %s: %w", short(rev), err)
	}
	var meta struct {
		LastModified int64  `json:"lastModified"`
		Path         string `json:"path"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &meta); err != nil || !strings.HasPrefix(meta.Path, "/nix/store/") {
		return NixpkgsRelease{}, fmt.Errorf("reading nixpkgs %s: unreadable nix output", short(rev))
	}
	v, err := os.ReadFile(filepath.Join(meta.Path, "lib", ".version"))
	if err != nil {
		return NixpkgsRelease{}, fmt.Errorf("reading nixpkgs %s: %w", short(rev), err)
	}
	return NixpkgsRelease{Version: strings.TrimSpace(string(v)), Modified: time.Unix(meta.LastModified, 0)}, nil
}

// olderNixpkgs reports whether a is older than b: an earlier NixOS release,
// or the same release made earlier. The release decides first, because a
// stable branch gets commits as new as unstable's with older packages.
func olderNixpkgs(a, b NixpkgsRelease) bool {
	if c := compareRelease(a.Version, b.Version); c != 0 {
		return c < 0
	}
	return a.Modified.Before(b.Modified)
}

// compareRelease compares NixOS releases such as 26.05 and 26.11.
func compareRelease(a, b string) int {
	var ay, am, by, bm int
	fmt.Sscanf(a, "%d.%d", &ay, &am)
	fmt.Sscanf(b, "%d.%d", &by, &bm)
	return cmp.Or(cmp.Compare(ay, by), cmp.Compare(am, bm))
}

// refuseDowngrade refuses a nixpkgs commit rev older than the one r's
// running generation was built with. A stack never built has nothing to
// lose; a release that cannot be read is refused too, since a downgrade
// could not be ruled out.
func (s *Service) refuseDowngrade(ctx context.Context, r store.Stack, rev string) error {
	if r.Generation == 0 {
		return nil
	}
	g, err := s.st.Queries().GetStackGeneration(ctx, store.GetStackGenerationParams{Stack: r.Name, ID: r.Generation})
	if err != nil || g.NixpkgsRev == "" || g.NixpkgsRev == rev {
		return nil
	}
	running, err := s.nixpkgsRelease(ctx, g.NixpkgsRev)
	if err != nil {
		return httpx.BadGateway("nixpkgs_lookup_failed", "%v", err)
	}
	next, err := s.nixpkgsRelease(ctx, rev)
	if err != nil {
		return httpx.BadGateway("nixpkgs_lookup_failed", "%v", err)
	}
	if !olderNixpkgs(next, running) {
		return nil
	}
	return httpx.Conflict("nixpkgs_downgrade",
		"stack %q runs %s (%s); this choice is %s (%s), which is older. Going back can break a stack whose data a newer version already upgraded, such as a database; choose it again with allow_downgrade to go ahead",
		r.Name, running, short(g.NixpkgsRev), next, short(rev))
}

// stableRE is a NixOS release branch.
var stableRE = regexp.MustCompile(`^nixos-([0-9]{2})\.([0-9]{2})$`)

// supportedReleases is how many of the newest NixOS releases are offered.
const supportedReleases = 2

// channelsTTL is how long the list of channels is kept.
const channelsTTL = time.Hour

// ListChannels reads nixpkgs's release branches and returns the channels
// offered for a stack: nixos-unstable and the newest NixOS releases,
// newest first.
func ListChannels(ctx context.Context, r cmdrunner.Runner) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "git", Args: []string{"ls-remote", "--heads", nixpkgsGit, "refs/heads/nixos-*"},
	})
	if err != nil {
		return nil, fmt.Errorf("listing nixpkgs channels: %w", err)
	}
	var releases []string
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		if _, ref, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			if b := strings.TrimPrefix(ref, "refs/heads/"); stableRE.MatchString(b) {
				releases = append(releases, b)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(releases)))
	return append([]string{"nixos-unstable"}, releases[:min(len(releases), supportedReleases)]...), nil
}

// Channels is ListChannels, kept for channelsTTL.
func (s *Service) Channels(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	if s.channels != nil && s.now().Sub(s.channelsAt) < channelsTTL {
		c := s.channels
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	c, err := s.listChannels(ctx)
	if err != nil {
		return nil, httpx.BadGateway("nixpkgs_lookup_failed", "%v", err)
	}
	s.mu.Lock()
	s.channels, s.channelsAt = c, s.now()
	s.mu.Unlock()
	return c, nil
}
