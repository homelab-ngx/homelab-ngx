package sysupdates

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Generation is one recorded NixOS system generation and what changed from
// the generation recorded before it. Persisted, because the profile links
// (and their store paths) are garbage-collected.
type Generation struct {
	Number       int            `json:"number"`
	Time         time.Time      `json:"time"`
	System       string         `json:"system"`
	NixosVersion string         `json:"nixos_version"`
	Baseline     bool           `json:"baseline,omitempty"`
	Changes      []UpdateChange `json:"changes"`
	// Explicit package names (both sides of the diff) → tier; nil if
	// recorded before tiers were and the store paths are gone.
	Tiers map[string]string `json:"tiers,omitempty"`
}

// Query is a timeline request.
type Query struct {
	Q      string // case-insensitive substring
	Source string // "" | manual | rollback
	Page   int    // 1-based
	Days   int    // days per page
	Loc    *time.Location
}

// buildEvents turns generations into timeline events, newest first.
func buildEvents(gens []Generation, current string) []UpdateEvent {
	sort.Slice(gens, func(i, j int) bool { return gens[i].Number < gens[j].Number })
	seen := map[string]bool{}
	events := make([]UpdateEvent, 0, len(gens))

	for _, g := range gens {
		ev := UpdateEvent{
			Id:           fmt.Sprintf("gen-%d", g.Number),
			Time:         g.Time,
			Source:       "manual",
			Generation:   g.Number,
			System:       g.System,
			NixosVersion: g.NixosVersion,
			Baseline:     g.Baseline,
			Current:      current != "" && g.System == current,
		}
		// Switching to a system that was live before is a rollback.
		if seen[g.System] {
			ev.Source = "rollback"
		}
		seen[g.System] = true
		for _, c := range g.Changes {
			if c.Tier = g.Tiers[c.Name]; c.Tier == "" {
				ev.Dependencies++
				continue
			}
			ev.Changes = append(ev.Changes, c)
		}
		ev.TotalChanges = len(ev.Changes)
		events = append(events, ev)
	}

	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.After(events[j].Time) })
	return events
}

// filterEvent applies q to one event. ok=false drops the event; otherwise
// the returned event carries only the matching changes.
func filterEvent(ev UpdateEvent, q Query) (UpdateEvent, bool) {
	if q.Source != "" && string(ev.Source) != q.Source {
		return ev, false
	}

	changes := ev.Changes
	if needle := strings.ToLower(strings.TrimSpace(q.Q)); needle != "" && !eventMatches(ev, needle) {
		changes = keep(changes, func(c UpdateChange) bool {
			return strings.Contains(strings.ToLower(c.Name+" "+c.From+" "+c.To), needle)
		})
		if len(changes) == 0 {
			return ev, false
		}
	}
	ev.Changes = changes
	return ev, true
}

// eventMatches is the event-level search: a hit keeps all its changes.
func eventMatches(ev UpdateEvent, needle string) bool {
	hay := []string{string(ev.Source), ev.NixosVersion}
	if ev.Generation > 0 {
		hay = append(hay, fmt.Sprintf("#%d", ev.Generation))
	}
	return strings.Contains(strings.ToLower(strings.Join(hay, "\n")), needle)
}

func keep(cs []UpdateChange, f func(UpdateChange) bool) []UpdateChange {
	out := make([]UpdateChange, 0, len(cs))
	for _, c := range cs {
		if f(c) {
			out = append(out, c)
		}
	}
	return out
}

// paginate filters the events and pages them by local calendar day.
func paginate(events []UpdateEvent, q Query) UpdateTimeline {
	if q.Loc == nil {
		q.Loc = time.Local
	}
	if q.Days <= 0 {
		q.Days = 7
	}
	var days []UpdateDay
	total := 0
	for _, ev := range events {
		ev, ok := filterEvent(ev, q)
		if !ok {
			continue
		}
		if ev.Changes == nil {
			ev.Changes = []UpdateChange{}
		}
		total++
		date := ev.Time.In(q.Loc).Format("2006-01-02")
		if n := len(days); n == 0 || days[n-1].Date != date {
			days = append(days, UpdateDay{Date: date})
		}
		days[len(days)-1].Events = append(days[len(days)-1].Events, ev)
	}

	pages := (len(days) + q.Days - 1) / q.Days
	page := min(max(q.Page, 1), max(pages, 1))
	lo := min((page-1)*q.Days, len(days))
	hi := min(lo+q.Days, len(days))
	out := days[lo:hi]
	if out == nil {
		out = []UpdateDay{}
	}
	return UpdateTimeline{Days: out, Page: page, Pages: pages, TotalDays: len(days), TotalEvents: total}
}
