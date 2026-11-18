package sysupdates

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tiers: why a package in a closure diff is worth showing. Anything else
// is a dependency and is only counted.
const (
	tierKernel    = "kernel"
	tierService   = "service"
	tierInstalled = "installed"
)

// scriptHelpers are the tools generated unit scripts call around the real
// program; they say nothing about what the service is.
var scriptHelpers = map[string]bool{
	"bash": true, "bash-interactive": true, "coreutils": true, "coreutils-full": true,
	"gnused": true, "gnugrep": true, "gawk": true, "findutils": true, "util-linux": true,
}

var (
	storePathRe = regexp.MustCompile(`/nix/store/[a-z0-9]{32}-([^/\s"']+)`)
	storeBinRe  = regexp.MustCompile(`/nix/store/[a-z0-9]{32}-([^/\s"']+)/s?bin/`)
)

// drvName is the package name of a store path's name, as nix
// parseDrvName splits it: everything before the first "-<digit>".
func drvName(name string) string {
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '-' && name[i+1] >= '0' && name[i+1] <= '9' {
			return name[:i]
		}
	}
	return name
}

// systemTiers maps the explicit package names of one system to their tier:
// its kernel, what its systemd services run, and what it installs on the
// PATH.
func systemTiers(system string) map[string]string {
	tiers := map[string]string{}
	for _, n := range installedNames(system) {
		tiers[n] = tierInstalled
	}
	for _, n := range serviceNames(system) {
		tiers[n] = tierService
	}
	if target, err := os.Readlink(filepath.Join(system, "kernel")); err == nil {
		if sm := storePathRe.FindStringSubmatch(target); sm != nil {
			tiers[drvName(sm[1])] = tierKernel
		}
	}
	return tiers
}

// installedNames lists the packages a system puts on the PATH: the store
// path each sw/bin entry links to. Script helpers are left out, since every
// system installs them.
func installedNames(system string) []string {
	bins, _ := os.ReadDir(filepath.Join(system, "sw/bin"))
	seen := map[string]bool{}
	for _, b := range bins {
		target, err := os.Readlink(filepath.Join(system, "sw/bin", b.Name()))
		if err != nil {
			continue
		}
		if sm := storePathRe.FindStringSubmatch(target); sm != nil {
			if n := drvName(sm[1]); !scriptHelpers[n] {
				seen[n] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// serviceNames lists the packages a system's services run: the binary of
// each ExecStart=, or what a generated unit script calls.
func serviceNames(system string) []string {
	units, _ := filepath.Glob(filepath.Join(system, "etc/systemd/system/*.service"))
	seen := map[string]bool{}
	for _, u := range units {
		data, err := os.ReadFile(u)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			v, ok := strings.CutPrefix(line, "ExecStart=")
			if !ok {
				continue
			}
			bin := strings.Fields(strings.TrimLeft(v, "-@+!:") + " ")[0]
			if !strings.Contains(bin, "-unit-script-") {
				if sm := storePathRe.FindStringSubmatch(bin); sm != nil {
					seen[drvName(sm[1])] = true
				}
				continue
			}
			script, err := os.ReadFile(bin)
			if err != nil {
				continue
			}
			// Skip the shebang: every script names its shell there.
			_, body, _ := strings.Cut(string(script), "\n")
			for _, sm := range storeBinRe.FindAllStringSubmatch(body, -1) {
				seen[drvName(sm[1])] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		if !scriptHelpers[n] && !strings.Contains(n, "unit-script") && !strings.HasPrefix(n, "security-wrapper-") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// mergeTiers overlays b on a (b wins). Both sides of a diff count, so a
// removed package keeps the tier it had.
func mergeTiers(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
