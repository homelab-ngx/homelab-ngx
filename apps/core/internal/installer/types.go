// Package installer is the daemon's installer mode: on the installer stick
// (the image whose /var holds the installer marker) the dashboard shows only
// the installer page. It lists the disks through UDisks2, can delete a
// partition to make room, shows systemd-repart's plan, installs (repart,
// then systemd-boot and the kernel), then restarts into the new system or
// turns the machine off.
package installer

import "homelab-ngx.com/core/internal/hostcfg"

// InstallPartition is one partition on a disk.
type InstallPartition struct {
	Number int    `json:"number"`
	Name   string `json:"name" doc:"Kernel name (nvme0n1p2): what the user types to delete it."`
	Path   string `json:"path"`
	Start  uint64 `json:"start" doc:"Offset in bytes."`
	Size   uint64 `json:"size"`
	Type   string `json:"type" doc:"GPT partition type GUID."`
	Holds  string `json:"holds" enum:"esp,windows,linux,homelab,empty,other" doc:"What the partition holds, from its type and file system."`
	Label  string `json:"label" doc:"The partition's name, else its file system's label."`
	FS     string `json:"fs" doc:"File system type, empty when none is known."`
}

// FreeRun is a run of unpartitioned space.
type FreeRun struct {
	Start uint64 `json:"start"`
	Size  uint64 `json:"size"`
}

// InstallChoice says whether an install mode is possible on a disk.
type InstallChoice struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty" doc:"Why not, when it is not possible."`
}

// InstallDisk is one disk the installer can install onto, or not.
type InstallDisk struct {
	Name       string             `json:"name" doc:"Kernel name (nvme0n1)."`
	Path       string             `json:"path"`
	Model      string             `json:"model"`
	Size       uint64             `json:"size"`
	Removable  bool               `json:"removable"`
	Bus        string             `json:"bus" doc:"usb, nvme, ata… as UDisks2 reports it."`
	Table      string             `json:"table" enum:"gpt,dos,none"`
	InUse      bool               `json:"in_use" doc:"A partition is mounted: the installer's own stick."`
	Partitions []InstallPartition `json:"partitions"`
	Free       []FreeRun          `json:"free"`
	FreeTotal  uint64             `json:"free_total"`
	Erase      InstallChoice      `json:"erase" doc:"Erase the disk and install."`
	Beside     InstallChoice      `json:"beside" doc:"Install into the free space, keeping the other partitions."`
}

// Step is where the install is.
type Step string

const (
	StepIdle       Step = "idle"
	StepInstalling Step = "installing"
	StepInstalled  Step = "installed"
	StepFailed     Step = "failed"
)

// InstallerState is the installer page's whole view.
type InstallerState struct {
	Active   bool          `json:"active" doc:"The daemon runs in installer mode (the installer stick)."`
	Claimed  bool          `json:"claimed" doc:"Someone opened the installer first and holds it."`
	Mine     bool          `json:"mine" doc:"The token sent with the request is the holder's."`
	Step     Step          `json:"step" enum:"idle,installing,installed,failed"`
	Phase    string        `json:"phase,omitempty" enum:"partition,boot,finish" doc:"While installing: partitioning and copying, the boot loader and kernel, the host name."`
	Disk     string        `json:"disk,omitempty" doc:"The disk being or last installed onto."`
	Hostname string        `json:"hostname,omitempty"`
	Log      []string      `json:"log" doc:"The install's output, newest last."`
	Error    string        `json:"error,omitempty"`
	Disks    []InstallDisk `json:"disks"`
	MinBytes uint64        `json:"min_bytes" doc:"The space an install needs."`
	Current  hostcfg.IPv4  `json:"current" doc:"The installer medium's own address from DHCP: where a static setting starts."`
	Machine  Machine       `json:"machine" doc:"What the installer found out about the machine before an install."`
	// CanRestart is false after an install whose boot entry the firmware
	// does not list: that machine is turned off and started by hand.
	CanRestart bool `json:"can_restart" doc:"Installed, and the firmware has a boot entry for the new system: restartInstaller can start it."`
}

// Machine is what an install depends on besides the disk.
type Machine struct {
	Memory     uint64 `json:"memory" doc:"RAM in bytes; 0 when it could not be read."`
	SecureBoot bool   `json:"secure_boot" doc:"Secure Boot is on. The image's kernel is not signed, so the installed system only starts with it off."`
	// BootEntries is false where the firmware's variables are missing or
	// read-only: the install cannot register its boot entry there.
	BootEntries bool `json:"boot_entries" doc:"The firmware's boot entries can be written: the install registers the new system, and can restart into it."`
}

// InstallPlanRow is one partition in systemd-repart's plan for a disk.
type InstallPlanRow struct {
	Type     string `json:"type"`
	Label    string `json:"label"`
	Node     string `json:"node"`
	Size     uint64 `json:"size"`
	Activity string `json:"activity" doc:"create, resize or unchanged, as repart reports it."`
	Offset   uint64 `json:"offset" doc:"Where on the disk, in bytes."`
}

// InstallPlan is what an install would do to a disk.
type InstallPlan struct {
	OK     bool             `json:"ok"`
	Reason string           `json:"reason,omitempty" doc:"repart's reason when the install would not fit."`
	Rows   []InstallPlanRow `json:"rows"`
}
