package bcachefs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// What these commands do in bcachefs-tools 1.39 is written up in
// docs/solutions/integration-issues/bcachefs-pool-commands.md.

// MinTools is the oldest bcachefs-tools the pool commands are written for:
// evacuate and the Leaving column of `fs usage` are different before it.
var MinTools = [2]int{1, 39}

var versionRE = regexp.MustCompile(`v?(\d+)\.(\d+)`)

// ToolsVersion is the major and minor version of the bcachefs tool.
func (c *Client) ToolsVersion(ctx context.Context) (major, minor int, err error) {
	out, err := c.run(ctx, "bcachefs", "version")
	if err != nil {
		return 0, 0, err
	}
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, fmt.Errorf("bcachefs version: cannot read %q", strings.TrimSpace(out))
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, nil
}

// Disk kinds: what a disk does in a pool with both.
const (
	KindSSD = "ssd"
	KindHDD = "hdd"
)

// Kind is ssd or hdd for a disk lsblk reports as (not) rotational.
func Kind(rotational bool) string {
	if rotational {
		return KindHDD
	}
	return KindSSD
}

// Candidate is a whole disk a pool can be made from or grown with: nothing on
// it is mounted and the system does not run from it.
type Candidate struct {
	Disk
	// Contents says what the disk holds, for a person; empty for a blank one.
	Contents string
	// Parts are the paths of the partitions and other devices on it.
	Parts []string
	// PoolUUID is the pool an old member's superblock names.
	PoolUUID string
}

type lsblkCandidate struct {
	Name        string           `json:"name"`
	Path        string           `json:"path"`
	Size        any              `json:"size"`
	Type        string           `json:"type"`
	Model       string           `json:"model"`
	Serial      string           `json:"serial"`
	FSType      string           `json:"fstype"`
	UUID        string           `json:"uuid"`
	Label       string           `json:"label"`
	Rota        bool             `json:"rota"`
	RO          bool             `json:"ro"`
	Mountpoint  string           `json:"mountpoint"`
	Mountpoints []string         `json:"mountpoints"`
	Children    []lsblkCandidate `json:"children"`
}

func lsblkSize(v any) uint64 {
	var size uint64
	switch v := v.(type) {
	case float64:
		size = uint64(v)
	case string:
		fmt.Sscanf(v, "%d", &size)
	}
	return size
}

// Candidates lists the disks that can go into a pool. A disk is left out when
// anything on it is mounted or is swap, when something is stacked on one of
// its partitions (LUKS, LVM, RAID), when it is read-only, and when one of the
// system's own mounts is on it.
func (c *Client) Candidates(ctx context.Context) ([]Candidate, error) {
	out, err := c.run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,MODEL,SERIAL,FSTYPE,UUID,LABEL,ROTA,RO,MOUNTPOINTS")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Blockdevices []lsblkCandidate `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse lsblk output: %w", err)
	}
	system := c.SystemDisks()
	pools := c.poolSources()
	// mounted: something on d is mounted. A disk that left a mounted pool is
	// not: the kernel keeps the device number of the pool's first disk for
	// the mount, so lsblk goes on listing the pool's mountpoint on it.
	mounted := func(d lsblkCandidate) bool {
		for _, m := range append([]string{d.Mountpoint}, d.Mountpoints...) {
			if m == "" {
				continue
			}
			if members, ok := pools[m]; ok && !slices.Contains(members, d.Path) {
				continue
			}
			return true
		}
		return false
	}
	var list []Candidate
	for _, d := range parsed.Blockdevices {
		if d.Type != "disk" || d.RO || system[d.Name] || lsblkSize(d.Size) == 0 {
			continue
		}
		busy := false
		var parts []string
		var walk func(devs []lsblkCandidate, depth int)
		walk = func(devs []lsblkCandidate, depth int) {
			for _, ch := range devs {
				// Anything but a partition directly on the disk is a holder.
				if ch.Type != "part" || depth > 0 {
					busy = true
				}
				if mounted(ch) {
					busy = true
				}
				parts = append(parts, ch.Path)
				walk(ch.Children, depth+1)
			}
		}
		busy = mounted(d)
		walk(d.Children, 0)
		if busy {
			continue
		}
		model := strings.TrimSpace(d.Model)
		friendly := model
		if hasSMART(d.Type, d.Name) {
			friendly = c.cachedFriendlyName(ctx, d.Path, model, lsblkSize(d.Size))
		}
		cand := Candidate{
			Disk: Disk{
				Name: d.Name, Path: d.Path, Size: lsblkSize(d.Size), Type: d.Type, Model: model,
				Serial: strings.TrimSpace(d.Serial), FSType: d.FSType, Label: d.Label,
				HasChildren: len(d.Children) > 0, FriendlyName: friendly, Rotational: d.Rota,
			},
			Parts: parts,
		}
		switch {
		case len(d.Children) == 1:
			cand.Contents = "1 partition"
		case len(d.Children) > 1:
			cand.Contents = fmt.Sprintf("%d partitions", len(d.Children))
		case d.FSType == "bcachefs":
			cand.Contents, cand.PoolUUID = named("bcachefs", d.Label), d.UUID
		case d.FSType != "":
			cand.Contents = named(d.FSType, d.Label)
		default:
			// A member whose first superblock is gone still answers from a
			// backup, and lsblk does not see it.
			if out, err := c.showSuper(ctx, d.Path); err == nil {
				if s := parseSuper(out); s.UUID != "" {
					cand.Contents, cand.PoolUUID = named("bcachefs", s.Label), s.UUID
				}
			}
		}
		list = append(list, cand)
	}
	return list, nil
}

// poolSources is, for each mounted bcachefs pool whose mount names its
// member devices (/dev/sdb:/dev/sdc), those devices by mountpoint.
func (c *Client) poolSources() map[string][]string {
	out := map[string][]string{}
	data, err := os.ReadFile(c.mountinfo)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		// 552 61 254:64 / /var/mnt/pool rw shared:9 - bcachefs /dev/sdb:/dev/sdc rw
		before, after, ok := strings.Cut(line, " - ")
		head, tail := strings.Fields(before), strings.Fields(after)
		if !ok || len(head) < 5 || len(tail) < 2 || tail[0] != "bcachefs" {
			continue
		}
		members := strings.Split(tail[1], ":")
		if slices.ContainsFunc(members, func(m string) bool { return !strings.HasPrefix(m, "/dev/") || strings.HasPrefix(m, "/dev/disk/") }) {
			continue
		}
		out[head[4]] = members
	}
	return out
}

func named(fstype, label string) string {
	if label == "" {
		return fstype
	}
	return fmt.Sprintf("%s %q", fstype, label)
}

// systemMounts are the mounts the system runs from.
var systemMounts = map[string]bool{"/": true, "/usr": true, "/var": true, "/boot": true, "/efi": true, "/nix": true}

// SystemDisks names the whole disks (sda, nvme0n1) under the system's own
// mounts, following device-mapper and RAID layers down to the disk.
func (c *Client) SystemDisks() map[string]bool {
	disks := map[string]bool{}
	f, err := os.Open(c.mountinfo)
	if err != nil {
		return disks
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime ...
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || !systemMounts[fields[4]] {
			continue
		}
		dir, err := filepath.EvalSymlinks(filepath.Join(c.sysDevBlock, fields[2]))
		if err != nil {
			continue
		}
		c.wholeDisks(dir, disks, 0)
	}
	return disks
}

// wholeDisks adds the disks under the block device whose sysfs folder is dir.
func (c *Client) wholeDisks(dir string, into map[string]bool, depth int) {
	if depth > 8 {
		return
	}
	slaves, _ := os.ReadDir(filepath.Join(dir, "slaves"))
	if len(slaves) > 0 {
		for _, s := range slaves {
			if sub, err := filepath.EvalSymlinks(filepath.Join(dir, "slaves", s.Name())); err == nil {
				c.wholeDisks(sub, into, depth+1)
			}
		}
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		into[filepath.Base(filepath.Dir(dir))] = true
		return
	}
	into[filepath.Base(dir)] = true
}

// FormatDevice is one disk of a new pool, with its label (ssd.ssd1).
type FormatDevice struct {
	Path  string
	Label string
}

// FormatSpec is a new pool.
type FormatSpec struct {
	Name    string
	Copies  int
	Devices []FormatDevice
	// Tiered sets the targets: the ssd group takes new writes, metadata and
	// cached reads, and data moves to the hdd group.
	Tiered bool
}

// TargetOptions are the four targets of a pool with both kinds of disk.
var TargetOptions = [][2]string{
	{"foreground_target", KindSSD}, {"promote_target", KindSSD},
	{"metadata_target", KindSSD}, {"background_target", KindHDD},
}

// formatArgs is the format command. A --label holds for every device after
// it, so each device carries its own.
func formatArgs(spec FormatSpec) []string {
	args := []string{
		"format", "-q", "--fs_label=" + spec.Name,
		"--replicas=" + strconv.Itoa(spec.Copies), "--compression=lz4",
	}
	for _, d := range spec.Devices {
		args = append(args, "--label="+d.Label, d.Path)
	}
	if spec.Tiered {
		for _, t := range TargetOptions {
			args = append(args, "--"+t[0]+"="+t[1])
		}
	}
	return args
}

// Format makes a pool. There is no -f: the tool asks before it writes over a
// file system and fails with stdin closed, so wipe a used disk first.
func (c *Client) Format(ctx context.Context, spec FormatSpec) error {
	_, err := c.run(ctx, "bcachefs", formatArgs(spec)...)
	return err
}

// AddDevice adds a blank disk to the mounted pool. Like Format it refuses a
// disk that holds something.
func (c *Client) AddDevice(ctx context.Context, mountpoint, device, label string) error {
	_, err := c.run(ctx, "bcachefs", "device", "add", "--label="+label, mountpoint, device)
	return err
}

// SetTargets makes the mounted pool that member is in a tiered one.
func (c *Client) SetTargets(ctx context.Context, member string) error {
	for _, t := range TargetOptions {
		if _, err := c.run(ctx, "bcachefs", "set-fs-option", "--"+t[0]+"="+t[1], member); err != nil {
			return err
		}
	}
	return nil
}

// Evacuate moves a member's data to the pool's other disks and returns when
// none is left. The kernel does the moving and goes on when ctx ends; the
// tool only waits. It never fails for want of room: watch DeviceUsage.
func (c *Client) Evacuate(ctx context.Context, device string) error {
	_, err := c.run(ctx, "bcachefs", "device", "evacuate", device)
	return err
}

// RemoveDevice takes an evacuated member out of its pool. The disk keeps its
// own superblock and still reads as a member until it is wiped.
func (c *Client) RemoveDevice(ctx context.Context, device string) error {
	_, err := c.run(ctx, "bcachefs", "device", "remove", device)
	return err
}

// SetDeviceState sets a member rw, ro, evacuating or spare. rw stops an
// evacuation.
func (c *Client) SetDeviceState(ctx context.Context, device, state string) error {
	_, err := c.run(ctx, "bcachefs", "device", "set-state", state, device)
	return err
}

// DeviceUsage is one member's row of `bcachefs fs usage`, in bytes.
type DeviceUsage struct {
	Index   int
	Label   string
	Name    string // vdb, sda1
	State   string // rw, ro, evacuating, ...
	Size    uint64
	Used    uint64
	Leaving uint64 // data still to move off it
}

var deviceRowRE = regexp.MustCompile(`^(.*) \(device (\d+)\):\s+(\S+)\s+(\S+)\s+(\d+)\s+(\d+)\s+\d+%(?:\s+(\d+))?\s*$`)

// DeviceUsage reads how full each member of the mounted pool is.
func (c *Client) DeviceUsage(ctx context.Context, mountpoint string) ([]DeviceUsage, error) {
	out, err := c.run(ctx, "bcachefs", "fs", "usage", mountpoint)
	if err != nil {
		return nil, err
	}
	rows := parseDeviceUsage(out)
	if len(rows) == 0 {
		return nil, fmt.Errorf("bcachefs fs usage %s: no device rows", mountpoint)
	}
	return rows, nil
}

func parseDeviceUsage(out string) []DeviceUsage {
	var rows []DeviceUsage
	for _, line := range strings.Split(out, "\n") {
		m := deviceRowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		u := DeviceUsage{Label: m[1], Name: m[3], State: m[4]}
		if u.Label == "(no label)" {
			u.Label = ""
		}
		u.Index, _ = strconv.Atoi(m[2])
		u.Size, _ = strconv.ParseUint(m[5], 10, 64)
		u.Used, _ = strconv.ParseUint(m[6], 10, 64)
		if m[7] != "" {
			u.Leaving, _ = strconv.ParseUint(m[7], 10, 64)
		}
		rows = append(rows, u)
	}
	return rows
}

// wipeHead is how much of a device Wipe zeroes. A member's second superblock
// starts 1 MiB in and is up to 1 MiB long; wipefs does not know it.
const wipeHead = 4 << 20

// ErrDeviceBusy is a device something else holds open: mounted, or in use.
var ErrDeviceBusy = errors.New("the disk is in use")

// Wipe leaves disk reading as blank: no file system, no partitions and no
// bcachefs superblock. parts are the devices on it.
func (c *Client) Wipe(ctx context.Context, disk string, parts []string) error {
	log.Info("wiping disk", "disk", disk, "parts", parts)
	for _, dev := range append(append([]string{}, parts...), disk) {
		if err := c.zeroHead(dev, wipeHead); err != nil {
			return fmt.Errorf("wipe %s: %w", dev, err)
		}
		if _, err := c.run(ctx, "wipefs", "-a", "-q", dev); err != nil {
			return err
		}
	}
	// A disk that had no table answers this with an error.
	if _, err := c.run(ctx, "blockdev", "--rereadpt", disk); err != nil {
		log.Debug("blockdev --rereadpt failed", "disk", disk, "error", err)
	}
	if _, err := c.run(ctx, "udevadm", "settle", "--timeout=10"); err != nil {
		log.Debug("udevadm settle failed", "error", err)
	}
	return nil
}

// StubZero replaces the write to the device, for tests of code that wipes.
func (c *Client) StubZero(fn func(path string, n int) error) { c.zeroHead = fn }

// zeroHead writes n zero bytes at the start of a block device, opened
// exclusively: the kernel refuses that for a device that is mounted or held.
func zeroHead(path string, n int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_EXCL, 0)
	if err != nil {
		if errors.Is(err, syscall.EBUSY) {
			return ErrDeviceBusy
		}
		return err
	}
	if _, err := f.Write(make([]byte, n)); err != nil && !errors.Is(err, syscall.ENOSPC) {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
