package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// A disabled service is a unit of the manifest's health check that the
// operator masked: it stops at once, stays stopped across restarts and
// updates, and cannot be started until it is enabled again.
//
// /etc in a stack is its NixOS system's, rebuilt on every switch, and a
// runtime mask (/run/systemd/system) ranks below it, so neither can mask a
// NixOS unit. /run/systemd/system.control ranks above /etc: a "<unit> ->
// /dev/null" link there masks it. The daemon keeps those links in a
// directory on the host's tmpfs (Host.UnitsDir/<name>) and binds it
// read-only at /run/systemd/system.control when it boots a stack that has a
// disabled service. A stack booted without that bind gets the link written
// into its own /run instead; either way a daemon-reload makes it count.

// stackMaskDir is where a stack's systemd reads the masks.
const stackMaskDir = "/run/systemd/system.control"

// unitsDir is the host's directory of the stack's mask links.
func (s *Service) unitsDir(name string) string { return filepath.Join(s.host.UnitsDir, name) }

// storedMasks are the units r's operator disabled, as stored.
func storedMasks(r store.Stack) []string {
	var out []string
	if r.MaskedUnits == "" || json.Unmarshal([]byte(r.MaskedUnits), &out) != nil {
		return nil
	}
	return out
}

// liveMasks are the stored masks of r that manifest m's health check still
// names, in the manifest's order. A mask of a unit m no longer has is
// ignored.
func liveMasks(r store.Stack, m Manifest) []string {
	return inOrder(m.Health.Units, storedMasks(r))
}

// inOrder is the units that are in set, in the order of units.
func inOrder(units, set []string) []string {
	var out []string
	for _, u := range units {
		if slices.Contains(set, u) {
			out = append(out, u)
		}
	}
	return out
}

// enabled is units without the disabled ones.
func enabled(units, masks []string) []string {
	out := []string{}
	for _, u := range units {
		if !slices.Contains(masks, u) {
			out = append(out, u)
		}
	}
	return out
}

// stackMasks are the disabled services of the stack name under manifest m;
// none when the stack cannot be read.
func (s *Service) stackMasks(ctx context.Context, name string, m Manifest) []string {
	r, err := s.st.Queries().GetStack(ctx, name)
	if err != nil {
		return nil
	}
	return liveMasks(r, m)
}

// writeMasks makes the stack's mask directory hold a link for each of
// masks and nothing else. The directory is changed in place, never
// replaced: a running stack may have it bound.
func (s *Service) writeMasks(name string, masks []string) error {
	if s.host.UnitsDir == "" {
		return nil
	}
	dir := s.unitsDir(name)
	// Readable by all: inside the stack the directory belongs to nobody,
	// and the stack's systemd must read it.
	if err := mkdirs(0o755, s.host.UnitsDir, dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !slices.Contains(masks, e.Name()) {
			errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
		}
	}
	for _, u := range masks {
		p := filepath.Join(dir, u)
		if t, err := os.Readlink(p); err == nil && t == os.DevNull {
			continue
		}
		_ = os.RemoveAll(p)
		errs = append(errs, os.Symlink(os.DevNull, p))
	}
	return errors.Join(errs...)
}

// removeMasks deletes a removed stack's mask directory.
func (s *Service) removeMasks(name string) error {
	if s.host.UnitsDir == "" {
		return nil
	}
	return os.RemoveAll(s.unitsDir(name))
}

// maskBound reports whether the boot arguments bind the mask directory.
func maskBound(args []string) bool {
	return slices.ContainsFunc(args, isMaskBind)
}

func isMaskBind(arg string) bool {
	return strings.HasPrefix(arg, "--bind-ro=") && strings.HasSuffix(arg, ":"+stackMaskDir)
}

// MaskUnit disables one of a stack's services (mask) or enables it again.
// name is the host's name for the unit (Unit.Name). Disabling stops the unit
// now; enabling starts nothing. The last enabled service cannot be
// disabled: that is stopping the stack.
func (s *Service) MaskUnit(ctx context.Context, name string, mask bool) error {
	stack, unit, ok := s.UnitStack(ctx, name)
	if !ok {
		return httpx.NotFound("unknown_unit", "no stack has a service unit named %q", name)
	}
	if s.host.UnitsDir == "" {
		return errors.New("no directory for disabled services is configured")
	}
	release, err := s.hold(ctx, stack)
	if err != nil {
		return err
	}
	defer release()
	r, err := s.row(ctx, stack)
	if err != nil {
		return err
	}
	g, err := s.generation(ctx, r)
	if err != nil {
		return err
	}
	if g == nil {
		return httpx.NotFound("unknown_unit", "no stack has a service unit named %q", name)
	}
	m, err := decode(g.Manifest)
	if err != nil {
		return err
	}
	masks := liveMasks(r, m)
	if slices.Contains(masks, unit) == mask {
		return nil
	}
	if mask {
		if len(enabled(m.Health.Units, masks)) <= 1 {
			return httpx.Conflict("last_unit", "%s is the last enabled service of stack %q; stop the stack instead", name, stack)
		}
		masks = inOrder(m.Health.Units, append(masks, unit))
	} else {
		masks = slices.DeleteFunc(masks, func(u string) bool { return u == unit })
	}
	running := s.isRunning(ctx, stack)
	if running && s.frozen(ctx, stack) {
		return stackFrozen(stack)
	}
	if err := s.st.Queries().SetStackMaskedUnits(ctx, store.SetStackMaskedUnitsParams{Name: stack, MaskedUnits: encodeMasks(masks)}); err != nil {
		return err
	}
	s.writeLock(ctx)
	if err := s.writeMasks(stack, masks); err != nil {
		return fmt.Errorf("writing the stack's disabled services: %w", err)
	}
	if running {
		if err := s.maskLive(ctx, r, unit, mask); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.unitsAt = time.Time{}
	s.mu.Unlock()
	verb := "enabled"
	if mask {
		verb = "disabled"
	}
	s.log.Info("stack unit "+verb, "stack", stack, "unit", unit)
	return nil
}

func encodeMasks(masks []string) string {
	if masks == nil {
		masks = []string{}
	}
	b, _ := json.Marshal(masks)
	return string(b)
}

// maskLive makes a mask count in the running stack at once. A stack booted
// with the mask directory bound sees the link the host just wrote; one
// booted without it gets the link written into its own /run, which its next
// boot replaces with the bind.
func (s *Service) maskLive(ctx context.Context, r store.Stack, unit string, mask bool) error {
	name := r.Name
	if !maskBound(bootedArgs(r)) {
		bin := stackProfile + "/system/sw/bin/"
		steps := [][]string{{bin + "rm", "-f", "--", stackMaskDir + "/" + unit}}
		if mask {
			steps = [][]string{
				{bin + "mkdir", "-p", "--", stackMaskDir},
				{bin + "ln", "-sfn", "--", os.DevNull, stackMaskDir + "/" + unit},
			}
		}
		for _, cmd := range steps {
			args := append([]string{"--machine=" + name, "--wait", "--collect", "--quiet", "--"}, cmd...)
			if _, err := s.run(ctx, time.Minute, "systemd-run", args...); err != nil {
				return fmt.Errorf("masking %s in stack %s: %w", unit, name, err)
			}
		}
	}
	if _, err := s.run(ctx, time.Minute, "systemctl", "--machine="+name, "daemon-reload"); err != nil {
		return fmt.Errorf("reloading systemd in stack %s: %w", name, err)
	}
	if !mask {
		return nil
	}
	if _, err := s.run(ctx, 5*time.Minute, "systemctl", "--machine="+name, "stop", "--", unit); err != nil {
		return fmt.Errorf("stopping %s in stack %s: %w", unit, name, err)
	}
	return nil
}

func maskedProblem(name string) *httpx.Problem {
	return httpx.Conflict("unit_masked", "%s is disabled; enable it first", name)
}

// unitMasked reports whether unit of the stack is disabled now.
func (s *Service) unitMasked(ctx context.Context, stack, unit string) bool {
	r, err := s.row(ctx, stack)
	if err != nil {
		return false
	}
	return slices.Contains(storedMasks(r), unit)
}
