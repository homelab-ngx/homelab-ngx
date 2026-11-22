// Package vm runs the image check's virtual machines: QEMU with UEFI
// firmware, disks of several kinds, a vsock device the guest's systemd
// reports its boots on, and QMP for the machine's own events.
package vm

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Kind is how a disk is attached.
type Kind string

const (
	// Virtio is a virtio-blk disk: vda, vdb, ... in the order given.
	Virtio Kind = "virtio"
	// USB is a USB stick (usb-storage), the only sd disk; the kernel finds
	// it after the others, as on a real machine.
	USB Kind = "usb"
	// PVSCSI is a disk behind VMware's paravirtual SCSI controller.
	PVSCSI Kind = "pvscsi"
	// MegaSAS is a disk behind a MegaRAID. QEMU's fails writes that force
	// unit access, so the disk says it has none.
	MegaSAS Kind = "megasas"
	// UAS is a disk in a USB enclosure that speaks UAS.
	UAS Kind = "uas"
)

// Disk is one disk of the machine. The firmware has no driver for PVSCSI,
// MegaSAS or UAS, so it cannot start from such a disk.
type Disk struct {
	Kind Kind
	Path string
}

// Format is the disk file's format, from its name: qcow2 or raw.
func (d Disk) Format() string {
	if strings.HasSuffix(d.Path, ".qcow2") {
		return "qcow2"
	}
	return "raw"
}

// Config is one start of a machine.
type Config struct {
	OVMFCode string // read-only firmware code
	Vars     string // the firmware's variables, kept across starts
	CPUs     int
	MemMiB   int
	// UUID is the firmware's UUID; empty leaves the machine without one,
	// as some boards are.
	UUID string
	// Order makes the firmware try the disks in the order given, like a
	// machine set to start from USB first. Every disk then gets a boot
	// index: OVMF deletes the boot entries of a disk that has none, also
	// the ones an install registered. Without Order the firmware starts
	// what its own boot order says.
	Order bool
	Disks []Disk
	// Credentials are systemd credentials passed through SMBIOS type 11.
	Credentials map[string][]byte
	// CID is the guest's vsock address; 0 means no vsock device.
	CID uint32
	// SSHPort and HTTPPort forward 127.0.0.1's ports to the guest's 22 and
	// 80 (0: no forward).
	SSHPort, HTTPPort int
	Serial            string // the serial port's log
	QMP               string // the QMP socket
}

// Args are QEMU's arguments for the start.
func (c Config) Args() []string {
	a := []string{
		"-machine", "q35,accel=kvm", "-cpu", "host",
		"-smp", strconv.Itoa(c.CPUs), "-m", strconv.Itoa(c.MemMiB),
	}
	if c.UUID != "" {
		a = append(a, "-uuid", c.UUID)
	}
	a = append(a,
		"-drive", "if=pflash,format=raw,readonly=on,file="+c.OVMFCode,
		"-drive", "if=pflash,format=raw,file="+c.Vars,
	)
	for _, d := range c.Disks {
		if d.Kind == USB {
			a = append(a, "-device", "qemu-xhci")
			break
		}
	}
	for n, d := range c.Disks {
		drive := fmt.Sprintf("format=%s,file=%s,cache=unsafe,discard=unmap,detect-zeroes=unmap", d.Format(), d.Path)
		dev := "virtio-blk-pci"
		switch d.Kind {
		case USB:
			dev = "usb-storage"
		case PVSCSI:
			a = append(a, "-device", fmt.Sprintf("pvscsi,id=scsi%d", n))
			dev = fmt.Sprintf("scsi-hd,bus=scsi%d.0", n)
		case MegaSAS:
			a = append(a, "-device", fmt.Sprintf("megasas-gen2,id=scsi%d", n))
			dev = fmt.Sprintf("scsi-hd,bus=scsi%d.0,dpofua=off", n)
		case UAS:
			a = append(a, "-device", "qemu-xhci", "-device", fmt.Sprintf("usb-uas,id=scsi%d", n))
			dev = fmt.Sprintf("scsi-hd,bus=scsi%d.0", n)
		}
		if !c.Order && d.Kind == Virtio {
			a = append(a, "-drive", "if=virtio,"+drive)
			continue
		}
		device := fmt.Sprintf("%s,drive=disk%d", dev, n)
		if c.Order {
			device += fmt.Sprintf(",bootindex=%d", n)
		}
		a = append(a, "-drive", fmt.Sprintf("if=none,id=disk%d,%s", n, drive), "-device", device)
	}
	names := make([]string, 0, len(c.Credentials))
	for k := range c.Credentials {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		a = append(a, "-smbios", "type=11,value=io.systemd.credential.binary:"+k+"="+base64.StdEncoding.EncodeToString(c.Credentials[k]))
	}
	if c.CID != 0 {
		a = append(a, "-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", c.CID))
	}
	nic := "user,model=virtio-net-pci"
	if c.SSHPort != 0 {
		nic += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:22", c.SSHPort)
	}
	if c.HTTPPort != 0 {
		nic += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:80", c.HTTPPort)
	}
	a = append(a, "-nic", nic, "-display", "none", "-serial", "file:"+c.Serial)
	if c.QMP != "" {
		a = append(a, "-qmp", "unix:"+c.QMP+",server,nowait")
	}
	return a
}

// First is the first disk's name without its extension, for log names.
func (c Config) First() string {
	if len(c.Disks) == 0 {
		return "none"
	}
	b := filepath.Base(c.Disks[0].Path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}
