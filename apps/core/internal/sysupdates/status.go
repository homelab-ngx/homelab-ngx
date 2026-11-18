package sysupdates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/notifications"
)

// bootedSystem is the system the host booted; a different kernel, initrd or
// module set in currentSystem means a reboot is needed to run it.
var bootedSystem = "/run/booted-system"

const (
	releaseInterval = 24 * time.Hour
	releaseDelay    = 2 * time.Minute // after start: let the boot settle first
)

// rebootDelay lets the 202 go out before the reboot; a var for tests.
var rebootDelay = 3 * time.Second

// ── release check ───────────────────────────────────────────────────────

func (m *Module) releaseLoop(ctx context.Context) {
	t := time.NewTimer(releaseDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.CheckReleases(ctx)
			t.Reset(releaseInterval)
		}
	}
}

// CheckReleases lists the NixOS release branches. A failed lookup keeps the
// previous result.
func (m *Module) CheckReleases(ctx context.Context) {
	releases, err := nixosReleases(ctx, m.r)
	if err != nil {
		log.Warn("release check failed", "error", err)
	}
	now := m.now()
	m.relMu.Lock()
	if err == nil {
		m.releases = releases
	}
	m.checked = &now
	m.relMu.Unlock()
	// After the unlock: the notification reads the releases back.
	if err == nil {
		m.notifyRelease(ctx)
	}
}

// releaseScope keys the one notification a released NixOS upgrade raises.
const releaseScope = "nixos-release:"

// notifyRelease raises a notification while a released (not pre-release)
// NixOS upgrade waits, and closes it once the host runs that release. It is
// the only way core reaches the operator outside Settings, so it says where
// to go.
func (m *Module) notifyRelease(ctx context.Context) {
	if m.notify == nil {
		return
	}
	current := []notifications.Problem{}
	if up := m.upgrade(m.release()); up != nil && !up.PreRelease {
		current = append(current, notifications.Problem{
			Key:    releaseScope + up.Release,
			Title:  "NixOS " + up.Release + " is available",
			Detail: "Upgrade core from Settings when it suits you; stacks keep their own nixpkgs. Release notes: " + up.NotesUrl,
			Path:   "/settings/core",
		})
	}
	if _, err := m.notify.Reconcile(ctx, releaseScope, current); err != nil {
		log.Warn("release notification not updated", "error", err)
	}
}

// release is the base nixpkgs' release, read from the repo's flake.lock:
// the branch it follows, else (pinned to a pre-release revision) the release
// of the running NixOS version. Without a readable lock it is unknown.
func (m *Module) release() NixOSRelease {
	var rel NixOSRelease
	follows, ok := baseInput(m.repo)
	if !ok {
		return rel
	}
	if follows != "" {
		rel.Follows = &follows
		rel.Current = releaseOf(follows)
		return rel
	}
	rel.Pinned = true
	if v, err := os.ReadFile(filepath.Join(currentSystem, "nixos-version")); err == nil {
		if r := strings.TrimSpace(string(v)); len(r) >= 5 && validRelease(r[:5]) {
			rel.Current = r[:5]
		}
	}
	return rel
}

// upgrade is the release the base can move to, if any.
func (m *Module) upgrade(rel NixOSRelease) *ReleaseUpgrade {
	m.relMu.Lock()
	releases := m.releases
	m.relMu.Unlock()
	if rel.Current == "" {
		return nil
	}
	next, ok := nextRelease(rel.Current, rel.Pinned, releases)
	if !ok {
		return nil
	}
	return &ReleaseUpgrade{
		Release:    next,
		PreRelease: !released(next, m.now()),
		NotesUrl:   "https://nixos.org/manual/nixos/stable/release-notes#sec-release-" + next,
	}
}

// rebootRequired: the running kernel, initrd or modules are not the
// installed system's.
func rebootRequired() bool {
	for _, f := range []string{"kernel", "initrd", "kernel-modules"} {
		booted, err1 := filepath.EvalSymlinks(filepath.Join(bootedSystem, f))
		current, err2 := filepath.EvalSymlinks(filepath.Join(currentSystem, f))
		if err1 == nil && err2 == nil && booted != current {
			return true
		}
	}
	return false
}

// ── status ──────────────────────────────────────────────────────────────

// Status is the System card: release, newer release, reboot needed and the
// running generation.
func (m *Module) Status(ctx context.Context) SystemUpdateStatus {
	m.relMu.Lock()
	checked := m.checked
	m.relMu.Unlock()
	rel := m.release()
	st := SystemUpdateStatus{
		Release:        rel,
		Upgrade:        m.upgrade(rel),
		RebootRequired: rebootRequired(),
		CheckedAt:      checked,
		Core:           m.CoreStatus(ctx, false),
	}
	current, _ := filepath.EvalSymlinks(currentSystem)
	m.mu.Lock()
	for _, g := range m.gens {
		if g.System == current && g.Number > st.Generation {
			st.Generation = g.Number
		}
	}
	m.mu.Unlock()
	return st
}

// Reboot reboots the host a few seconds from now.
func (m *Module) Reboot() {
	log.Warn("reboot requested")
	go func() {
		time.Sleep(rebootDelay)
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := m.r.Run(bg, cmdrunner.Cmd{Module: "sysupdates", Name: "systemctl", Args: []string{"reboot"}}); err != nil {
			log.Error("reboot failed", "error", err)
		}
	}()
}
