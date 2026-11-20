package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"homelab-ngx.com/core/internal/store"
)

// A partition is a block device with a file system of its own that is not a
// bcachefs pool: a partition of a disk, or a whole disk formatted without a
// table. The daemon mounts one under MountRoot like a pool, by name, and
// remembers it by the file system's UUID.

// partitionFS are the file systems a partition is listed with, each with
// the type mount is given. The image's build fails without their modules.
var partitionFS = map[string]string{
	"ext4": "ext4", "ext3": "ext3", "ext2": "ext2", "xfs": "xfs", "btrfs": "btrfs",
	"exfat": "exfat", "vfat": "vfat", "ntfs": "ntfs3",
}

// systemPartTypes are partition types that are never offered: EFI system
// partitions (GPT and MBR), the image's root and verity partitions, swap.
var systemPartTypes = map[string]bool{
	"c12a7328-f81f-11d2-ba4b-00a0c93ec93b": true, "0xef": true,
	"4f68bce3-e8cd-4db1-96e7-fbcaf984b709": true,
	"2c7357ed-ebd2-46d9-aec1-23d437ec2bf5": true,
	"0657fd6d-a4ab-43c4-84e5-0933c84b4f4f": true, "0x82": true,
}

// DefaultPartitionName is the name of a partition without a usable label.
const DefaultPartitionName = "disk"

// partitionWait is how long the boot waits for a remembered partition's
// device: a USB disk can be unplugged, and the stacks start after this.
const partitionWait = 30 * time.Second

// ErrUnknownPartition means no listed partition has the UUID.
var ErrUnknownPartition = errors.New("unknown partition")

// ErrPartitionState is a mount of a mounted partition, or an unmount of one
// that is not mounted.
var ErrPartitionState = errors.New("the partition is not in that state")

// PartitionStore keeps the mounted partitions; *store.Store satisfies it.
type PartitionStore interface {
	Partitions(ctx context.Context) ([]store.PartitionRecord, error)
	UpsertPartition(ctx context.Context, p store.PartitionRecord) error
}

// UsePartitions turns on mounting partitions, remembered in ps.
func (s *Service) UsePartitions(ps PartitionStore) { s.parts = ps }

var notNameChar = regexp.MustCompile(`[^a-z0-9-]+`)

// partitionName makes a label a name: "My Data" is "my-data".
func partitionName(label string) string {
	name := strings.Trim(notNameChar.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if len(name) > 31 {
		name = strings.TrimRight(name[:31], "-")
	}
	if !mountNameRE.MatchString(name) {
		return DefaultPartitionName
	}
	return name
}

func (s *Service) storedPartitions(ctx context.Context) map[string]store.PartitionRecord {
	out := map[string]store.PartitionRecord{}
	if s.parts == nil {
		return out
	}
	recs, err := s.parts.Partitions(ctx)
	if err != nil {
		s.log.Error("failed to read the remembered partitions", "error", err)
		return out
	}
	for _, r := range recs {
		out[r.UUID] = r
	}
	return out
}

// listPartitions is every partition that can be mounted or that the daemon
// mounted. One the system mounted itself (/var, /boot) is left out.
func (s *Service) listPartitions(ctx context.Context) ([]Partition, error) {
	if s.parts == nil {
		return []Partition{}, nil
	}
	devs, err := s.bc.BlockFilesystems(ctx)
	if err != nil {
		return nil, err
	}
	stored := s.storedPartitions(ctx)
	out := []Partition{}
	seen := map[string]bool{}
	for _, d := range devs {
		if _, ok := partitionFS[d.FSType]; !ok || d.UUID == "" || seen[d.UUID] {
			continue
		}
		if (d.Type != "part" && d.Type != "disk") || systemPartTypes[d.PartType] {
			continue
		}
		p := Partition{
			Uuid: d.UUID, Path: d.Path, Disk: d.Disk, Fstype: d.FSType, Label: d.Label, Size: int64(d.Size),
			State: Unmounted, MountName: partitionName(d.Label),
			AutoMount: stored[d.UUID].AutoMount, ReadOnly: stored[d.UUID].ReadOnly,
		}
		if n := stored[d.UUID].Name; n != "" {
			p.MountName = n
		}
		if len(d.Mountpoints) > 0 {
			name, ok := MountName(d.Mountpoints[0])
			if !ok || len(d.Mountpoints) > 1 {
				continue // the system's own
			}
			p.State, p.Mountdir, p.MountName = "mounted", d.Mountpoints[0], name
			if total, free, err := s.statfs(p.Mountdir); err == nil {
				p.Usage = &PartitionUsage{Total: int64(total), Free: int64(free)}
			}
		}
		seen[d.UUID] = true
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) findPartition(ctx context.Context, uuid string) (Partition, error) {
	list, err := s.listPartitions(ctx)
	if err != nil {
		return Partition{}, err
	}
	for _, p := range list {
		if p.Uuid == uuid {
			return p, nil
		}
	}
	return Partition{}, fmt.Errorf("%w: %q", ErrUnknownPartition, uuid)
}

// nameTaken reports whether a pool or another partition has the name.
func (s *Service) nameTaken(ctx context.Context, name, uuid string) bool {
	mountpoint := Mountpoint(name)
	for _, p := range s.cfg.Current().Pools {
		if p.UUID != uuid && p.Mountpoint == mountpoint {
			return true
		}
	}
	for _, r := range s.storedPartitions(ctx) {
		if r.UUID != uuid && r.Name == name && r.AutoMount {
			return true
		}
	}
	devs, err := s.bc.BlockFilesystems(ctx)
	if err != nil {
		return false
	}
	for _, d := range devs {
		if d.UUID != uuid && len(d.Mountpoints) > 0 && d.Mountpoints[0] == mountpoint {
			return true
		}
	}
	return false
}

// MountPartition mounts a partition at MountRoot/<name> and remembers it
// for the next boot. An empty name is the one it had, or its label's.
func (s *Service) MountPartition(ctx context.Context, uuid, name string, readOnly bool) error {
	p, err := s.findPartition(ctx, uuid)
	if err != nil {
		return err
	}
	if p.State == "mounted" {
		return fmt.Errorf("%w: mounted at %s", ErrPartitionState, p.Mountdir)
	}
	if name == "" {
		name = p.MountName
	}
	if !mountNameRE.MatchString(name) {
		return ErrMountName
	}
	if s.nameTaken(ctx, name, uuid) {
		return fmt.Errorf("%w: %s", ErrNameTaken, name)
	}
	mountpoint := Mountpoint(name)
	s.invalidate()
	defer s.invalidate()
	if err := s.open(ctx, mountpoint); err != nil {
		return err
	}
	if err := s.bc.MountFS(ctx, partitionFS[p.Fstype], uuid, mountpoint, mountOptions(readOnly)); err != nil {
		s.guard(ctx, mountpoint)
		return err
	}
	rec := store.PartitionRecord{UUID: uuid, Name: name, Fstype: p.Fstype, ReadOnly: readOnly, AutoMount: true}
	if err := s.parts.UpsertPartition(ctx, rec); err != nil {
		s.log.Error("partition mounted but not remembered", "uuid", uuid, "error", err)
	}
	return nil
}

func mountOptions(readOnly bool) string {
	if readOnly {
		return "nodev,nosuid,ro"
	}
	return "nodev,nosuid"
}

// UnmountPartition unmounts a partition and stops mounting it at boot.
func (s *Service) UnmountPartition(ctx context.Context, uuid string) error {
	p, err := s.findPartition(ctx, uuid)
	if err != nil {
		return err
	}
	if p.State != "mounted" {
		return fmt.Errorf("%w: not mounted", ErrPartitionState)
	}
	s.invalidate()
	defer s.invalidate()
	if err := s.bc.Unmount(ctx, p.Mountdir); err != nil {
		return err
	}
	s.guard(ctx, p.Mountdir)
	rec := store.PartitionRecord{UUID: uuid, Name: p.MountName, Fstype: p.Fstype, ReadOnly: p.ReadOnly, AutoMount: false}
	if err := s.parts.UpsertPartition(ctx, rec); err != nil {
		s.log.Error("partition unmounted but still set to mount at boot", "uuid", uuid, "error", err)
	}
	return nil
}

// SetPartitionAutoMount changes whether a remembered partition mounts at boot.
func (s *Service) SetPartitionAutoMount(ctx context.Context, uuid string, auto bool) error {
	rec, ok := s.storedPartitions(ctx)[uuid]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownPartition, uuid)
	}
	rec.AutoMount = auto
	defer s.invalidate()
	return s.parts.UpsertPartition(ctx, rec)
}

// autoMountPartitions mounts the remembered partitions at boot. One whose
// device does not show up in partitionWait (a USB disk unplugged) is logged
// and its directory guarded: the boot goes on.
func (s *Service) autoMountPartitions(ctx context.Context) {
	pending := map[string]store.PartitionRecord{}
	for uuid, r := range s.storedPartitions(ctx) {
		if r.AutoMount {
			pending[uuid] = r
		}
	}
	if len(pending) == 0 {
		return
	}
	deadline := s.now().Add(min(s.discoverTimeout, partitionWait))
	for {
		devs, err := s.bc.BlockFilesystems(ctx)
		if err != nil {
			s.log.Error("failed to list partitions during boot", "error", err)
		}
		for _, d := range devs {
			r, ok := pending[d.UUID]
			if !ok || d.UUID == "" {
				continue
			}
			delete(pending, d.UUID)
			mountpoint := Mountpoint(r.Name)
			if len(d.Mountpoints) > 0 {
				s.log.Info("partition already mounted", "uuid", d.UUID, "mountpoint", d.Mountpoints[0])
				continue
			}
			s.log.Info("auto-mounting partition", "uuid", d.UUID, "mountpoint", mountpoint)
			if err := s.open(ctx, mountpoint); err != nil {
				s.log.Error("failed to auto-mount partition", "uuid", d.UUID, "error", err)
				continue
			}
			if err := s.bc.MountFS(ctx, partitionFS[r.Fstype], d.UUID, mountpoint, mountOptions(r.ReadOnly)); err != nil {
				s.log.Error("failed to auto-mount partition", "uuid", d.UUID, "error", err)
				s.guard(ctx, mountpoint)
			}
		}
		if len(pending) == 0 {
			return
		}
		if s.now().After(deadline) {
			for uuid, r := range pending {
				s.log.Error("auto-mount partition not found", "uuid", uuid, "name", r.Name)
				if err := s.mkdir(Mountpoint(r.Name)); err == nil {
					s.guard(ctx, Mountpoint(r.Name))
				}
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.retryInterval):
		}
	}
}

func statfs(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}

// ErrNotAPlace is a folder asked for somewhere that is not a mounted pool
// or partition, or under a name that is not one folder.
var ErrNotAPlace = errors.New("a folder is made on a mounted pool or partition")

var folderNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// FolderPlaces are the directories a folder can be made in: the mounted
// pools, and the mounted partitions that are not read-only.
func (s *Service) FolderPlaces(ctx context.Context) ([]string, error) {
	st, err := s.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, p := range st.Pools {
		if p.State == "mounted" && p.Mountdir != "" {
			out = append(out, p.Mountdir)
		}
	}
	for _, p := range st.Partitions {
		if p.State == "mounted" && !p.ReadOnly {
			out = append(out, p.Mountdir)
		}
	}
	return out, nil
}

// MakeFolder makes place/name and returns its path: a subvolume when place
// is a bcachefs pool, a plain folder on a partition. An empty folder that
// is there already is used; one with something in it is refused.
func (s *Service) MakeFolder(ctx context.Context, place, name string) (string, error) {
	places, err := s.FolderPlaces(ctx)
	if err != nil {
		return "", err
	}
	if !slices.Contains(places, place) {
		return "", fmt.Errorf("%w: %s is neither", ErrNotAPlace, place)
	}
	if !folderNameRE.MatchString(name) {
		return "", fmt.Errorf("%w: %q is not a folder name", ErrNotAPlace, name)
	}
	path := place + "/" + name
	if entries, err := os.ReadDir(path); err == nil {
		if len(entries) > 0 {
			return "", fmt.Errorf("%s is there already and has files in it: set it as the folder instead", path)
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if s.onBcachefs(place) {
		if err := s.CreateSubvolume(ctx, path); err != nil {
			return "", err
		}
		return path, nil
	}
	defer s.invalidate()
	return path, os.Mkdir(path, 0o755)
}
