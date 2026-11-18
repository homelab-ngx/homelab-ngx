package stacks

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// BaseModule is the NixOS module every version 7 stack is built on.
//
//go:embed stack-base.nix
var BaseModule string

// BaseModuleFile is the base module's name next to the wrapper flake.
const BaseModuleFile = "stack-base.nix"

// WrapperSystem is the system the wrapper flake builds.
const WrapperSystem = "nixosConfigurations.stack.config.system.build.toplevel"

// Wrapper is what a version 7 stack is built from: the stack's locked flake
// reference, the nixpkgs commit, and what the base module is told.
type Wrapper struct {
	// Name is the installed stack name, the container's hostname.
	Name string
	// Ref is the stack's locked flake reference.
	Ref string
	// NixpkgsRev is the NixOS/nixpkgs commit the stack is built with.
	NixpkgsRev string
	// TimeZone is the host's; empty leaves NixOS's default (UTC).
	TimeZone string
	// Host is what a version 8 stack is given beyond the base module; nil
	// for an older one, whose wrapper stays as it was.
	Host *HostModule
}

// HostModule is the part of a version 8 stack's system the daemon writes
// from its manifest and the host: the ids of the users and groups that own
// its path variables' host directories, the ports its firewall opens, and
// the host's GPU drivers.
type HostModule struct {
	// Users and Groups map a name in the stack to the host id it takes.
	Users, Groups map[string]uint32
	// TCP and UDP are the ports the stack's firewall opens: the routes',
	// the health probe's and the port grants' targets.
	TCP, UDP []int
	// GPU is set with the GPU grant.
	GPU *GPU
}

// pkgAttrRE is a nixpkgs attribute name the host may list as a GPU driver.
var pkgAttrRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// nix is the module as Nix, indented for the wrapper's module list.
func (h HostModule) nix() (string, error) {
	var b strings.Builder
	b.WriteString("        ({ lib, pkgs, ... }: {\n")
	for _, n := range sortedKeys(h.Users) {
		fmt.Fprintf(&b, "          users.users.%s.uid = lib.mkForce %d;\n", nixString(n), h.Users[n])
	}
	for _, n := range sortedKeys(h.Groups) {
		fmt.Fprintf(&b, "          users.groups.%s.gid = lib.mkForce %d;\n", nixString(n), h.Groups[n])
	}
	fmt.Fprintf(&b, "          networking.firewall.allowedTCPPorts = [ %s];\n", nixInts(h.TCP))
	fmt.Fprintf(&b, "          networking.firewall.allowedUDPPorts = [ %s];\n", nixInts(h.UDP))
	if g := h.GPU; g != nil {
		names := make([]string, len(g.Packages))
		for i, p := range g.Packages {
			if !pkgAttrRE.MatchString(p) {
				return "", fmt.Errorf("%q is not a nixpkgs attribute", p)
			}
			names[i] = nixString(p) + " "
		}
		list := "[ " + strings.Join(names, "") + "]"
		b.WriteString("          hardware.graphics.enable = true;\n")
		fmt.Fprintf(&b, "          hardware.graphics.extraPackages = map (n: pkgs.${n}) %s;\n", list)
		// A driver nixpkgs flags as vulnerable (Intel's Media SDK, which QSV
		// needs before Gen12) is the host's choice, so it is permitted.
		fmt.Fprintf(&b, "          nixpkgs.config.permittedInsecurePackages = map (p: p.name) (builtins.filter (p: (p.meta.knownVulnerabilities or [ ]) != [ ]) (map (n: nixpkgs.legacyPackages.x86_64-linux.${n}) %s));\n", list)
		if g.LibvaDriver != "" {
			fmt.Fprintf(&b, "          environment.sessionVariables.LIBVA_DRIVER_NAME = %s;\n", nixString(g.LibvaDriver))
		}
	}
	b.WriteString("        })\n")
	return b.String(), nil
}

func nixInts(ns []int) string {
	var b strings.Builder
	for _, n := range ns {
		fmt.Fprintf(&b, "%d ", n)
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Files are the wrapper flake's files, by name.
func (w Wrapper) Files() (map[string]string, error) {
	if !ValidName(w.Name) {
		return nil, fmt.Errorf("%q is not a stack name", w.Name)
	}
	if !commitRE.MatchString(w.NixpkgsRev) {
		return nil, fmt.Errorf("%q is not a nixpkgs commit", w.NixpkgsRev)
	}
	if w.Ref == "" {
		return nil, errors.New("no flake reference to build")
	}
	tz := "null"
	if w.TimeZone != "" {
		tz = nixString(w.TimeZone)
	}
	host := ""
	if w.Host != nil {
		var err error
		if host, err = w.Host.nix(); err != nil {
			return nil, err
		}
	}
	flake := `# Written by the homelab daemon: the stack's module on the daemon's base,
# built with the nixpkgs chosen for the stack.
{
  inputs.stack.url = ` + nixString(w.Ref) + `;
  inputs.nixpkgs.url = ` + nixString("github:NixOS/nixpkgs/"+w.NixpkgsRev) + `;
  inputs.stack.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { stack, nixpkgs, ... }: {
    nixosConfigurations.stack = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ./` + BaseModuleFile + `
        stack.nixosModules.default
        ({ lib, ... }: {
          networking.hostName = ` + nixString(w.Name) + `;
          time.timeZone = lib.mkDefault ` + tz + `;
        })
` + host + `      ];
    };
  };
}
`
	return map[string]string{"flake.nix": flake, BaseModuleFile: BaseModule}, nil
}

// Write puts the wrapper flake in dir, replacing what was there.
func (w Wrapper) Write(dir string) error {
	files, err := w.Files()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// nixString quotes s as a Nix string.
func nixString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "${", `\${`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// HostTimeZone is the host's time zone from /etc/localtime, or "".
func HostTimeZone() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok {
		return zone
	}
	return ""
}
