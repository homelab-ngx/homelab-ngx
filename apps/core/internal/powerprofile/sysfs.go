package powerprofile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Paths are the kernel files the profile reads and writes.
type Paths struct {
	CPU        string // /sys/devices/system/cpu
	PCI        string // /sys/bus/pci/devices
	USB        string // /sys/bus/usb/devices
	SCSIHost   string // /sys/class/scsi_host
	ASPMPolicy string // /sys/module/pcie_aspm/parameters/policy
	ProcSys    string // /proc/sys
	Proc       string // /proc
	PMC        string // /sys/kernel/debug/pmc_core
	Powercap   string // /sys/class/powercap
	// Boot keeps the values the kernel booted with. It lives on /run, so it
	// outlasts a daemon restart and is gone after a reboot.
	Boot string
}

// DefaultPaths is the layout on the homelab host.
func DefaultPaths() Paths {
	return Paths{
		CPU: "/sys/devices/system/cpu", PCI: "/sys/bus/pci/devices", USB: "/sys/bus/usb/devices",
		SCSIHost: "/sys/class/scsi_host", ASPMPolicy: "/sys/module/pcie_aspm/parameters/policy",
		ProcSys: "/proc/sys", Proc: "/proc", PMC: "/sys/kernel/debug/pmc_core", Powercap: "/sys/class/powercap",
		Boot: "/run/homelab/power-profile-boot.json",
	}
}

// read is a small kernel file's value without the trailing newline.
func read(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// current is the value a file holds now. A choice file lists every option
// and brackets the one in effect ("[default] performance powersave").
func current(path string) (string, bool) {
	v, ok := read(path)
	if !ok {
		return "", false
	}
	if i := strings.IndexByte(v, '['); i >= 0 {
		if j := strings.IndexByte(v[i:], ']'); j > 0 {
			return v[i+1 : i+j], true
		}
	}
	return v, true
}

// glob is filepath.Glob, sorted, without errors (a bad pattern is a bug the
// tests catch).
func glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	slices.Sort(m)
	return m
}

// exists reports whether a file is there.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// policies are the cpufreq policy directories.
func (p Paths) policies() []string {
	return glob(filepath.Join(p.CPU, "cpufreq", "policy*"))
}

// driver is the cpufreq driver, from the first policy.
func (p Paths) driver() string {
	for _, d := range p.policies() {
		if v, ok := read(filepath.Join(d, "scaling_driver")); ok {
			return v
		}
	}
	return ""
}

// governors are the governors the first policy offers.
func (p Paths) governors() []string {
	for _, d := range p.policies() {
		if v, ok := read(filepath.Join(d, "scaling_available_governors")); ok {
			return strings.Fields(v)
		}
	}
	return []string{}
}

// pciDevices are the PCI device directories.
func (p Paths) pciDevices() []string {
	return glob(filepath.Join(p.PCI, "*"))
}

// usbDevices are the USB devices; interfaces (1-2:1.0) are left out, they
// have no power control of their own.
func (p Paths) usbDevices() []string {
	var out []string
	for _, d := range glob(filepath.Join(p.USB, "*")) {
		if !strings.Contains(filepath.Base(d), ":") {
			out = append(out, d)
		}
	}
	return out
}

// pciID is a PCI device's vendor:device, without the 0x.
func pciID(dir string) string {
	v, _ := read(filepath.Join(dir, "vendor"))
	d, _ := read(filepath.Join(dir, "device"))
	return strings.TrimPrefix(v, "0x") + ":" + strings.TrimPrefix(d, "0x")
}

// usbID is a USB device's idVendor:idProduct.
func usbID(dir string) string {
	v, _ := read(filepath.Join(dir, "idVendor"))
	d, _ := read(filepath.Join(dir, "idProduct"))
	return v + ":" + d
}

// driverOf is the name of the driver bound to a device, or empty.
func driverOf(dir string) string {
	l, err := os.Readlink(filepath.Join(dir, "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(l)
}
