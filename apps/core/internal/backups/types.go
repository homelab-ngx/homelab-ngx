package backups

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Exclude is one skip pattern with the reason it is skipped.
type Exclude struct {
	// Pattern Anchored at the folder: starts with / or **/ and uses only literals, *, ** and ?.
	Pattern string `json:"pattern" doc:"Anchored at the folder: starts with / or **/ and uses only literals, *, ** and ?."`
	Reason  string `json:"reason"`
}

// Keep is a restic destination's retention rule; all zero keeps everything.
type Keep struct {
	Daily   int `json:"daily" minimum:"0" maximum:"9999"`
	Weekly  int `json:"weekly" minimum:"0" maximum:"9999"`
	Monthly int `json:"monthly" minimum:"0" maximum:"9999"`
	Yearly  int `json:"yearly" minimum:"0" maximum:"9999"`
}

// DestinationKind is the kind of place a backup is copied to.
type DestinationKind string

const (
	KindS3     DestinationKind = "s3"
	KindRclone DestinationKind = "rclone"
	KindRestic DestinationKind = "restic"
)

// Schema documents DestinationKind as its enum.
func (DestinationKind) Schema(huma.Registry) *huma.Schema {
	return enumSchema(KindS3, KindRclone, KindRestic)
}

// S3Settings are an S3 destination's settings that are not secret.
type S3Settings struct {
	Endpoint    string `json:"endpoint" doc:"The provider's S3 endpoint, e.g. https://s3.eu-central-003.backblazeb2.com."`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix" doc:"Folder inside the bucket; empty for the bucket's root."`
	AccessKeyId string `json:"access_key_id"`
}

// RcloneSettings are an rclone destination's settings that are not secret.
type RcloneSettings struct {
	// Remote The remote's name, from the pasted config section.
	Remote string `json:"remote" doc:"The remote's name, from the pasted config section."`
	// Type The remote's rclone backend, from the pasted config section.
	Type string `json:"type" doc:"The remote's rclone backend, from the pasted config section."`
	Path string `json:"path" doc:"Bucket or folder on the remote that the backup is copied under."`
}

// ResticSettings are a restic destination's settings that are not secret.
type ResticSettings struct {
	Repository string `json:"repository" doc:"A restic repository, e.g. s3:https://host/bucket/path, b2:bucket:path or /mnt/disk/repo. No password in the URL."`
	// EnvNames The names of the backend credentials stored with the destination.
	EnvNames []string `json:"env_names" doc:"The names of the backend credentials stored with the destination."`
}

// Destination is where backups are copied. It never carries a secret.
type Destination struct {
	Id   int64           `json:"id"`
	Name string          `json:"name"`
	Kind DestinationKind `json:"kind"`

	S3     *S3Settings     `json:"s3,omitempty"`
	Rclone *RcloneSettings `json:"rclone,omitempty"`
	Restic *ResticSettings `json:"restic,omitempty"`

	// DeleteRemoved s3 and rclone: files removed on the host, or left out by a skip pattern, are removed at the destination too.
	DeleteRemoved bool `json:"delete_removed" doc:"s3 and rclone: files removed on the host, or left out by a skip pattern, are removed at the destination too."`
	// Keep restic: the snapshots to keep.
	Keep Keep `json:"keep" doc:"restic: the snapshots to keep."`
	// PasswordConfirmed restic: the operator confirmed the repository password is saved. A run refuses the destination until then.
	PasswordConfirmed bool `json:"password_confirmed" doc:"restic: the operator confirmed the repository password is saved. A run refuses the destination until then."`
	// UsedBy The names of the backups that copy to this destination.
	UsedBy    []string  `json:"used_by" doc:"The names of the backups that copy to this destination."`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// S3Input is an S3 destination as the operator enters it.
type S3Input struct {
	Endpoint    string `json:"endpoint" minLength:"1"`
	Region      string `json:"region,omitempty"`
	Bucket      string `json:"bucket" minLength:"1"`
	Prefix      string `json:"prefix,omitempty"`
	AccessKeyId string `json:"access_key_id" minLength:"1"`
	// SecretAccessKey Required on create; on update an absent or empty value keeps the stored one.
	SecretAccessKey string `json:"secret_access_key,omitempty" doc:"Required on create; on update an absent or empty value keeps the stored one."`
}

// RcloneInput is an rclone destination as the operator enters it.
type RcloneInput struct {
	// Config One section of an rclone config file ([name] and its lines). Required on create; on update an absent or empty value keeps the stored one.
	Config string `json:"config,omitempty" doc:"One section of an rclone config file ([name] and its lines). Required on create; on update an absent or empty value keeps the stored one."`
	Path   string `json:"path,omitempty"`
}

// ResticInput is a restic destination as the operator enters it.
type ResticInput struct {
	Repository string `json:"repository" minLength:"1"`
	// Password Only to add a repository that already exists. Leave it out and the daemon generates one, returned once by the create call.
	Password string `json:"password,omitempty" doc:"Only to add a repository that already exists. Leave it out and the daemon generates one, returned once by the create call."`
	// Env Backend credentials as environment variables (AWS_ACCESS_KEY_ID, B2_ACCOUNT_KEY, ...). On update an absent value keeps the stored ones.
	Env *map[string]string `json:"env,omitempty" doc:"Backend credentials as environment variables (AWS_ACCESS_KEY_ID, B2_ACCOUNT_KEY, ...). On update an absent value keeps the stored ones."`
}

// DestinationInput creates or replaces a destination. The member named by
// kind is required; a destination's kind never changes.
type DestinationInput struct {
	Name          string          `json:"name" minLength:"1" maxLength:"64"`
	Kind          DestinationKind `json:"kind"`
	S3            *S3Input        `json:"s3,omitempty"`
	Rclone        *RcloneInput    `json:"rclone,omitempty"`
	Restic        *ResticInput    `json:"restic,omitempty"`
	DeleteRemoved bool            `json:"delete_removed,omitempty"`
	Keep          Keep            `json:"keep,omitzero"`
}

// DestinationCreated is a new destination and, once, the password the
// daemon generated for a new restic repository.
type DestinationCreated struct {
	Destination Destination `json:"destination"`
	// GeneratedPassword Shown once. Without it the repository cannot be read; confirm it is saved before the first run.
	GeneratedPassword string `json:"generated_password,omitempty" doc:"Shown once. Without it the repository cannot be read; confirm it is saved before the first run."`
}

// DestinationTest is the outcome of trying a destination.
type DestinationTest struct {
	Ok     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// RevealRequest confirms a password reveal by repeating the destination's name.
type RevealRequest struct {
	Confirm string `json:"confirm" doc:"The destination's name."`
}

// DestinationPassword is a restic repository's password.
type DestinationPassword struct {
	Password string `json:"password"`
}

// SourceKind is what a backup source is.
type SourceKind string

const (
	SourceStack SourceKind = "stack"
	SourcePath  SourceKind = "path"
	SourceCore  SourceKind = "core"
)

// Schema documents SourceKind as its enum.
func (SourceKind) Schema(huma.Registry) *huma.Schema {
	return enumSchema(SourceStack, SourcePath, SourceCore)
}

// SourceInput is one thing a backup includes.
type SourceInput struct {
	Kind SourceKind `json:"kind"`
	// Stack kind stack: the stack.
	Stack string `json:"stack,omitempty" doc:"kind stack: the stack."`
	// Folder kind stack: the key of one of the stack's recommended folders.
	Folder string `json:"folder,omitempty" doc:"kind stack: the key of one of the stack's recommended folders."`
	// Path kind path: an absolute folder on the host.
	Path string `json:"path,omitempty" doc:"kind path: an absolute folder on the host."`
	// RemotePath Folder at an s3 or rclone destination. Absent mirrors the host path; an empty string is the destination's root.
	RemotePath *string `json:"remote_path,omitempty" doc:"Folder at an s3 or rclone destination. Absent mirrors the host path; an empty string is the destination's root."`
	// Excludes Extra skip patterns, on top of the ones a stack declares.
	Excludes []Exclude `json:"excludes,omitempty" doc:"Extra skip patterns, on top of the ones a stack declares."`
	// SizeOnly s3 and rclone: treat a file as unchanged when its size is, ignoring its modification time. For a copy that another tool uploaded.
	SizeOnly bool `json:"size_only,omitempty" doc:"s3 and rclone: treat a file as unchanged when its size is, ignoring its modification time. For a copy that another tool uploaded."`
}

// Source is a backup source with what it resolves to right now.
type Source struct {
	Kind       SourceKind `json:"kind"`
	Stack      string     `json:"stack"`
	Folder     string     `json:"folder"`
	Path       string     `json:"path"`
	RemotePath *string    `json:"remote_path"`
	Excludes   []Exclude  `json:"excludes"`
	SizeOnly   bool       `json:"size_only"`

	Label string `json:"label"`
	// HostPath The folder on the host; empty when it cannot be resolved.
	HostPath string `json:"host_path" doc:"The folder on the host; empty when it cannot be resolved."`
	// DeclaredExcludes kind stack: the skip patterns the stack declares.
	DeclaredExcludes []Exclude `json:"declared_excludes" doc:"kind stack: the skip patterns the stack declares."`
	// Problem Why the source cannot be backed up right now; empty when it can.
	Problem string `json:"problem" doc:"Why the source cannot be backed up right now; empty when it can."`
}

// DestinationRef names a destination of a backup.
type DestinationRef struct {
	Id   int64           `json:"id"`
	Name string          `json:"name"`
	Kind DestinationKind `json:"kind"`
	// Ready False for a restic destination whose password is not confirmed yet.
	Ready bool `json:"ready" doc:"False for a restic destination whose password is not confirmed yet."`
}

// Backup is a named set of sources copied to its destinations on a schedule.
type Backup struct {
	Id      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Schedule 5-field cron expression; empty runs only by hand.
	Schedule string `json:"schedule" doc:"5-field cron expression; empty runs only by hand."`
	// Bwlimit Upload limit, e.g. 4M (bytes per second with a K, M or G suffix); empty for none.
	Bwlimit string `json:"bwlimit" doc:"Upload limit, e.g. 4M (bytes per second with a K, M or G suffix); empty for none."`
	// Transfers Parallel transfers; 0 keeps the tool's default.
	Transfers int `json:"transfers" doc:"Parallel transfers; 0 keeps the tool's default."`
	// HealthcheckUrl Pinged at start, on success and, with /fail, on failure.
	HealthcheckUrl string `json:"healthcheck_url" doc:"Pinged at start, on success and, with /fail, on failure."`

	Sources      []Source         `json:"sources"`
	Destinations []DestinationRef `json:"destinations"`

	// LastRun The newest run; null if it never ran.
	LastRun *BackupRun `json:"last_run" doc:"The newest run; null if it never ran."`
	// RecentRuns The runs started in the last 15 days, oldest first.
	RecentRuns []BackupRun `json:"recent_runs" doc:"The runs started in the last 15 days, oldest first."`
	// LastSuccess When the newest fully successful run ended.
	LastSuccess *time.Time `json:"last_success" doc:"When the newest fully successful run ended."`
	// NextRun Null when disabled or unscheduled.
	NextRun *time.Time `json:"next_run" doc:"Null when disabled or unscheduled."`
	// Overdue No successful run within two schedule intervals (counted from daemon start when it never succeeded): the rule behind the backup-stale notification.
	Overdue bool `json:"overdue" doc:"No successful run within two schedule intervals (counted from daemon start when it never succeeded): the rule behind the backup-stale notification."`
	// Running A run is in flight; last_run is that run.
	Running   bool      `json:"running" doc:"A run is in flight; last_run is that run."`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BackupInput creates a backup.
type BackupInput struct {
	Name           string        `json:"name" minLength:"1" maxLength:"64"`
	Enabled        *bool         `json:"enabled,omitempty" doc:"Default true."`
	Schedule       string        `json:"schedule,omitempty"`
	Bwlimit        string        `json:"bwlimit,omitempty"`
	Transfers      int           `json:"transfers,omitempty" minimum:"0" maximum:"64"`
	HealthcheckUrl string        `json:"healthcheck_url,omitempty"`
	Sources        []SourceInput `json:"sources,omitempty"`
	DestinationIds []int64       `json:"destination_ids,omitempty"`
}

// BackupPatch changes a backup. An absent member stays as it is; sources and
// destination_ids replace the whole list when present.
type BackupPatch struct {
	Name           *string        `json:"name,omitempty" minLength:"1" maxLength:"64"`
	Enabled        *bool          `json:"enabled,omitempty"`
	Schedule       *string        `json:"schedule,omitempty"`
	Bwlimit        *string        `json:"bwlimit,omitempty"`
	Transfers      *int           `json:"transfers,omitempty" minimum:"0" maximum:"64"`
	HealthcheckUrl *string        `json:"healthcheck_url,omitempty"`
	Sources        *[]SourceInput `json:"sources,omitempty"`
	DestinationIds *[]int64       `json:"destination_ids,omitempty"`
}

// BackupRunStarted is the run a manual start recorded.
type BackupRunStarted struct {
	RunId int64 `json:"run_id"`
}

// BackupRun is one recorded run of a backup.
type BackupRun struct {
	Id int64 `json:"id"`
	// BackupId Null when the backup was deleted since.
	BackupId   *int64           `json:"backup_id" doc:"Null when the backup was deleted since."`
	BackupName string           `json:"backup_name"`
	Trigger    BackupRunTrigger `json:"trigger"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at"`
	Result     BackupRunResult  `json:"result"`
	Error      string           `json:"error"`
	Files      int64            `json:"files"`
	Bytes      int64            `json:"bytes"`
}

// BackupRunDetail is a run with its results per destination and folder, and
// the copy in flight while it runs.
type BackupRunDetail struct {
	BackupRun
	Items []BackupRunItem `json:"items"`
	// Progress The copy in flight; null when the run is not copying.
	Progress *BackupProgress `json:"progress" doc:"The copy in flight; null when the run is not copying."`
}

// BackupRunItem is the outcome of one destination (its check, and for restic
// the clean-up of old snapshots) or of one folder on one destination.
type BackupRunItem struct {
	Kind        BackupItemKind   `json:"kind"`
	Destination string           `json:"destination"`
	Label       string           `json:"label"`
	Path        string           `json:"path"`
	Result      BackupItemResult `json:"result"`
	Files       int64            `json:"files"`
	Bytes       int64            `json:"bytes"`
	StartedAt   *time.Time       `json:"started_at"`
	FinishedAt  *time.Time       `json:"finished_at"`
	Error       string           `json:"error"`
	Note        string           `json:"note"`
}

// BackupProgress is the copy in flight.
type BackupProgress struct {
	Destination string `json:"destination"`
	Folder      string `json:"folder"`
	BytesDone   int64  `json:"bytes_done"`
	// BytesTotal 0 while the tool is still counting.
	BytesTotal int64 `json:"bytes_total" doc:"0 while the tool is still counting."`
	FilesDone  int64 `json:"files_done"`
	FilesTotal int64 `json:"files_total"`
	// Speed Bytes per second.
	Speed float64 `json:"speed" doc:"Bytes per second."`
}

// Coverage is every stack's recommended folders with the backups that
// include them.
type Coverage struct {
	Stacks []StackCoverage `json:"stacks"`
	// Uncovered How many recommended folders no enabled backup with a destination includes.
	Uncovered int `json:"uncovered" doc:"How many recommended folders no enabled backup with a destination includes."`
}

// StackCoverage is one stack's recommended folders.
type StackCoverage struct {
	Stack string `json:"stack"`
	// Prepare The unit the stack asks to run before its folders are copied; empty for none.
	Prepare string           `json:"prepare" doc:"The unit the stack asks to run before its folders are copied; empty for none."`
	Folders []FolderCoverage `json:"folders"`
}

// FolderCoverage is one recommended folder.
type FolderCoverage struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	HostPath string    `json:"host_path"`
	Excludes []Exclude `json:"excludes"`
	// Problem Why the folder cannot be resolved; empty when it can.
	Problem string `json:"problem" doc:"Why the folder cannot be resolved; empty when it can."`
	// CoveredBy The enabled backups with a destination that include the folder; empty means not backed up.
	CoveredBy []string `json:"covered_by" doc:"The enabled backups with a destination that include the folder; empty means not backed up."`
}

// BackupRunTrigger is what started a run.
type BackupRunTrigger string

// BackupRunResult is how a run ended.
type BackupRunResult string

// BackupItemKind is what a run item is about.
type BackupItemKind string

// BackupItemResult is how a run item ended.
type BackupItemResult string

const (
	BackupRunResultRunning   BackupRunResult = "running"
	BackupRunResultSuccess   BackupRunResult = "success"
	BackupRunResultPartial   BackupRunResult = "partial"
	BackupRunResultFailure   BackupRunResult = "failure"
	BackupRunResultCancelled BackupRunResult = "cancelled"
	BackupRunResultUnknown   BackupRunResult = "unknown"
)

const (
	BackupRunTriggerCron     BackupRunTrigger = "cron"
	BackupRunTriggerImported BackupRunTrigger = "imported"
	BackupRunTriggerManual   BackupRunTrigger = "manual"
)

// Schema documents BackupRunTrigger as its enum.
func (BackupRunTrigger) Schema(huma.Registry) *huma.Schema {
	return enumSchema(BackupRunTriggerCron, BackupRunTriggerManual, BackupRunTriggerImported)
}

// Schema documents BackupRunResult as its enum.
func (BackupRunResult) Schema(huma.Registry) *huma.Schema {
	return enumSchema(BackupRunResultRunning, BackupRunResultSuccess, BackupRunResultPartial,
		BackupRunResultFailure, BackupRunResultCancelled, BackupRunResultUnknown)
}

// Schema documents BackupItemKind as its enum.
func (BackupItemKind) Schema(huma.Registry) *huma.Schema {
	return enumSchema[BackupItemKind]("destination", "folder")
}

// Schema documents BackupItemResult as its enum.
func (BackupItemResult) Schema(huma.Registry) *huma.Schema {
	return enumSchema[BackupItemResult]("pending", "running", "success", "partial", "failure", "skipped")
}

func enumSchema[T ~string](values ...T) *huma.Schema {
	s := &huma.Schema{Type: huma.TypeString}
	for _, v := range values {
		s.Enum = append(s.Enum, string(v))
	}
	return s
}

// Logs is a snapshot of a unit's recent journal lines.
type Logs struct {
	Logs string `json:"logs"`
}

// nullable turns an object property into "that object or null": huma refuses
// the nullable tag on an object field.
func nullable(s *huma.Schema, prop, doc string) {
	if p := s.Properties[prop]; p != nil && p.Ref != "" {
		s.Properties[prop] = &huma.Schema{Description: doc, OneOf: []*huma.Schema{{Ref: p.Ref}, {Type: "null"}}}
	}
}

// TransformSchema makes last_run nullable.
func (Backup) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	nullable(s, "last_run", "The newest run; null if it never ran.")
	return s
}

// TransformSchema makes progress nullable.
func (BackupRunDetail) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	nullable(s, "progress", "The copy in flight; null when the run is not copying.")
	return s
}
