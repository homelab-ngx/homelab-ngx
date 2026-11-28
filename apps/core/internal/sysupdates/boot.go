package sysupdates

// What the image host boots, and what its boot check found (#23).
//
// The boot loader (systemd-boot) boots the newest version that has not
// failed its tries. An operator can boot another installed version once, or
// make one the default until the next image is installed; both are boot
// loader variables, set with bootctl. A version that failed its tries can
// be booted once the same way: when it comes up, it counts as good again.
//
// The boot check (image/base: usr/lib/homelab/boot-check) leaves two files
// the daemon reads: why this boot did not pass, and the core version it
// replaced with the one before.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/notifications"
)

// Variables for tests.
var (
	efiVars          = "/sys/firmware/efi/efivars"
	bootCheckFile    = "/run/homelab/boot-check-failed"
	coreFellBackFile = "/var/lib/homelab/core-fell-back"
)

const (
	loaderGUID     = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
	imageBootScope = "image-boot:"
	// autoInstallWindow is how long after the set time an install may still
	// start: the release tick runs every ten minutes.
	autoInstallWindow = time.Hour
)

// Errors of the boot operations.
var (
	ErrNoSuchVersion = errors.New("that image version is not installed")
	ErrBadBootMode   = errors.New(`mode: "once" or "default"`)
	ErrBadTime       = errors.New(`at: a time of day such as 03:30, or "" for never`)
)

// loaderVar reads one of systemd-boot's EFI variables (UTF-16 after four
// attribute bytes); "" when it is not set.
func loaderVar(name string) string {
	b, err := os.ReadFile(filepath.Join(efiVars, name+"-"+loaderGUID))
	if err != nil || len(b) < 6 {
		return ""
	}
	b = b[4:]
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if c := binary.LittleEndian.Uint16(b[i:]); c != 0 {
			u = append(u, c)
		}
	}
	return string(utf16.Decode(u))
}

// entryVersion is the image version a boot entry's id names; "" for
// another entry.
func entryVersion(id string) string {
	if m := stagedImageRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return ""
}

// BootImage sets the installed version the host boots: once at the next
// reboot (mode "once"), or by default until the next image is installed
// (mode "default"; version "" goes back to the newest). With reboot, the
// host reboots after the answer.
func (m *Module) BootImage(ctx context.Context, version, mode string, reboot bool) (ImageStatus, error) {
	if !m.image {
		return ImageStatus{}, ErrNotImageHost
	}
	verb := map[string]string{"once": "set-oneshot", "default": "set-default"}[mode]
	if verb == "" || (version == "" && mode != "default") {
		return ImageStatus{}, ErrBadBootMode
	}
	id := ""
	if version != "" {
		found := false
		for _, v := range parseUKIs(ukiNames(), bootedVersion()) {
			found = found || v.Version == version
		}
		if !found {
			return ImageStatus{}, ErrNoSuchVersion
		}
		id = "homelab_" + version + ".efi"
	}
	if _, err := m.r.Run(ctx, cmdrunner.Cmd{Module: "sysupdates", Name: "bootctl", Args: []string{verb, id}}); err != nil {
		return ImageStatus{}, err
	}
	log.Info("boot entry set", "mode", mode, "version", version, "reboot", reboot)
	st, err := m.ImageStatus(ctx)
	if err == nil && reboot {
		err = m.reboot(ctx)
	}
	return st, err
}

// bootCheckFailure is why this boot did not pass its check; "" when it did.
func bootCheckFailure() string {
	b, _ := os.ReadFile(bootCheckFile)
	return strings.TrimSpace(string(b))
}

// coreFallback is the boot check's note: core From did not start, so it
// merged To, the version before.
type coreFallback struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// coreFellBack reads the note. It holds while the host runs the version the
// check went back to; after a core update it is removed.
func coreFellBack() (coreFallback, bool) {
	var fb coreFallback
	b, err := os.ReadFile(coreFellBackFile)
	if err != nil {
		return fb, false
	}
	if json.Unmarshal(b, &fb) != nil || fb.From == "" || fb.To != activeCore() {
		_ = os.Remove(coreFellBackFile)
		return fb, false
	}
	return fb, true
}

// notifyBoot tells the operator what the boot check found, and closes that
// once it no longer holds.
func (m *Module) notifyBoot(ctx context.Context) {
	if m.notify == nil {
		return
	}
	current := []notifications.Problem{}
	if why := bootCheckFailure(); why != "" {
		current = append(current, notifications.Problem{
			Key:    imageBootScope + "check",
			Title:  "This boot did not pass its check",
			Detail: "The boot check found: " + why + ". The host stayed up, because this version ran before; restarting it would not help.",
			Path:   "/settings/updates",
			Event:  "core.update",
		})
	}
	if fb, ok := coreFellBack(); ok {
		current = append(current, notifications.Problem{
			Key:    imageBootScope + "core:" + fb.From,
			Title:  "Core " + fb.From + " did not start",
			Detail: "The daemon did not answer on it at boot, so the host runs core " + fb.To + ", the version before.",
			Path:   "/settings/updates",
			Event:  "core.update",
		})
	}
	if _, err := m.notify.Reconcile(ctx, imageBootScope, current); err != nil {
		log.Warn("boot check notification not sent", "error", err)
	}
}

// ── installing by itself ────────────────────────────────────────────────

func (m *Module) autoInstallAt() string {
	m.rel.mu.Lock()
	defer m.rel.mu.Unlock()
	return m.releaseStateLocked().AutoInstallAt
}

// SetAutoInstall sets the time of day a downloaded release installs by
// itself ("HH:MM"), or "" for only when asked.
func (m *Module) SetAutoInstall(ctx context.Context, at string) (ImageStatus, error) {
	if !m.image {
		return ImageStatus{}, ErrNotImageHost
	}
	if at != "" && !rebootAtRE.MatchString(at) {
		return ImageStatus{}, ErrBadTime
	}
	m.rel.mu.Lock()
	st := m.releaseStateLocked()
	st.AutoInstallAt, st.AutoTried = at, ""
	m.saveReleaseLocked(st)
	m.rel.mu.Unlock()
	return m.ImageStatus(ctx)
}

// inWindow: now is at or up to d after the time of day at ("HH:MM"), today
// or, past midnight, yesterday's.
func inWindow(now time.Time, at string, d time.Duration) bool {
	t, err := time.Parse("15:04", at)
	if err != nil {
		return false
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	for _, s := range []time.Time{start, start.AddDate(0, 0, -1)} {
		if !now.Before(s) && now.Before(s.Add(d)) {
			return true
		}
	}
	return false
}

// autoInstall installs a staged release at the set time, each version once:
// one that failed is left for the operator.
func (m *Module) autoInstall(ctx context.Context) {
	m.rel.mu.Lock()
	st := m.releaseStateLocked()
	m.rel.mu.Unlock()
	if st.AutoInstallAt == "" || !inWindow(m.now(), st.AutoInstallAt, autoInstallWindow) {
		return
	}
	img, err := m.ImageStatus(ctx)
	if err != nil {
		return
	}
	staged, reboot := "", ""
	if len(img.Core.Available) > 0 {
		staged = img.Core.Available[0]
	}
	if len(img.Available) > 0 {
		staged, reboot = img.Available[0], "now"
	}
	if staged == "" || staged == st.AutoTried {
		return
	}
	if _, err := m.start(ctx, KindInstall, "--reboot", reboot); err != nil {
		if !errors.Is(err, ErrDeployRunning) {
			log.Warn("could not start the install", "version", staged, "error", err)
		}
		return
	}
	log.Info("installing the staged release at its set time", "version", staged)
	m.rel.mu.Lock()
	st = m.releaseStateLocked()
	st.AutoTried = staged
	m.saveReleaseLocked(st)
	m.rel.mu.Unlock()
}
