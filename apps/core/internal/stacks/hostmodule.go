package stacks

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
)

// statOwner is the uid and gid that own a host path.
func statOwner(path string) (uid, gid uint32, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("%s: no owner", path)
	}
	return sys.Uid, sys.Gid, nil
}

// BuildHostModule is what a version 8 stack is given beyond the base module.
// paths are the host directories its path variables resolve to (by name; an
// unset optional one is absent), stat reads who owns one, and gpu is the
// host's graphics stack. ids is the module's users and groups as one string,
// which a generation records: when the ids change, the stack has to be built
// again. Below version 8 there is no module.
func BuildHostModule(m Manifest, paths map[string]string, stat func(string) (uint32, uint32, error), gpu GPU) (h *HostModule, ids string, err error) {
	if m.Version < 8 {
		return nil, "", nil
	}
	h = &HostModule{Users: map[string]uint32{}, Groups: map[string]uint32{}}
	take := func(kind, name, from string, id uint32, into map[string]uint32) error {
		if id == 0 {
			return fmt.Errorf("%s is owned by root; give it a %s of its own (chown) for the stack's %s %s", from, kind, kind, name)
		}
		if old, ok := into[name]; ok && old != id {
			return fmt.Errorf("the stack's %s %s would take two ids, %d and %d, from its folders", kind, name, old, id)
		}
		into[name] = id
		return nil
	}
	for _, v := range m.pathVars() {
		if v.Owner == "" && v.Group == "" {
			continue
		}
		p, ok := paths[v.Name]
		if !ok {
			continue
		}
		uid, gid, err := stat(p)
		if err != nil {
			return nil, "", fmt.Errorf("%s: reading the owner of %s: %w", v.Name, p, err)
		}
		if v.Owner != "" {
			if err := take("user", v.Owner, p, uid, h.Users); err != nil {
				return nil, "", err
			}
		}
		if v.Group != "" {
			if err := take("group", v.Group, p, gid, h.Groups); err != nil {
				return nil, "", err
			}
		}
	}
	tcp, udp := map[int]bool{}, map[int]bool{}
	for _, r := range m.Routes {
		tcp[r.Port] = true
	}
	if m.Health.HTTP != nil {
		tcp[m.Health.HTTP.Port] = true
	}
	for _, p := range m.Ports {
		if p.Proto == "udp" {
			udp[p.To] = true
		} else {
			tcp[p.To] = true
		}
	}
	h.TCP, h.UDP = sortedInts(tcp), sortedInts(udp)
	if m.GPU && len(gpu.Packages) > 0 {
		g := gpu
		h.GPU = &g
	}
	var parts []string
	for _, n := range sortedKeys(h.Users) {
		parts = append(parts, fmt.Sprintf("user %s=%d", n, h.Users[n]))
	}
	for _, n := range sortedKeys(h.Groups) {
		parts = append(parts, fmt.Sprintf("group %s=%d", n, h.Groups[n]))
	}
	return h, strings.Join(parts, ", "), nil
}

func sortedInts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// pathValues are the host directories m's path variables resolve to with
// the stored values vars, by name.
func pathValues(m Manifest, vars []storedVar) map[string]string {
	out := map[string]string{}
	for _, d := range m.pathVars() {
		if value, src := resolve(d, vars); src != SourceUnset {
			out[d.Name] = value
		}
	}
	return out
}

// hostModule is BuildHostModule for an installed stack, from its stored
// values and the host's settings.
func (s *Service) hostModule(name string, m Manifest) (*HostModule, string, error) {
	if m.Version < 8 {
		return nil, "", nil
	}
	s.varsMu.Lock()
	vars, err := s.loadVars(name)
	s.varsMu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return BuildHostModule(m, pathValues(m, vars), s.statOwner, s.host.GPU)
}
