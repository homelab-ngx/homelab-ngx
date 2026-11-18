package sysupdates

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// UpdateChange is one thing that moved between two system generations.
type UpdateChange struct {
	From string           `json:"from" doc:"Empty = added."`
	Kind UpdateChangeKind `json:"kind" enum:"upgrade,downgrade,added,removed,changed"`
	Name string           `json:"name"`
	Tier string           `json:"tier" doc:"Why a package counts as explicit: service or kernel."`
	To   string           `json:"to" doc:"Empty = removed."`
}

// UpdateChangeKind is how a change moved.
type UpdateChangeKind string

// UpdateChangeKind values.
const (
	Added     UpdateChangeKind = "added"
	Changed   UpdateChangeKind = "changed"
	Downgrade UpdateChangeKind = "downgrade"
	Removed   UpdateChangeKind = "removed"
	Upgrade   UpdateChangeKind = "upgrade"
)

// UpdateEvent is one timeline entry, a system generation.
type UpdateEvent struct {
	Baseline     bool              `json:"baseline,omitempty" doc:"First recorded generation; nothing to diff against."`
	Changes      []UpdateChange    `json:"changes"`
	Current      bool              `json:"current,omitempty" doc:"The running system."`
	Dependencies int               `json:"dependencies" doc:"Dependency version changes, not listed."`
	Generation   int               `json:"generation,omitempty"`
	Id           string            `json:"id"`
	NixosVersion string            `json:"nixos_version,omitempty"`
	Source       UpdateEventSource `json:"source" enum:"manual,rollback" doc:"rollback: a switch to a system that was live before; manual: any other deploy."`
	System       string            `json:"system,omitempty"`
	Time         time.Time         `json:"time"`
	TotalChanges int               `json:"total_changes" doc:"Before search filtering."`
}

// UpdateEventSource is how a generation came to be.
type UpdateEventSource string

// UpdateEventSource values.
const (
	UpdateEventSourceManual   UpdateEventSource = "manual"
	UpdateEventSourceRollback UpdateEventSource = "rollback"
)

// UpdateDay is a page's events for one local calendar date.
type UpdateDay struct {
	Date   string        `json:"date" pattern:"^\\d{4}-\\d{2}-\\d{2}$" doc:"YYYY-MM-DD."`
	Events []UpdateEvent `json:"events"`
}

// UpdateTimeline is one page of the generation timeline.
type UpdateTimeline struct {
	Days        []UpdateDay `json:"days"`
	Page        int         `json:"page" doc:"1-based."`
	Pages       int         `json:"pages"`
	TotalDays   int         `json:"total_days"`
	TotalEvents int         `json:"total_events"`
}

// NixOSRelease is the release the base nixpkgs (the flake's `nixpkgs` input) is on.
type NixOSRelease struct {
	Current string  `json:"current" doc:"e.g. 26.11; empty = unknown."`
	Follows *string `json:"follows" doc:"Branch the base follows; null = pinned to a revision."`
	Pinned  bool    `json:"pinned" doc:"Pinned to a pre-release revision; current is then read from the running NixOS version."`
}

// ReleaseUpgrade is a newer NixOS release the base can move to.
type ReleaseUpgrade struct {
	NotesUrl   string `json:"notes_url"`
	PreRelease bool   `json:"pre_release" doc:"The branch exists but the release date has not passed."`
	Release    string `json:"release"`
}

// SystemUpdateStatus is the System card.
type SystemUpdateStatus struct {
	CheckedAt      *time.Time      `json:"checked_at" doc:"Last release check; null = not yet."`
	Core           CoreStatus      `json:"core"`
	Generation     int             `json:"generation" doc:"Running system's generation; 0 = unknown."`
	RebootRequired bool            `json:"reboot_required" doc:"The running kernel/initrd differs from the installed system's."`
	Release        NixOSRelease    `json:"release"`
	Upgrade        *ReleaseUpgrade `json:"upgrade"`
}

// TransformSchema makes upgrade nullable: huma refuses the nullable tag on
// an object field, so the property becomes "ReleaseUpgrade or null".
func (SystemUpdateStatus) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	if p := s.Properties["upgrade"]; p != nil && p.Ref != "" {
		s.Properties["upgrade"] = &huma.Schema{
			Description: "A newer release; null = none.",
			OneOf:       []*huma.Schema{{Ref: p.Ref}, {Type: "null"}},
		}
	}
	return s
}
