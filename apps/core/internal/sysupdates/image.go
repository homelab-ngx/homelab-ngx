package sysupdates

// The host's updates. The host image (root and UKI, A/B with boot counting)
// and the homelab-core system extension are both updated by
// systemd-sysupdate from the host's own update folder. A release is
// downloaded into it (KindDownload, fetch.go), or the host builds main
// itself (KindBuild runs image/build.sh). The operations run in a transient
// unit (deploy.go), since a core update restarts the daemon.
//
// The update folder is root's and local, so sysupdate reads it with
// --verify=no: a download checks the release's signed SHA256SUMS before it
// stages anything.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// The kinds of update operation.
const (
	KindBuild       = "build"
	KindCoreUpdate  = "core-update"
	KindImageUpdate = "image-update"
	// KindInstall installs what is staged, in order: the image into the
	// free slot, the core extension, then the reboot choice.
	KindInstall = "install"
)

// Where the image host keeps its updates; variables for tests.
var (
	imageUpdates  = "/var/lib/homelab/updates"
	imageBuildDir = "/var/cache/homelab-image"
	imageESPs     = []string{"/boot/EFI/Linux", "/efi/EFI/Linux"}
	coreExtDir    = "/var/lib/extensions.d"
	coreExtLink   = "/var/lib/extensions/homelab-core.raw"
	osRelease     = "/etc/os-release"
	sysupdateBin  = "/usr/lib/systemd/systemd-sysupdate"
	githubTokens  = []string{"/var/lib/homelab/github-token", "/run/secrets/nix/GITHUB_TOKEN"}
	// coreMinImageBin prints the oldest image version a core extension
	// runs on; an image from before the rule has no such program. A core
	// that did not start is listed in coreFailedFile, and no boot uses it
	// again by itself (the image's usr/lib/homelab/core-at-boot).
	coreMinImageBin = "/usr/lib/homelab/core-min-image"
	coreFailedFile  = "/var/lib/homelab/core-failed"
	imageKeep       = 2
	imageBuildTime  = 2 * time.Hour
)

// Errors of the image operations.
var (
	ErrNotImageHost = errors.New("this host does not run the homelab image, so it has no updates")
	ErrBadReboot    = errors.New(`reboot: "" (later), "now" or a time today such as 03:30`)
)

var rebootAtRE = regexp.MustCompile(`^([01]?\d|2[0-3]):[0-5]\d$`)

// ImageVersion is one installed version of the host image.
type ImageVersion struct {
	Version string `json:"version"`
	// State: booted (running now), good (booted fine before), pending (not
	// booted yet; the next reboot tries it), bad (failed every try).
	State     string `json:"state" enum:"booted,good,pending,bad"`
	TriesLeft int    `json:"tries_left,omitempty" doc:"Boots a pending version still gets before it counts as bad."`
}

// CoreExtension is the homelab-core system extension's versions.
type CoreExtension struct {
	Active    string   `json:"active" doc:"The version merged now; empty when none is installed."`
	Installed []string `json:"installed"`
	Available []string `json:"available" doc:"Staged in the update folder, newer than the active one."`
	// FellBackFrom: the boot check put the core before back (boot.go).
	FellBackFrom string `json:"fell_back_from,omitempty" doc:"A core version that did not start at boot; the host runs the one before."`
}

// ImageStatus is where the image host stands.
type ImageStatus struct {
	Booted    string         `json:"booted" doc:"The image version running now."`
	Installed []ImageVersion `json:"installed" doc:"Versions in the boot menu, newest first."`
	Available []string       `json:"available" doc:"Staged in the update folder, newer than every installed one."`
	// PendingReboot: a newer version is installed and boots next.
	PendingReboot bool `json:"pending_reboot"`
	// FellBackFrom is a newer version that failed its tries, so the host is
	// back on the one before.
	FellBackFrom string `json:"fell_back_from,omitempty"`
	Default      string `json:"default,omitempty" doc:"The version set to boot by default with bootImage; empty: the newest version that is not bad."`
	Oneshot      string `json:"oneshot,omitempty" doc:"The version set to boot once, at the next reboot."`
	BootCheck    string `json:"boot_check,omitempty" doc:"Why this boot did not pass its check; empty when it did. The host stays up on a version that was good before."`
	// AutoInstallAt: see SetAutoInstall.
	AutoInstallAt string        `json:"auto_install_at,omitempty" doc:"The time of day (HH:MM, the host's time zone) a downloaded release installs by itself, with a reboot for a new image; empty: only when asked."`
	Core          CoreExtension `json:"core"`
	Source        string        `json:"source" doc:"What a build fetches: the repository core comes from."`
	Release       ReleaseCheck  `json:"release" doc:"The last look at the release server."`
}

// SetImage says the host runs the homelab image.
func (m *Module) SetImage(on bool) { m.image = on }

// IsImage reports whether the host is an image host.
func (m *Module) IsImage() bool { return m.image }

var ukiRE = regexp.MustCompile(`^homelab_([^+]+?)(?:\+(\d+)(?:-(\d+))?)?\.efi$`)

// parseUKIs reads the image versions in the boot menu from their UKI file
// names (systemd-boot's boot counting: name+LEFT-DONE.efi), newest first.
func parseUKIs(names []string, booted string) []ImageVersion {
	var out []ImageVersion
	for _, n := range names {
		m := ukiRE.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		v := ImageVersion{Version: m[1], State: "good"}
		if m[2] != "" {
			left, _ := strconv.Atoi(m[2])
			v.TriesLeft = left
			v.State = "pending"
			if left == 0 {
				v.State = "bad"
			}
		}
		if v.Version == booted {
			v.State, v.TriesLeft = "booted", 0
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b ImageVersion) int { return compareVersions(b.Version, a.Version) })
	return out
}

// versionsIn lists the versions of the files in dir that re matches (one
// group: the version), newest first.
func versionsIn(dir string, re *regexp.Regexp) []string {
	entries, _ := os.ReadDir(dir)
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if m := re.FindStringSubmatch(e.Name()); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	slices.SortFunc(out, func(a, b string) int { return compareVersions(b, a) })
	return out
}

var (
	stagedImageRE = regexp.MustCompile(`^homelab_([^+]+)\.efi$`)
	coreFileRE    = regexp.MustCompile(`^homelab-core_(.+)\.raw$`)
	// stagedFileRE is any of a staged version's files: its UKI, root and
	// root verity.
	stagedFileRE = regexp.MustCompile(`^homelab_(.+)\.(?:efi|root(?:-verity)?\.[0-9a-fA-F-]+\.raw)$`)
)

func bootedVersion() string {
	b, _ := os.ReadFile(osRelease)
	m := regexp.MustCompile(`(?m)^IMAGE_VERSION="?([^"\n]+)"?$`).FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func ukiNames() []string {
	for _, d := range imageESPs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	return nil
}

func activeCore() string {
	t, err := os.Readlink(coreExtLink)
	if err != nil {
		return ""
	}
	if m := coreFileRE.FindStringSubmatch(filepath.Base(t)); m != nil {
		return m[1]
	}
	return ""
}

func newer(list []string, than string) []string {
	out := []string{}
	for _, v := range list {
		if than == "" || compareVersions(v, than) > 0 {
			out = append(out, v)
		}
	}
	return out
}

// ImageStatus reads the image host's versions from its files.
func (m *Module) ImageStatus(context.Context) (ImageStatus, error) {
	if !m.image {
		return ImageStatus{}, ErrNotImageHost
	}
	st := ImageStatus{Booted: bootedVersion(), Source: m.dep.Source, Release: m.Release()}
	st.Installed = parseUKIs(ukiNames(), st.Booted)
	if st.Installed == nil {
		st.Installed = []ImageVersion{}
	}
	newest := st.Booted
	for _, v := range st.Installed {
		if compareVersions(v.Version, newest) > 0 {
			newest = v.Version
		}
		if compareVersions(v.Version, st.Booted) > 0 {
			switch v.State {
			case "pending", "good":
				st.PendingReboot = true
			case "bad":
				if st.FellBackFrom == "" {
					st.FellBackFrom = v.Version
				}
			}
		}
	}
	st.Available = newer(versionsIn(imageUpdates, stagedImageRE), newest)
	st.Core.Active = activeCore()
	st.Core.Installed = versionsIn(coreExtDir, coreFileRE)
	if st.Core.Installed == nil {
		st.Core.Installed = []string{}
	}
	top := st.Core.Active
	if len(st.Core.Installed) > 0 && compareVersions(st.Core.Installed[0], top) > 0 {
		top = st.Core.Installed[0]
	}
	st.Core.Available = newer(versionsIn(filepath.Join(imageUpdates, "core"), coreFileRE), top)
	st.Default, st.Oneshot = entryVersion(loaderVar("LoaderEntryDefault")), entryVersion(loaderVar("LoaderEntryOneShot"))
	st.BootCheck = bootCheckFailure()
	if fb, ok := coreFellBack(); ok {
		st.Core.FellBackFrom = fb.From
	}
	st.AutoInstallAt = m.autoInstallAt()
	return st, nil
}

// StartImage starts a build, a core update, an image update or an install
// of everything staged. reboot is for a new image: "" leaves the new version
// for the next reboot, "now" reboots at once, "HH:MM" at that time.
func (m *Module) StartImage(ctx context.Context, kind, reboot string) (CoreDeploy, error) {
	if !m.image {
		return CoreDeploy{}, ErrNotImageHost
	}
	if kind == KindBuild && m.dep.Source == "" {
		return CoreDeploy{}, ErrDeployNotSet
	}
	if reboot != "" && ((kind != KindImageUpdate && kind != KindInstall) || (reboot != "now" && !rebootAtRE.MatchString(reboot))) {
		return CoreDeploy{}, ErrBadReboot
	}
	return m.start(ctx, kind, "--reboot", reboot)
}

// ── the work, in the transient unit ──────────────────────────────────────

func (x *deployRun) work(ctx context.Context) (result, reason string) {
	switch x.o.Kind {
	case KindBuild:
		return x.build(ctx)
	case KindCoreUpdate:
		return x.coreUpdate(ctx)
	case KindImageUpdate:
		return x.imageUpdate(ctx)
	case KindInstall:
		return x.install(ctx)
	case KindDownload:
		return x.download(ctx)
	}
	return DeployFailed, "unknown kind " + x.o.Kind
}

// gitURL is a clonable URL for a flake reference of the repository.
func gitURL(source string) (string, error) {
	switch {
	case strings.HasPrefix(source, "github:"):
		parts := strings.SplitN(strings.TrimPrefix(source, "github:"), "/", 3)
		if len(parts) < 2 {
			return "", fmt.Errorf("%q is not github:owner/repo", source)
		}
		return "https://github.com/" + parts[0] + "/" + strings.SplitN(parts[1], "?", 2)[0] + ".git", nil
	case strings.HasPrefix(source, "git+"):
		u := strings.SplitN(strings.TrimPrefix(source, "git+"), "?", 2)[0]
		return strings.TrimPrefix(u, "file://"), nil
	}
	return "", fmt.Errorf("%q is not a github: or git+ reference", source)
}

// gitEnv authenticates a fetch from GitHub with the host's token, when it
// has one, without putting it on a command line.
func gitEnv() []string {
	for _, p := range githubTokens {
		b, err := os.ReadFile(p)
		if tok := strings.TrimSpace(string(b)); err == nil && tok != "" {
			auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
			return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
				"GIT_CONFIG_VALUE_0=Authorization: Basic " + auth, "GIT_TERMINAL_PROMPT=0"}
		}
	}
	return []string{"GIT_TERMINAL_PROMPT=0"}
}

func (x *deployRun) cmd(ctx context.Context, timeout time.Duration, env []string, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out strings.Builder
	_, err := x.r.Run(cctx, cmdrunner.Cmd{Module: "sysupdates", Name: name, Args: args, Env: env,
		OnLine: func(_, line string) { out.WriteString(line + "\n"); x.logLine(line) }})
	return out.String(), err
}

// build fetches main and builds the image and the core extension from it
// into the update folder.
func (x *deployRun) build(ctx context.Context) (string, string) {
	x.phase("Fetch")
	url, err := gitURL(x.o.Source)
	if err != nil {
		return DeployFailed, err.Error()
	}
	src := filepath.Join(imageBuildDir, "src")
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		_ = os.RemoveAll(src)
		if err := os.MkdirAll(src, 0o755); err != nil {
			return DeployFailed, err.Error()
		}
		if _, err := x.cmd(ctx, time.Minute, nil, "git", "init", "-q", src); err != nil {
			return DeployFailed, err.Error()
		}
	}
	// The release tags come along: build.sh versions a build of main above
	// the newest release.
	if _, err := x.cmd(ctx, 10*time.Minute, gitEnv(), "git", "-C", src, "fetch", "-q", "--depth", "1", "--tags", url, "HEAD"); err != nil {
		return DeployFailed, "main could not be fetched: " + err.Error()
	}
	if _, err := x.cmd(ctx, time.Minute, nil, "git", "-C", src, "reset", "--hard", "-q", "FETCH_HEAD"); err != nil {
		return DeployFailed, err.Error()
	}
	rev, _ := x.cmd(ctx, time.Minute, nil, "git", "-C", src, "rev-parse", "HEAD")
	x.d.Rev = strings.TrimSpace(rev)

	x.phase("Build")
	out := filepath.Join(imageBuildDir, "out")
	_ = os.RemoveAll(out)
	// Run through its shebang: cmdrunner refuses a shell as the command.
	// One Nix build at a time: the host keeps its stacks running meanwhile.
	if _, err := x.cmd(ctx, imageBuildTime, []string{"HOMELAB_IMAGE_CACHE=" + imageBuildDir, "HOMELAB_IMAGE_OUT=" + out, "HOMELAB_BUILD_JOBS=1"},
		filepath.Join(src, "image", "build.sh")); err != nil {
		return DeployBuildFailed, "the build failed: " + err.Error()
	}

	x.phase("Stage")
	versions := versionsIn(out, stagedImageRE)
	if len(versions) == 0 {
		return DeployBuildFailed, "the build made no image"
	}
	v := versions[0]
	x.d.Version = v
	if err := stage(out, v); err != nil {
		return DeployFailed, "staging the update: " + err.Error()
	}
	_ = os.RemoveAll(out)
	x.phase("Built " + v)
	return DeployHealthy, ""
}

// stage copies version v's UKI, root slot and core extension from out into
// the update folder and keeps the newest imageKeep of each.
func stage(out, v string) error { return stageWith(out, v, copyFile) }

// stageWith is stage with put moving each file into place.
func stageWith(out, v string, put func(from, to string) error) error {
	core := filepath.Join(imageUpdates, "core")
	if err := os.MkdirAll(core, 0o755); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(out, "homelab_"+v+".root*.raw"))
	files = append(files, filepath.Join(out, "homelab_"+v+".efi"))
	for _, f := range files {
		if err := put(f, filepath.Join(imageUpdates, filepath.Base(f))); err != nil {
			return err
		}
	}
	if err := put(filepath.Join(out, "core", "homelab-core_"+v+".raw"), filepath.Join(core, "homelab-core_"+v+".raw")); err != nil {
		return err
	}
	prune(imageUpdates, stagedImageRE, stagedFileRE)
	prune(core, coreFileRE, coreFileRE)
	return nil
}

// prune removes all but the newest imageKeep versions in dir: the files
// that files matches with an old version as its group. A version is matched
// whole, not as a prefix: 0.1.0's files are not 0.1.0.202611190130's.
func prune(dir string, re, files *regexp.Regexp) {
	vs := versionsIn(dir, re)
	if len(vs) <= imageKeep {
		return
	}
	old := map[string]bool{}
	for _, v := range vs[imageKeep:] {
		old[v] = true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if m := files.FindStringSubmatch(e.Name()); m != nil && old[m[1]] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// copyFile copies through a temporary file, so sysupdate never sees half of
// one; a root slot is gigabytes, so it streams.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, to)
}

// moveFile renames, and copies when from is on another file system.
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	if err := copyFile(from, to); err != nil {
		return err
	}
	return os.Remove(from)
}

// coreWaits is the newest staged core when it is newer than the active one
// and needs a newer image than the host runs, with that image's version;
// "" when the staged core can be used now. An image without
// coreMinImageBin knows no minimum, and a core is tried on it as before.
func (x *deployRun) coreWaits(ctx context.Context) (core, image string) {
	staged := versionsIn(filepath.Join(imageUpdates, "core"), coreFileRE)
	if len(staged) == 0 || compareVersions(staged[0], activeCore()) <= 0 {
		return "", ""
	}
	if _, err := os.Stat(coreMinImageBin); err != nil {
		return "", ""
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := x.r.Run(cctx, cmdrunner.Cmd{Module: "sysupdates", Name: coreMinImageBin,
		Args: []string{filepath.Join(imageUpdates, "core", "homelab-core_"+staged[0]+".raw")}})
	min := strings.TrimSpace(res.Stdout)
	if err != nil || min == "" || compareVersions(min, bootedVersion()) <= 0 {
		return "", ""
	}
	return staged[0], min
}

// coreFailed lists the cores that did not start.
func coreFailed() []string {
	b, _ := os.ReadFile(coreFailedFile)
	return strings.Fields(string(b))
}

// markCoreFailed writes down (failed) or forgets a core that did not start.
func markCoreFailed(v string, failed bool) {
	list := slices.DeleteFunc(coreFailed(), func(s string) bool { return s == v })
	if failed {
		list = append(list, v)
	}
	if len(list) == 0 {
		_ = os.Remove(coreFailedFile)
		return
	}
	if err := os.WriteFile(coreFailedFile, []byte(strings.Join(list, "\n")+"\n"), 0o644); err != nil {
		log.Warn("the cores that did not start were not written down", "error", err)
	}
}

// coreUpdate merges the newest staged core extension and restarts the
// daemon and Caddy on it; if they do not come back, the one before goes
// back and no boot tries the new one again by itself. A core that needs a
// newer image than the host runs is refused.
func (x *deployRun) coreUpdate(ctx context.Context) (string, string) {
	prev := activeCore()
	if v, min := x.coreWaits(ctx); v != "" {
		x.d.Version = v
		return DeployFailed, fmt.Sprintf("core %s needs image %s or newer, and the host runs %s: install the image and restart", v, min, bootedVersion())
	}
	x.phase("Update")
	if out, err := x.cmd(ctx, 10*time.Minute, nil, sysupdateBin, "--component=homelab-core", "--verify=no", "update"); err != nil {
		return DeployFailed, "sysupdate: " + strings.TrimSpace(lastLines(out, 5))
	}
	now := activeCore()
	x.d.Version = now
	if now == prev {
		x.phase("Already the newest core " + now)
		return DeployHealthy, ""
	}
	// A unit in its own mount namespace (Caddy) keeps the old /usr until it
	// restarts; a reload is not enough.
	x.phase("Restart")
	x.restartsFrom = x.restarts(ctx)
	if _, err := x.cmd(ctx, 2*time.Minute, nil, "systemctl", "restart", "homelab-daemon.service", "caddy.service"); err != nil {
		x.logf("the restart reported: %v", err)
	}
	x.phase("Health check")
	why := x.waitHealthy(ctx)
	if why == "" {
		// Asked for and healthy: an earlier failure of it is forgotten.
		markCoreFailed(now, false)
		return DeployHealthy, ""
	}
	if prev == "" {
		return DeployFailed, why
	}
	x.phase("Roll back")
	markCoreFailed(now, true)
	if err := os.Remove(coreExtLink); err == nil || errors.Is(err, os.ErrNotExist) {
		_ = os.Symlink(filepath.Join("..", filepath.Base(coreExtDir), "homelab-core_"+prev+".raw"), coreExtLink)
	}
	_, _ = x.cmd(ctx, time.Minute, nil, "systemd-sysext", "refresh")
	_, _ = x.cmd(ctx, 2*time.Minute, nil, "systemctl", "restart", "homelab-daemon.service", "caddy.service")
	if again := x.waitHealthy(ctx); again != "" {
		return DeployFailed, why + "; core " + prev + " did not come back either: " + again
	}
	return DeployRolledBack, why
}

// imageUpdate installs the newest staged image into the free slot; it boots
// at the next reboot, which this may start.
func (x *deployRun) imageUpdate(ctx context.Context) (string, string) {
	v, why := x.installImage(ctx)
	if why != "" {
		return DeployFailed, why
	}
	if v == "" {
		return DeployHealthy, ""
	}
	return x.rebootChoice(ctx)
}

// install installs what is staged in the order that keeps the host on a
// working pair: the image into the free slot (nothing runs it yet), then
// the core extension, which is checked and put back when it does not start,
// and only then the reboot into the new image. A core that needs the new
// image is left for that image's first boot, which applies it.
func (x *deployRun) install(ctx context.Context) (string, string) {
	image, why := x.installImage(ctx)
	if why != "" {
		return DeployFailed, why
	}
	if v, min := x.coreWaits(ctx); image != "" && v != "" && compareVersions(min, image) <= 0 {
		x.logf("core %s needs image %s or newer: it is applied when image %s starts", v, min, image)
		return x.rebootChoice(ctx)
	}
	result, reason := x.coreUpdate(ctx)
	if image == "" {
		return result, reason
	}
	x.d.Version = image
	if result != DeployHealthy {
		return result, reason + "; image " + image + " is installed and boots at the next reboot"
	}
	return x.rebootChoice(ctx)
}

// installImage puts the newest staged image in the free slot and returns its
// version, "" when the host already has the newest; why says what failed.
func (x *deployRun) installImage(ctx context.Context) (version, why string) {
	booted := bootedVersion()
	x.phase("Update")
	if out, err := x.cmd(ctx, 30*time.Minute, nil, sysupdateBin, "--verify=no", "update"); err != nil {
		return "", "sysupdate: " + strings.TrimSpace(lastLines(out, 5))
	}
	vs := parseUKIs(ukiNames(), booted)
	if len(vs) == 0 || compareVersions(vs[0].Version, booted) <= 0 {
		x.phase("Already the newest image " + booted)
		return "", ""
	}
	x.d.Version = vs[0].Version
	// A default set by hand (bootImage) would keep the host on the version
	// it names: a new version takes over again.
	if loaderVar("LoaderEntryDefault") != "" {
		if _, err := x.cmd(ctx, time.Minute, nil, "bootctl", "set-default", ""); err != nil {
			x.logf("the default boot entry could not be cleared: %v", err)
		}
	}
	return vs[0].Version, ""
}

// rebootChoice does what the operation was asked to do after a new image is
// in its slot: leave it for the next reboot, reboot now, or at a time.
func (x *deployRun) rebootChoice(ctx context.Context) (string, string) {
	switch reboot := x.o.Reboot; reboot {
	case "":
		x.phase("Installed " + x.d.Version + "; it boots at the next reboot")
	case "now":
		x.phase("Installed " + x.d.Version + "; rebooting")
		fin := x.now()
		x.d.Result, x.d.FinishedAt = DeployHealthy, &fin
		if err := x.save(); err != nil {
			return DeployFailed, err.Error()
		}
		if _, err := x.cmd(ctx, time.Minute, nil, "systemctl", "reboot"); err != nil {
			return DeployFailed, "the reboot did not start: " + err.Error()
		}
	default:
		x.phase("Installed " + x.d.Version + "; rebooting at " + reboot)
		if _, err := x.cmd(ctx, time.Minute, nil, "shutdown", "-r", reboot); err != nil {
			return DeployFailed, "the reboot could not be scheduled: " + err.Error()
		}
	}
	return DeployHealthy, ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
