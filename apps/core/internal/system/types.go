package system

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Version is the daemon's build identity.
type Version struct {
	Commit  string `json:"commit"`
	Version string `json:"version"`
}

// LogViewerConfig is the Logs page's unit list.
type LogViewerConfig struct {
	// Units Grouped core, then backup, then stack; sorted by label within a group.
	Units []LogViewerUnit `json:"units" doc:"Grouped core, then backup, then stack; sorted by label within a group."`
}

// LogViewerUnit is one unit the Logs page can show.
type LogViewerUnit struct {
	Group LogViewerUnitGroup `json:"group"`

	// Id Unit name without the .service suffix (a stack unit as <stack>-<unit>); the Logs page ?service= value and the /logs unit.
	Id    string `json:"id" doc:"Unit name without the .service suffix (a stack unit as <stack>-<unit>); the Logs page ?service= value and the /logs unit."`
	Label string `json:"label"`
}

// LogViewerUnitGroup is the group a unit is listed under.
type LogViewerUnitGroup string

const (
	LogViewerUnitGroupBackup LogViewerUnitGroup = "backup"
	LogViewerUnitGroupCore   LogViewerUnitGroup = "core"
	LogViewerUnitGroupStack  LogViewerUnitGroup = "stack"
)

// Schema documents LogViewerUnitGroup as its enum.
func (LogViewerUnitGroup) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{
		string(LogViewerUnitGroupCore), string(LogViewerUnitGroupBackup), string(LogViewerUnitGroupStack),
	}}
}

// LogPage is one page of journal lines.
type LogPage struct {
	// Entries Newest first.
	Entries []LogEntry `json:"entries" doc:"Newest first."`
	More    bool       `json:"more"`
}

// LogEntry is one journal line.
type LogEntry struct {
	// Cursor Journal cursor of this line.
	Cursor  string `json:"cursor" doc:"Journal cursor of this line."`
	Message string `json:"message"`

	// Priority Syslog priority: 0 emerg … 3 error, 4 warning, 5 notice, 6 info, 7 debug.
	Priority int       `json:"priority" doc:"Syslog priority: 0 emerg … 3 error, 4 warning, 5 notice, 6 info, 7 debug."`
	Time     time.Time `json:"time"`
}

// GetLogsParams is the query of GET /logs.
type GetLogsParams struct {
	// Unit A unit id from `/log-viewer-config`.
	Unit  string `query:"unit" required:"true" doc:"A unit id from /log-viewer-config."`
	Lines int    `query:"lines" minimum:"1" maximum:"500" default:"50"`

	// Before Journal cursor; return lines older than it.
	Before string `query:"before" doc:"Journal cursor; return lines older than it."`

	// After Journal cursor; return lines newer than it.
	After string `query:"after" doc:"Journal cursor; return lines newer than it."`

	// Priority Highest syslog priority to include (3 error, 4 warning, 6 info, 7 debug).
	Priority int `query:"priority" minimum:"0" maximum:"7" doc:"Highest syslog priority to include (3 error, 4 warning, 6 info, 7 debug)."`

	// Q Text the message must contain, matched literally and case-insensitively.
	Q string `query:"q" doc:"Text the message must contain, matched literally and case-insensitively."`
}
