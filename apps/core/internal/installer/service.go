package installer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
	// ErrLocked: too many wrong take-over codes.
	ErrLocked = errors.New("too many wrong codes; wait a minute")
)

// The installer is one browser's at a time: the first to ask holds it (a
// token every change carries). Its hold ends when the page has been silent
// for claimIdle, or when someone types the take-over code shown on the
// machine's own console; a restart of the daemon is no longer the only way
// back in after a closed tab.
const (
	claimIdle     = 10 * time.Minute
	takeOverTries = 5
	takeOverLock  = time.Minute
)

// CodeFile is where the take-over code is kept for the console's greeting
// (usr/share/homelab/shell.bash); root's.
const CodeFile = "/run/homelab-installer/takeover-code"

// settleTries is how often the install looks for its new partitions, a
// second apart: UDisks can list a disk before it has read the new table.
var (
	settleTries = 15
	settleWait  = time.Second
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

	mu    sync.Mutex
	token string
	// seen is when the holder's page last called; code is the take-over
	// code, wrong counts the wrong tries and locked ends a lock-out.
	seen     time.Time
	code     string
	wrong    int
	locked   time.Time
	now      func() time.Time
	step     Step
	disk     string
	hostname string
	ipv4     hostcfg.IPv4
	phase    string
	lines    []string
	err      string
	// entry is the firmware's boot entry for the installed system (four hex
	// digits), empty when none was found.
	entry string
	// onShared says the install's boot loader is on an ESP that was there
	// before it, which another system may start from.
	onShared bool
}

// New returns the installer; disks is UDisks{} on the stick.
func New(r cmdrunner.Runner, disks Disks, log *slog.Logger) *Service {
	return &Service{r: r, disks: disks, log: log, step: StepIdle, code: newCode(), now: time.Now}
}

// newCode is six digits, easy to read off a screen and type on a phone.
func newCode() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no randomness: nothing here would be safe
	}
	return fmt.Sprintf("%06d", (uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]))%1000000)
}

// WriteCode leaves the take-over code where the console's greeting reads it.
func (s *Service) WriteCode(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(s.code+"\n"), 0o600)
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
	var ip *hostcfg.IPv4
	if s.ipv4.Mode != "" {
		cp := s.ipv4
		cp.DNS = slices.Clone(s.ipv4.DNS)
		ip = &cp
	}
	return InstallerState{
		Active: true, Claimed: s.token != "" && !s.lapsed(), Step: s.step, Phase: s.phase, Disk: s.disk, Hostname: s.hostname,
		Log: append([]string{}, s.lines...), Error: s.err, Disks: disks, MinBytes: MinBytes,
		Current:    s.lease(ctx),
		IPv4:       ip,
		Machine:    machine(),
		CanRestart: s.step == StepInstalled && s.entry != "",
		SharedESP:  s.step == StepInstalled && s.onShared,
	}, nil
}

// Where the machine's facts are read; variables for tests.
var (
	meminfo = "/proc/meminfo"
	efivars = "/sys/firmware/efi/efivars"
)

var memTotalRE = regexp.MustCompile(`(?m)^MemTotal:\s+(\d+) kB`)

func machine() Machine {
	var m Machine
	b, _ := os.ReadFile(meminfo)
	if g := memTotalRE.FindSubmatch(b); g != nil {
		kb, _ := strconv.ParseUint(string(g[1]), 10, 64)
		m.Memory = kb << 10
	}
	// Four attribute bytes, then 1 when Secure Boot is on.
	if v, err := os.ReadFile(filepath.Join(efivars, "SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b2c")); err == nil && len(v) >= 5 {
		m.SecureBoot = v[4] == 1
	}
	m.BootEntries = syscall.Access(efivars, 2 /* W_OK */) == nil
	return m
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
// A holder whose page went silent gives way to the next caller.
func (s *Service) Claim() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && !s.lapsed() {
		return "", ErrClaimed
	}
	return s.issue()
}

// lapsed: the holder's page has not called for claimIdle and no install is
// running. The caller holds s.mu.
func (s *Service) lapsed() bool {
	return s.step != StepInstalling && s.now().Sub(s.seen) > claimIdle
}

// issue makes a new token, which ends the old one. The caller holds s.mu.
func (s *Service) issue() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s.token, s.seen = hex.EncodeToString(b), s.now()
	return s.token, nil
}

// TakeOver gives the installer to whoever types the code on the machine's
// console, whoever held it. An install that is running goes on.
func (s *Service) TakeOver(code string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now().Before(s.locked) {
		return "", ErrLocked
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(s.code)) != 1 {
		if s.wrong++; s.wrong >= takeOverTries {
			s.wrong, s.locked = 0, s.now().Add(takeOverLock)
		}
		return "", fmt.Errorf("%w: that is not the code on the machine's screen", ErrInvalid)
	}
	s.wrong = 0
	s.log.Info("the installer was taken over with the console's code")
	return s.issue()
}

// Seen notes that the holder's page is still open, and reports whether
// token is the holder's.
func (s *Service) Seen(token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holds(token) == nil
}

// holds checks the token and counts the call as the holder's page being
// open. The caller holds s.mu.
func (s *Service) holds(token string) error {
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		return ErrClaimed
	}
	s.seen = s.now()
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
	existing := existingESP(d, erase, shared)
	if why, err := s.espHasRoom(ctx, existing); err != nil {
		return InstallPlan{}, err
	} else if why != "" {
		return InstallPlan{Reason: why, Rows: []InstallPlanRow{}}, nil
	}
	defs, done, err := definitionsFor(existing != "")
	if err != nil {
		return InstallPlan{}, err
	}
	defer done()
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
		Args: []string{"--definitions=" + defs, seedRandom, "--dry-run=yes", "--empty=" + empty, "--json=short", d.Path},
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
			case p.Size < espRoom:
				return nil, fmt.Errorf("%w: %s is smaller than 400 MB", ErrInvalid, name)
			}
			return &p, nil
		}
	}
	return nil, fmt.Errorf("%w: no partition %s", ErrInvalid, name)
}

// existingESP is the ESP an install uses as it is: another disk's (shared),
// or the target disk's own when the install goes into its free space. ""
// when the install makes a new one.
func existingESP(d InstallDisk, erase bool, shared *InstallPartition) string {
	switch {
	case shared != nil:
		return shared.Path
	case !erase:
		return partOf(d, typeESP)
	}
	return ""
}

// espFree is the free space on the file system mounted at dir, with what
// this system's own kernels take there (an install over an earlier one
// replaces them); a variable for tests.
var espFree = func(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail*uint64(st.Bsize) + ownKernels(dir), nil
}

// ownKernels is the size of this system's kernels on the ESP mounted at dir.
func ownKernels(dir string) (size uint64) {
	own, _ := filepath.Glob(filepath.Join(dir, "EFI", "Linux", "homelab_*.efi"))
	for _, f := range own {
		if fi, err := os.Stat(f); err == nil {
			size += uint64(fi.Size())
		}
	}
	return size
}

// espHasRoom says why the existing ESP dev cannot take the install's boot
// loader and kernels, "" when it can (or dev is "": the install makes its
// own). It mounts the ESP read-only at a place of its own, since a plan may
// be asked for while an install has the ESP mounted.
func (s *Service) espHasRoom(ctx context.Context, dev string) (string, error) {
	if dev == "" {
		return "", nil
	}
	dir, err := workDir("room-")
	if err != nil {
		return "", err
	}
	defer os.Remove(dir)
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "mount", Args: []string{"-o", "ro", dev, dir}}); err != nil {
		return "", fmt.Errorf("mount %s: %w", dev, err)
	}
	free, ferr := espFree(dir)
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "umount", Args: []string{dir}}); err != nil && ferr == nil {
		ferr = fmt.Errorf("umount %s: %w", dev, err)
	}
	if ferr != nil {
		return "", ferr
	}
	if free < espRoom {
		return fmt.Sprintf("The EFI partition %s has %d MB free; this system's kernels need 400 MB there.", filepath.Base(dev), free/mib), nil
	}
	return "", nil
}

// definitionsFor is the image's partition definitions, without the ESP's
// when the install uses an ESP that is already there (existingESP): repart
// matches partitions to definitions by type, and would try to grow that ESP
// to the definition's size. That copy is the caller's own folder, removed
// by done: a plan asked for while another plan or an install runs must not
// take its definitions away.
func definitionsFor(withoutESP bool) (dir string, done func(), err error) {
	if !withoutESP {
		return definitions, func() {}, nil
	}
	dir, err = workDir("definitions-")
	if err != nil {
		return "", nil, err
	}
	done = func() { _ = os.RemoveAll(dir) }
	entries, err := os.ReadDir(definitions)
	if err != nil {
		done()
		return "", nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "00-esp") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(definitions, e.Name()))
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644)
		}
		if err != nil {
			done()
			return "", nil, err
		}
	}
	return dir, done, nil
}

// workDir makes a folder of the caller's own next to the mount point.
func workDir(prefix string) (string, error) {
	base := filepath.Dir(mountDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		return "", err
	}
	// repart reads the definitions as root; the mode is for a plain reader.
	return dir, os.Chmod(dir, 0o755)
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

// seedRandom has systemd-repart make up partition UUIDs. Its default seed
// is the machine ID, so two disks installed on one machine would get the
// same ESP, /var and /nix partition UUIDs, and a system could not tell its
// own from the other's.
const seedRandom = "--seed=random"

var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Install starts the install onto disk (erased, or into
// its free space), then the ESP made the way image updates expect it.
func (s *Service) Install(ctx context.Context, token, disk string, erase bool, setup Setup) error {
	setup.Hostname = strings.ToLower(strings.TrimSpace(setup.Hostname))
	setup.Timezone = strings.TrimSpace(setup.Timezone)
	if !hostnameRE.MatchString(setup.Hostname) {
		return fmt.Errorf("%w: a host name is letters, digits and dashes, up to 63", ErrInvalid)
	}
	if setup.Timezone != "" && !hostcfg.KnownTimezone(zoneinfo, setup.Timezone) {
		return fmt.Errorf("%w: %s is not a known time zone", ErrInvalid, setup.Timezone)
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
	shared, err := s.sharedESP(ctx, d, setup.ESP)
	if err != nil {
		return err
	}
	// Only the installer's holder has a disk mounted to be looked at.
	s.mu.Lock()
	err = s.holds(token)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if why, err := s.espHasRoom(ctx, existingESP(d, erase, shared)); err != nil {
		return err
	} else if why != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, why)
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
	s.step, s.phase, s.disk, s.hostname, s.ipv4, s.lines, s.err, s.entry = StepInstalling, "partition", disk, setup.Hostname, setup.IPv4, nil, "", ""
	s.onShared = existingESP(d, erase, shared) != ""
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
	err = s.cmd(ctx, "systemd-repart", "--definitions="+defs, seedRandom, "--empty="+empty, "--dry-run=no", d.Path)
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
	if err != nil && !erase {
		// What it made in the free space would be in the next try's way.
		s.removeNew(ctx, d)
	}
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
// kernel on the disk's ESP, then the host name and a machine ID in the new
// /var.
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
	existing := existingESP(d, erase, shared)
	var fallback []byte
	if existing != "" {
		_ = s.withMount(ctx, existing, func(dir string) error {
			fallback, _ = os.ReadFile(filepath.Join(dir, "EFI", "BOOT", "BOOTX64.EFI"))
			return nil
		})
	}
	defs, done, err := definitionsFor(existing != "")
	if err != nil {
		return err
	}
	defer done()
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
	sharedPath := ""
	if shared != nil {
		sharedPath = shared.Path
	}
	esp, varPart, err := s.newPartitions(ctx, d.Name, sharedPath)
	if err != nil {
		return err
	}
	s.setPhase("boot")
	s.line("Installing the boot loader and kernel " + version)
	err = s.withMount(ctx, esp, func(dir string) error {
		// The machine-id token keeps bootctl from writing an entry token
		// into the read-only root. --graceful: firmware that takes no boot
		// entry (its variables full or read-only) does not fail the install;
		// the page then says how the system is started (bootFirst finds no
		// entry).
		if err := s.cmd(ctx, "bootctl", "--esp-path="+dir, "--entry-token=machine-id",
			"--make-entry-directory=no", "--variables=yes", "--graceful", "install"); err != nil {
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

// removeNew deletes the partitions a failed install into free space left
// on d: the ones that were not there before it started. The next try then
// finds the free region as it was.
func (s *Service) removeNew(ctx context.Context, before InstallDisk) {
	_ = s.unreserve(ctx, before.Path)
	_ = s.cmd(ctx, "udevadm", "settle")
	after, err := s.find(ctx, before.Name)
	if err != nil {
		s.line("Could not look for what the failed install left: " + err.Error())
		return
	}
	was := map[uint64]bool{}
	for _, p := range before.Partitions {
		was[p.Start] = true
	}
	for _, p := range after.Partitions {
		if was[p.Start] {
			continue
		}
		s.line("Removing " + p.Path + ", which the failed install made")
		if err := s.disks.DeletePartition(ctx, p.Path); err != nil {
			s.line("Could not remove " + p.Path + ": " + err.Error())
		}
	}
}

// Sweep deletes the temporary partitions an install holds free regions
// with (region.go), left on a disk when the installer stopped between
// making and deleting them. The installer runs it once at its start.
func (s *Service) Sweep(ctx context.Context) {
	disks, err := s.disks.List(ctx)
	if err != nil {
		s.log.Warn("could not list the disks to sweep", "error", err)
		return
	}
	for _, d := range disks {
		if d.InUse {
			continue
		}
		for _, p := range d.Partitions {
			if p.Label != reservedName && !strings.EqualFold(p.Type, reservedType) {
				continue
			}
			s.log.Info("removing temporary partitions an earlier install left", "disk", d.Path)
			if err := s.unreserve(ctx, d.Path); err != nil {
				s.log.Warn("could not remove them", "disk", d.Path, "error", err)
			}
			break
		}
	}
}

// newPartitions finds the ESP (sharedESP when the boot loader goes on
// another disk's) and the /var partition after partitioning. udev has
// settled by then, but UDisks reads the new table on its own time, so it
// looks a few times before it gives up.
func (s *Service) newPartitions(ctx context.Context, disk, sharedESP string) (esp, varPart string, err error) {
	for try := 1; ; try++ {
		after, err := s.find(ctx, disk)
		if err != nil {
			return "", "", err
		}
		esp, varPart = partOf(after, typeESP), partOf(after, typeVar)
		if sharedESP != "" {
			esp = sharedESP
		}
		if esp != "" && varPart != "" {
			return esp, varPart, nil
		}
		if try >= settleTries {
			return "", "", errors.New("the new partitions are not on the disk")
		}
		time.Sleep(settleWait)
	}
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
// one (another system's systemd-boot) in front of it. The entry is kept for
// Restart, also when the order could not be changed.
// blkid probes the partition itself (-p): its cache has no partition UUIDs.
// bootctl also adds a fallback entry (systemd-boot-fallback…), which is not
// the one to start.
func (s *Service) bootFirst(ctx context.Context, esp string) error {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "blkid", Args: []string{"-p", "-s", "PART_ENTRY_UUID", "-o", "value", esp}})
	if err != nil {
		return err
	}
	uuid := strings.TrimSpace(res.Stdout)
	if uuid == "" {
		return fmt.Errorf("no partition UUID for %s", esp)
	}
	res, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "efibootmgr"})
	if err != nil {
		return err
	}
	entry, order, ok := firstInOrder(res.Stdout, uuid)
	if !ok {
		return fmt.Errorf("no boot entry for %s", esp)
	}
	s.mu.Lock()
	s.entry = entry
	s.mu.Unlock()
	return s.cmd(ctx, "efibootmgr", "-q", "-o", order)
}

var (
	bootOrderRE = regexp.MustCompile(`(?m)^BootOrder: ([0-9A-Fa-f,]+)`)
	bootEntryRE = regexp.MustCompile(`(?m)^Boot([0-9A-Fa-f]{4})\*? .*HD\(\d+,GPT,([0-9a-fA-F-]+),.*\\EFI\\systemd\\systemd-boot[a-z0-9]+\.efi`)
)

// firstInOrder reads efibootmgr's listing and returns the systemd-boot
// entry on the ESP with partition UUID uuid, and the boot order with that
// entry first.
func firstInOrder(listing, uuid string) (entry, order string, ok bool) {
	var ours string
	for _, m := range bootEntryRE.FindAllStringSubmatch(listing, -1) {
		if strings.EqualFold(m[2], uuid) {
			ours = strings.ToUpper(m[1])
		}
	}
	if ours == "" {
		return "", "", false
	}
	list := []string{ours}
	if m := bootOrderRE.FindStringSubmatch(listing); m != nil {
		for _, n := range strings.Split(m[1], ",") {
			if n = strings.ToUpper(n); n != "" && n != ours {
				list = append(list, n)
			}
		}
	}
	return ours, strings.Join(list, ","), true
}

// finishESP puts the running kernel where image updates keep theirs
// (EFI/Linux, as homelab_VERSION.efi) in place of an earlier install's, makes the image the boot menu's
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
	// An earlier install's kernels on a shared ESP start nothing any more,
	// and espFree counted their room as this install's.
	old, _ := filepath.Glob(filepath.Join(linux, "homelab_*.efi"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
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

// MachineIDFile is the installed system's machine ID on its /var. The
// image's initrd binds it over /etc/machine-id, so every install has an ID
// of its own, whatever the firmware's UUID is.
const MachineIDFile = "/var/lib/homelab/machine-id"

// seedVar writes the Setup and a new machine ID into the new /var (mounted
// at dir), checks repart copied the core extension in, and makes the
// directories the install created root's and world-readable, as a fresh
// /var's are.
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
	if err := rootOwned(p, 0o644); err != nil {
		return err
	}
	id, err := machineID()
	if err != nil {
		return err
	}
	m := filepath.Join(dir, strings.TrimPrefix(MachineIDFile, "/var/"))
	// Read-only, so one left by an install that failed is removed first.
	if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(m, []byte(id+"\n"), 0o444); err != nil {
		return err
	}
	return rootOwned(m, 0o444)
}

// machineID is a random machine ID as machine-id(5) wants it: 32 lowercase
// hex digits, a version 4 UUID without its dashes.
func machineID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b), nil
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

// Restart starts the installed system: the firmware's next boot is set to
// its entry, whatever the boot order says, and the machine restarts. The
// installer medium can stay in.
func (s *Service) Restart(ctx context.Context, token string) error {
	s.mu.Lock()
	err := s.holds(token)
	entry := s.entry
	switch {
	case err != nil:
	case s.step != StepInstalled:
		err = fmt.Errorf("%w: nothing is installed yet", ErrInvalid)
	case entry == "":
		err = fmt.Errorf("%w: the firmware has no boot entry for the installed system; turn the machine off and pick the disk in the firmware's boot menu", ErrInvalid)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "efibootmgr", Args: []string{"-q", "-n", entry}}); err != nil {
		return fmt.Errorf("setting the next boot to entry %s: %w", entry, err)
	}
	_, err = s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "systemctl", Args: []string{"reboot"}})
	return err
}
