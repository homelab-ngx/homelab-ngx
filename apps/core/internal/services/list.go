package services

import (
	"context"
	"sort"
	"strings"
	"time"
)

// unitName derives the display name of a unit: the ".service"/".socket"
// suffix removed.
func unitName(u string) string {
	name := strings.TrimSuffix(u, ".service")
	return strings.TrimSuffix(name, ".socket")
}

// List returns the service units inside stacks, listed as "<stack>-<unit>"
// so the name says where the unit runs, sorted by type then name. A unit
// whose name a core unit already has is left out. The daemon does not
// restart them: the stack's own systemd and the stack monitor look after
// them.
func (s *Service) List(ctx context.Context) []ServiceInfo {
	out := []ServiceInfo{}
	if s.stacks == nil {
		return out
	}
	for _, u := range s.stacks.Units(ctx) {
		if _, known := s.resolveCore(u.Name); known {
			continue
		}
		var startedAt *time.Time
		if !u.StartedAt.IsZero() {
			t := u.StartedAt.UTC()
			startedAt = &t
		}
		out = append(out, ServiceInfo{
			Name: unitName(u.Name), Type: Native, UnitName: u.Name, Stack: u.Stack,
			ActiveState: or(u.ActiveState, "inactive"), SubState: or(u.SubState, "dead"),
			Description: "In the " + u.Stack + " stack",
			StartedAt:   startedAt,
			Usage:       u.Usage,
			DiskBytes:   u.DiskBytes,
			Masked:      u.Masked,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Core returns the vital, systemd-owned infrastructure units on the host (the
// nix-owned core_services list), sorted by name.
func (s *Service) Core(ctx context.Context) []ServiceInfo {
	snap := s.cfg.Current()
	core := snap.Nix.CoreServices
	props := s.sd.Props(ctx, core, "Id", "ActiveState", "SubState", "Description")
	out := make([]ServiceInfo, 0, len(core))
	for _, u := range core {
		p := props[u]
		out = append(out, ServiceInfo{
			Name:        unitName(u),
			Type:        Native,
			ActiveState: or(p["ActiveState"], "inactive"),
			SubState:    or(p["SubState"], "dead"),
			Description: p["Description"],
			UnitName:    u,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolveCore resolves a name against the core units.
func (s *Service) resolveCore(name string) (string, bool) {
	snap := s.cfg.Current()
	for _, c := range []string{name, name + ".service"} {
		for _, core := range snap.Nix.CoreServices {
			if core == c {
				return core, true
			}
		}
	}
	return "", false
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
