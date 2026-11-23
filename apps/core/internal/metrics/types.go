package metrics

import (
	"math"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
)

var nan = math.NaN()

// Num is one figure of a point. NaN (no reading) is sent as null, and
// figures are rounded to one decimal: nothing on the page needs more.
type Num float64

// MarshalJSON writes null for NaN and a number rounded to one decimal.
func (n Num) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, math.Round(f*10)/10, 'f', -1, 64), nil
}

// UnmarshalJSON reads null as NaN.
func (n *Num) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = Num(nan)
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	*n = Num(f)
	return err
}

// Schema documents Num as a nullable number.
func (Num) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeNumber, Nullable: true}
}

// MetricSeries is one scope's figures over time, one column per figure, aligned
// with Frames.At. A null is a moment the scope has no reading (the stack was
// stopped, or has no network of its own).
type MetricSeries struct {
	// CPU is the share of the whole machine in percent: the stacks and the parts of core add up to the host.
	CPU []Num `json:"cpu" doc:"Share of the whole machine in percent; the stacks and the core parts (services, sessions, kernel) add up to the host."`
	// Memory in use in bytes, leaving out page cache the kernel can drop at once.
	Memory []Num `json:"memory" doc:"Bytes in use, leaving out page cache the kernel can drop at once."`
	// PressureCPU is the share of time some task waited for CPU, in percent.
	PressureCPU    []Num `json:"pressure_cpu" doc:"Share of time some task waited for CPU, in percent."`
	PressureMemory []Num `json:"pressure_memory" doc:"Share of time some task waited for memory, in percent."`
	PressureIO     []Num `json:"pressure_io" doc:"Share of time some task waited for disk, in percent."`
	DiskRead       []Num `json:"disk_read" doc:"Bytes per second."`
	DiskWrite      []Num `json:"disk_write" doc:"Bytes per second."`
	NetRx          []Num `json:"net_rx" doc:"Bytes per second received; null for the core parts and for a stack without its own network."`
	NetTx          []Num `json:"net_tx" doc:"Bytes per second sent."`
	// CPUMax and MemoryMax are the highest 2 s reading in each point (history only).
	CPUMax    []Num `json:"cpu_max,omitempty" doc:"Highest reading within each point (history only)."`
	MemoryMax []Num `json:"memory_max,omitempty" doc:"Highest reading within each point (history only)."`
}

// MetricFrames are points in time with every scope's figures at each.
type MetricFrames struct {
	// At is each point's time in unix seconds, oldest first.
	At []int64 `json:"at" doc:"Each point's time in unix seconds, oldest first."`
	// Step is the usual distance between points in seconds.
	Step int `json:"step" doc:"The usual distance between points in seconds."`
	// Host is the whole machine.
	Host MetricSeries `json:"host"`
	// Services, Sessions and Kernel are the host less every stack; they
	// carry CPU, memory and disk only. Services is system.slice (the daemon,
	// Caddy, sshd, journald and the rest) and systemd itself.
	Services MetricSeries `json:"services" doc:"The host's services, the homelab daemon among them, and systemd itself. CPU, memory and disk only."`
	// Sessions is user.slice: login sessions and the user services they run.
	Sessions MetricSeries `json:"sessions" doc:"Login sessions and the user services they run (user.slice), such as coding agents. CPU, memory and disk only."`
	// Kernel is what is left of the host: the kernel's own work and caches,
	// such as the file system's btree cache.
	Kernel MetricSeries            `json:"kernel" doc:"The rest of the host: the kernel's own work and caches, such as the file system's. CPU, memory and disk only."`
	Stacks map[string]MetricSeries `json:"stacks"`
	// TempCPU and TempDisk are the CPU package and NVMe temperatures in °C.
	TempCPU  []Num `json:"temp_cpu" doc:"CPU package temperature in °C."`
	TempDisk []Num `json:"temp_disk" doc:"System disk temperature in °C."`
	// PowerCPU is the CPU packages' draw in watts, from RAPL.
	PowerCPU []Num `json:"power_cpu" doc:"CPU package power in W, from RAPL; null where the host has none."`
}

// Info are facts about the host that do not change while it runs.
type HostInfo struct {
	Cores       int    `json:"cores"`
	MemoryTotal int64  `json:"memory_total" doc:"Bytes."`
	BootedAt    int64  `json:"booted_at" doc:"Unix seconds."`
	Model       string `json:"model" doc:"The machine's maker and model, or its motherboard's where the maker left the model unset; empty where the firmware says neither."`
	CPU         string `json:"cpu" doc:"The processor's model name; empty where /proc/cpuinfo has none."`
}

// MetricsSnapshot is the first event of the stream: the host's facts and the
// recent past, so a chart is full as soon as it opens.
type MetricsSnapshot struct {
	Info   HostInfo     `json:"info"`
	Frames MetricFrames `json:"frames"`
}

// MetricsSample is every later event of the stream: one new point.
type MetricsSample struct {
	Frames MetricFrames `json:"frames"`
}

// MetricsRange is how far back a history read goes.
type MetricsRange string

const (
	Range24h MetricsRange = "24h"
	Range7d  MetricsRange = "7d"
	Range30d MetricsRange = "30d"
)

// Schema documents MetricsRange as its enum.
func (MetricsRange) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{string(Range24h), string(Range7d), string(Range30d)}}
}

// History is the stored past at the resolution its range is read at.
type MetricsHistory struct {
	Frames MetricFrames `json:"frames"`
}
