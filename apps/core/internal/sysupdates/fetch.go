package sysupdates

// Releases: an image host looks for a newer release on the release server
// once a day. A newer one is downloaded in the deploy unit (KindDownload),
// its SHA256SUMS checked against the release key and every file against its
// sum, and staged in the update folder as a build from main would be. The
// operator is told once and installs it from the Core host page; nothing
// installs by itself.
//
// The server's layout (the release workflow publishes it):
//
//	/latest                                   the newest version
//	/<v>/SHA256SUMS, /<v>/SHA256SUMS.gpg      every file below, signed
//	/<v>/homelab_<v>.efi                      UKI          } an image update
//	/<v>/homelab_<v>.root*.raw                root, verity }
//	/<v>/core/homelab-core_<v>.raw            core extension
//	/<v>/homelab_<v>.raw                      installer (never downloaded here)

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"homelab-ngx.com/core/internal/notifications"
)

// KindDownload downloads the newest release into the update folder.
const KindDownload = "download"

// releaseKey is the public key releases are signed with (primary
// 74BE38EB9E1F070C2AF87E4481D74B7E90B6ADB0).
//
//go:embed release-signing.asc
var releaseKey []byte

const (
	defaultReleaseURL = "https://dl.homelab-ngx.com"
	// HOMELAB_RELEASE_URL and HOMELAB_RELEASE_KEYRING (an armored public key
	// file) point a host at another release server; the image check uses
	// them. They come from the daemon's environment file, which is root's.
	releaseURLEnv     = "HOMELAB_RELEASE_URL"
	releaseKeyringEnv = "HOMELAB_RELEASE_KEYRING"

	releaseCheckEvery = 24 * time.Hour
	releaseTick       = 10 * time.Minute
	releaseFile       = "image-release.json"
	imageReleaseScope = "image-release:"
	smallFileMax      = 64 << 10
	latestTimeout     = 30 * time.Second
	fileTimeout       = 2 * time.Hour
)

// releaseClient fetches from the release server; a variable for tests.
var releaseClient = &http.Client{Transport: &http.Transport{
	Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute, TLSHandshakeTimeout: 30 * time.Second,
}}

// ReleaseCheck is what the host last heard from the release server.
type ReleaseCheck struct {
	Url       string     `json:"url" doc:"Where the host looks for releases."`
	Latest    string     `json:"latest" doc:"The newest release there; empty until a check succeeds."`
	CheckedAt *time.Time `json:"checked_at" doc:"The last check, successful or not; null: not yet."`
	Error     string     `json:"error,omitempty" doc:"Why the last check failed."`
}

// releaseState is the release check's file: the check, and the release the
// operator was told about.
type releaseState struct {
	ReleaseCheck
	Notified string `json:"notified,omitempty"`
}

type imageRelease struct {
	mu     sync.Mutex
	st     releaseState
	loaded bool
}

func releaseURL() string {
	if u := strings.TrimRight(os.Getenv(releaseURLEnv), "/"); u != "" {
		return u
	}
	return defaultReleaseURL
}

func releaseKeys() (openpgp.EntityList, error) {
	key := releaseKey
	if p := os.Getenv(releaseKeyringEnv); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		key = b
	}
	return openpgp.ReadArmoredKeyRing(bytes.NewReader(key))
}

func getSmall(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := releaseClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, smallFileMax+1))
	if err == nil && len(b) > smallFileMax {
		err = fmt.Errorf("%s is larger than %d bytes", url, smallFileMax)
	}
	return b, err
}

var releaseVersionRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// latestRelease reads the newest version from the server. The query keeps a
// cache from answering with an old one.
func latestRelease(ctx context.Context, base string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, latestTimeout)
	defer cancel()
	b, err := getSmall(ctx, base+"/latest?t="+strconv.FormatInt(time.Now().Unix(), 10))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if !releaseVersionRE.MatchString(v) {
		return "", fmt.Errorf("%s/latest names no version", base)
	}
	return v, nil
}

// localNewest is the newest image and core version the host has: running,
// installed (a version that failed its tries included) or staged.
func localNewest() (image, core string) {
	image = bootedVersion()
	for _, v := range parseUKIs(ukiNames(), image) {
		image = maxVersion(image, v.Version)
	}
	for _, v := range versionsIn(imageUpdates, stagedImageRE) {
		image = maxVersion(image, v)
	}
	core = activeCore()
	for _, v := range append(versionsIn(coreExtDir, coreFileRE), versionsIn(filepath.Join(imageUpdates, "core"), coreFileRE)...) {
		core = maxVersion(core, v)
	}
	return image, core
}

func maxVersion(a, b string) string {
	if compareVersions(b, a) > 0 {
		return b
	}
	return a
}

// wantsRelease: v is newer than the image or the core the host has.
func wantsRelease(v string) bool {
	image, core := localNewest()
	return compareVersions(v, image) > 0 || compareVersions(v, core) > 0
}

// releaseFiles reads the files an update needs from version v's SHA256SUMS:
// the UKI, the root and its verity, and the core extension, by their path
// in the release. Anything else (the installer) is left out.
func releaseFiles(sums []byte, v string) (map[string]string, error) {
	q := regexp.QuoteMeta(v)
	want := regexp.MustCompile(`^(homelab_` + q + `\.efi|homelab_` + q + `\.root(-verity)?\.[0-9a-fA-F-]+\.raw|core/homelab-core_` + q + `\.raw)$`)
	files := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		hash, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		name = strings.TrimPrefix(strings.TrimSpace(name), "*")
		if len(hash) == 64 && want.MatchString(name) {
			files[name] = strings.ToLower(hash)
		}
	}
	var root, verity bool
	for name := range files {
		root = root || strings.Contains(name, ".root.")
		verity = verity || strings.Contains(name, ".root-verity.")
	}
	_, efi := files["homelab_"+v+".efi"]
	_, core := files["core/homelab-core_"+v+".raw"]
	if !efi || !root || !verity || !core {
		return nil, fmt.Errorf("SHA256SUMS of %s lacks the UKI, the root, its verity or the core extension", v)
	}
	return files, nil
}

// fetchFile downloads url to path and checks it against its sum.
func fetchFile(ctx context.Context, url, path, sum string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, fileTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := releaseClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return n, fmt.Errorf("%s does not match its sum", filepath.Base(path))
	}
	return n, nil
}

// ── the work, in the transient unit ──────────────────────────────────────

// download fetches the newest release and stages it, if the host has no
// newer image or core.
func (x *deployRun) download(ctx context.Context) (string, string) {
	x.phase("Check")
	base := releaseURL()
	v, err := latestRelease(ctx, base)
	if err != nil {
		return DeployFailed, "the release server: " + err.Error()
	}
	x.d.Version = v
	if !wantsRelease(v) {
		x.phase("Up to date: the newest release is " + v)
		return DeployHealthy, ""
	}

	x.phase("Verify")
	sums, err := getSmall(ctx, base+"/"+v+"/SHA256SUMS")
	if err != nil {
		return DeployFailed, "the release server: " + err.Error()
	}
	sig, err := getSmall(ctx, base+"/"+v+"/SHA256SUMS.gpg")
	if err != nil {
		return DeployFailed, "the release server: " + err.Error()
	}
	keys, err := releaseKeys()
	if err != nil {
		return DeployFailed, "the release key: " + err.Error()
	}
	if _, err := openpgp.CheckDetachedSignature(keys, bytes.NewReader(sums), bytes.NewReader(sig), nil); err != nil {
		return DeployFailed, "SHA256SUMS of " + v + " is not signed by the release key: " + err.Error()
	}
	files, err := releaseFiles(sums, v)
	if err != nil {
		return DeployFailed, err.Error()
	}

	x.phase("Download")
	// In the update folder: on its file system, so staging is a rename.
	tmp := filepath.Join(imageUpdates, ".download")
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "core"), 0o755); err != nil {
		return DeployFailed, err.Error()
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		n, err := fetchFile(ctx, base+"/"+v+"/"+name, filepath.Join(tmp, filepath.FromSlash(name)), files[name])
		if err != nil {
			return DeployFailed, "downloading " + v + ": " + err.Error()
		}
		x.logf("%s: %d MB, sum checked", name, n>>20)
	}

	x.phase("Stage")
	if err := stageWith(tmp, v, moveFile); err != nil {
		return DeployFailed, "staging the update: " + err.Error()
	}
	x.phase("Downloaded " + v)
	return DeployHealthy, ""
}

// ── daemon side ─────────────────────────────────────────────────────────

func (m *Module) releasePath() string { return filepath.Join(filepath.Dir(m.file), releaseFile) }

// releaseState reads the state; the caller holds m.rel.mu.
func (m *Module) releaseStateLocked() releaseState {
	if !m.rel.loaded {
		m.rel.loaded = true
		if b, err := os.ReadFile(m.releasePath()); err == nil {
			if err := json.Unmarshal(b, &m.rel.st); err != nil {
				log.Warn("could not read the release check", "error", err)
			}
		}
	}
	return m.rel.st
}

func (m *Module) saveReleaseLocked(st releaseState) {
	m.rel.st = st
	b, err := json.Marshal(st)
	if err == nil {
		tmp := m.releasePath() + ".tmp"
		if err = os.WriteFile(tmp, b, 0o640); err == nil {
			err = os.Rename(tmp, m.releasePath())
		}
	}
	if err != nil {
		log.Warn("could not save the release check", "error", err)
	}
}

// Release is the last release check.
func (m *Module) Release() ReleaseCheck {
	m.rel.mu.Lock()
	defer m.rel.mu.Unlock()
	c := m.releaseStateLocked().ReleaseCheck
	c.Url = releaseURL()
	return c
}

// CheckRelease asks the release server for its newest release and records
// the answer. A failed check keeps the release seen before.
func (m *Module) CheckRelease(ctx context.Context) ReleaseCheck {
	base := releaseURL()
	v, err := latestRelease(ctx, base)
	now := m.now()
	m.rel.mu.Lock()
	defer m.rel.mu.Unlock()
	st := m.releaseStateLocked()
	st.Url, st.CheckedAt, st.Error = base, &now, ""
	if err != nil {
		st.Error = err.Error()
		log.Warn("release check failed", "error", err)
	} else {
		st.Latest = v
	}
	m.saveReleaseLocked(st)
	return st.ReleaseCheck
}

// DownloadRelease checks for a newer release and starts its download; the
// download records "up to date" when there is none.
func (m *Module) DownloadRelease(ctx context.Context) (CoreDeploy, error) {
	if !m.image {
		return CoreDeploy{}, ErrNotImageHost
	}
	m.CheckRelease(ctx)
	return m.start(ctx, KindDownload)
}

func (m *Module) imageReleaseLoop(ctx context.Context) {
	t := time.NewTimer(releaseDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.releaseTick(ctx)
			t.Reset(releaseTick)
		}
	}
}

// releaseTick checks the server once a day and starts a download when it has
// something newer; every tick, it tells the operator about a staged release.
func (m *Module) releaseTick(ctx context.Context) {
	if c := m.Release(); c.CheckedAt == nil || m.now().Sub(*c.CheckedAt) >= releaseCheckEvery {
		if c = m.CheckRelease(ctx); c.Error == "" && wantsRelease(c.Latest) {
			if _, err := m.start(ctx, KindDownload); err != nil && !errors.Is(err, ErrDeployRunning) {
				log.Warn("could not start the release download", "error", err)
			}
		}
	}
	m.notifyImageRelease(ctx)
}

// notifyImageRelease tells the operator once that a release is downloaded
// and ready to install, and closes that once the host runs it.
func (m *Module) notifyImageRelease(ctx context.Context) {
	if m.notify == nil {
		return
	}
	m.rel.mu.Lock()
	defer m.rel.mu.Unlock()
	st := m.releaseStateLocked()
	booted := bootedVersion()
	var current []notifications.Problem
	switch {
	case st.Notified != "" && compareVersions(booted, st.Notified) >= 0:
		st.Notified = ""
	case st.Latest != "" && st.Latest != st.Notified && compareVersions(st.Latest, booted) > 0 &&
		slices.Contains(versionsIn(imageUpdates, stagedImageRE), st.Latest):
		current = []notifications.Problem{{
			Key:    imageReleaseScope + st.Latest,
			Title:  "Version " + st.Latest + " is ready to install",
			Detail: "It is downloaded and verified. Install it from Settings when it suits you; it boots at the next reboot.",
			Path:   "/settings/core",
		}}
		st.Notified = st.Latest
	default:
		return
	}
	if _, err := m.notify.Reconcile(ctx, imageReleaseScope, current); err != nil {
		log.Warn("release notification not sent", "error", err)
		return
	}
	m.saveReleaseLocked(st)
}
