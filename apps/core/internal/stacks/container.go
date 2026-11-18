package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/store"
)

// Host is what the runtime uses of the machine it runs on.
type Host struct {
	Runner cmdrunner.Runner
	// StateDir holds one directory per stack: the container root, the
	// stored variables (variables.age) and the network file. It is inside
	// the host's backups.
	StateDir string
	// VarsDir holds one directory per stack with its variables decrypted,
	// bound read-only at /run/secrets in a stack of manifest version 4 or
	// later. It is
	// on tmpfs and outside the daemon's RuntimeDirectory, which systemd
	// empties on every daemon stop while the stacks keep running.
	VarsDir string
	// AgeKeyFile holds the host's age identities. Stored variables are
	// encrypted to them and decrypted with them.
	AgeKeyFile string
	// ProfilesDir holds one directory per stack with its system profile,
	// which is the GC root of everything the stack runs.
	ProfilesDir string
	// RoutesDir is the directory of route snippets Caddy imports.
	RoutesDir string
	// MirrorsDir holds a bare git mirror per stack, for its commit list.
	// Empty: commits are not read.
	MirrorsDir string
	// GitHubTokenFile is the token the mirrors fetch private repos with.
	GitHubTokenFile string
	// BuildsDir holds one wrapper flake per version 7 stack: the stack's
	// module on the daemon's base. Daemon state, not backed up.
	BuildsDir string
	// DashboardURL is the homelab dashboard's address, handed to version 8
	// stacks as HOMELAB_DASHBOARD_URL.
	DashboardURL string
	// LAN are the host's LAN subnets, the clients a port grant from "lan"
	// lets through.
	LAN []string
	// GPU is what a version 8 stack with the GPU grant is given.
	GPU GPU
}

// GPU is the host's graphics stack as a version 8 stack gets it with the GPU
// grant: the nixpkgs packages of its drivers and the VA-API driver name.
type GPU struct {
	Packages    []string
	LibvaDriver string
}

// DefaultHost is the layout on the homelab host.
func DefaultHost(r cmdrunner.Runner) Host {
	return Host{
		Runner: r, StateDir: StacksStateDir, ProfilesDir: "/nix/var/nix/profiles/per-stack", RoutesDir: "/var/lib/homelab-stack-routes",
		MirrorsDir: "/var/lib/homelab-daemon/stack-mirrors", GitHubTokenFile: "/run/secrets/nix/GITHUB_TOKEN",
		BuildsDir: "/var/lib/homelab-daemon/stack-builds",
		VarsDir:   "/run/homelab-stack-vars", AgeKeyFile: "/var/lib/sops-age/keys.txt",
	}
}

const (
	buildTimeout  = 2 * time.Hour
	startTimeout  = 5 * time.Minute
	stopTimeout   = 5 * time.Minute
	switchTimeout = 15 * time.Minute

	// Paths inside a stack.
	stackProfile = "/nix/var/nix/profiles"
	stackNetFile = "/run/systemd/network/10-homelab-host0.network"
	stackVarsDir = "/run/secrets"

	// Files in a stack's state directory on the host.
	netFileName = "host0.network"
)

func unitName(name string) string { return "homelab-stack-" + name + ".service" }

// JournalDir is a stack's persistent journal on the host, inside its root. It
// is readable with `journalctl --directory` whether or not the stack runs.
func JournalDir(name string) string {
	return filepath.Join(StacksStateDir, name, "root", "var", "log", "journal")
}

func (s *Service) stateDir(name string) string   { return filepath.Join(s.host.StateDir, name) }
func (s *Service) profileDir(name string) string { return filepath.Join(s.host.ProfilesDir, name) }
func (s *Service) profile(name string) string    { return filepath.Join(s.profileDir(name), "system") }
func (s *Service) netFile(name string) string {
	return filepath.Join(s.stateDir(name), netFileName)
}

func (s *Service) run(ctx context.Context, timeout time.Duration, name string, args ...string) (cmdrunner.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: name, Args: args})
}

// hostBind is one host directory bound into a stack: a version 4 storage
// grant, or a version 5 path variable with its resolved host path.
type hostBind struct {
	Host, Mount string
	ReadOnly    bool
}

// hostBinds are the binds manifest m gives with the stored values vars:
// storage as declared, and each path variable at the host path it resolves
// to. An optional path with no value has no bind; a required one is an
// error, since the stack would write into its own root instead.
func hostBinds(m Manifest, vars []storedVar) ([]hostBind, error) {
	var out []hostBind
	for _, st := range m.Storage {
		out = append(out, hostBind{st.HostPath, st.Mount, st.ReadOnly})
	}
	for _, d := range m.pathVars() {
		value, src := resolve(d, vars)
		if src == SourceUnset {
			if d.Required {
				return nil, fmt.Errorf("the path %s has no value", d.Name)
			}
			continue
		}
		out = append(out, hostBind{value, d.Mount, d.ReadOnly})
	}
	return out, nil
}

// nspawnArgs is the systemd-run command line that boots a stack as the
// transient unit homelab-stack-<name>.service: a picked user namespace with
// ID-mapped ownership, a veth, the store read-only and the stack's own
// profile directory. Grants add binds: the host directories with `:idmap`
// (the stack shares uids with the data on disk), the GPU as /dev/dri, and
// containers the system calls podman needs, and an API grant the directory
// with the stack's token file. A stack of manifest version 4 or later gets
// its variables at /run/secrets; keyed off the version, so the update to
// version 4 changes the arguments and restarts the container, which gains
// the bind. Storage and path binds give the same flag, so moving one to the
// other changes nothing here.
func nspawnArgs(name, stateDir, profileDir, varsDir string, m Manifest, binds []hostBind) []string {
	a := []string{
		"--unit=" + strings.TrimSuffix(unitName(name), ".service"),
		"--description=Stack " + name,
		"-p", "Type=notify", "-p", "NotifyAccess=all", "-p", "KillMode=mixed", "-p", "Delegate=yes",
		"-p", fmt.Sprintf("TimeoutStartSec=%d", int(startTimeout.Seconds())),
		"systemd-nspawn", "-M", name, "-D", filepath.Join(stateDir, "root"),
		"--notify-ready=yes", "--kill-signal=SIGRTMIN+3",
		"--private-users=pick", "--private-users-ownership=auto", "--network-veth",
		"--bind-ro=/nix/store",
		"--bind-ro=" + profileDir + ":" + stackProfile,
		"--bind-ro=" + filepath.Join(stateDir, netFileName) + ":" + stackNetFile,
	}
	if m.Version >= 4 {
		a = append(a, "--bind-ro="+varsDir+":"+stackVarsDir+":idmap")
	}
	if m.GPU {
		a = append(a, "--bind=/dev/dri", "--property=DeviceAllow=char-drm rwm")
	}
	if m.Containers {
		a = append(a, "--system-call-filter=bpf add_key keyctl")
	}
	if len(m.API) > 0 {
		a = append(a, "--bind-ro="+filepath.Join(stateDir, apiEnvDirName)+":"+stackAPIDir+":idmap")
	}
	for _, b := range binds {
		flag := "--bind="
		if b.ReadOnly {
			flag = "--bind-ro="
		}
		a = append(a, flag+b.Host+":"+b.Mount+":idmap")
	}
	// No journal link: with a user namespace nspawn links the host's
	// /var/log/journal/<id> to a temporary path that is gone after the
	// stop. `journalctl -M <name>` reads the stack's journal without it.
	return append(a, "--link-journal=no", stackProfile+"/system/init")
}

// bootArgs is the command line the stack boots m with under the values
// stored now.
func (s *Service) bootArgs(name string, m Manifest) ([]string, error) {
	var vars []storedVar
	if len(m.pathVars()) > 0 {
		s.varsMu.Lock()
		var err error
		vars, err = s.loadVars(name)
		s.varsMu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	binds, err := hostBinds(m, vars)
	if err != nil {
		return nil, err
	}
	return nspawnArgs(name, s.stateDir(name), s.profileDir(name), s.varsDir(name), m, binds), nil
}

// bootedArgs are the arguments the stack's container was last booted with,
// or nil when that is not recorded (booted by a daemon before the record).
func bootedArgs(r store.Stack) []string {
	var a []string
	if r.BootedArgs == "" || json.Unmarshal([]byte(r.BootedArgs), &a) != nil {
		return nil
	}
	return a
}

// prepare creates what nspawn needs before the first boot: the profile
// directory, a root it accepts as an OS tree and the network file.
func (s *Service) prepare(ctx context.Context, name, subnet string) error {
	root := filepath.Join(s.stateDir(name), "root")
	if err := mkdirs(0o700, s.stateDir(name)); err != nil {
		return err
	}
	// World-readable on purpose: inside the stack the profile belongs to
	// nobody, and the root must be open to the stack's own users.
	if err := mkdirs(0o755, s.host.ProfilesDir, s.profileDir(name)); err != nil {
		return err
	}
	// The root is the stack's to write once it has booted, so the daemon
	// fills it in only when creating it: a later path-based mkdir or chmod
	// as host root would follow a symlink the stack left there.
	if !exists(root) {
		if err := mkdirs(0o755, root, filepath.Join(root, "etc"), filepath.Join(root, "usr"),
			filepath.Join(root, "var"), filepath.Join(root, "var/lib")); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(root, "etc/os-release"), "", 0o644); err != nil {
			return err
		}
	}
	nf, err := networkFile(subnet)
	if err != nil {
		return err
	}
	return writeFile(s.netFile(name), nf, 0o644)
}

// mkdirs creates the directories with exactly mode, whatever the daemon's
// umask is.
func mkdirs(mode os.FileMode, paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, mode); err != nil {
			return err
		}
		if err := os.Chmod(p, mode); err != nil {
			return err
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// unitState is systemd's state of the stack's unit ("active", "inactive",
// "failed", "activating", …), or "" when systemd did not answer.
func (s *Service) unitState(ctx context.Context, name string) string {
	res, _ := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "systemctl", Args: []string{"is-active", unitName(name)}, ExpectExit: []int{3, 4},
	})
	return strings.TrimSpace(res.Stdout)
}

// isRunning reports whether the stack's unit is active.
func (s *Service) isRunning(ctx context.Context, name string) bool {
	return s.unitState(ctx, name) == "active"
}

// isStopped reports whether the stack's container is known to be gone. No
// answer, or a unit that is starting or stopping, is not stopped: the
// stack's firewall may only be removed once nothing can use its interface.
func (s *Service) isStopped(ctx context.Context, name string) bool {
	st := s.unitState(ctx, name)
	return (st == "inactive" || st == "failed") && !s.machineRegistered(ctx, name)
}

// machineRegistered reports whether systemd still knows a container of this
// name. The container's own processes live in a scope beside the stack's
// unit, so they can outlive it when nspawn itself is killed.
func (s *Service) machineRegistered(ctx context.Context, name string) bool {
	_, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "machinectl", Args: []string{"show", name, "--property=State"}, ExpectExit: []int{1},
	})
	return err == nil
}

// reap ends a container that outlived its unit, so the name, the interface
// and the root are free for a new one.
func (s *Service) reap(ctx context.Context, name string) error {
	if !s.machineRegistered(ctx, name) {
		return nil
	}
	s.log.Warn("ending a stack container that outlived its unit", "stack", name)
	_, _ = s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "machinectl", Args: []string{"terminate", name}, ExpectExit: []int{1},
	})
	deadline := time.Now().Add(30 * time.Second)
	for s.machineRegistered(ctx, name) {
		if time.Now().After(deadline) {
			return fmt.Errorf("container %s is still registered after it was terminated", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.healthEvery):
		}
	}
	return nil
}

// boot starts the stack's container and returns when its system is up.
func (s *Service) boot(ctx context.Context, name string, m Manifest) error {
	if err := s.reap(ctx, name); err != nil {
		return err
	}
	// A unit left over from a failed start would block the new one.
	_, _ = s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "systemctl", Args: []string{"reset-failed", unitName(name)}, ExpectExit: []int{1},
	})
	// The token file is written again when it went missing; the directory
	// is bound whenever the generation asks for the API, token or not.
	if err := s.syncAPIToken(ctx, name); err != nil {
		return fmt.Errorf("issuing the stack's API token: %w", err)
	}
	if len(m.API) > 0 {
		if err := mkdirs(0o700, s.apiEnvDir(name)); err != nil {
			return err
		}
	}
	// The variables are rendered again on every boot: /run is empty after
	// a reboot, and a value may have changed while the stack was stopped.
	if m.Version >= 4 {
		if err := s.renderVars(ctx, name, m); err != nil {
			return fmt.Errorf("delivering the stack's variables: %w", err)
		}
	}
	// A path the approval could not store gets its default stored now, so
	// the directory booted with is the one kept.
	s.materializePathsLogged(name, m)
	args, err := s.bootArgs(name, m)
	if err != nil {
		return fmt.Errorf("binding the stack's paths: %w", err)
	}
	if _, err := s.run(ctx, startTimeout+time.Minute, "systemd-run", args...); err != nil {
		return fmt.Errorf("starting the container: %w", err)
	}
	b, _ := json.Marshal(args)
	if err := s.st.Queries().SetStackBootedArgs(ctx, store.SetStackBootedArgsParams{Name: name, Args: string(b)}); err != nil {
		s.log.Warn("the stack's boot arguments not recorded", "stack", name, "error", err)
	}
	if err := s.st.Queries().SetStackBootedVars(ctx, store.SetStackBootedVarsParams{Name: name, Digest: s.deliveredDigest(name)}); err != nil {
		s.log.Warn("the stack's delivered values not recorded", "stack", name, "error", err)
	}
	return nil
}

// halt stops the stack's container. A unit systemd no longer knows (exit
// code 5: a transient unit is forgotten once it has stopped) is stopped.
func (s *Service) halt(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	_, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "systemctl", Args: []string{"stop", unitName(name)}, ExpectExit: []int{5},
	})
	var ce *cmdrunner.Error
	if err != nil && !(errors.As(err, &ce) && ce.Result.ExitCode == 5) {
		return fmt.Errorf("stopping the container: %w", err)
	}
	return s.reap(ctx, name)
}

// buildPath is the one store path `nix build --print-out-paths` prints.
func buildPath(stdout string) (string, error) {
	path := strings.TrimSpace(stdout)
	if !strings.HasPrefix(path, "/nix/store/") || strings.ContainsAny(path, " \n") {
		return "", errors.New("building: nix did not print one store path")
	}
	return path, nil
}

var currentGenRE = regexp.MustCompile(`(?m)^\s*(\d+)\s.*\(current\)\s*$`)

// setProfile makes system the newest generation of the stack's profile and
// returns its number.
func (s *Service) setProfile(ctx context.Context, name, system string) (int64, error) {
	if err := mkdirs(0o755, s.host.ProfilesDir, s.profileDir(name)); err != nil {
		return 0, err
	}
	if _, err := s.run(ctx, 5*time.Minute, "nix-env", "--profile", s.profile(name), "--set", system); err != nil {
		return 0, fmt.Errorf("setting the profile: %w", err)
	}
	res, err := s.run(ctx, time.Minute, "nix-env", "--profile", s.profile(name), "--list-generations")
	if err != nil {
		return 0, fmt.Errorf("reading the profile: %w", err)
	}
	m := currentGenRE.FindStringSubmatch(res.Stdout)
	if m == nil {
		return 0, errors.New("reading the profile: no current generation")
	}
	return strconv.ParseInt(m[1], 10, 64)
}

// switchProfile points the stack's profile at an earlier generation.
func (s *Service) switchProfile(ctx context.Context, name string, generation int64) error {
	if _, err := s.run(ctx, 5*time.Minute, "nix-env", "--profile", s.profile(name),
		"--switch-generation", strconv.FormatInt(generation, 10)); err != nil {
		return fmt.Errorf("switching the profile: %w", err)
	}
	return nil
}

// switchSystem activates the profile's current generation inside the
// running stack.
func (s *Service) switchSystem(ctx context.Context, name string) error {
	if _, err := s.run(ctx, switchTimeout, "systemd-run", "--machine="+name, "--wait", "--collect", "--quiet", "--",
		stackProfile+"/system/bin/switch-to-configuration", "switch"); err != nil {
		return fmt.Errorf("switching the system: %w", err)
	}
	return nil
}

// checkHealth runs the manifest's health check once. A manifest without one
// is healthy as soon as the container is up.
func (s *Service) checkHealth(ctx context.Context, name, addr string, h Health) error {
	for _, u := range h.Units {
		res, _ := s.r.Run(ctx, cmdrunner.Cmd{
			Module: "stacks", Name: "systemctl", Args: []string{"--machine=" + name, "is-active", "--", u}, ExpectExit: []int{1, 3, 4},
		})
		if st := strings.TrimSpace(res.Stdout); st != "active" {
			if st == "" {
				st = "unknown"
			}
			return fmt.Errorf("%s is %s", u, st)
		}
	}
	if h.HTTP != nil {
		url := fmt.Sprintf("http://%s:%d%s", addr, h.HTTP.Port, h.HTTP.Path)
		if err := s.probe(ctx, url); err != nil {
			return fmt.Errorf("%s: %w", url, err)
		}
	}
	return nil
}

// waitHealthy polls the health check until it passes or healthTimeout runs
// out, and then returns the last failure.
func (s *Service) waitHealthy(ctx context.Context, name, addr string, h Health) error {
	deadline := s.now().Add(s.healthTimeout)
	for {
		err := s.checkHealth(ctx, name, addr, h)
		if err == nil {
			return nil
		}
		if !s.now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.healthEvery):
		}
	}
}

// probeClient does not follow redirects: the answer comes from the stack,
// and a redirect would send the daemon to an address the stack itself is
// not allowed to reach.
var probeClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// httpProbe is the default probe: a GET that must answer 2xx.
func httpProbe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
