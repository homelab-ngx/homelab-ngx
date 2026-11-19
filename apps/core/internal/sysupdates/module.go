// Package sysupdates updates the host: the image and the homelab-core
// system extension, from a release or a build of main. It also reboots the
// host and reads package changes out of `nix store diff-closures` for the
// stacks.
package sysupdates

import (
	"context"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	logging "homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/notifications"
)

var log = logging.Logger("sysupdates")

// releaseDelay is how long after start the first release check waits: the
// boot settles first.
const releaseDelay = 2 * time.Minute

// rebootDelay lets the 202 go out before the reboot; a var for tests.
var rebootDelay = 3 * time.Second

type Module struct {
	dir string           // the daemon's state: the update records and the release check
	r   cmdrunner.Runner // systemd-run, systemctl

	notify Notifier // tells the operator about a release or a failed boot check; may be nil

	dep DeployConfig
	// image: the host runs the homelab image. A daemon run anywhere else
	// (a development machine) refuses the update operations.
	image bool
	ds    deployState
	rel   imageRelease // the release check (fetch.go)

	now func() time.Time // seam for tests
}

// Notifier keeps the release and boot check notifications in line with
// what the host found (*notifications.Notifier).
type Notifier interface {
	Reconcile(ctx context.Context, scope string, current []notifications.Problem) (notifications.Result, error)
}

// SetNotifier makes the release check notify the operator.
func (m *Module) SetNotifier(n Notifier) { m.notify = n }

// New keeps its records under stateDir. r starts the update unit and the
// reboot.
func New(r cmdrunner.Runner, stateDir string) *Module {
	return &Module{dir: stateDir, r: r, now: time.Now}
}

// Start looks for releases and tells the operator what the boot check
// found, until ctx ends.
func (m *Module) Start(ctx context.Context) {
	if m.image {
		m.imageReleaseLoop(ctx)
	}
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
