package installer

import (
	"sort"
	"strings"
)

const (
	gib = uint64(1) << 30
	mib = uint64(1) << 20
	// MinBytes is the space an install needs: the minimums of the image's
	// partition definitions (usr/lib/repart.sysinstall.d) add up to 57.4 GiB
	// (ESP 0.3, two roots with their verity 8.1, /var 49, the Nix store on
	// it), with room to spare here. A disk sold as 64 GB has 59.6 GiB, so
	// the page says 64 GB.
	MinBytes = 59 * gib
	// minESP is the smallest existing ESP an install shares: room for two
	// kernels (A/B), as the ESP definition's SizeMinBytes says.
	minESP = 300 * mib
)

// InstallPartition type GUIDs (upper case, as UDisks2 reports them lower case).
const (
	typeESP     = "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"
	typeMSData  = "EBD0A0A2-B9E5-4433-87C0-68B6B72699C7"
	typeMSR     = "E3C9E316-0B5C-4DB8-817D-F92DF00215AE"
	typeWinRE   = "DE94BBA4-06D1-4D40-A16A-BFD50179D6AC"
	typeNix     = "073D2B22-9F7E-4817-8D45-890277F7DD55"
	typeVar     = "4D21B016-B534-45C2-A9FB-5C16E091FD2D"
	typeRootX64 = "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709"
	typeVerX64  = "2C7357ED-EBD2-46D9-AEC1-23D437EC2BF5"
)

// holds says what a partition holds, from its type and file system.
func holds(typ, fs, label string) string {
	t := strings.ToUpper(typ)
	switch {
	case t == typeESP:
		return "esp"
	case t == typeNix, strings.HasPrefix(label, "homelab_"):
		return "homelab"
	case t == typeMSR, t == typeWinRE, fs == "ntfs", fs == "BitLocker", fs == "exfat" && t == typeMSData:
		return "windows"
	case t == typeMSData && fs == "":
		return "windows"
	}
	switch fs {
	case "":
		return "empty"
	case "ext2", "ext3", "ext4", "btrfs", "xfs", "swap", "crypto_LUKS", "LVM2_member", "bcachefs", "f2fs":
		return "linux"
	}
	return "other"
}

// sameTypes is a partition repart would take for one of the install's own:
// it matches existing partitions to its definitions by type.
func sameTypes(typ string) bool {
	switch strings.ToUpper(typ) {
	case typeRootX64, typeVerX64, typeVar, typeNix:
		return true
	}
	return false
}

// freeRuns lists the unpartitioned runs of a GPT disk of size bytes, 1 MiB
// or more, skipping the first MiB (the partition table and alignment) and
// the backup table at the end.
func freeRuns(size uint64, parts []InstallPartition) []FreeRun {
	sorted := append([]InstallPartition(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var out []FreeRun
	at, end := mib, size
	if end > mib {
		end -= mib
	}
	for _, p := range sorted {
		if p.Start > at && p.Start-at >= mib {
			out = append(out, FreeRun{Start: at, Size: p.Start - at})
		}
		if e := p.Start + p.Size; e > at {
			at = e
		}
	}
	if end > at && end-at >= mib {
		out = append(out, FreeRun{Start: at, Size: end - at})
	}
	return out
}

// assess fills in a disk's free space and what can be done with it.
func assess(d InstallDisk) InstallDisk {
	if d.Partitions == nil {
		d.Partitions = []InstallPartition{}
	}
	d.Free = []FreeRun{}
	switch d.Table {
	case "gpt":
		d.Free = append(d.Free, freeRuns(d.Size, d.Partitions)...)
	case "none":
		d.Free = []FreeRun{{Start: 0, Size: d.Size}}
	}
	d.FreeTotal = 0
	for _, f := range d.Free {
		d.FreeTotal += f.Size
	}
	switch {
	case d.InUse:
		d.Erase = InstallChoice{Reason: "The installer runs from this disk."}
	case d.Size < MinBytes:
		d.Erase = InstallChoice{Reason: "Smaller than 64 GB."}
	default:
		d.Erase = InstallChoice{OK: true}
	}
	d.Beside = beside(d)
	return d
}

func beside(d InstallDisk) InstallChoice {
	if d.InUse {
		return InstallChoice{Reason: "The installer runs from this disk."}
	}
	if len(d.Partitions) == 0 {
		return InstallChoice{Reason: "The disk is empty: erase it instead."}
	}
	if d.Table != "gpt" {
		return InstallChoice{Reason: "Its partition table is not GPT."}
	}
	for _, p := range d.Partitions {
		if sameTypes(p.Type) {
			return InstallChoice{Reason: "It holds another system with the same partition types (" + p.Name + "): erase it or delete those partitions."}
		}
		if p.Holds == "esp" && p.Size < minESP {
			return InstallChoice{Reason: "Its EFI partition is smaller than 300 MB."}
		}
	}
	if d.FreeTotal < MinBytes {
		return InstallChoice{Reason: "Less than 64 GB of free space. Shrink a partition in its own system (Windows: Disk Management) or delete one."}
	}
	return InstallChoice{OK: true}
}

func sortParts(p []InstallPartition) {
	sort.Slice(p, func(i, j int) bool { return p[i].Start < p[j].Start })
}
func sortDisks(d []InstallDisk) { sort.Slice(d, func(i, j int) bool { return d[i].Name < d[j].Name }) }
