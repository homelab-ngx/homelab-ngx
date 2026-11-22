package drafts

import "time"

// The API's view of drafts. Huma names each schema after its Go type.

// DraftFile is one file of a draft's repo.
type DraftFile struct {
	Path    string `json:"path" doc:"The file's path in the repo, with / between folders."`
	Size    int64  `json:"size" doc:"Its size in bytes."`
	Binary  bool   `json:"binary" doc:"Not UTF-8 text, or larger than 256 KiB: listed and kept, but not edited here, and content is empty."`
	Content string `json:"content" doc:"The file's text; empty when binary is true."`
}

// DraftChange is one change to a draft's files: a file written whole, or a
// file deleted.
type DraftChange struct {
	Path    string  `json:"path" maxLength:"256" doc:"The file's path in the repo, with / between folders; nothing under .git."`
	Content *string `json:"content,omitempty" maxLength:"262144" doc:"The file's whole new text; a new path makes the file. Absent with delete."`
	Delete  bool    `json:"delete,omitempty" doc:"Delete the file. homelab-stack.json and flake.nix cannot be deleted."`
}

// DraftSummary is a draft in the list.
type DraftSummary struct {
	Name        string    `json:"name" doc:"The draft's name; the name it is installed under."`
	Rev         string    `json:"rev" doc:"The commit of the last save."`
	SavedAt     time.Time `json:"saved_at" doc:"When it was last saved."`
	InstalledAs []string  `json:"installed_as" doc:"The installed stacks built from this draft; empty when none is."`
	ForkedFrom  string    `json:"forked_from,omitempty" doc:"The source of the stack the draft was forked from; absent for one started from the template."`
	ForkedRev   string    `json:"forked_rev,omitempty" doc:"The commit of that stack the draft started from."`
}

// Draft is a stack written in the dashboard: its files at the last save, and
// the last check and build.
type Draft struct {
	Name        string      `json:"name" doc:"The draft's name; the name it is installed under."`
	Source      string      `json:"source" doc:"The draft's repo as a stack source (git+file://), for POST /stacks."`
	Rev         string      `json:"rev" doc:"The commit of the last save."`
	SavedAt     time.Time   `json:"saved_at" doc:"When it was last saved."`
	CreatedAt   time.Time   `json:"created_at"`
	Files       []DraftFile `json:"files" doc:"Every file of the repo at the last save, by path."`
	InstalledAs []string    `json:"installed_as" doc:"The installed stacks built from this draft; empty when none is. A save shows there as an update."`
	Check       *DraftCheck `json:"check,omitempty" doc:"The last check, absent before the first."`
	Build       *DraftBuild `json:"build,omitempty" doc:"The running or last build, absent before the first."`
	ForkedFrom  string      `json:"forked_from,omitempty" doc:"The source of the stack the draft was forked from; absent for one started from the template."`
	ForkedRev   string      `json:"forked_rev,omitempty" doc:"The commit of that stack the draft started from."`
}

// DraftCreate starts a draft.
type DraftCreate struct {
	Name string `json:"name" doc:"1-12 characters of a-z, 0-9 and '-', starting with a letter. It is also the stack's name once installed."`
	From string `json:"from,omitempty" doc:"An installed stack to fork: the draft starts from a copy of its approved commit, and the stack stays on its own source. Absent: the template."`
}

// DraftSave saves changes to a draft's files as a commit.
type DraftSave struct {
	Changes []DraftChange `json:"changes" maxItems:"500" doc:"The changes, applied in order to the saved files."`
	Message string        `json:"message,omitempty" maxLength:"500" doc:"The commit message; default \"Edit in dashboard\"."`
}

// DraftCheckIn is what to check.
type DraftCheckIn struct {
	Changes *[]DraftChange `json:"changes,omitempty" maxItems:"500" doc:"Unsaved changes to check on top of the saved files; absent checks the saved ones."`
}

// DraftProblem is one thing a check found.
type DraftProblem struct {
	File    string `json:"file" doc:"The path of the file it is in."`
	Message string `json:"message" doc:"What is wrong, with the field's path for a manifest error."`
}

// DraftCheck is a check's result: the manifest parsed and validated as an
// install would, flake.nix present, and the Nix files parsed.
type DraftCheck struct {
	CheckedAt time.Time      `json:"checked_at"`
	Saved     bool           `json:"saved" doc:"The saved files were checked, not an unsaved edit."`
	Rev       string         `json:"rev,omitempty" doc:"The commit checked, when the saved files were."`
	OK        bool           `json:"ok"`
	Problems  []DraftProblem `json:"problems"`
}

// DraftBuildResult is how a build ended.
type DraftBuildResult string

const (
	BuildRunning     DraftBuildResult = "running"
	BuildBuilt       DraftBuildResult = "built"
	BuildFailed      DraftBuildResult = "failed"
	BuildInterrupted DraftBuildResult = "interrupted"
)

// DraftBuild is a build of a saved commit, the way an install builds it.
type DraftBuild struct {
	Rev        string           `json:"rev" doc:"The commit built."`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
	Result     DraftBuildResult `json:"result" enum:"running,built,failed,interrupted" doc:"running, built, failed, or interrupted (the daemon stopped during it)."`
	System     string           `json:"system,omitempty" doc:"The built system's store path."`
	Error      string           `json:"error,omitempty" doc:"Why it failed: nix's error messages, or the step that failed."`
	Log        string           `json:"log" doc:"Everything the build printed, its head and tail when it is long."`
}
