package notifications

import (
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// EventCategory groups notification events so channels can filter them cleanly.
type EventCategory string

const (
	CategoryUpdates  EventCategory = "updates"
	CategoryStacks   EventCategory = "stacks"
	CategoryBackups  EventCategory = "backups"
	CategoryServices EventCategory = "services"
	CategorySecurity EventCategory = "security"
)

// AllCategories lists every category in UI and normalization order.
var AllCategories = []EventCategory{
	CategoryUpdates,
	CategoryStacks,
	CategoryBackups,
	CategoryServices,
	CategorySecurity,
}

// Schema documents EventCategory as its enum.
func (EventCategory) Schema(huma.Registry) *huma.Schema {
	return enumSchema(CategoryUpdates, CategoryStacks, CategoryBackups, CategoryServices, CategorySecurity)
}

// ChannelMinSeverity selects which severities leave the host on a channel.
type ChannelMinSeverity string

const (
	MinSeverityAll   ChannelMinSeverity = "all"
	MinSeverityAlert ChannelMinSeverity = "alert"
)

// Schema documents ChannelMinSeverity as its enum.
func (ChannelMinSeverity) Schema(huma.Registry) *huma.Schema {
	return enumSchema(MinSeverityAll, MinSeverityAlert)
}

// ChannelFilter controls which non-problem notifications a channel sends
// externally. Everything still goes to the daemon log and the dashboard tray,
// and open/resolved problem notifications always bypass the filter.
type ChannelFilter struct {
	MinSeverity ChannelMinSeverity `json:"min_severity,omitempty" doc:"Minimum severity sent externally: all (alerts and info) or alert (alerts only). Open problems are always sent."`
	Muted       []EventCategory    `json:"muted,omitempty" doc:"Event kinds held back from this channel (updates, stacks, backups, services, security). Open problems are always sent."`
}

// Validate checks that every filter setting is known.
func (f ChannelFilter) Validate() error {
	if f.MinSeverity != "" && f.MinSeverity != MinSeverityAll && f.MinSeverity != MinSeverityAlert {
		return invalidf("filter.min_severity: all or alert")
	}
	for _, c := range f.Muted {
		if !slices.Contains(AllCategories, c) {
			return invalidf("filter.muted: unknown kind %q", c)
		}
	}
	return nil
}

// Normalize returns f with default min_severity ("all") and a canonical
// non-nil Muted slice (deduped and sorted in AllCategories order).
func (f ChannelFilter) Normalize() ChannelFilter {
	out := ChannelFilter{
		MinSeverity: f.MinSeverity,
		Muted:       []EventCategory{},
	}
	if out.MinSeverity == "" {
		out.MinSeverity = MinSeverityAll
	}
	for _, cat := range AllCategories {
		if slices.Contains(f.Muted, cat) {
			out.Muted = append(out.Muted, cat)
		}
	}
	return out
}

// Allows reports whether a notification should be delivered to a channel
// with this filter. Open/resolved problem alerts (isProblem) are always sent.
func (f ChannelFilter) Allows(cat EventCategory, sev Severity, isProblem bool) bool {
	if isProblem {
		return true
	}
	if f.MinSeverity == MinSeverityAlert && sev != Alert {
		return false
	}
	if cat != "" && slices.Contains(f.Muted, cat) {
		return false
	}
	return true
}

var eventCategoryPrefixes = []struct {
	prefix   string
	category EventCategory
}{
	{"core.", CategoryUpdates},
	{"host.", CategoryUpdates},
	{"stack.", CategoryStacks},
	{"backup.", CategoryBackups},
	{"service.", CategoryServices},
	{"storage.", CategoryServices},
	{"doctor.", CategoryServices},
	{"security.", CategorySecurity},
}

var keyCategoryPrefixes = []struct {
	prefix   string
	category EventCategory
}{
	{"image-release:", CategoryUpdates},
	{"image-boot:", CategoryUpdates},
	{"update:", CategoryUpdates},
	{"core-deploy:", CategoryUpdates},
	{"host-rebuild:", CategoryUpdates},
	{"host:", CategoryUpdates},
	{"stack:", CategoryStacks},
	{"stack-update:", CategoryStacks},
	{"stack-install:", CategoryStacks},
	{"stack-rollback:", CategoryStacks},
	{"backup:", CategoryBackups},
	{"backup-stale:", CategoryBackups},
	{"backup-coverage:", CategoryBackups},
	{"backup-stopped:", CategoryBackups},
	{"unit:", CategoryServices},
	{"churn:", CategoryServices},
	{"daemon-crash", CategoryServices},
	{"disk:", CategoryServices},
	{"disk-remove:", CategoryServices},
	{"audit:", CategoryServices},
	{"security:", CategorySecurity},
}

// CategoryFor maps an event identifier (or fallback key) to its filter category.
// Returns "" when the notification does not belong to a known category.
func CategoryFor(event, key string) EventCategory {
	for _, p := range eventCategoryPrefixes {
		if strings.HasPrefix(event, p.prefix) {
			return p.category
		}
	}
	for _, p := range keyCategoryPrefixes {
		if strings.HasPrefix(key, p.prefix) {
			return p.category
		}
	}
	return ""
}
