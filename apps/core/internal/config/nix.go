// Package config is the daemon's view of its configuration: a read-only
// nix-rendered file (/etc/homelab-daemon/config.json) overlaid with the
// operational overrides stored in the runtime DB, published as immutable
// atomic snapshots.
//
// config deliberately does not import internal/store; the OverrideStore
// interface (manager.go) is satisfied by a thin adapter at the composition
// root.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/robfig/cron/v3"
)

// SupportedVersion is the config.json schema version this build understands.
const SupportedVersion = 2

// Nix is a one-to-one mirror of what apps/core/module.nix emits. Unknown
// fields are rejected at load so nix/Go drift fails loudly.
type Nix struct {
	Version          int              `json:"version"`
	Paths            Paths            `json:"paths"`
	DB               DB               `json:"db"`
	CoreServices     []string         `json:"core_services"`
	CloudflareTunnel CloudflareTunnel `json:"cloudflare_tunnel"`
	Stacks           Stacks           `json:"stacks"`
	Doctor           Doctor           `json:"doctor"`
	// Security is the host's exposure and access, for the security audit;
	// absent from an older config.json.
	Security Security `json:"security"`
}

// Stacks is what this host gives a stack of manifest version 8: the domain
// its route labels are served under, the dashboard's address, the LAN a port
// grant from "lan" admits, and the graphics drivers of the GPU grant.
type Stacks struct {
	Domain       string    `json:"domain"`
	DashboardURL string    `json:"dashboard_url"`
	LAN          []string  `json:"lan"`
	GPU          StacksGPU `json:"gpu"`
}

// StacksGPU is the GPU grant's drivers: nixpkgs attributes and the VA-API
// driver name.
type StacksGPU struct {
	Packages    []string `json:"packages"`
	LibvaDriver string   `json:"libva_driver"`
}

type Paths struct {
	StateDir    string `json:"state_dir"`
	DB          string `json:"db"`
	Socket      string `json:"socket"`
	DBBackupDir string `json:"db_backup_dir"`
	// BackupRunner is the homelab-backup-runner binary a backup run starts.
	BackupRunner string `json:"backup_runner"`
	// CacheDir holds what the host keeps outside the daemon's state: stack
	// state and DB snapshots. Empty is DefaultCacheDir.
	CacheDir string `json:"cache_dir"`
	// PoolDir is where the storage pool mounts; empty means the host has no
	// pool.
	PoolDir string `json:"pool_dir"`
}

// DefaultCacheDir is the cache directory of a config that names none.
const DefaultCacheDir = "/var/lib/homelab"

// Cache is CacheDir, or DefaultCacheDir when it is empty.
func (p Paths) Cache() string {
	if p.CacheDir == "" {
		return DefaultCacheDir
	}
	return p.CacheDir
}

// Doctor is what the health checks look at beyond the daemon's own config.
type Doctor struct {
	// ScrutinyURL is Scrutiny's summary API; empty means no disk health
	// monitor, and the SMART check says so.
	ScrutinyURL string `json:"scrutiny_url"`
}

type DB struct {
	SnapshotSchedule string `json:"snapshot_schedule"` // 5-field cron
	SnapshotKeep     int    `json:"snapshot_keep"`
	RetentionDays    int    `json:"retention_days"`
}

// CloudflareTunnel is what the health audit checks of the tunnel: its local
// metrics and the published hostnames' protection.
type CloudflareTunnel struct {
	Enabled         bool              `json:"enabled"`
	MetricsURL      string            `json:"metrics_url"`
	Hostnames       []string          `json:"hostnames"`
	PublicHostnames map[string]string `json:"public_hostnames"`
	// Unit is cloudflared's systemd unit, named in the check's fix.
	Unit string `json:"unit"`
	// Connectors is how many connections a healthy tunnel holds; 0 is 4,
	// cloudflared's default.
	Connectors int `json:"connectors"`
}

// LoadNix reads and validates the nix-rendered config file.
func LoadNix(path string) (*Nix, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open nix config: %w", err)
	}
	defer f.Close()
	n, err := decodeNix(f)
	if err != nil {
		return nil, fmt.Errorf("nix config %s: %w", path, err)
	}
	return n, nil
}

func decodeNix(r io.Reader) (*Nix, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var n Nix
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("decode: trailing data after JSON document")
	}
	if err := n.validate(); err != nil {
		return nil, err
	}
	return &n, nil
}

func (n *Nix) validate() error {
	if n.Version != SupportedVersion {
		return fmt.Errorf("unsupported version %d (want %d)", n.Version, SupportedVersion)
	}
	if _, err := cron.ParseStandard(n.DB.SnapshotSchedule); err != nil {
		return fmt.Errorf("db.snapshot_schedule %q: %w", n.DB.SnapshotSchedule, err)
	}
	for field, dir := range map[string]string{"paths.cache_dir": n.Paths.CacheDir, "paths.pool_dir": n.Paths.PoolDir} {
		if dir != "" && (!filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/") {
			return fmt.Errorf("%s: %q is not a clean absolute directory", field, dir)
		}
	}
	return n.Stacks.validate()
}

func (s Stacks) validate() error {
	for _, c := range s.LAN {
		p, err := netip.ParsePrefix(c)
		if err != nil || !p.Addr().Is4() || p.Bits() == 0 {
			return fmt.Errorf("stacks.lan: %q is not an IPv4 subnet such as 192.168.1.0/24", c)
		}
	}
	if s.Domain != "" && !strings.Contains(s.Domain, ".") {
		return fmt.Errorf("stacks.domain: %q is not a domain", s.Domain)
	}
	return nil
}

// ValidateCron reports whether spec is a valid 5-field cron expression
// (the format robfig/cron's standard parser accepts, including descriptors
// such as @daily).
func ValidateCron(spec string) error {
	if _, err := cron.ParseStandard(spec); err != nil {
		return fmt.Errorf("invalid cron %q: %w", spec, err)
	}
	return nil
}
