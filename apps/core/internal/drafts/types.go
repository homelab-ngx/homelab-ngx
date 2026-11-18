package drafts

import "time"

// The API's view of drafts. Huma names each schema after its Go type.

// DraftFiles are a draft's two files.
type DraftFiles struct {
	Manifest string `json:"manifest" maxLength:"262144" doc:"homelab-stack.json: what the stack asks the host for."`
	Flake    string `json:"flake" maxLength:"262144" doc:"flake.nix: the stack's NixOS module, exported as nixosModules.default."`
}

// DraftSummary is a draft in the list.
type DraftSummary struct {
	Name        string    `json:"name" doc:"The draft's name; the name it is installed under."`
	Rev         string    `json:"rev" doc:"The commit of the last save."`
	SavedAt     time.Time `json:"saved_at" doc:"When it was last saved."`
	InstalledAs []string  `json:"installed_as" doc:"The installed stacks built from this draft; empty when none is."`
}

// Draft is a stack written in the dashboard: its files at the last save, and
// the last check and build.
type Draft struct {
	Name        string      `json:"name" doc:"The draft's name; the name it is installed under."`
	Source      string      `json:"source" doc:"The draft's repo as a stack source (git+file://), for POST /stacks."`
	Rev         string      `json:"rev" doc:"The commit of the last save."`
	SavedAt     time.Time   `json:"saved_at" doc:"When it was last saved."`
	CreatedAt   time.Time   `json:"created_at"`
	Files       DraftFiles  `json:"files" doc:"The files at the last save."`
	InstalledAs []string    `json:"installed_as" doc:"The installed stacks built from this draft; empty when none is. A save shows there as an update."`
	Check       *DraftCheck `json:"check,omitempty" doc:"The last check, absent before the first."`
	Build       *DraftBuild `json:"build,omitempty" doc:"The running or last build, absent before the first."`
}

// DraftCreate starts a draft.
type DraftCreate struct {
	Name string `json:"name" doc:"1-12 characters of a-z, 0-9 and '-', starting with a letter. It is also the stack's name once installed."`
}

// DraftSave saves a draft's files as a commit.
type DraftSave struct {
	Files   DraftFiles `json:"files"`
	Message string     `json:"message,omitempty" maxLength:"500" doc:"The commit message; default \"Edit in dashboard\"."`
}

// DraftCheckIn is what to check.
type DraftCheckIn struct {
	Files *DraftFiles `json:"files,omitempty" doc:"Unsaved files to check; absent checks the saved ones."`
}

// DraftProblem is one thing a check found.
type DraftProblem struct {
	File    string `json:"file" enum:"homelab-stack.json,flake.nix" doc:"The file it is in."`
	Message string `json:"message" doc:"What is wrong, with the field's path for a manifest error."`
}

// DraftCheck is a check's result: the manifest parsed and validated as an
// install would, and flake.nix present.
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
