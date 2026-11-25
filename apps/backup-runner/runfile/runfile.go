// Package runfile is the contract between the daemon and the backup runner:
// the spec the daemon writes before a run and the state the runner writes
// while it works. The daemon imports this package; nothing else is shared.
//
// The runner never opens the daemon's database. It reads one Spec, deletes
// it (it holds secrets), and rewrites one State file atomically after every
// event, so the file is complete at any moment the runner might die.
package runfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Version is the format of both files. A runner refuses a spec of another
// version; the daemon treats a state of another version as unreadable.
const Version = 1

// Destination kinds. The daemon turns an S3 destination into an rclone one
// (a generated `type = s3` section), so the runner knows two.
const (
	KindRclone = "rclone"
	KindRestic = "restic"
)

// Item kinds.
const (
	ItemDestination = "destination"
	ItemFolder      = "folder"
)

// Results. A run ends success, partial, failure or cancelled; an item also
// passes through pending and running, and may be skipped. An item that was
// not done when the run was stopped is cancelled.
const (
	ResultPending   = "pending"
	ResultRunning   = "running"
	ResultSuccess   = "success"
	ResultPartial   = "partial"
	ResultFailure   = "failure"
	ResultSkipped   = "skipped"
	ResultCancelled = "cancelled"
)

// Spec is one run's plan, secrets included. It is written 0600 on a tmpfs
// and deleted by the runner as soon as it is read.
type Spec struct {
	Version int    `json:"version"`
	RunID   int64  `json:"run_id"`
	Backup  string `json:"backup"`

	// Bwlimit is an upload cap in rclone's notation ("4M"); empty for none.
	Bwlimit string `json:"bwlimit,omitempty"`
	// Transfers is the number of parallel transfers; 0 keeps the tool's default.
	Transfers int `json:"transfers,omitempty"`

	// PoolMounts are mountpoints that are empty directories when their pool is
	// not mounted. A folder under one that is not mounted fails.
	PoolMounts []string `json:"pool_mounts,omitempty"`

	// WorkDir is a private writable directory (rclone config, password files).
	WorkDir string `json:"work_dir"`
	// CacheDir is restic's cache directory.
	CacheDir string `json:"cache_dir,omitempty"`

	// CheckOnly only checks that each destination can be reached: one
	// destination item each, no folders, no restic init and no prune.
	CheckOnly bool `json:"check_only,omitempty"`
	// Preview compares every folder with its destination and changes nothing
	// there. The runner does it only when it is also started with --preview,
	// so a runner that does not know the mode refuses instead of copying.
	Preview bool `json:"preview,omitempty"`

	Destinations []Destination `json:"destinations"`
	Folders      []Folder      `json:"folders"`
}

// Destination is where the folders are copied.
type Destination struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`

	// rclone: Config is the text of an rclone config file holding one section,
	// Remote the base every folder is copied under ("name:bucket/prefix").
	Config string `json:"config,omitempty"`
	Remote string `json:"remote,omitempty"`
	// DeleteRemoved removes files at the destination that are gone on the host
	// or left out by a skip pattern (rclone sync --delete-excluded); otherwise
	// they stay (rclone copy).
	DeleteRemoved bool `json:"delete_removed,omitempty"`
	// Bucket, when set, is the part of Remote that must exist already
	// ("name:bucket"). A missing folder under it is fine: the first run makes
	// it. A missing bucket is not, since nothing creates it.
	Bucket string `json:"bucket,omitempty"`

	// restic: Env holds the backend's credentials. Init creates the repository
	// when it does not exist; without it a missing repository is a failure.
	Repository string            `json:"repository,omitempty"`
	Password   string            `json:"password,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Init       bool              `json:"init,omitempty"`
	Keep       Keep              `json:"keep,omitzero"`
}

// Keep is a restic retention rule; all zero keeps every snapshot.
type Keep struct {
	Daily   int `json:"daily,omitempty"`
	Weekly  int `json:"weekly,omitempty"`
	Monthly int `json:"monthly,omitempty"`
	Yearly  int `json:"yearly,omitempty"`
}

// Folder is one directory to copy to every destination.
type Folder struct {
	Label string `json:"label"`
	// Path is the absolute directory on the host.
	Path string `json:"path"`
	// RemotePath is where it lands under an rclone destination's Remote.
	RemotePath string `json:"remote_path"`
	// Excludes are anchored patterns relative to Path ("/cache/**",
	// "**/thumbs/**"), using only literals, *, ** and ?.
	Excludes []string `json:"excludes,omitempty"`
	// Files, when set, limits the copy to these patterns relative to Path
	// (same syntax as Excludes); everything else is left out.
	Files []string `json:"files,omitempty"`
	// SizeOnly makes rclone treat a file as unchanged when its size is,
	// ignoring its modification time. restic ignores it.
	SizeOnly bool `json:"size_only,omitempty"`
	// Fail, when set, records the folder as a failure with this error on every
	// destination without copying it (its stack's prepare unit failed).
	Fail string `json:"fail,omitempty"`
	// Note is carried to the folder's items unchanged.
	Note string `json:"note,omitempty"`
}

// State is a run as far as it has come.
type State struct {
	Version    int        `json:"version"`
	RunID      int64      `json:"run_id"`
	StartedAt  time.Time  `json:"started_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Result is empty while the run is in flight.
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	// Preview marks the state of a preview: nothing was copied.
	Preview bool `json:"preview,omitempty"`

	Items    []Item    `json:"items"`
	Progress *Progress `json:"progress,omitempty"`
}

// Final reports whether the runner finished and said how.
func (s *State) Final() bool { return s.Result != "" }

// Item is the outcome of one destination (its check, init and prune) or of
// one folder on one destination.
type Item struct {
	Kind          string `json:"kind"`
	DestinationID int64  `json:"destination_id"`
	Destination   string `json:"destination"`
	Label         string `json:"label,omitempty"`
	Path          string `json:"path,omitempty"`
	Result        string `json:"result"`
	// Files and Bytes are what this run sent, not the size of the folder.
	Files      int64      `json:"files"`
	Bytes      int64      `json:"bytes"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Note       string     `json:"note,omitempty"`
	// Preview is what a run would do to this folder; set by a preview only.
	Preview *Preview `json:"preview,omitempty"`
}

// SampleSize is how many paths of each kind a preview keeps.
const SampleSize = 100

// Preview is what a run would do to one folder at one destination. The
// samples hold the first SampleSize paths of their kind, relative to the
// folder.
type Preview struct {
	// New files are not at the destination; Changed ones are, and differ.
	New          int64 `json:"new"`
	NewBytes     int64 `json:"new_bytes"`
	Changed      int64 `json:"changed"`
	ChangedBytes int64 `json:"changed_bytes"`
	Unchanged    int64 `json:"unchanged"`
	// Extra files are only at the destination. Deletes says whether a run
	// removes them or leaves them.
	Extra   int64 `json:"extra"`
	Deletes bool  `json:"deletes"`
	// Errors counts the files that could not be compared.
	Errors int64 `json:"errors,omitempty"`

	NewSample     []string `json:"new_sample,omitempty"`
	ChangedSample []string `json:"changed_sample,omitempty"`
	ExtraSample   []string `json:"extra_sample,omitempty"`
}

// Progress is the copy in flight.
type Progress struct {
	Destination string `json:"destination"`
	Folder      string `json:"folder"`
	BytesDone   int64  `json:"bytes_done"`
	// BytesTotal is 0 while the tool is still counting.
	BytesTotal int64 `json:"bytes_total"`
	FilesDone  int64 `json:"files_done"`
	FilesTotal int64 `json:"files_total"`
	// Speed is bytes per second.
	Speed float64 `json:"speed"`
}

// ReadSpec loads a spec and checks its version.
func ReadSpec(path string) (*Spec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if s.Version != Version {
		return nil, fmt.Errorf("spec version %d, this runner reads %d", s.Version, Version)
	}
	return &s, nil
}

// WriteSpec writes a spec readable by its owner only.
func WriteSpec(path string, s *Spec) error { return writeAtomic(path, s, 0o600) }

// ReadState loads a state file. A missing file is os.ErrNotExist.
func ReadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.Version != Version {
		return nil, fmt.Errorf("state version %d, this build reads %d", s.Version, Version)
	}
	return &s, nil
}

// WriteState replaces the state file in one rename, so a reader never sees
// half of it.
func WriteState(path string, s *State) error { return writeAtomic(path, s, 0o600) }

func writeAtomic(path string, v any, mode os.FileMode) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
