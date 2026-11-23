// Package powerprofile keeps the host's power profile: the CPU governor and
// energy preference, PCIe ASPM, runtime power management, USB autosuspend,
// SATA link power and a few kernel tunables, written straight to sysfs and
// /proc/sys. It also reports how deep the host idles and what keeps it from
// going deeper.
package powerprofile

import (
	"github.com/danielgtaylor/huma/v2"
)

// Profile is one of the three power profiles.
type PowerProfile string

const (
	// PowerSaver turns on every saving the hardware has, including ASPM on
	// every PCIe link and USB autosuspend on every device.
	PowerSaver PowerProfile = "power-saver"
	// Balanced saves what is safe and leaves the rest as the host booted.
	Balanced PowerProfile = "balanced"
	// Performance runs the CPU at its highest preference and keeps links
	// awake.
	Performance PowerProfile = "performance"
)

// Schema documents Profile as its enum.
func (PowerProfile) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: "string", Enum: []any{string(PowerSaver), string(Balanced), string(Performance)}}
}

func (p PowerProfile) valid() bool {
	return p == PowerSaver || p == Balanced || p == Performance
}

// Settings are what the operator chooses. They are kept in the power
// section of host_settings.
type PowerProfileSettings struct {
	Profile PowerProfile `json:"profile"`
	// Governor overrides the profile's CPU governor; empty keeps the
	// profile's.
	Governor string `json:"governor" doc:"A name from governors, or empty for the profile's own."`
}

// TunableState is how a tunable stands after the last apply.
type PowerTunableState string

const (
	// StateSet: the profile's value is written on every file.
	StateSet PowerTunableState = "set"
	// StateBoot: the profile leaves it as the host booted.
	StateBoot PowerTunableState = "boot"
	// StatePartial: some files refused the value.
	StatePartial PowerTunableState = "partial"
	// StateRefused: every file refused the value.
	StateRefused PowerTunableState = "refused"
	// StateUnsupported: the host has no such file.
	StateUnsupported PowerTunableState = "unsupported"
)

// Schema documents TunableState as its enum.
func (PowerTunableState) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: "string", Enum: []any{
		string(StateSet), string(StateBoot), string(StatePartial), string(StateRefused), string(StateUnsupported),
	}}
}

// Tunable is one kind of setting the profile writes, over all the files it
// covers (every CPU policy, every PCI device...).
type PowerTunable struct {
	Name  string            `json:"name" enum:"governor,epp,aspm_policy,aspm_links,pci_runtime_pm,usb_autosuspend,sata_alpm,nmi_watchdog,dirty_writeback"`
	Value string            `json:"value" doc:"The value written, or empty when the profile leaves the boot value."`
	State PowerTunableState `json:"state"`
	Files int               `json:"files" doc:"How many files the tunable covers on this host."`
	// Refused is how many of them refused the write.
	Refused int    `json:"refused"`
	Detail  string `json:"detail" doc:"Why a write was refused, from the first refusal."`
}

// IdleState is one CPU or package idle state and its share of the window.
type PowerIdleState struct {
	Name string `json:"name"`
	// LatencyUS is the exit latency of a CPU state; 0 for a package state.
	LatencyUS int     `json:"latency_us"`
	Share     float64 `json:"share" doc:"Its share of the window, 0 to 1."`
}

// Idle is where the CPU spent the last window.
type PowerIdle struct {
	WindowSeconds float64 `json:"window_seconds"`
	// Active is the CPUs' share outside every idle state, 0 to 1.
	Active    float64          `json:"active"`
	CPUStates []PowerIdleState `json:"cpu_states"`
	// PackageStates are the package's C-states, deepest last. Empty where
	// the platform does not report them.
	PackageStates []PowerIdleState `json:"package_states"`
	// Deepest is the deepest package state with time in the window.
	Deepest string `json:"deepest"`
	// Watts is the CPU package's draw from RAPL; null without RAPL or before
	// a second reading.
	Watts *float64 `json:"watts" nullable:"true"`
}

// BlockerKind is what keeps a device awake.
type PowerBlockerKind string

const (
	// BlockerASPM: the device's PCIe link has L1 off.
	BlockerASPM PowerBlockerKind = "aspm"
	// BlockerRuntimePM: the PCI device's runtime power management is on.
	BlockerRuntimePM PowerBlockerKind = "runtime_pm"
	// BlockerUSB: the USB device never autosuspends.
	BlockerUSB PowerBlockerKind = "usb_autosuspend"
)

// Schema documents BlockerKind as its enum.
func (PowerBlockerKind) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: "string", Enum: []any{string(BlockerASPM), string(BlockerRuntimePM), string(BlockerUSB)}}
}

// Blocker is a device that keeps the package out of a deeper state.
type PowerBlocker struct {
	Kind   PowerBlockerKind `json:"kind"`
	Device string           `json:"device" doc:"The PCI address or USB port, as in sysfs."`
	ID     string           `json:"id" doc:"vendor:device."`
	Driver string           `json:"driver"`
	Name   string           `json:"name" doc:"The USB product name; empty for PCI."`
	// Fix is the profile that turns it off.
	Fix PowerProfile `json:"fix"`
}

// Status is the profile in effect and what it does on this host.
type PowerProfileStatus struct {
	PowerProfileSettings
	// ReadOnly: the host is not on the image, so nothing is written.
	ReadOnly bool `json:"read_only"`
	// Driver is the cpufreq driver; empty without CPU frequency scaling.
	Driver    string         `json:"driver"`
	Governors []string       `json:"governors"`
	Tunables  []PowerTunable `json:"tunables"`
	Idle      PowerIdle      `json:"idle"`
	Blockers  []PowerBlocker `json:"blockers"`
}
