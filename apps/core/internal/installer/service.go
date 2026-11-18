package installer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/hostcfg"
)

// Marker is the file on the installer stick's /var that starts the daemon
// in installer mode. The install does not copy it.
const Marker = "/var/lib/homelab/installer"

// definitions are the image's partition definitions for an install.
var definitions = "/usr/lib/repart.sysinstall.d"

// mountDir is where the installer mounts a target's ESP.
var mountDir = "/run/homelab-installer/esp"

// maxLog is how many output lines the page gets.
const maxLog = 400

var (
	// ErrClaimed: someone else opened the installer first.
	ErrClaimed = errors.New("the installer is open in another browser")
	// ErrBusy: an install is running or done.
	ErrBusy = errors.New("an install is running or finished")
	// ErrInvalid: the request names a disk, partition or host name that will not do.
	ErrInvalid = errors.New("invalid")
)

// Active reports whether the daemon runs on the installer stick.
func Active() bool {
	_, err := os.Stat(Marker)
	return err == nil
}

// Service is the installer.
type Service struct {
	r     cmdrunner.Runner
	disks Disks
	log   *slog.Logger

	mu       sync.Mutex
	token    string
	step     Step
	disk     string
	hostname string
	phase    string
	lines    []string
	err      string
}

// New returns the installer; disks is UDisks{} on the stick.
func New(r cmdrunner.Runner, disks Disks, log *slog.Logger) *Service {
	return &Service{r: r, disks: disks, log: log, step: StepIdle}
}

// InstallerState returns the page's view. A nil service (any host but the stick)
// says the installer is not active.
func (s *Service) State(ctx context.Context) (InstallerState, error) {
	if s == nil {
		return InstallerState{Log: []string{}, Disks: []InstallDisk{}, Step: StepIdle, MinBytes: MinBytes, Current: hostcfg.IPv4{Mode: "dhcp"}}, nil
	}
	disks, err := s.disks.List(ctx)
	if err != nil {
		return InstallerState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return InstallerState{
		Active: true, Claimed: s.token != "", Step: s.step, Phase: s.phase, Disk: s.disk, Hostname: s.hostname,
		Log: append([]string{}, s.lines...), Error: s.err, Disks: disks, MinBytes: MinBytes,
		Current: s.lease(ctx),
	}, nil
}

// lease is the installer medium's own IPv4 setting from DHCP: where a static
// setting starts.
func (s *Service) lease(ctx context.Context) hostcfg.IPv4 {
	out := hostcfg.IPv4{Mode: "dhcp"}
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "ip", Args: []string{"-4", "-j", "route", "show", "default"}})
	var routes []struct {
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if err != nil || json.Unmarshal([]byte(res.Stdout), &routes) != nil || len(routes) == 0 {
		return out
	}
	out.Gateway = routes[0].Gateway
	res, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "ip", Args: []string{"-4", "-j", "addr", "show", "dev", routes[0].Dev}})
	var links []struct {
		Addr []struct {
			Local     string `json:"local"`
			Prefixlen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err == nil && json.Unmarshal([]byte(res.Stdout), &links) == nil && len(links) > 0 && len(links[0].Addr) > 0 {
		a := links[0].Addr[0]
		out.Address = fmt.Sprintf("%s/%d", a.Local, a.Prefixlen)
	}
	b, _ := os.ReadFile(resolvConf)
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "nameserver" && !strings.Contains(f[1], ":") && len(out.DNS) < 3 {
			out.DNS = append(out.DNS, f[1])
		}
	}
	return out
}

// resolvConf lists the DNS servers DHCP gave (systemd-resolved's upstreams).
var resolvConf = "/run/systemd/resolve/resolv.conf"

// Claim gives the first caller the installer: the token every change needs.
func (s *Service) Claim() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" {
		return "", ErrClaimed
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s.token = hex.EncodeToString(b)
	return s.token, nil
}

func (s *Service) holds(token string) error {
	if token == "" || token != s.token {
		return ErrClaimed
	}
	return nil
}

// disk finds a disk by kernel name.
func (s *Service) find(ctx context.Context, name string) (InstallDisk, error) {
	disks, err := s.disks.List(ctx)
	if err != nil {
		return InstallDisk{}, err
	}
	for _, d := range disks {
		if d.Name == name {
			return d, nil
		}
	}
	return InstallDisk{}, fmt.Errorf("%w: no disk %q", ErrInvalid, name)
}

// DeletePartition deletes one partition; confirm must be its name.
func (s *Service) DeletePartition(ctx context.Context, token, disk, part, confirm string) error {
	s.mu.Lock()
	err := s.holds(token)
	if err == nil && s.step != StepIdle && s.step != StepFailed {
		err = ErrBusy
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	d, err := s.find(ctx, disk)
	if err != nil {
		return err
	}
	if d.InUse {
		return fmt.Errorf("%w: the installer runs from %s", ErrInvalid, disk)
	}
	for _, p := range d.Partitions {
		if p.Name == part {
			if confirm != p.Name {
				return fmt.Errorf("%w: type %s to delete it", ErrInvalid, p.Name)
			}
			s.log.Info("deleting partition", "partition", p.Path, "holds", p.Holds)
			return s.disks.DeletePartition(ctx, p.Path)
		}
	}
	return fmt.Errorf("%w: no partition %q on %s", ErrInvalid, part, disk)
}

// InstallPlan asks systemd-repart what an install would do to a disk.
// Into free space, region is the start of the free run to fill (0: the
// first that fits).
func (s *Service) Plan(ctx context.Context, disk string, erase bool, esp string, region uint64) (InstallPlan, error) {
	d, err := s.find(ctx, disk)
	if err != nil {
		return InstallPlan{}, err
	}
	if c := choice(d, erase); !c.OK {
		return InstallPlan{Reason: c.Reason, Rows: []InstallPlanRow{}}, nil
	}
	shared, err := s.sharedESP(ctx, d, esp)
	if err != nil {
		return InstallPlan{}, err
	}
	defs, err := definitionsFor(shared != nil)
	if err != nil {
		return InstallPlan{}, err
	}
	empty := "allow"
	if erase {
		empty = "force"
	} else if region != 0 {
		if !regionOK(d, region) {
			return InstallPlan{}, fmt.Errorf("%w: no free region at byte %d of %s", ErrInvalid, region, disk)
		}
		return s.planOnCopy(ctx, d, defs, region)
	}
	res, _ := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "installer", Name: "systemd-repart",
		Args: []string{"--definitions=" + defs, "--dry-run=yes", "--empty=" + empty, "--json=short", d.Path},
	})
	return parsePlan(res.Stdout, res.Output), nil
}

// sharedESP is the ESP on another disk an install puts its boot loader on,
// named by its kernel name (nvme1n1p3), or nil for the target disk's own.
func (s *Service) sharedESP(ctx context.Context, target InstallDisk, name string) (*InstallPartition, error) {
	if name == "" {
		return nil, nil
	}
	disks, err := s.disks.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range disks {
		for _, p := range d.Partitions {
			if p.Name != name {
				continue
			}
			switch {
			case p.Holds != "esp":
				return nil, fmt.Errorf("%w: %s is not an EFI partition", ErrInvalid, name)
			case d.InUse:
				return nil, fmt.Errorf("%w: %s is on the installer medium", ErrInvalid, name)
			case d.Name == target.Name:
				return nil, nil // the target's own ESP: repart keeps it as it is
			case p.Size < minESP:
				return nil, fmt.Errorf("%w: %s is smaller than 300 MB", ErrInvalid, name)
			}
			return &p, nil
		}
	}
	return nil, fmt.Errorf("%w: no partition %s", ErrInvalid, name)
}

// definitionsFor is the image's partition definitions, without the ESP's
// when the boot loader goes on another disk's ESP.
func definitionsFor(sharedESP bool) (string, error) {
	if !sharedESP {
		return definitions, nil
	}
	dir := filepath.Join(filepath.Dir(mountDir), "definitions")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(definitions)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "00-esp") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(definitions, e.Name()))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func choice(d InstallDisk, erase bool) InstallChoice {
	if erase {
		return d.Erase
	}
	return d.Beside
}

// parsePlan reads repart's --json output; without it, the plan failed and
// repart's last line says why.
func parsePlan(stdout, output string) InstallPlan {
	var rows []map[string]any
	if i := strings.Index(stdout, "["); i >= 0 && json.Unmarshal([]byte(stdout[i:]), &rows) == nil {
		p := InstallPlan{OK: true, Rows: []InstallPlanRow{}}
		for _, r := range rows {
			s := func(k string) string { v, _ := r[k].(string); return v }
			n, _ := r["raw_size"].(float64)
			off, _ := r["offset"].(float64)
			p.Rows = append(p.Rows, InstallPlanRow{Type: s("type"), Label: s("label"), Node: s("node"), Size: uint64(n), Offset: uint64(off), Activity: s("activity")})
		}
		return p
	}
	reason := "systemd-repart made no plan"
	for _, l := range strings.Split(strings.TrimSpace(output), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			reason = l
		}
	}
	return InstallPlan{Reason: reason, Rows: []InstallPlanRow{}}
}

var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Install starts the install onto disk (erased, or into
// its free space), then the ESP made the way image updates expect it.
func (s *Service) Install(ctx context.Context, token, disk string, erase bool, setup Setup) error {
	setup.Hostname = strings.ToLower(strings.TrimSpace(setup.Hostname))
	setup.Timezone = strings.TrimSpace(setup.Timezone)
	if !hostnameRE.MatchString(setup.Hostname) {
		return fmt.Errorf("%w: a host name is letters, digits and dashes, up to 63", ErrInvalid)
	}
	if setup.Timezone != "" {
		if _, err := os.Stat(filepath.Join(zoneinfo, setup.Timezone)); err != nil || strings.Contains(setup.Timezone, "..") {
			return fmt.Errorf("%w: %s is not a known time zone", ErrInvalid, setup.Timezone)
		}
	}
	keys, err := hostcfg.CheckSSHKeys(setup.SSHKeys)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), hostcfg.ErrInvalid.Error()+": "))
	}
	setup.SSHKeys = keys
	ip, err := hostcfg.CheckIPv4(setup.IPv4)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), hostcfg.ErrInvalid.Error()+": "))
	}
	setup.IPv4 = ip
	d, err := s.find(ctx, disk)
	if err != nil {
		return err
	}
	if c := choice(d, erase); !c.OK {
		return fmt.Errorf("%w: %s", ErrInvalid, c.Reason)
	}
	if _, err := s.sharedESP(ctx, d, setup.ESP); err != nil {
		return err
	}
	if !erase && setup.Region != 0 && !regionOK(d, setup.Region) {
		return fmt.Errorf("%w: no free region at byte %d of %s", ErrInvalid, setup.Region, disk)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.holds(token); err != nil {
		return err
	}
	if s.step == StepInstalling || s.step == StepInstalled {
		return ErrBusy
	}
	s.step, s.phase, s.disk, s.hostname, s.lines, s.err = StepInstalling, "partition", disk, setup.Hostname, nil, ""
	go s.run(context.WithoutCancel(ctx), d, erase, setup)
	return nil
}

// partition runs systemd-repart on d, into the free run at region when
// one was picked.
func (s *Service) partition(ctx context.Context, d InstallDisk, defs, empty string, erase bool, region uint64) (err error) {
	if !erase && region != 0 {
		release, err := s.reserve(ctx, d, region)
		if err != nil {
			return err
		}
		defer func() {
			if rerr := release(); rerr != nil && err == nil {
				err = rerr
			}
			_ = s.cmd(ctx, "udevadm", "settle")
		}()
	}
	s.line("Partitioning " + d.Path)
	err = s.cmd(ctx, "systemd-repart", "--definitions="+defs, "--empty="+empty, "--dry-run=no", d.Path)
	_ = s.cmd(ctx, "udevadm", "settle")
	return err
}

func (s *Service) setPhase(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = p
}

func (s *Service) line(l string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, l)
	if len(s.lines) > maxLog {
		s.lines = s.lines[len(s.lines)-maxLog:]
	}
}

func (s *Service) run(ctx context.Context, d InstallDisk, erase bool, setup Setup) {
	err := s.install(ctx, d, erase, setup)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.step, s.err = StepFailed, err.Error()
		s.log.Error("install failed", "disk", d.Path, "error", err)
		return
	}
	s.step = StepInstalled
	s.log.Info("installed", "disk", d.Path, "hostname", setup.Hostname, "timezone", setup.Timezone)
}

// install puts the system on d: the image's partition definitions through
// systemd-repart (the running root slot is copied block for block, and the
// new /var gets the core extension), then systemd-boot and the running
// kernel on the disk's ESP, then the host name in the new /var.
//
// systemd-sysinstall runs the same steps, but its `bootctl install` writes
// /etc/kernel/entry-token into the new root, which is read-only here, and
// its `bootctl link` puts the kernel where image updates do not keep it.
func (s *Service) install(ctx context.Context, d InstallDisk, erase bool, setup Setup) error {
	// bootctl install writes its own EFI/BOOT/BOOTX64.EFI; another system's
	// fallback loader on a shared ESP is put back afterwards.
	shared, err := s.sharedESP(ctx, d, setup.ESP)
	if err != nil {
		return err
	}
	existing := ""
	switch {
	case shared != nil:
		existing = shared.Path
	case !erase:
		existing = partOf(d, typeESP)
	}
	var fallback []byte
	if existing != "" {
		_ = s.withMount(ctx, existing, func(dir string) error {
			fallback, _ = os.ReadFile(filepath.Join(dir, "EFI", "BOOT", "BOOTX64.EFI"))
			return nil
		})
	}
	defs, err := definitionsFor(shared != nil)
	if err != nil {
		return err
	}
	version, kernel, err := s.runningKernel(ctx)
	if err != nil {
		return err
	}
	empty := "allow"
	if erase {
		empty = "force"
	}
	if err := s.partition(ctx, d, defs, empty, erase, setup.Region); err != nil {
		return err
	}
	after, err := s.find(ctx, d.Name)
	if err != nil {
		return err
	}
	esp, varPart := partOf(after, typeESP), partOf(after, typeVar)
	if shared != nil {
		esp = shared.Path
	}
	if esp == "" || varPart == "" {
		return errors.New("the new partitions are not on the disk")
	}
	s.setPhase("boot")
	s.line("Installing the boot loader and kernel " + version)
	err = s.withMount(ctx, esp, func(dir string) error {
		// The machine-id token keeps bootctl from writing an entry token
		// into the read-only root.
		if err := s.cmd(ctx, "bootctl", "--esp-path="+dir, "--entry-token=machine-id",
			"--make-entry-directory=no", "--variables=yes", "install"); err != nil {
			return err
		}
		return finishESP(dir, kernel, version, fallback)
	})
	if err != nil {
		return err
	}
	if err := s.bootFirst(ctx, esp); err != nil {
		// The firmware may still start another loader first; its boot
		// menu reaches this one. Not a failed install.
		s.line("Could not put this system first in the firmware's boot order: " + err.Error())
	}
	s.setPhase("finish")
	s.line("Setting the host name " + setup.Hostname)
	return s.withMount(ctx, varPart, func(dir string) error { return seedVar(dir, setup) })
}

// cmd runs one install step, its output in the page's log.
func (s *Service) cmd(ctx context.Context, name string, args ...string) error {
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: name, Args: args, OnLine: func(_, l string) { s.line(l) }}); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// runningKernel is the version the stick runs and its kernel image on the
// stick's ESP.
func (s *Service) runningKernel(ctx context.Context) (string, string, error) {
	version := imageVersion(osRelease)
	if version == "" {
		return "", "", errors.New("the running image has no IMAGE_VERSION")
	}
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "bootctl", Args: []string{"--print-esp-path"}})
	if err != nil {
		return "", "", fmt.Errorf("the stick's ESP: %w", err)
	}
	found, _ := filepath.Glob(filepath.Join(strings.TrimSpace(res.Stdout), "EFI", "Linux", "homelab_"+version+"*.efi"))
	for _, f := range found {
		if m := ukiRE.FindStringSubmatch(filepath.Base(f)); m != nil && m[1] == version {
			return version, f, nil
		}
	}
	return "", "", fmt.Errorf("no kernel for %s on the stick's ESP", version)
}

var (
	osRelease = "/etc/os-release"
	ukiRE     = regexp.MustCompile(`^homelab_([^+]+?)(?:\+\d+(?:-\d+)?)?\.efi$`)
	versionRE = regexp.MustCompile(`(?m)^IMAGE_VERSION="?([^"\n]+)"?$`)
)

func imageVersion(path string) string {
	b, _ := os.ReadFile(path)
	if m := versionRE.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

func partOf(d InstallDisk, typ string) string {
	for _, p := range d.Partitions {
		if strings.EqualFold(p.Type, typ) {
			return p.Path
		}
	}
	return ""
}

func (s *Service) withMount(ctx context.Context, dev string, f func(dir string) error) error {
	if err := os.MkdirAll(mountDir, 0o700); err != nil {
		return err
	}
	// An ESP is FAT: without a umask every file is world-readable (bootctl
	// warns about the random seed).
	args := []string{dev, mountDir}
	if fs, _ := s.fsType(ctx, dev); fs == "vfat" {
		args = []string{"-o", "umask=0077", dev, mountDir}
	}
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "mount", Args: args}); err != nil {
		return fmt.Errorf("mount %s: %w", dev, err)
	}
	ferr := f(mountDir)
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "umount", Args: []string{mountDir}}); err != nil && ferr == nil {
		ferr = fmt.Errorf("umount %s: %w", dev, err)
	}
	return ferr
}

func (s *Service) fsType(ctx context.Context, dev string) (string, error) {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "blkid", Args: []string{"-s", "TYPE", "-o", "value", dev}})
	return strings.TrimSpace(res.Stdout), err
}

// bootFirst puts the firmware's boot entry for systemd-boot on esp first
// in the boot order: bootctl install adds its entry but can leave an older
// one (another system's systemd-boot) in front of it.
func (s *Service) bootFirst(ctx context.Context, esp string) error {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "blkid", Args: []string{"-s", "PART_ENTRY_UUID", "-o", "value", esp}})
	if err != nil {
		return err
	}
	uuid := strings.TrimSpace(res.Stdout)
	res, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "efibootmgr"})
	if err != nil {
		return err
	}
	order, ok := firstInOrder(res.Stdout, uuid)
	if !ok {
		return fmt.Errorf("no boot entry for %s", esp)
	}
	return s.cmd(ctx, "efibootmgr", "-q", "-o", order)
}

var (
	bootOrderRE = regexp.MustCompile(`(?m)^BootOrder: ([0-9A-Fa-f,]+)`)
	bootEntryRE = regexp.MustCompile(`(?m)^Boot([0-9A-Fa-f]{4})\*? .*HD\(\d+,GPT,([0-9a-fA-F-]+),.*\\EFI\\systemd\\systemd-boot`)
)

// firstInOrder reads efibootmgr's listing and returns the boot order with
// the systemd-boot entry on the ESP with partition UUID uuid first.
func firstInOrder(listing, uuid string) (string, bool) {
	var ours string
	for _, m := range bootEntryRE.FindAllStringSubmatch(listing, -1) {
		if strings.EqualFold(m[2], uuid) {
			ours = strings.ToUpper(m[1])
		}
	}
	if ours == "" {
		return "", false
	}
	order := []string{ours}
	if m := bootOrderRE.FindStringSubmatch(listing); m != nil {
		for _, n := range strings.Split(m[1], ",") {
			if n = strings.ToUpper(n); n != "" && n != ours {
				order = append(order, n)
			}
		}
	}
	return strings.Join(order, ","), true
}

// finishESP puts the running kernel where image updates keep theirs
// (EFI/Linux, as homelab_VERSION.efi), makes the image the boot menu's
// default, and puts back a fallback loader another system had.
func finishESP(dir, kernel, version string, fallback []byte) error {
	b, err := os.ReadFile(kernel)
	if err != nil {
		return err
	}
	linux := filepath.Join(dir, "EFI", "Linux")
	if err := os.MkdirAll(linux, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(linux, "homelab_"+version+".efi"), b, 0o644); err != nil {
		return err
	}
	if err := setDefault(filepath.Join(dir, "loader", "loader.conf")); err != nil {
		return err
	}
	if len(fallback) > 0 {
		p := filepath.Join(dir, "EFI", "BOOT", "BOOTX64.EFI")
		if cur, _ := os.ReadFile(p); !bytes.Equal(cur, fallback) {
			if err := os.WriteFile(p, fallback, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// setDefault makes the newest homelab kernel the boot menu's default,
// keeping the rest of loader.conf. Unless it says otherwise, the menu shows
// for 3 seconds (another system on the ESP is one key press away) and its
// kernel command line cannot be edited.
func setDefault(path string) error {
	b, _ := os.ReadFile(path)
	var out []string
	set := map[string]bool{}
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == "default" {
			continue
		}
		if len(f) > 0 {
			set[f[0]] = true
		}
		if l != "" || len(out) > 0 {
			out = append(out, l)
		}
	}
	if !set["timeout"] {
		out = append(out, "timeout 3")
	}
	if !set["editor"] {
		out = append(out, "editor no")
	}
	out = append(out, "default homelab_*")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// Setup is what the page asks besides the disk.
type Setup struct {
	// ESP is another disk's ESP (kernel name) the boot loader goes on;
	// empty for the target disk's own. Not written to the new /var.
	ESP string `json:"-"`
	// Region is the start of the free run an install into free space
	// fills; 0 for the first that fits.
	Region   uint64       `json:"-"`
	Hostname string       `json:"hostname"`
	Timezone string       `json:"timezone,omitempty"`
	SSHKeys  []string     `json:"ssh_keys,omitempty"`
	IPv4     hostcfg.IPv4 `json:"ipv4"`
}

// SetupFile is where the installer leaves the Setup in the new /var; the
// daemon takes it into the host settings at its first start.
const SetupFile = "/var/lib/homelab/install.json"

// zoneinfo holds the time zones a Setup may name.
var zoneinfo = "/usr/share/zoneinfo"

// seedVar writes the Setup into the new /var (mounted at dir), checks
// repart copied the core extension in, and makes the directories the
// install created root's and world-readable, as a fresh /var's are.
func seedVar(dir string, setup Setup) error {
	if _, err := os.Stat(filepath.Join(dir, "lib", "extensions", "homelab-core.raw")); err != nil {
		return fmt.Errorf("the core extension is not in the new /var: %w", err)
	}
	p := filepath.Join(dir, strings.TrimPrefix(SetupFile, "/var/"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(setup)
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		return err
	}
	for _, sub := range []string{".", "lib", "lib/extensions", "lib/extensions.d", "lib/homelab"} {
		if err := rootOwned(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	return rootOwned(p, 0o644)
}

// rootOwned makes path root's with mode; a path the install did not create
// is left alone.
func rootOwned(path string, mode os.FileMode) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.Lchown(path, 0, 0); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return os.Chmod(path, mode)
}

// PowerOff turns the machine off once the install is done.
func (s *Service) PowerOff(ctx context.Context, token string) error {
	s.mu.Lock()
	err := s.holds(token)
	if err == nil && s.step != StepInstalled {
		err = fmt.Errorf("%w: nothing is installed yet", ErrInvalid)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "systemctl", Args: []string{"poweroff"}})
	return err
}
