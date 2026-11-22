// Package system serves the daemon's own endpoints: build version, the
// effective configuration with provenance, and the Logs page (its unit list
// and journal lines).
package system

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/httpx"
)

// Build identity. Set with
//
//	-ldflags "-X homelab-ngx.com/core/internal/system.version=1.2.3 \
//	          -X homelab-ngx.com/core/internal/system.commit=abc1234"
//
// Unset, version is "dev" and commit comes from commitFile, which the NixOS
// module writes per deploy (stamping it into the nix build would rebuild the
// daemon on every commit), then from the VCS revision the Go toolchain embeds.
//
// On the image host the version is not built in: the core extension is the
// same file whatever release it ships in, and its file name carries the
// release's version. coreLink points at the extension in use (sysupdates
// keeps it), so that name is the version this daemon runs as.
var (
	version    = "dev"
	commit     = ""
	commitFile = "/etc/homelab-daemon/commit"
	coreLink   = "/var/lib/extensions/homelab-core.raw"
)

var coreFileRE = regexp.MustCompile(`^homelab-core_(.+)\.raw$`)

// ConfigSource is the part of config.Manager the system endpoints use.
type ConfigSource interface {
	Current() *config.Snapshot
}

// System serves the system operations.
type System struct {
	cfg    ConfigSource
	r      cmdrunner.Runner
	stacks StackUnits
}

// New builds the system endpoints. r runs journalctl for the Logs page;
// stacks (nil: none) adds the stacks' units to it.
func New(cfg ConfigSource, r cmdrunner.Runner, stacks StackUnits) *System {
	return &System{cfg: cfg, r: r, stacks: stacks}
}

// HTTP is the adapter type every feature package exports; System is its own.
type HTTP = System

// NewHTTP is New under the name every feature package uses.
func NewHTTP(cfg ConfigSource, r cmdrunner.Runner, stacks StackUnits) *HTTP {
	return New(cfg, r, stacks)
}

// BuildInfo returns the version and commit of the core in use now: on an
// image host, the extension coreLink points at.
func BuildInfo() (string, string) {
	return buildVersion(), buildCommit()
}

// running is BuildInfo as it was at the first call. The daemon makes that
// call as it starts, so a core switched under a running daemon is not
// reported as the daemon's until the daemon restarts into it.
var running = sync.OnceValues(BuildInfo)

// Running returns the version and commit this process started as.
func Running() (string, string) {
	return running()
}

func buildVersion() string {
	if t, err := os.Readlink(coreLink); err == nil {
		if m := coreFileRE.FindStringSubmatch(filepath.Base(t)); m != nil {
			return m[1]
		}
	}
	return version
}

func buildCommit() string {
	if commit != "" {
		return commit
	}
	if b, err := os.ReadFile(commitFile); err == nil {
		if c := strings.TrimSpace(string(b)); c != "" {
			return c
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			if kv.Key == "vcs.revision" {
				return kv.Value
			}
		}
	}
	return ""
}

func (s *System) snapshot() (*config.Snapshot, error) {
	snap := s.cfg.Current()
	if snap == nil {
		return nil, httpx.Unavailable("config_not_loaded", "configuration has not been loaded yet")
	}
	return snap, nil
}

// Config returns the effective configuration with provenance.
func (s *System) Config() (config.ConfigView, error) {
	snap, err := s.snapshot()
	if err != nil {
		return config.ConfigView{}, err
	}
	return config.View(snap), nil
}

// LogViewerConfig returns the Logs page's unit list.
func (s *System) LogViewerConfig(ctx context.Context) (LogViewerConfig, error) {
	snap, err := s.snapshot()
	if err != nil {
		return LogViewerConfig{}, err
	}
	units := s.logUnits(ctx, snap)
	out := make([]LogViewerUnit, len(units))
	for i, u := range units {
		out[i] = LogViewerUnit{Id: u.ID, Label: u.Label, Group: u.Group}
	}
	return LogViewerConfig{Units: out}, nil
}
