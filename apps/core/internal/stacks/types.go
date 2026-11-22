package stacks

import (
	"time"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/services"
	"homelab-ngx.com/core/internal/sysupdates"
)

// The API's view of stacks. Huma names each schema after its Go type, so
// these names are the schema names the dashboard's types use.

// Stack is one installed stack as the API shows it.
type Stack struct {
	// Address The stack's address on its private network. Empty until one is allocated.
	Address string `json:"address" doc:"The stack's address on its private network. Empty until one is allocated."`

	// Approved The commit the runtime may build. Absent until the first approval.
	Approved *StackRevision `json:"approved,omitempty" doc:"The commit the runtime may build. Absent until the first approval."`

	// Autostart The stack starts after the host boots.
	Autostart bool `json:"autostart" doc:"The stack starts after the host boots. Off, it stays stopped until it is started. A restart of the daemon alone changes nothing."`

	// Commits Commits of the fetched source since the running generation, newest first. Empty when it is up to date or the history cannot be read.
	Commits   []StackCommit `json:"commits" doc:"Commits of the fetched source since the running generation, newest first. Empty when it is up to date or the history cannot be read."`
	CreatedAt time.Time     `json:"created_at"`
	Fetched   StackRevision `json:"fetched"`

	// Frozen The stack's processes are frozen where they are: they keep
	// their memory and use no CPU until it is thawed.
	Frozen bool `json:"frozen" doc:"The stack's processes are frozen where they are: they keep their memory and use no CPU, and its addresses do not answer until it is thawed. A reboot loses what they held. Running stays true."`

	// Generation The generation of the stack's system profile in use. 0 when nothing is built.
	Generation int `json:"generation" doc:"The generation of the stack's system profile in use. 0 when nothing is built."`

	// Generations Built generations, newest first.
	Generations []StackGeneration `json:"generations" doc:"Built generations, newest first."`

	// Label The name the dashboard shows for the stack, set by the operator.
	// Empty: show the stack's name.
	Label string `json:"label" doc:"The name the dashboard shows for the stack, set by the operator. Empty: show the stack's name."`

	// Name The stack's name, chosen at install and fixed after it: its
	// container, its units (<name>-<unit>) and its state are keyed on it.
	Name string `json:"name" doc:"The stack's name, chosen at install and fixed after it: its container, its units (<name>-<unit>) and its state are keyed on it."`

	// Nixpkgs The nixpkgs the operator chose (version 7); empty follows the manifest's.
	Nixpkgs string `json:"nixpkgs,omitempty" doc:"The nixpkgs the operator chose for a version 7 stack: nixos-unstable, nixos-YY.MM or a commit. Absent: the one the approved manifest recommends, followed as it changes."`

	// NixpkgsRev The nixpkgs commit the approved manifest resolves to now.
	NixpkgsRev string `json:"nixpkgs_rev,omitempty" doc:"The nixpkgs commit the approved manifest's nixpkgs resolves to now (version 7). When the running generation was built with another, an update is ready."`

	// NewGrants What the fetched manifest asks for beyond the approved grants.
	// Not empty means the fetched commit is waiting for approval.
	NewGrants []StackGrant `json:"new_grants" doc:"What the fetched manifest asks for beyond the approved grants. Not empty means the fetched commit is waiting for approval."`

	// Operation The running or last start, stop, update or rollback since the daemon started.
	Operation *StackOperation `json:"operation,omitempty" doc:"The running or last start, stop, update or rollback since the daemon started."`

	// Running The stack's container is up.
	Running bool `json:"running" doc:"The stack's container is up."`

	// RestartNeeded The running container was booted with other binds than
	// its generation and the stored values give now, such as after a path
	// changed. Restarting the stack applies them; nothing restarts on its own.
	RestartNeeded bool `json:"restart_needed" doc:"What the stack was booted with differs from what it would get now: a variable's value, a path, or the API token changed since. Services read them only when they start, so restarting the stack applies them; nothing restarts on its own."`

	Source string `json:"source"`

	// SourceDirty A local checkout had uncommitted changes at the last fetch. Only its commits are installed.
	SourceDirty bool `json:"source_dirty" doc:"A local checkout had uncommitted changes at the last fetch. Only its commits are installed."`

	// UpdateState `needs_approval`: the fetched commit asks for new grants.
	// `commits_available`: the approved commit is not the one running.
	UpdateState StackUpdateState `json:"update_state" enum:"up_to_date,commits_available,needs_approval" doc:"needs_approval: the fetched commit asks for new grants. commits_available: the approved commit is not the one running."`

	// Folders The added folders.
	Folders []StackFolder `json:"folders" doc:"The added folders: host folders the operator binds into the stack on top of its manifest, by mount. They need no approval."`

	// Variables The stack's variables: the ones the approved and fetched
	// manifests declare first, in manifest order, then the operator's own
	// by name. A secret's value is never returned.
	Variables []StackVariable `json:"variables" doc:"The stack's variables: the ones the approved and fetched manifests declare first, in manifest order, then the operator's own by name. A secret's value is never returned."`

	// VariablesReady Every variable the newest manifest (the fetched one)
	// requires has a value, stored or a default. An install or update of a
	// commit waits until the variables its manifest requires have values.
	VariablesReady bool `json:"variables_ready" doc:"Every variable the newest manifest (the fetched one) requires has a value, stored or a default. An install or update of a commit waits until the variables its manifest requires have values."`

	// Usage The whole stack's CPU and memory. Absent when it is not running.
	Usage *services.Usage `json:"usage,omitempty" doc:"The whole stack's CPU and memory. Absent when it is not running."`

	// WantRunning The operator last started it; the monitor starts it again
	// when it stops. Each boot sets it to Autostart.
	WantRunning bool `json:"want_running" doc:"The operator last started it, and the monitor starts it again when its container stops. Each boot of the host sets it to autostart."`
}

// StackUpdateState `needs_approval`: the fetched commit asks for new grants.
// `commits_available`: the approved commit is not the one running.
type StackUpdateState string

// Values of StackUpdateState.
const (
	CommitsAvailable StackUpdateState = "commits_available"
	NeedsApproval    StackUpdateState = "needs_approval"
	UpToDate         StackUpdateState = "up_to_date"
)

// StackAdd is the body of addStack.
type StackAdd struct {
	// Source Flake reference of the stack's repo, optionally with a branch:
	// `github:owner/repo`, `github:owner/repo/branch`, or a
	// `git+ssh://`, `git+https://` or `git+file://` URL.
	Source string `json:"source" doc:"Flake reference of the stack's repo, optionally with a branch: github:owner/repo, github:owner/repo/branch, or a git+ssh://, git+https:// or git+file:// URL."`

	// Name The name to install the stack under: 1-12 characters of a-z, 0-9
	// and '-', starting with a letter. Empty: the name its manifest suggests.
	Name string `json:"name,omitempty" doc:"The name to install the stack under: 1-12 characters of a-z, 0-9 and '-', starting with a letter. Empty: the name its manifest suggests."`
}

// StackPatch is the body of patchStack. A field left out is not changed.
type StackPatch struct {
	// Autostart Start the stack after the host boots.
	Autostart *bool `json:"autostart,omitempty" doc:"Start the stack after the host boots. Takes effect at the next boot."`

	// Label The name the dashboard shows for the stack. Empty clears it.
	Label *string `json:"label,omitempty" maxLength:"40" doc:"The name the dashboard shows for the stack: up to 40 characters. Empty clears it, and the stack's name is shown."`

	// Nixpkgs The nixpkgs to build with (version 7). Empty: the manifest's.
	Nixpkgs *string `json:"nixpkgs,omitempty" doc:"The nixpkgs a version 7 stack is built with: nixos-unstable, nixos-YY.MM or a 40-character NixOS/nixpkgs commit. Empty follows the one its manifest recommends. Takes effect on the next update."`

	// Source The repo the stack is fetched from.
	Source *string `json:"source,omitempty" doc:"Move the stack to another source, such as github:owner/repo, and fetch it there. The running generation, the variables and the data stay. The commit found is approved by itself when its manifest asks for nothing new, and waits as an update otherwise. When the fetch fails the stack keeps its source."`

	// AllowDowngrade Accept a nixpkgs older than the running one.
	AllowDowngrade bool `json:"allow_downgrade,omitempty" doc:"Accept a nixpkgs older than the one the stack runs. Without it such a choice is refused with nixpkgs_downgrade, since a newer version may already have upgraded the stack's data."`
}

// StackNixpkgsChannels is the body of listNixpkgsChannels.
type StackNixpkgsChannels struct {
	// Channels nixos-unstable and the newest NixOS releases, newest first.
	Channels []string `json:"channels" doc:"nixos-unstable and the newest NixOS releases, newest first."`
}

// StackApprove is the body of approveStack.
type StackApprove struct {
	// Rev The fetched commit being approved.
	Rev string `json:"rev" doc:"The fetched commit being approved."`
}

// StackCommit is one commit of a stack's source.
type StackCommit struct {
	At      time.Time `json:"at"`
	Rev     string    `json:"rev"`
	Subject string    `json:"subject"`
}

// StackGeneration is one built generation of a stack's system profile.
type StackGeneration struct {
	BuiltAt time.Time `json:"built_at"`

	// Failed It was unhealthy after an update and rolled back from.
	Failed bool `json:"failed" doc:"It was unhealthy after an update and rolled back from."`

	// Id Generation number of the stack's system profile.
	Id int `json:"id" doc:"Generation number of the stack's system profile."`

	// NixpkgsRev The nixpkgs commit it was built with (manifest version 7).
	NixpkgsRev string `json:"nixpkgs_rev,omitempty" doc:"The nixpkgs commit it was built with; absent for a manifest older than version 7."`

	// Rev Commit it was built from.
	Rev string `json:"rev" doc:"Commit it was built from."`
}

// StackGrant One thing a manifest asks of the host.
type StackGrant struct {
	// Detail What is asked, for the operator to read.
	Detail string         `json:"detail" doc:"What is asked, for the operator to read."`
	Kind   StackGrantKind `json:"kind" enum:"route,storage,gpu,egress,port,containers,api"`
}

// TransformSchema documents the schema.
func (StackGrant) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	s.Description = "One thing a manifest asks of the host."
	return s
}

// StackGrantKind is the kind of a StackGrant.
type StackGrantKind string

// Values of StackGrantKind. The generated client named them Route, Storage
// and so on; here those names are the manifest's own types.
const (
	StackGrantKindAPI        StackGrantKind = GrantAPI
	StackGrantKindContainers StackGrantKind = GrantContainers
	StackGrantKindEgress     StackGrantKind = GrantEgress
	StackGrantKindGpu        StackGrantKind = GrantGPU
	StackGrantKindPort       StackGrantKind = GrantPort
	StackGrantKindRoute      StackGrantKind = GrantRoute
	StackGrantKindStorage    StackGrantKind = GrantStorage
)

// StackLock is the lock of the installed stacks as the API carries it. It
// mirrors Lock's JSON.
type StackLock struct {
	Stacks  []StackLockEntry `json:"stacks"`
	Version int              `json:"version"`
}

// StackLockEntry is one stack in a StackLock.
type StackLockEntry struct {
	// Autostart The stack starts after the host boots, and after a restore.
	Autostart *bool `json:"autostart,omitempty" doc:"The stack starts after the host boots, and after a restore. Absent (a lock written before autostart existed): want_running decides."`

	// Manifest The validated `homelab-stack.json` of a commit, with defaults filled
	// in. The system built is its `nixosModules.default` on the daemon's base module; manifests older than version 7 still parse but are not built.
	Manifest StackManifest `json:"manifest"`

	// Label The name the dashboard shows for the stack. Absent: its name.
	Label string `json:"label,omitempty" doc:"The name the dashboard shows for the stack. Absent: its name."`
	Name  string `json:"name"`

	// Nixpkgs The operator's choice of nixpkgs. Absent: the manifest's.
	Nixpkgs string `json:"nixpkgs,omitempty" doc:"The nixpkgs the operator chose. Absent: the one the manifest recommends."`

	// NixpkgsRev The nixpkgs commit the stack builds with (manifest version 7).
	NixpkgsRev string `json:"nixpkgs_rev,omitempty" doc:"The nixpkgs commit a version 7 stack builds with, so a restore rebuilds the same system."`

	// Rev Approved commit.
	Rev    string `json:"rev" doc:"Approved commit."`
	Source string `json:"source"`
	Subnet string `json:"subnet"`

	// Url Locked flake reference of that commit.
	Url         string `json:"url" doc:"Locked flake reference of that commit."`
	WantRunning bool   `json:"want_running"`
}

// StackManifest The validated `homelab-stack.json` of a commit, with defaults filled
// in. The system built is its `nixosModules.default` on the daemon's base module; manifests older than version 7 still parse but are not built.
// It mirrors Manifest's JSON.
type StackManifest struct {
	// Api Scopes of the daemon API the stack may call (version 3).
	Api []string `json:"api,omitempty" enum:"read,control" doc:"Scopes of the daemon API the stack may call on the stack listener (version 3)."`

	// Containers The stack may run podman (version 2).
	Containers bool                  `json:"containers" doc:"The stack may run podman (version 2)."`
	Egress     []StackManifestEgress `json:"egress"`
	Gpu        bool                  `json:"gpu"`
	Health     StackManifestHealth   `json:"health"`
	Name       string                `json:"name"`

	// Nixpkgs The nixpkgs the repo recommends (version 7): a channel or a commit.
	Nixpkgs string `json:"nixpkgs,omitempty" doc:"The nixpkgs the repo recommends (version 7): nixos-unstable, nixos-YY.MM or a NixOS/nixpkgs commit."`

	// Secrets Secrets the stack needs a value for, filled in on the host (version 4).
	Secrets []StackManifestSecret `json:"secrets,omitempty" doc:"Secrets the stack needs a value for, filled in on the host (version 4)."`

	// Variables Env, secret and path values filled in on the host (version 5, which has no secrets or storage).
	Variables []StackManifestVariable `json:"variables,omitempty" doc:"Env, secret and path values filled in on the host (version 5, which has no secrets or storage)."`

	// Backup What the stack recommends backing up (version 6).
	Backup *StackManifestBackup `json:"backup,omitempty" doc:"What the stack recommends backing up, and the unit that prepares it (version 6)."`

	// Ports Host ports forwarded to the stack for the named clients (version 2).
	Ports   []StackManifestPort    `json:"ports" doc:"Host ports forwarded to the stack for the named clients (version 2)."`
	Routes  []StackManifestRoute   `json:"routes"`
	Storage []StackManifestStorage `json:"storage"`
	Version int                    `json:"version"`
}

// StackManifestBackup is a manifest's backup section.
type StackManifestBackup struct {
	// Folders Directories worth backing up.
	Folders []StackManifestBackupFolder `json:"folders,omitempty" doc:"Directories worth backing up. None is backed up until it is added to a backup."`

	// Prepare Unit inside the stack started before its folders are copied.
	Prepare string `json:"prepare,omitempty" doc:"A oneshot unit inside the stack that is started and waited for before its folders are copied."`
}

// StackManifestBackupFolder is a recommended folder: exactly one of path
// and variable is set.
type StackManifestBackupFolder struct {
	Exclude []StackManifestBackupExclude `json:"exclude,omitempty"`
	Label   string                       `json:"label"`

	// Path A directory inside the stack.
	Path string `json:"path,omitempty" doc:"A directory inside the stack."`

	// Variable A path variable whose host directory is the folder.
	Variable string `json:"variable,omitempty" doc:"A path variable whose host directory is the folder."`
}

// StackManifestBackupExclude is a pattern left out of a recommended folder.
type StackManifestBackupExclude struct {
	// Pattern Anchored with / or **/; only literals, *, ** and ?.
	Pattern string `json:"pattern" doc:"Starts with / (the folder itself) or **/ (any depth); only literal characters, *, ** and ?."`
	Reason  string `json:"reason"`
}

// StackManifestSecret is a secret a manifest declares.
type StackManifestSecret struct {
	Description string `json:"description"`
	Name        string `json:"name"`
}

// StackManifestVariable is a variable a manifest declares.
type StackManifestVariable struct {
	// Default What an env or path variable resolves to while no value is stored.
	Default     *string `json:"default,omitempty" doc:"What an env or path variable resolves to while no value is stored. From version 7 an env variable has none: the stack's flake holds it."`
	Description string  `json:"description"`

	// Enum The values an env variable allows (version 7).
	Enum []string          `json:"enum,omitempty" doc:"The values an env variable allows (version 7); any other is refused."`
	Kind StackVariableKind `json:"kind" enum:"env,secret,path,address"`

	// Mount Where a path is bound inside the stack.
	Mount    string `json:"mount,omitempty" doc:"Where a path is bound inside the stack."`
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only,omitempty"`

	// Owner The user in the stack that takes the uid of a path's host directory (version 8).
	Owner string `json:"owner,omitempty" doc:"The user in the stack that takes the uid of a path's host directory (version 8)."`

	// Group The group in the stack that takes the gid of a path's host directory (version 8).
	Group    string `json:"group,omitempty" doc:"The group in the stack that takes the gid of a path's host directory (version 8)."`
	Required bool   `json:"required"`

	// Type What an env variable's value is, for the dashboard's input (version 7).
	Type string `json:"type,omitempty" enum:"string,number,boolean" doc:"What an env variable's value is, for the dashboard's input (version 7). Values are not checked against it."`
}

// StackManifestEgress is an egress grant.
type StackManifestEgress struct {
	Port  int                      `json:"port"`
	Proto StackManifestEgressProto `json:"proto" enum:"tcp,udp"`

	// To `host`, a CIDR, or $NAME, an address variable (version 8).
	To string `json:"to" doc:"host, a CIDR, or $NAME, an address variable (version 8)."`
}

// StackManifestHealth is the health check.
type StackManifestHealth struct {
	Http  *StackManifestHealthHttp `json:"http,omitempty"`
	Units []string                 `json:"units"`
}

// StackManifestHealthHttp is the health check's HTTP probe.
type StackManifestHealthHttp struct {
	Path string `json:"path"`
	Port int    `json:"port"`
}

// StackManifestPort is a port grant.
type StackManifestPort struct {
	// From Client CIDRs allowed through.
	From []string `json:"from" doc:"Clients allowed through: CIDRs and, from version 8, lan (the host's LAN) and $NAME (an address variable)."`

	// Port Port on the host's own addresses.
	Port  int                     `json:"port" doc:"Port on the host's own addresses."`
	Proto StackManifestPortsProto `json:"proto" enum:"tcp,udp"`

	// To Port in the stack.
	To int `json:"to" doc:"Port in the stack."`
}

// StackManifestRoute is a route grant.
type StackManifestRoute struct {
	Host string `json:"host"`

	// MaxBody Request body limit, such as 50GB.
	MaxBody string `json:"max_body,omitempty" doc:"Request body limit, such as 50GB."`
	Port    int    `json:"port"`
}

// StackManifestStorage is a storage grant.
type StackManifestStorage struct {
	HostPath string `json:"host_path"`
	Mount    string `json:"mount"`
	ReadOnly bool   `json:"read_only"`
}

// TransformSchema documents the schema.
func (StackManifest) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	s.Description = "The validated homelab-stack.json of a commit, with defaults filled in. The system built is its nixosModules.default on the daemon's base module; manifests older than version 7 still parse but are not built."
	return s
}

// StackManifestEgressProto is an egress grant's protocol.
type StackManifestEgressProto string

// StackManifestPortsProto is a port grant's protocol.
type StackManifestPortsProto string

// Values of the protocols.
const (
	StackManifestEgressProtoTcp StackManifestEgressProto = "tcp"
	StackManifestEgressProtoUdp StackManifestEgressProto = "udp"
	StackManifestPortsProtoTcp  StackManifestPortsProto  = "tcp"
	StackManifestPortsProtoUdp  StackManifestPortsProto  = "udp"
)

// StackOperation is a stack's running or last background operation.
type StackOperation struct {
	// Error Why it failed. Absent on success and while it runs.
	Error string `json:"error,omitempty" doc:"Why it failed. Absent on success and while it runs."`

	// FinishedAt Absent while it runs.
	FinishedAt *time.Time         `json:"finished_at,omitempty" doc:"Absent while it runs."`
	Kind       StackOperationKind `json:"kind" enum:"start,stop,update,rollback"`

	// Phase What it is doing, or did last.
	Phase     string    `json:"phase" doc:"What it is doing, or did last."`
	StartedAt time.Time `json:"started_at"`
}

// StackOperationKind is the kind of a StackOperation.
type StackOperationKind string

// Values of StackOperationKind.
const (
	StackOperationKindRollback StackOperationKind = "rollback"
	StackOperationKindStart    StackOperationKind = "start"
	StackOperationKindStop     StackOperationKind = "stop"
	StackOperationKindUpdate   StackOperationKind = "update"
)

// StackRestoreResult says what a restore did with each locked stack.
type StackRestoreResult struct {
	Restored []string `json:"restored"`

	// Skipped Stack name to why it was not restored or not started.
	Skipped map[string]string `json:"skipped" doc:"Stack name to why it was not restored or not started."`
}

// StackRevision is a commit of a stack's source with its manifest.
type StackRevision struct {
	// At When it was fetched or approved.
	At time.Time `json:"at" doc:"When it was fetched or approved."`

	// Manifest The validated `homelab-stack.json` of a commit, with defaults filled
	// in. The system built is its `nixosModules.default` on the daemon's base module; manifests older than version 7 still parse but are not built.
	Manifest StackManifest `json:"manifest"`

	// Rev Commit hash.
	Rev string `json:"rev" doc:"Commit hash."`

	// Url Locked flake reference of that commit.
	Url string `json:"url" doc:"Locked flake reference of that commit."`
}

// StackVariable is one variable of a stack. An env or secret one reaches the
// stack as /run/secrets/<NAME> and, when in_env, as a line of
// /run/secrets/env; a path is the host directory bound at its mount.
type StackVariable struct {
	// Builtin The host fills it in (the API token); it cannot be set or removed.
	Builtin bool `json:"builtin,omitempty" doc:"The host fills it in: the API token of a stack with API access, and the domain and dashboard address of a version 8 stack. It cannot be set or removed."`

	// Declared A manifest (the approved or the fetched one) declares it.
	Declared bool `json:"declared" doc:"A manifest (the approved or the fetched one) declares it. Not declared: the operator added it, or a newer commit dropped it; the daemon keeps it until the operator removes it."`

	// Default The manifest's default of an env or path variable.
	Default *string `json:"default,omitempty" doc:"The manifest's default of an env or path variable, used while no value is stored."`

	// Description From the manifest; empty for the operator's own.
	Description string `json:"description" doc:"From the manifest; empty for the operator's own."`

	// Enum The values the manifest allows (version 7 env variables).
	Enum []string `json:"enum,omitempty" doc:"The values the manifest allows; any other is refused (version 7 env variables)."`

	// InEnv The value is also a line of /run/secrets/env. A value with a
	// line break, surrounding whitespace, a quote, a backslash or a $ is
	// delivered only as its file.
	InEnv bool `json:"in_env" doc:"The value (stored or default) is also a line of /run/secrets/env. A value with a line break, surrounding whitespace, a quote, a backslash or a $ is delivered only as its file. Never for a path, and from manifest version 7 never for a secret: the env file then overrides every service's environment."`

	// Kind env, secret or path. The operator's own are env or secret.
	Kind StackVariableKind `json:"kind" enum:"env,secret,path,address" doc:"env: a plain value. secret: write-only. path: a host directory bound into the stack. address: IPv4 addresses or CIDRs, which the stack's egress and port grants that name the variable use (version 8). The operator's own are env or secret."`

	// Mount Where a path is bound inside the stack.
	Mount string `json:"mount,omitempty" doc:"Where a path is bound inside the stack. Only for a path."`
	Name  string `json:"name"`

	// Owner The stack's user that takes the uid of the path's host directory.
	Owner string `json:"owner,omitempty" doc:"The user in the stack that takes the uid of the path's host directory (version 8). Only for a path."`

	// Group The stack's group that takes the gid of the path's host directory.
	Group string `json:"group,omitempty" doc:"The group in the stack that takes the gid of the path's host directory (version 8). Only for a path."`

	// ReadOnly A path is bound read-only.
	ReadOnly *bool `json:"read_only,omitempty" doc:"The path is bound read-only, by the manifest or by the operator. Only for a path."`

	// ReadOnlyByOperator The operator binds a path read-only that the
	// manifest asks to write.
	ReadOnlyByOperator bool `json:"read_only_by_operator,omitempty" doc:"The operator binds the path read-only although the manifest asks for read-write. Only for a path."`

	// FolderUID and FolderGID are who owns a path's folder now.
	FolderUID *int64 `json:"folder_uid,omitempty" doc:"For a path with an owner or group: the uid that owns the folder now."`
	FolderGID *int64 `json:"folder_gid,omitempty" doc:"For a path with an owner or group: the gid of the folder now."`
	// StackUID and StackGID are this host's ids for the owner and group.
	StackUID *int64 `json:"stack_uid,omitempty" doc:"This host's id for the path's owner; absent until it has one."`
	StackGID *int64 `json:"stack_gid,omitempty" doc:"This host's id for the path's group; absent until it has one."`
	// OwnerState How the folder's owner stands against the stack ids.
	OwnerState string `json:"owner_state,omitempty" enum:"ok,own,root,group_differs" doc:"For a path with an owner or group and a folder: ok (it has the stack ids), own (ids of its own, which the stack takes), root (owned by root: the stack cannot use it), group_differs (another stack's folder has another id for the same group)."`

	// Required An install or update waits until it has a value.
	Required bool `json:"required" doc:"An install or update waits until it has a value, stored or a default. False for the operator's own."`

	// Secret Write-only: the value is never returned. Declared ones always are.
	Secret bool `json:"secret" doc:"Write-only: the value is never returned. A declared secret always is; a path never is."`

	// Set It has a value.
	Set bool `json:"set" doc:"A value is stored."`

	// Source Where the value comes from.
	Source StackVariableSource `json:"source" enum:"stored,default,unset" doc:"Where the value comes from: stored, the manifest's default, or nowhere (unset)."`

	// Type What the value is, for the input (version 7 env variables).
	Type string `json:"type,omitempty" enum:"string,number,boolean" doc:"What the value is, for the dashboard's input (version 7 env variables)."`

	// UpdatedAt When the value was last set. Absent when it has none.
	UpdatedAt *time.Time `json:"updated_at,omitempty" doc:"When the value was last stored. Absent when none is."`

	// Value The value of a plain variable. Absent for a secret.
	Value string `json:"value,omitempty" doc:"The stored value of a plain variable; for a path, the host path. Absent for a secret and when none is stored."`
}

// StackVariableKind is the kind of a StackVariable.
type StackVariableKind string

// Values of StackVariableKind.
const (
	StackVariableKindEnv     StackVariableKind = VarEnv
	StackVariableKindPath    StackVariableKind = VarPath
	StackVariableKindSecret  StackVariableKind = VarSecret
	StackVariableKindAddress StackVariableKind = VarAddress
)

// StackVariableSource is where a StackVariable's value comes from.
type StackVariableSource string

// Values of StackVariableSource.
const (
	StackVariableSourceDefault StackVariableSource = SourceDefault
	StackVariableSourceStored  StackVariableSource = SourceStored
	StackVariableSourceUnset   StackVariableSource = SourceUnset
)

// StackVariableSet is the body of setStackVariable.
type StackVariableSet struct {
	// Secret Make the variable write-only. A secret stays secret; false is
	// refused for one. Absent: unchanged, plain for a new variable the
	// manifest does not declare. The manifest's kind decides for a declared
	// one: true is refused for an env or path variable.
	Secret *bool `json:"secret,omitempty" doc:"Make the variable write-only. A secret stays secret; false is refused for one. Absent: unchanged, and plain for a new variable the manifest does not declare. True is refused for a declared env or path variable."`

	// Value The value, stored as given.
	Value string `json:"value" minLength:"1" maxLength:"65536" doc:"The value, stored as given. For a declared path, an existing directory on the host."`

	// ReadOnly Bind a declared path read-only.
	ReadOnly *bool `json:"read_only,omitempty" doc:"For a declared path: bind it read-only although the manifest asks for read-write; false binds it as the manifest says. Absent: unchanged. A path the manifest binds read-only stays read-only."`
}

// StackFolder is an added folder: a host folder the operator binds into the
// stack on top of its manifest.
type StackFolder struct {
	Path       string    `json:"path" doc:"The host folder."`
	Mount      string    `json:"mount" doc:"Where it is bound inside the stack."`
	ReadOnly   bool      `json:"read_only" doc:"It is bound read-only."`
	ShadowedBy string    `json:"shadowed_by,omitempty" doc:"What in the running manifest claims the same mount, or one above or under it. Such a folder is not bound: the manifest wins."`
	FolderUID  *int64    `json:"folder_uid,omitempty" doc:"The uid that owns the folder; absent when the folder cannot be read."`
	FolderGID  *int64    `json:"folder_gid,omitempty" doc:"The folder's gid; absent when the folder cannot be read."`
	UpdatedAt  time.Time `json:"updated_at"`
}

// StackFolderIn is one added folder in StackFoldersSet.
type StackFolderIn struct {
	Path     string `json:"path" minLength:"1" maxLength:"4096" doc:"An existing folder on the host, under the same rules as a path variable's value."`
	Mount    string `json:"mount" minLength:"1" maxLength:"4096" doc:"Where to bind it inside the stack: an absolute path that no grant or other folder claims, nor one above or under it."`
	ReadOnly bool   `json:"read_only" doc:"Bind it read-only."`
}

// StackFoldersSet replaces a stack's added folders.
type StackFoldersSet struct {
	Folders []StackFolderIn `json:"folders" maxItems:"32" doc:"Every added folder; one left out is no longer bound. A change takes effect at the stack's next restart."`
}

// StackUpdate is one install, update or rollback attempt.
type StackUpdate struct {
	// Changes Packages changed from the generation before, once built.
	Changes []sysupdates.UpdateChange `json:"changes" doc:"Packages changed from the generation before, once built."`

	// FinishedAt Absent while it runs.
	FinishedAt *time.Time `json:"finished_at,omitempty" doc:"Absent while it runs."`

	// FromGeneration 0 for an install.
	FromGeneration int             `json:"from_generation" doc:"0 for an install."`
	Id             int64           `json:"id"`
	Kind           StackUpdateKind `json:"kind" enum:"install,update,rollback"`

	// Log Phases and build output
	Log string `json:"log,omitempty" doc:"Phases and build output, capped. Only on a single attempt."`

	// Reason Why it did not end healthy.
	Reason string            `json:"reason" doc:"Why it did not end healthy."`
	Result StackUpdateResult `json:"result" enum:"running,healthy,rolled_back,build_failed,failed,interrupted"`

	// Rev The commit installed or updated to; empty for a rollback.
	Rev       string    `json:"rev" doc:"The commit installed or updated to; empty for a rollback."`
	Stack     string    `json:"stack"`
	StartedAt time.Time `json:"started_at"`

	// ToGeneration 0 until built.
	ToGeneration int `json:"to_generation" doc:"0 until built."`
}

// StackUpdateKind is the kind of a StackUpdate.
type StackUpdateKind string

// Values of StackUpdateKind.
const (
	StackUpdateKindInstall  StackUpdateKind = "install"
	StackUpdateKindRollback StackUpdateKind = "rollback"
	StackUpdateKindUpdate   StackUpdateKind = "update"
)

// StackUpdateResult is how a StackUpdate ended.
type StackUpdateResult string

// Values of StackUpdateResult.
const (
	StackUpdateResultBuildFailed StackUpdateResult = "build_failed"
	StackUpdateResultFailed      StackUpdateResult = "failed"
	StackUpdateResultHealthy     StackUpdateResult = "healthy"
	StackUpdateResultInterrupted StackUpdateResult = "interrupted"
	StackUpdateResultRolledBack  StackUpdateResult = "rolled_back"
	StackUpdateResultRunning     StackUpdateResult = "running"
)

// StackRollback is the optional body of rollbackStack.
type StackRollback struct {
	// Generation The generation to go back to.
	Generation int `json:"generation,omitempty" doc:"The generation to go back to."`
}

// StackUpdateAll is the answer of updateAllStacks.
type StackUpdateAll struct {
	// Queued The stacks that will be updated, in order.
	Queued []string `json:"queued" doc:"The stacks that will be updated, in order."`
}

// StackFolderCreate makes a path variable's folder.
type StackFolderCreate struct {
	Place  string `json:"place,omitempty" maxLength:"4096" doc:"The pool or partition to make it on, by its directory (/var/mnt/pool). Empty: the host's only one."`
	Folder string `json:"folder,omitempty" maxLength:"64" doc:"The folder's name there. Empty: the stack's name."`
}

// StackFolderOwn changes the owner of a path variable's folder.
type StackFolderOwn struct {
	Confirm string `json:"confirm" minLength:"1" maxLength:"4096" doc:"The folder's path, typed to confirm: the change cannot be undone."`
}

// StackID is this host's id for a user or group name its stacks declare.
type StackID struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// StackIDList is the host's stack ids and their range.
type StackIDList struct {
	IDs []StackID `json:"ids"`
	Max int64     `json:"max"`
	Min int64     `json:"min"`
}

// StackIDSet pins a name's id.
type StackIDSet struct {
	ID int64 `json:"id" minimum:"2000" maximum:"2099"`
}
