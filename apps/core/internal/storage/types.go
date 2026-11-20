package storage

import (
	"time"

	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// DiskIOStats are per-device I/O counters from /proc/diskstats.
type DiskIOStats struct {
	DeviceName   string `json:"device_name"`
	IoInProgress int64  `json:"io_in_progress"`
	ReadIos      int64  `json:"read_ios"`
	ReadSectors  int64  `json:"read_sectors"`
	WriteIos     int64  `json:"write_ios"`
	WriteSectors int64  `json:"write_sectors"`
}

// DiskHealth is a disk's last SMART reading, checked in the background.
type DiskHealth struct {
	Passed       bool      `json:"passed" doc:"The disk's own overall health check."`
	TemperatureC int       `json:"temperature_c"`
	PowerOnHours int       `json:"power_on_hours"`
	BadSectors   int64     `json:"bad_sectors" doc:"Reallocated plus pending sectors (ATA), or media errors (NVMe)."`
	CheckedAt    time.Time `json:"checked_at"`
}

// PoolUsage is reported by `bcachefs fs usage`.
type PoolUsage struct {
	AvailableBytes int64   `json:"available_bytes"`
	TotalBytes     int64   `json:"total_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

// Disk is one physical device inside a pool, or one a pool can be made from.
type Disk struct {
	BcachefsLabel  string       `json:"bcachefs_label"`
	Contents       string       `json:"contents" doc:"What a spare disk holds, such as ext4 \"Data\" or 2 partitions; empty for a blank one. A pool erases it."`
	DataTarget     string       `json:"data_target"`
	FriendlyName   string       `json:"friendly_name"`
	Fstype         string       `json:"fstype"`
	HasChildren    bool         `json:"has_children,omitempty"`
	Health         *DiskHealth  `json:"health,omitempty" doc:"Last SMART reading; absent until the first check or when the disk has no SMART."`
	Io             *DiskIOStats `json:"io,omitempty"`
	Kind           string       `json:"kind" enum:"ssd,hdd" doc:"In a pool with both, SSDs take new writes and HDDs keep the data."`
	Label          string       `json:"label"`
	MetadataTarget string       `json:"metadata_target"`
	Model          string       `json:"model"`
	Mountpoint     string       `json:"mountpoint"`
	Name           string       `json:"name"`
	Path           string       `json:"path"`
	Serial         string       `json:"serial"`
	Size           int64        `json:"size"`
	State          string       `json:"state" doc:"A pool member's state: rw, ro, evacuating. Empty for a spare disk."`
	Type           string       `json:"type"`
}

// PoolState is whether a pool is mounted.
type PoolState string

// PoolState values.
const (
	Mounted   PoolState = "mounted"
	Unmounted PoolState = "unmounted"
)

// Pool is one bcachefs pool with its member disks.
type Pool struct {
	AutoMount        bool           `json:"auto_mount"`
	BackgroundTarget string         `json:"background_target" doc:"Disk group data is moved to; empty = none."`
	DataReplicas     int            `json:"data_replicas"`
	Disks            []Disk         `json:"disks"`
	ForegroundTarget string         `json:"foreground_target" doc:"Disk group new writes land on; empty = none."`
	Label            string         `json:"label"`
	MetadataReplicas int            `json:"metadata_replicas"`
	MetadataTarget   string         `json:"metadata_target" doc:"Disk group metadata is kept on; empty = none."`
	Mountdir         string         `json:"mountdir"`
	MountName        string         `json:"mount_name" doc:"The name it mounts under when none is given: the one it had before, or pool."`
	Name             string         `json:"name"`
	Operation        *PoolOperation `json:"operation,omitempty" doc:"The removal of a disk that runs or last ran."`
	PromoteTarget    string         `json:"promote_target" doc:"Disk group reads are cached on; empty = none."`
	State            PoolState      `json:"state" enum:"mounted,unmounted"`
	Usage            *PoolUsage     `json:"usage,omitempty"`
	Uuid             string         `json:"uuid"`
}

// PoolOperation is the removal of a disk from a pool. It runs in the
// background: poll the storage status for it.
type PoolOperation struct {
	Kind       string     `json:"kind" enum:"remove"`
	Disk       string     `json:"disk" doc:"The device that leaves, such as /dev/sdc."`
	Phase      string     `json:"phase" enum:"moving,removing,erasing,done,failed"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	TotalBytes int64      `json:"total_bytes" doc:"What the disk held when the removal started."`
	LeftBytes  int64      `json:"left_bytes" doc:"What is still to move to the other disks."`
}

// PoolCreateRequest makes a pool.
type PoolCreateRequest struct {
	Name    string   `json:"name" pattern:"^[a-z0-9][a-z0-9-]{0,30}$" doc:"The pool mounts at /var/mnt/<name>."`
	Disks   []string `json:"disks" minItems:"1" doc:"Devices from the status's unassigned list, such as /dev/sdb."`
	Copies  int      `json:"copies" minimum:"1" maximum:"3" doc:"How many disks hold each file. Not more than there are disks."`
	Confirm string   `json:"confirm,omitempty" doc:"The pool's name again, required when a disk has contents: they are erased."`
}

// PoolDiskRequest adds a disk to a pool.
type PoolDiskRequest struct {
	Disk    string `json:"disk" minLength:"1" doc:"A device from the status's unassigned list."`
	Confirm string `json:"confirm,omitempty" doc:"The pool's name, required when the disk has contents: they are erased."`
}

// Subvolume is one bcachefs subvolume on a mounted pool.
type Subvolume struct {
	Id         string     `json:"id"`
	Path       string     `json:"path"`
	UsedBytes  int64      `json:"used_bytes" doc:"-1 if not yet measured."`
	MeasuredAt *time.Time `json:"measured_at,omitempty" doc:"When used_bytes was measured."`
}

// StorageStatus is the storage page: pools, unassigned disks, subvolumes.
type StorageStatus struct {
	Pools      []Pool      `json:"pools"`
	Subvolumes []Subvolume `json:"subvolumes"`
	Unassigned []Disk      `json:"unassigned"`
	Partitions []Partition `json:"partitions" doc:"File systems that are not a pool: the ones the daemon mounted, and the ones it can mount."`
}

// Partition is a file system that is not a bcachefs pool: a partition of a
// disk, or a whole disk formatted without a table.
type Partition struct {
	AutoMount bool            `json:"auto_mount"`
	Disk      string          `json:"disk" doc:"The model of the disk it is on, or that disk's name."`
	Fstype    string          `json:"fstype"`
	Label     string          `json:"label"`
	Mountdir  string          `json:"mountdir"`
	MountName string          `json:"mount_name" doc:"The name it mounts under when none is given: the one it had before, or its label's."`
	Path      string          `json:"path" doc:"The device, such as /dev/sdb1."`
	ReadOnly  bool            `json:"read_only"`
	Size      int64           `json:"size"`
	State     PoolState       `json:"state" enum:"mounted,unmounted"`
	Usage     *PartitionUsage `json:"usage,omitempty" doc:"While mounted."`
	Uuid      string          `json:"uuid" doc:"The file system's UUID."`
}

// PartitionUsage is a mounted partition's room, in bytes.
type PartitionUsage struct {
	Free  int64 `json:"free"`
	Total int64 `json:"total"`
}

// PartitionMountRequest mounts a partition.
type PartitionMountRequest struct {
	Name     string `json:"name,omitempty" pattern:"^[a-z0-9][a-z0-9-]{0,30}$" doc:"It mounts at /var/mnt/<name>. Empty is the name it had before, or its label's."`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// MountRequest mounts a pool from its devices.
type MountRequest struct {
	Devices []string `json:"devices" minItems:"1"`
	// Name: the pool mounts at MountRoot/<name>. Empty is the name it was
	// last mounted under, or DefaultMountName.
	Name string `json:"name,omitempty" pattern:"^[a-z0-9][a-z0-9-]{0,30}$" doc:"The pool mounts at /var/mnt/<name>. Empty is the name it had before, or pool."`
}

// UnmountRequest unmounts the pool at a mountpoint.
type UnmountRequest struct {
	Mountpoint string `json:"mountpoint" minLength:"1"`
}

// PoolPatch changes a pool's auto-mount flag.
type PoolPatch struct {
	AutoMount bool `json:"auto_mount"`
}

// SubvolumeUsageRequest names the subvolumes to measure.
type SubvolumeUsageRequest struct {
	Paths []string `json:"paths"`
}

// SubvolumeUsage is bytes used per path; -1 where it could not be computed.
type SubvolumeUsage struct {
	Usage map[string]int64 `json:"usage"`
}

// SubvolumeRequest names a subvolume to create.
type SubvolumeRequest struct {
	Path string `json:"path" minLength:"1"`
}

// Entry is one file or folder in a browsed folder, read with lstat: a
// symlink is listed as itself.
type Entry struct {
	Name       string    `json:"name"`
	Type       string    `json:"type" enum:"dir,file,symlink,other"`
	Size       int64     `json:"size" doc:"Apparent size in bytes. Not meaningful for a folder."`
	DiskBytes  int64     `json:"disk_bytes" doc:"Bytes allocated on disk, as stat reports them."`
	ModifiedAt time.Time `json:"modified_at"`
	Mode       string    `json:"mode" doc:"Type and permissions, as ls prints them."`
	Owner      string    `json:"owner" doc:"User name, or the uid when it has none."`
	Group      string    `json:"group" doc:"Group name, or the gid when it has none."`
}

// Listing is one browsed folder.
type Listing struct {
	Path      string  `json:"path" doc:"The folder, symlinks resolved."`
	Root      string  `json:"root" doc:"The browse root the folder is in. Empty above a root, where only the folders leading to one are listed."`
	Entries   []Entry `json:"entries"`
	Total     int     `json:"total" doc:"How many entries the folder has."`
	Truncated bool    `json:"truncated" doc:"True when entries holds only the first of total, by name."`
}

// FolderFigures are bcdu's figures for the folders directly in a browsed
// folder.
type FolderFigures struct {
	Available  bool                            `json:"available" doc:"False when there are no figures: see reason."`
	Reason     string                          `json:"reason,omitempty" enum:"not_bcachefs,failed" doc:"Why there are no figures. Absent above a browse root."`
	MeasuredAt *time.Time                      `json:"measured_at,omitempty"`
	Folders    map[string]bcachefs.FolderUsage `json:"folders" doc:"By folder name."`
}
