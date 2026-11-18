package system

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode"

	"homelab-ngx.com/core/internal/config"
)

// StackUnit is a stack's service unit as the Logs page reads it.
type StackUnit struct {
	// Name is the host's name for it, "<stack>-<unit>".
	Name string
	// Unit is its name inside the stack.
	Unit string
	// JournalDir is the stack's persistent journal on the host.
	JournalDir string
}

// BackupUnit is the unit a backup's runs are logged under.
type BackupUnit struct {
	// Name is the backup's name.
	Name string
	Unit string
}

// StackUnits lists the stacks' service units and the backups' units.
type StackUnits interface {
	LogUnits(ctx context.Context) []StackUnit
	BackupLogUnits(ctx context.Context) []BackupUnit
}

// logUnit is one entry of the Logs page's unit list and where its journal is.
type logUnit struct {
	ID         string
	Label      string
	Group      LogViewerUnitGroup
	Unit       string
	JournalDir string // empty: the host journal
}

// logUnits lists every core unit, backup job and stack unit for the Logs page
// picker: grouped in that order, sorted by label within a group.
func (s *System) logUnits(ctx context.Context, snap *config.Snapshot) []logUnit {
	var stackUnits []StackUnit
	var backupUnits []BackupUnit
	if s.stacks != nil {
		stackUnits = s.stacks.LogUnits(ctx)
		backupUnits = s.stacks.BackupLogUnits(ctx)
	}
	return logViewerUnits(snap, backupUnits, stackUnits)
}

func logViewerUnits(snap *config.Snapshot, backupUnits []BackupUnit, stackUnits []StackUnit) []logUnit {
	units := make([]logUnit, 0, len(snap.Nix.CoreServices)+len(backupUnits)+len(stackUnits))
	add := func(g LogViewerUnitGroup, entries []logUnit) {
		slices.SortStableFunc(entries, func(a, b logUnit) int { return cmp.Compare(a.Label, b.Label) })
		for _, e := range entries {
			e.Group = g
			units = append(units, e)
		}
	}
	host := func(names []string) []logUnit {
		out := make([]logUnit, 0, len(names))
		for _, unit := range names {
			id := strings.TrimSuffix(unit, ".service")
			out = append(out, logUnit{ID: id, Label: titleCase(id), Unit: unit})
		}
		return out
	}
	backups := make([]logUnit, 0, len(backupUnits))
	for _, b := range backupUnits {
		backups = append(backups, logUnit{ID: strings.TrimSuffix(b.Unit, ".service"), Label: b.Name, Unit: b.Unit})
	}
	stacks := make([]logUnit, 0, len(stackUnits))
	for _, u := range stackUnits {
		id := strings.TrimSuffix(u.Name, ".service")
		stacks = append(stacks, logUnit{ID: id, Label: titleCase(id), Unit: u.Unit, JournalDir: u.JournalDir})
	}
	add(LogViewerUnitGroupCore, host(snap.Nix.CoreServices))
	add(LogViewerUnitGroupBackup, backups)
	add(LogViewerUnitGroupStack, stacks)
	return units
}

// titleCase turns a kebab/snake-case name into title case:
// "immich-server" -> "Immich Server", "b2_backup_appdata" -> "B2 Backup Appdata".
func titleCase(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}
