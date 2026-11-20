package bcachefs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"homelab-ngx.com/core/internal/cmdrunner"
	logging "homelab-ngx.com/core/internal/logging"
)

var log = logging.Logger("storage")

// Client runs the bcachefs/lsblk/mount tooling through a cmdrunner.Runner.
// Everything that touches /proc is a field so tests can point it elsewhere.
type Client struct {
	r cmdrunner.Runner

	procMounts string
	diskstats  string
	sysfs      string // /sys/fs/bcachefs: one folder per mounted filesystem
	statfs     func(path string, st *syscall.Statfs_t) error
	// For Candidates and Wipe.
	mountinfo   string // /proc/self/mountinfo
	sysDevBlock string // /sys/dev/block: one link per device number
	zeroHead    func(path string, n int) error

	friendlyMu    sync.Mutex
	friendlyNames map[string]string
}

// New returns a Client that executes commands through r.
func New(r cmdrunner.Runner) *Client {
	return &Client{
		r: r, procMounts: "/proc/mounts", diskstats: "/proc/diskstats", sysfs: "/sys/fs/bcachefs",
		statfs:    syscall.Statfs,
		mountinfo: "/proc/self/mountinfo", sysDevBlock: "/sys/dev/block", zeroHead: zeroHead,
		friendlyNames: make(map[string]string),
	}
}

type DiskIOStats struct {
	DeviceName   string `json:"device_name"`
	ReadIOs      uint64 `json:"read_ios"`       // completed reads
	WriteIOs     uint64 `json:"write_ios"`      // completed writes
	ReadSectors  uint64 `json:"read_sectors"`   // sectors read
	WriteSectors uint64 `json:"write_sectors"`  // sectors written
	IOInProgress uint64 `json:"io_in_progress"` // currently in-flight I/Os
}

type PoolUsage struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

type Disk struct {
	Name           string       `json:"name"`
	Path           string       `json:"path"`
	Size           uint64       `json:"size"`
	Type           string       `json:"type"`
	Model          string       `json:"model"`
	Serial         string       `json:"serial"`
	FSType         string       `json:"fstype"`
	Mountpoint     string       `json:"mountpoint"`
	Label          string       `json:"label"`
	HasChildren    bool         `json:"has_children"`
	BcachefsLabel  string       `json:"bcachefs_label"`  // NEW — e.g. "ssd.ssd1", "hdd.hdd1"
	DataTarget     string       `json:"data_target"`     // NEW — "foreground", "background", or ""
	MetadataTarget string       `json:"metadata_target"` // NEW — "foreground", "background", or ""
	IO             *DiskIOStats `json:"io,omitempty"`
	FriendlyName   string       `json:"friendly_name"` // NEW — friendly display name via smartctl
	Rotational     bool         `json:"rotational"`    // lsblk's ROTA: a disk that spins
	State          string       `json:"state"`         // a pool member's state: rw, ro, evacuating, ...
}

type Pool struct {
	UUID             string     `json:"uuid"`
	Name             string     `json:"name"`  // NEW — user-set or label fallback
	State            string     `json:"state"` // mounted, unmounted
	Mountdir         string     `json:"mountdir"`
	Label            string     `json:"label"`             // NEW — from bcachefs show-super
	DataReplicas     int        `json:"data_replicas"`     // NEW — e.g. 1
	MetadataReplicas int        `json:"metadata_replicas"` // NEW — e.g. 2
	ForegroundTarget string     `json:"foreground_target"` // disk group new writes land on, "" = none
	BackgroundTarget string     `json:"background_target"` // disk group data is moved to, "" = none
	PromoteTarget    string     `json:"promote_target"`    // disk group reads are cached on, "" = none
	MetadataTarget   string     `json:"metadata_target"`   // disk group metadata is kept on, "" = none
	Disks            []Disk     `json:"disks"`
	Usage            *PoolUsage `json:"usage,omitempty"` // NEW — only when mounted
}

type Subvolume struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	UsedBytes int64  `json:"used_bytes"` // NEW — -1 if not yet calculated
}

// run runs a command as root (the daemon already is) and returns its stdout.
func (c *Client) run(ctx context.Context, name string, args ...string) (string, error) {
	res, err := c.r.Run(ctx, cmdrunner.Cmd{Module: "storage", Name: name, Args: args})
	return res.Stdout, err
}

// showSuper probes a device for a bcachefs superblock. Exit 1 is the normal
// answer for a device that has none.
func (c *Client) showSuper(ctx context.Context, path string) (string, error) {
	res, err := c.r.Run(ctx, cmdrunner.Cmd{Module: "storage", Name: "bcachefs", Args: []string{"show-super", path}, ExpectExit: []int{1}})
	return res.Stdout, err
}

// lsblkOutput represents the JSON output from lsblk.
type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Size        interface{}   `json:"size"` // Sometimes a string or int depending on lsblk version, we requested bytes (-b) so it should be int or string representation of int
	Type        string        `json:"type"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	FSType      string        `json:"fstype"`
	Mountpoint  string        `json:"mountpoint"`
	Mountpoints []string      `json:"mountpoints"` // Newer lsblk uses an array
	Label       string        `json:"label"`
	Rota        bool          `json:"rota"`
	Children    []lsblkDevice `json:"children"`
}

// GetDisks returns all block devices using lsblk.
func (c *Client) GetDisks(ctx context.Context) ([]Disk, error) {
	out, err := c.run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,MODEL,SERIAL,FSTYPE,MOUNTPOINT,LABEL,ROTA")
	if err != nil {
		return nil, err
	}

	var output lsblkOutput
	if err := json.Unmarshal([]byte(out), &output); err != nil {
		return nil, fmt.Errorf("failed to parse lsblk output: %w", err)
	}

	var disks []Disk
	var extractDisks func([]lsblkDevice)
	extractDisks = func(devices []lsblkDevice) {
		for _, dev := range devices {
			// Convert size
			var size uint64
			switch v := dev.Size.(type) {
			case float64:
				size = uint64(v)
			case string:
				fmt.Sscanf(v, "%d", &size)
			}

			mountpoint := dev.Mountpoint
			if mountpoint == "" && len(dev.Mountpoints) > 0 {
				mountpoint = dev.Mountpoints[0]
			}

			friendly := strings.TrimSpace(dev.Model)
			if hasSMART(dev.Type, dev.Name) {
				friendly = c.cachedFriendlyName(ctx, dev.Path, friendly, size)
			}
			disks = append(disks, Disk{
				Name:         dev.Name,
				Path:         dev.Path,
				Size:         size,
				Type:         dev.Type,
				Model:        strings.TrimSpace(dev.Model),
				Serial:       strings.TrimSpace(dev.Serial),
				FSType:       dev.FSType,
				Mountpoint:   mountpoint,
				Label:        dev.Label,
				HasChildren:  len(dev.Children) > 0,
				FriendlyName: friendly,
				Rotational:   dev.Rota,
			})
			if len(dev.Children) > 0 {
				extractDisks(dev.Children)
			}
		}
	}
	extractDisks(output.Blockdevices)
	return disks, nil
}

// findBcachefsMount returns the bcachefs mountpoint of a pool, reading
// /proc/mounts authoritatively. bcachefs mounts a multi-device pool via a
// single /dev/disk/by-uuid/<UUID> source line, not per-member device paths,
// so we match on the pool's External UUID rather than a disk path. Returns
// "" if the pool isn't mounted anywhere.
func (c *Client) findBcachefsMount(uuid string) string {
	data, err := os.ReadFile(c.procMounts)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[2] == "bcachefs" && strings.Contains(fields[0], uuid) {
			return fields[1]
		}
	}
	return ""
}

// DiscoverPools finds bcachefs pools by grouping disks with fstype="bcachefs".
// We group them by their UUID using `bcachefs show-super`.
func (c *Client) DiscoverPools(ctx context.Context) ([]Pool, error) {
	log.Debug("Discovering bcachefs pools...")
	disks, err := c.GetDisks(ctx)
	if err != nil {
		log.Error("failed to get disks during discovery", "error", err)
		return nil, err
	}

	// Group bcachefs disks by UUID.
	// show-super for any disk in a pool returns info for all members, so we
	// cache the parsed result and reuse it for sibling disks in the same pool.
	poolMap := make(map[string]*Pool)
	// Cache the parsed superblock after the first show-super call.
	// A single show-super returns device info for all members of the array
	// (all 4 disks share one External UUID), so sibling disks can skip the
	// CLI invocation entirely.
	var super ParsedSuper
	var superValid bool

	for _, d := range disks {
		// Sometimes lsblk doesn't identify the FSType of an offline bcachefs device.
		// We should probe it if it's explicitly bcachefs OR if it has no fstype/mountpoint.
		if d.FSType != "bcachefs" && d.FSType != "" {
			continue
		}
		// Optical drives, and disks carrying a partition table (their
		// partitions are listed and probed on their own), can't be members.
		if d.Type == "rom" || d.HasChildren {
			continue
		}

		if superValid {
			// Only reuse the cache if this device is actually listed in the
			// parsed superblock's device list. Non-bcachefs devices (NVMe,
			// CD-ROM) pass the empty-fstype filter but aren't pool members.
			found := false
			for _, dev := range super.Devices {
				if d.Path == dev.Path || strings.HasSuffix(d.Path, dev.Path) || (dev.Path != "" && strings.HasSuffix(dev.Path, d.Name)) {
					found = true
					break
				}
			}
			if !found {
				log.Debug("device not in cached superblock, running fresh show-super", "device", d.Path)
				superValid = false
			}
		}
		if !superValid {
			out, err := c.showSuper(ctx, d.Path)
			if err != nil {
				log.Debug("bcachefs show-super failed (likely not a bcachefs device)", "device", d.Path, "error", err)
				continue
			}
			super = parseSuper(out)
			if super.UUID == "" {
				log.Warn("Could not extract External UUID from output", "device", d.Path)
				continue
			}
			superValid = true
		}

		log.Debug("found bcachefs device", "device", d.Path, "uuid", super.UUID, "mountpoint", d.Mountpoint)

		p, exists := poolMap[super.UUID]
		if !exists {
			p = &Pool{
				UUID:             super.UUID,
				State:            "unmounted",
				Label:            super.Label,
				DataReplicas:     super.DataReplicas,
				MetadataReplicas: super.MetadataReplicas,
				ForegroundTarget: super.ForegroundTarget,
				BackgroundTarget: super.BackgroundTarget,
				PromoteTarget:    super.PromoteTarget,
				MetadataTarget:   super.MetadataTarget,
			}
			poolMap[super.UUID] = p
		}

		for _, dev := range super.Devices {
			if d.Path == dev.Path || (dev.Path != "" && strings.HasSuffix(d.Path, dev.Path)) || (d.Name != "" && strings.HasSuffix(dev.Path, d.Name)) {
				d.BcachefsLabel = dev.Label
				d.State = dev.State
				d.DataTarget, d.MetadataTarget = determineDiskTargets(dev.Label, super.ForegroundTarget, super.MetadataTarget, super.BackgroundTarget)
				break
			}
		}

		// Prefer the bcachefs mountpoint from /proc/mounts. lsblk's
		// `mountpoint` field is alphabetical and can return a bind or autofs
		// overlay instead of the real mount. Fall back to whatever lsblk
		// reported if /proc/mounts has nothing.
		if canon := c.findBcachefsMount(super.UUID); canon != "" {
			p.State = "mounted"
			p.Mountdir = canon
		} else if d.Mountpoint != "" {
			p.State = "mounted"
			p.Mountdir = d.Mountpoint
		}

		p.Disks = append(p.Disks, d)
	}

	var pools []Pool
	for _, p := range poolMap {
		pools = append(pools, *p)
	}
	return pools, nil
}

type ParsedSuper struct {
	UUID             string
	Label            string
	DataReplicas     int
	MetadataReplicas int
	ForegroundTarget string
	MetadataTarget   string
	BackgroundTarget string
	PromoteTarget    string
	Devices          []ParsedDevice
}

type ParsedDevice struct {
	Index string
	Path  string
	Model string
	Label string
	State string // rw, ro, evacuating, spare, ...
}

func parseSuper(output string) ParsedSuper {
	var super ParsedSuper
	lines := strings.Split(output, "\n")

	super.ForegroundTarget = ""
	super.MetadataTarget = ""
	super.BackgroundTarget = ""

	var currentDevice *ParsedDevice

	for _, line := range lines {
		if strings.Contains(line, "Device ") && strings.Contains(line, ":") && !strings.Contains(line, "Device index:") {
			idx := strings.Index(line, "Device ")
			colonIdx := strings.Index(line[idx:], ":")
			if colonIdx != -1 {
				colonIdx += idx
				devNum := strings.TrimSpace(line[idx+7 : colonIdx])

				rest := line[colonIdx+1:]
				fields := strings.Fields(rest)
				path := ""
				model := ""
				if len(fields) > 0 {
					path = fields[0]
				}
				if len(fields) > 1 {
					model = fields[1]
				}

				var dev ParsedDevice
				dev.Index = devNum
				dev.Path = path
				dev.Model = model
				super.Devices = append(super.Devices, dev)
				currentDevice = &super.Devices[len(super.Devices)-1]
			}
		}

		if currentDevice != nil {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "State:"); ok {
				if fields := strings.Fields(rest); len(fields) > 0 {
					currentDevice.State = fields[0]
				}
			}
			if strings.Contains(line, "Label:") {
				parts := strings.SplitN(line, "Label:", 2)
				if len(parts) == 2 {
					lbl := strings.TrimSpace(parts[1])
					fields := strings.Fields(lbl)
					if len(fields) > 0 {
						currentDevice.Label = fields[0]
						if currentDevice.Label == "(none)" {
							currentDevice.Label = ""
						}
					}
				}
			}
		} else {
			if strings.Contains(line, "External UUID:") {
				parts := strings.SplitN(line, "External UUID:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						super.UUID = fields[0]
					}
				}
			}
			if strings.Contains(line, "Label:") && !strings.Contains(line, "Device ") {
				parts := strings.SplitN(line, "Label:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						super.Label = fields[0]
						if super.Label == "(none)" {
							super.Label = ""
						}
					}
				}
			}
			if strings.Contains(line, "data_replicas:") {
				parts := strings.SplitN(line, "data_replicas:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						fmt.Sscanf(fields[0], "%d", &super.DataReplicas)
					}
				}
			}
			if strings.Contains(line, "metadata_replicas:") {
				parts := strings.SplitN(line, "metadata_replicas:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						fmt.Sscanf(fields[0], "%d", &super.MetadataReplicas)
					}
				}
			}
			if strings.Contains(line, "foreground_target:") {
				parts := strings.SplitN(line, "foreground_target:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						super.ForegroundTarget = fields[0]
					}
				}
			}
			if strings.Contains(line, "metadata_target:") {
				parts := strings.SplitN(line, "metadata_target:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						super.MetadataTarget = fields[0]
					}
				}
			}
			if strings.Contains(line, "background_target:") {
				parts := strings.SplitN(line, "background_target:", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					fields := strings.Fields(val)
					if len(fields) > 0 {
						super.BackgroundTarget = fields[0]
					}
				}
			}
			if strings.Contains(line, "promote_target:") {
				parts := strings.SplitN(line, "promote_target:", 2)
				if len(parts) == 2 {
					fields := strings.Fields(parts[1])
					if len(fields) > 0 {
						super.PromoteTarget = fields[0]
					}
				}
			}
		}
	}
	// show-super prints "none" for an unset target; normalise to "".
	for _, t := range []*string{&super.ForegroundTarget, &super.MetadataTarget, &super.BackgroundTarget, &super.PromoteTarget} {
		if *t == "none" {
			*t = ""
		}
	}
	return super
}

func determineDiskTargets(diskLabel, fgTarget, metaTarget, bgTarget string) (string, string) {
	dataTarget := ""
	metadataTarget := ""

	if diskLabel != "" {
		group := diskLabel
		if idx := strings.Index(diskLabel, "."); idx != -1 {
			group = diskLabel[:idx]
		}

		// Report only what the superblock actually sets. An ssd→hdd guess used
		// to label the HDDs "background" even with background_target unset,
		// hiding that nothing was ever migrated off the SSD.
		if fgTarget != "" && group == fgTarget {
			dataTarget = "foreground"
		} else if bgTarget != "" && group == bgTarget {
			dataTarget = "background"
		}

		if metaTarget != "" && group == metaTarget {
			metadataTarget = "foreground"
		}
	}

	return dataTarget, metadataTarget
}

func extractField(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, prefix) {
			parts := strings.SplitN(line, prefix, 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				if idx := strings.Index(val, "\x1b"); idx != -1 {
					val = val[:idx]
				}
				return strings.TrimSpace(val)
			}
		}
	}
	return ""
}

// GetUnassignedDisks returns disks that are not part of any filesystem and not partitions of mounted disks.
func (c *Client) GetUnassignedDisks(ctx context.Context) ([]Disk, error) {
	log.Debug("Scanning for unassigned disks...")
	disks, err := c.GetDisks(ctx)
	if err != nil {
		return nil, err
	}

	var unassigned []Disk
	for _, d := range disks {
		if d.Type != "disk" {
			continue
		}
		if d.HasChildren {
			continue
		}
		if d.FSType != "" || d.Mountpoint != "" {
			continue
		}
		out, err := c.showSuper(ctx, d.Path)
		if err == nil && extractField(out, "External UUID:") != "" {
			log.Debug("disk belongs to bcachefs pool, not unassigned", "device", d.Path)
			continue
		} else if err != nil {
			log.Debug("unassigned disk check bcachefs probe failed (expected for empty disks)", "device", d.Path, "error", err)
		}

		log.Debug("found unassigned disk", "device", d.Path, "size", d.Size)
		unassigned = append(unassigned, d)
	}
	log.Debug("unassigned disks scan complete", "count", len(unassigned))
	return unassigned, nil
}

// Mount mounts a bcachefs pool by passing all its devices to the mount command.
func (c *Client) Mount(ctx context.Context, devices []string, mountpoint string) error {
	log.Info("Mounting bcachefs pool", "devices", devices, "mountpoint", mountpoint)
	if len(devices) == 0 {
		log.Error("mount failed", "reason", "no devices provided")
		return errors.New("no devices provided")
	}
	devStr := strings.Join(devices, ":")
	_, err := c.run(ctx, "mount", "-t", "bcachefs", devStr, mountpoint)
	if err != nil {
		log.Error("mount failed", "error", err)
	} else {
		log.Info("mount successful", "mountpoint", mountpoint)
	}
	return err
}

// Unmount unmounts a bcachefs pool.
func (c *Client) Unmount(ctx context.Context, mountpoint string) error {
	log.Info("Unmounting bcachefs pool", "mountpoint", mountpoint)
	_, err := c.run(ctx, "umount", mountpoint)
	if err != nil {
		log.Error("unmount failed", "error", err)
	} else {
		log.Info("unmount successful", "mountpoint", mountpoint)
	}
	return err
}

// ListSubvolumes lists subvolumes for a given mountpoint.
// bcachefs subvolume list output format:
//
//	Path                     ID       Created          Flags        Size
//	test2                    2        2026-05-27 21:46 -
//
// Paths are relative to the mountpoint; we prepend it to return absolute paths.
func (c *Client) ListSubvolumes(ctx context.Context, mountpoint string) ([]Subvolume, error) {
	log.Debug("Listing bcachefs subvolumes", "mountpoint", mountpoint)
	out, err := c.run(ctx, "bcachefs", "subvolume", "list", mountpoint)
	if err != nil {
		log.Error("failed to list subvolumes", "error", err)
		return nil, err
	}

	var subvolumes []Subvolume
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Subvolumes") || strings.HasPrefix(line, "Path") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		relPath := parts[0]
		absPath := filepath.Join(mountpoint, relPath)
		subvolumes = append(subvolumes, Subvolume{
			ID:        parts[1],
			Path:      absPath,
			UsedBytes: -1,
		})
	}
	return subvolumes, nil
}

// CreateSubvolume creates a subvolume at the given path.
func (c *Client) CreateSubvolume(ctx context.Context, path string) error {
	log.Info("Creating bcachefs subvolume", "path", path)
	_, err := c.run(ctx, "bcachefs", "subvolume", "create", path)
	if err != nil {
		log.Error("failed to create subvolume", "error", err)
	}
	return err
}

// ErrSubvolumeNotEmpty is returned by DeleteSubvolume when the target still
// contains files. The caller (UI/API) is expected to surface this so the user
// can clean the contents out first; we never silently destroy data.
var ErrSubvolumeNotEmpty = errors.New("subvolume is not empty")

// DeleteSubvolume deletes a subvolume at the given path. Refuses to proceed
// if the subvolume still contains entries — the user must clean them out
// first via their own filesystem tools.
func (c *Client) DeleteSubvolume(ctx context.Context, path string) error {
	log.Info("Deleting bcachefs subvolume", "path", path)

	entries, err := os.ReadDir(path)
	if err == nil && len(entries) > 0 {
		log.Warn("refusing to delete non-empty subvolume", "path", path, "entries", len(entries))
		return fmt.Errorf("%w: %d entries remain at %s — remove its contents first", ErrSubvolumeNotEmpty, len(entries), path)
	}

	_, err = c.run(ctx, "bcachefs", "subvolume", "delete", path)
	if err != nil {
		log.Error("failed to delete subvolume", "error", err)
	}
	return err
}

// GetPoolUsage returns usage stats for a mounted filesystem.
func (c *Client) GetPoolUsage(ctx context.Context, mountdir string) (*PoolUsage, error) {
	var stat syscall.Statfs_t
	err := c.statfs(mountdir, &stat)
	if err != nil {
		return nil, err
	}

	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	used := total - free

	var usedPercent float64
	if total > 0 {
		usedPercent = float64(used) / float64(total) * 100.0
	}

	return &PoolUsage{
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: avail,
		UsedPercent:    usedPercent,
	}, nil
}

// GetDiskIO reads /proc/diskstats and filters by given device names.
func (c *Client) GetDiskIO(ctx context.Context, deviceNames []string) (map[string]DiskIOStats, error) {
	file, err := os.Open(c.diskstats)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	devMap := make(map[string]bool)
	for _, name := range deviceNames {
		devMap[name] = true
	}

	stats := make(map[string]DiskIOStats)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}
		devName := fields[2]
		if !devMap[devName] {
			continue
		}

		readIOs, _ := strconv.ParseUint(fields[3], 10, 64)
		readSectors, _ := strconv.ParseUint(fields[5], 10, 64)
		writeIOs, _ := strconv.ParseUint(fields[7], 10, 64)
		writeSectors, _ := strconv.ParseUint(fields[9], 10, 64)
		ioInProgress, _ := strconv.ParseUint(fields[11], 10, 64)

		stats[devName] = DiskIOStats{
			DeviceName:   devName,
			ReadIOs:      readIOs,
			WriteIOs:     writeIOs,
			ReadSectors:  readSectors,
			WriteSectors: writeSectors,
			IOInProgress: ioInProgress,
		}
	}

	return stats, nil
}

// bcduReport is the part of `bcdu --json` read here.
type bcduReport struct {
	Folders []struct {
		Path      string `json:"path"`
		Total     int64  `json:"total"`
		Exclusive int64  `json:"exclusive"`
		Shared    int64  `json:"shared"`
	} `json:"folders"`
}

// FolderUsage is bcdu's reading of one folder, in bytes: what its files
// occupy with shared data counted once, the part no file outside the folder
// holds (what deleting it frees), and the rest.
type FolderUsage struct {
	Total     int64 `json:"total"`
	Exclusive int64 `json:"exclusive"`
	Shared    int64 `json:"shared"`
}

// folderTreeDepth is deeper than any real tree: bcdu walks everything under
// its path whatever the depth, which only limits what it prints.
const folderTreeDepth = "1000"

// FolderTree measures root and every folder under it in one bcdu run, at
// normal priority (someone is waiting for it). Keys are full paths.
func (c *Client) FolderTree(ctx context.Context, root string) (map[string]FolderUsage, error) {
	stdout, err := c.run(ctx, "bcdu", "--json", "-d", folderTreeDepth, root)
	if err != nil {
		return nil, fmt.Errorf("bcdu %s: %w", root, err)
	}
	var r bcduReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		return nil, fmt.Errorf("bcdu %s: %w", root, err)
	}
	out := make(map[string]FolderUsage, len(r.Folders))
	for _, f := range r.Folders {
		out[f.Path] = FolderUsage{Total: f.Total, Exclusive: f.Exclusive, Shared: f.Shared}
	}
	return out, nil
}

// OnBcachefs reports whether path is on a mounted bcachefs filesystem.
func (c *Client) OnBcachefs(path string) bool { return c.bcachefsMountOf(path) != "" }

// FolderSizes measures folders with bcdu (github.com/ogglord/bcdu), which
// reads the pool's extents and reflink btrees: data files share through
// reflinks or hardlinks counts once, where du counts it for every copy. A
// folder's size is what its files occupy. bcdu reads the whole pool's btrees
// whatever it walks, so it runs once per pool, from the folders' common
// parent. A folder it couldn't measure is missing from the map.
func (c *Client) FolderSizes(ctx context.Context, paths []string) (map[string]int64, error) {
	byPool := map[string][]string{}
	for _, p := range paths {
		p = filepath.Clean(p)
		m := c.bcachefsMountOf(p)
		byPool[m] = append(byPool[m], p)
	}

	out := make(map[string]int64, len(paths))
	var errs []error
	for _, ps := range byPool {
		root, depth := commonParent(ps)
		// Idle I/O class and lowest CPU priority: it reads gigabytes of btree
		// in the background and must not slow the services down.
		stdout, err := c.run(ctx, "ionice", "-c3", "nice", "-n19", "bcdu", "--json", "-d", strconv.Itoa(depth), root)
		if err != nil {
			errs = append(errs, fmt.Errorf("bcdu %s: %w", root, err))
			continue
		}
		var r bcduReport
		if err := json.Unmarshal([]byte(stdout), &r); err != nil {
			errs = append(errs, fmt.Errorf("bcdu %s: %w", root, err))
			continue
		}
		want := make(map[string]bool, len(ps))
		for _, p := range ps {
			want[p] = true
		}
		for _, f := range r.Folders {
			if want[f.Path] {
				out[f.Path] = f.Total
			}
		}
	}
	return out, errors.Join(errs...)
}

// btreeShrinkCount is more nodes than any pool's btree cache holds, so one
// write is one whole pass of the cache's shrinker.
const btreeShrinkCount = "1000000"

// ShrinkBtreeCache hands back the btree nodes a bcdu run left cached. bcdu
// reads every node of the extents and reflink btrees, and the kernel keeps
// them all (1.5 GiB on the homelab pool, charged to whoever read them) until
// memory runs short. One shrinker pass frees the nodes nothing has touched
// since they were last passed over; the working set survives it. A second
// pass would cut into the working set too, so this makes exactly one.
func (c *Client) ShrinkBtreeCache() error {
	triggers, _ := filepath.Glob(filepath.Join(c.sysfs, "*", "internal", "trigger_btree_cache_shrink"))
	var errs []error
	for _, t := range triggers {
		if err := os.WriteFile(t, []byte(btreeShrinkCount), 0); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// bcachefsMountOf returns the bcachefs mountpoint path is under, or "" if
// /proc/mounts doesn't say.
func (c *Client) bcachefsMountOf(path string) string {
	data, err := os.ReadFile(c.procMounts)
	if err != nil {
		return ""
	}
	var best string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] != "bcachefs" {
			continue
		}
		if (path == f[1] || strings.HasPrefix(path, strings.TrimSuffix(f[1], "/")+"/")) && len(f[1]) > len(best) {
			best = f[1]
		}
	}
	return best
}

// commonParent returns the deepest folder all of paths are in (a single path
// is its own), and how many levels below it the deepest path is.
func commonParent(paths []string) (string, int) {
	split := func(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }
	common := split(paths[0])
	for _, p := range paths[1:] {
		parts := split(p)
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
	}
	depth := 0
	for _, p := range paths {
		depth = max(depth, len(split(p))-len(common))
	}
	return "/" + strings.Join(common, "/"), depth
}

// DiskHealth is one SMART reading of a disk.
type DiskHealth struct {
	Passed       bool
	TemperatureC int
	PowerOnHours int
	// BadSectors is reallocated plus pending sectors (ATA attributes 5 and
	// 197), or the media error count on NVMe.
	BadSectors int64
}

// ErrNoReading means smartctl returned no health verdict: the disk is asleep
// (-n standby leaves it so) or does not support SMART.
var ErrNoReading = errors.New("no SMART reading")

type smartHealthJSON struct {
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	ATA struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe *struct {
		MediaErrors int64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
}

// smartExpectedExits are smartctl exit codes that still come with a JSON
// report. The status is a bitmask: 2 is "asleep under -n standby" (or open
// failed), and the higher bits flag a failing disk or logged errors, which the
// report itself describes. Only bit 0 (bad command line) is unexpected.
var smartExpectedExits = func() []int {
	var out []int
	for code := 1; code < 256; code++ {
		if code&1 == 0 {
			out = append(out, code)
		}
	}
	return out
}()

// DiskHealth reads a disk's SMART health, temperature, hours on and bad
// sectors. It never wakes a disk in standby: that returns ErrNoReading.
func (c *Client) DiskHealth(ctx context.Context, path string) (DiskHealth, error) {
	res, err := c.r.Run(ctx, cmdrunner.Cmd{
		Module: "storage", Name: "smartctl",
		Args:       []string{"-j", "-n", "standby", "-H", "-A", path},
		ExpectExit: smartExpectedExits,
	})
	if strings.TrimSpace(res.Stdout) == "" {
		if err == nil {
			err = ErrNoReading
		}
		return DiskHealth{}, err
	}
	return parseDiskHealth(res.Stdout)
}

func parseDiskHealth(out string) (DiskHealth, error) {
	var j smartHealthJSON
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return DiskHealth{}, fmt.Errorf("parse smartctl json: %w", err)
	}
	if j.SmartStatus == nil {
		return DiskHealth{}, ErrNoReading
	}
	h := DiskHealth{Passed: j.SmartStatus.Passed, TemperatureC: j.Temperature.Current, PowerOnHours: j.PowerOnTime.Hours}
	for _, a := range j.ATA.Table {
		if a.ID == 5 || a.ID == 197 {
			h.BadSectors += a.Raw.Value
		}
	}
	if j.NVMe != nil {
		h.BadSectors += j.NVMe.MediaErrors
	}
	return h, nil
}

// smartctlJSON represents the structured JSON output from smartctl -j -i.
type smartctlJSON struct {
	ModelFamily  string `json:"model_family"`
	ModelName    string `json:"model_name"`
	UserCapacity struct {
		Bytes uint64 `json:"bytes"`
	} `json:"user_capacity"`
}

// cachedFriendlyName resolves a clean display name, caching per device path.
func (c *Client) cachedFriendlyName(ctx context.Context, path string, model string, bytes uint64) string {
	if path == "" {
		return model
	}
	c.friendlyMu.Lock()
	defer c.friendlyMu.Unlock()
	if name, ok := c.friendlyNames[path]; ok {
		return name
	}
	name := c.friendlyName(ctx, path, model, bytes)
	c.friendlyNames[path] = name
	return name
}

// hasSMART reports whether smartctl can identify the device: optical drives,
// loop devices and zram swap have no SMART data, so asking only fails.
func hasSMART(typ, name string) bool {
	return typ != "rom" && typ != "loop" && !strings.HasPrefix(name, "zram")
}

// friendlyName runs smartctl -j -i and formats a friendly commercial disk name.
// Capacity is intentionally omitted — it's already shown in the table's Capacity column.
func (c *Client) friendlyName(ctx context.Context, path string, defaultModel string, _ uint64) string {
	out, err := c.run(ctx, "smartctl", "-j", "-i", path)
	if err != nil {
		log.Warn("smartctl query failed", "device", path, "error", err)
		return defaultModel
	}

	var info smartctlJSON
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		log.Warn("failed to parse smartctl json", "device", path, "error", err)
		return defaultModel
	}

	modelName := strings.TrimSpace(info.ModelName)
	family := strings.TrimSpace(info.ModelFamily)

	if modelName == "" {
		modelName = defaultModel
	}

	// If modelName already contains a brand (has spaces), use it as-is
	// (e.g. "Samsung SSD 850 PRO 1TB").
	if strings.Contains(info.ModelName, " ") {
		return info.ModelName
	}

	// Strip trailing revision codes like "-2DR166" or "-EXM02B6Q".
	if idx := strings.Index(modelName, "-"); idx != -1 && len(modelName[idx:]) > 4 {
		modelName = modelName[:idx]
	}

	if family != "" {
		return fmt.Sprintf("%s %s", family, modelName)
	}
	return modelName
}

// SetImmutable runs `chattr +i` on path: the empty mountpoint directory of a
// pool that is not mounted, so nothing can write to it.
func (c *Client) SetImmutable(ctx context.Context, path string) error {
	_, err := c.run(ctx, "chattr", "+i", path)
	return err
}

// ClearImmutable runs `chattr -i` on path. The empty mountpoint directory is
// kept +i while the pool is not mounted (SetImmutable); the flag goes before
// mounting over it. Errors are returned for the
// caller to log: a directory that was never immutable is fine.
func (c *Client) ClearImmutable(ctx context.Context, path string) error {
	_, err := c.run(ctx, "chattr", "-i", path)
	return err
}

// BlockFS is a block device as lsblk sees it: a disk or one of its
// partitions, with the file system on it.
type BlockFS struct {
	Name        string
	Path        string
	Size        uint64
	Type        string // disk, part, ...
	FSType      string
	UUID        string // the file system's
	Label       string
	PartType    string // GPT type GUID or MBR type, lowercase
	Mountpoints []string
	// Disk is the model of the disk it is on, or that disk's name.
	Disk string
}

type lsblkFS struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Size        any       `json:"size"`
	Type        string    `json:"type"`
	FSType      string    `json:"fstype"`
	UUID        string    `json:"uuid"`
	Label       string    `json:"label"`
	PartType    string    `json:"parttype"`
	Model       string    `json:"model"`
	Mountpoints []string  `json:"mountpoints"`
	Children    []lsblkFS `json:"children"`
}

// BlockFilesystems lists every block device and partition, flat.
func (c *Client) BlockFilesystems(ctx context.Context) ([]BlockFS, error) {
	out, err := c.run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,FSTYPE,UUID,LABEL,PARTTYPE,MODEL,MOUNTPOINTS")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Blockdevices []lsblkFS `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse lsblk output: %w", err)
	}
	var all []BlockFS
	var walk func(devs []lsblkFS, disk string)
	walk = func(devs []lsblkFS, disk string) {
		for _, d := range devs {
			on := disk
			if on == "" {
				if on = strings.TrimSpace(d.Model); on == "" {
					on = d.Name
				}
			}
			var size uint64
			switch v := d.Size.(type) {
			case float64:
				size = uint64(v)
			case string:
				fmt.Sscanf(v, "%d", &size)
			}
			var mounts []string
			for _, m := range d.Mountpoints {
				if m != "" {
					mounts = append(mounts, m)
				}
			}
			all = append(all, BlockFS{
				Name: d.Name, Path: d.Path, Size: size, Type: d.Type, FSType: d.FSType, UUID: d.UUID,
				Label: d.Label, PartType: strings.ToLower(d.PartType), Mountpoints: mounts, Disk: on,
			})
			walk(d.Children, on)
		}
	}
	walk(parsed.Blockdevices, "")
	return all, nil
}

// MountFS mounts the file system with the given UUID at mountpoint.
func (c *Client) MountFS(ctx context.Context, fstype, uuid, mountpoint, options string) error {
	_, err := c.run(ctx, "mount", "-t", fstype, "-o", options, "UUID="+uuid, mountpoint)
	return err
}
