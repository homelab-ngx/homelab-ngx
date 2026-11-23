package services

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// ServiceInfo is the runtime state of one unit.
type ServiceInfo struct {
	ActiveState string `json:"active_state"`
	Description string `json:"description"`

	// Masked The operator disabled the unit: it stays stopped and cannot be started until it is enabled again.
	Masked bool   `json:"masked" doc:"The operator disabled the unit: it stays stopped, across restarts and updates of its stack, and cannot be started until it is enabled again. Its stack's health check leaves it out. Always false for a core unit."`
	Name   string `json:"name"`

	// Stack The stack the unit runs in; only these units can be started, stopped and restarted. Absent for a core unit on the host.
	Stack string `json:"stack,omitempty" doc:"The stack the unit runs in; only these units can be started, stopped and restarted. Absent for a core unit on the host."`

	// StartedAt Null when the unit is not active.
	StartedAt *time.Time      `json:"started_at" doc:"Null when the unit is not active."`
	SubState  string          `json:"sub_state"`
	Type      ServiceInfoType `json:"type"`
	UnitName  string          `json:"unit_name"`

	// Usage CPU and memory of a running stack unit. Absent otherwise.
	Usage *Usage `json:"usage,omitempty" doc:"CPU and memory of a running stack unit. Absent otherwise."`

	// DiskBytes Bytes the unit's user owns in its stack's root, measured weekly and after each update or rollback.
	DiskBytes *int64 `json:"disk_bytes,omitempty" doc:"Bytes the unit's user owns in its stack's root, measured weekly and after each update or rollback. It leaves out /nix/store and the host folders bound into the stack. Absent for a unit that runs as root and before the first measurement."`
}

// ServiceInfoType is the kind of unit; every unit is Native now.
type ServiceInfoType string

// Native is a systemd unit.
const Native ServiceInfoType = "Native"

// Schema documents ServiceInfoType as its enum.
func (ServiceInfoType) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{string(Native)}}
}

// Usage is the CPU and memory of a running stack unit.
type Usage struct {
	// CpuPercent CPU over the last sample, in percent of one core (top-style: 250 is 2.5 cores).
	CpuPercent  float32 `json:"cpu_percent" doc:"CPU over the last sample, in percent of one core (top-style: 250 is 2.5 cores)."`
	MemoryBytes int64   `json:"memory_bytes"`
}
