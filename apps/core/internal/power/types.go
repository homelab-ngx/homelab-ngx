package power

import "time"

// PowerAction is a reboot or a poweroff.
type PowerAction string

const (
	Reboot   PowerAction = "reboot"
	PowerOff PowerAction = "poweroff"
)

// PowerPhase is how far a shutdown has come.
type PowerPhase string

const (
	PhaseIdle   PowerPhase = "idle" // no shutdown under way
	PhaseStacks PowerPhase = "stopping_stacks"
	PhaseMounts PowerPhase = "unmounting"
	PhaseDown   PowerPhase = "going_down" // handed to systemd: the daemon stops next
	PhaseFailed PowerPhase = "failed"     // the shutdown did not start; see Error
)

// PowerStepKind is what a step stops.
type PowerStepKind string

const (
	StepStack PowerStepKind = "stack"
	StepMount PowerStepKind = "mount"
)

// PowerStepState is how a step is going.
type PowerStepState string

const (
	StateWaiting PowerStepState = "waiting"
	StateWorking PowerStepState = "working"
	StateDone    PowerStepState = "done"
	StateFailed  PowerStepState = "failed"
)

// PowerStep is one stack stopped or one file system unmounted.
type PowerStep struct {
	Kind   PowerStepKind  `json:"kind" enum:"stack,mount"`
	Name   string         `json:"name" doc:"The stack name, or the mountpoint."`
	State  PowerStepState `json:"state" enum:"waiting,working,done,failed"`
	Detail string         `json:"detail,omitempty" doc:"Why a step failed. A stack that did not halt was killed; a mount still in use is unmounted last, as the host goes down."`
	// WaitingOn is set once a stack has been stopping for a while.
	WaitingOn *PowerWait `json:"waiting_on,omitempty" doc:"A stack still stopping after 30 s: the unit it waits for and when systemd kills it."`
}

// PowerWait is the unit a stopping stack waits for.
type PowerWait struct {
	Unit        string `json:"unit" doc:"The unit inside the stack that has not stopped, or the stack's own unit once nothing inside answers."`
	Description string `json:"description,omitempty"`
	KillIn      int    `json:"kill_in" doc:"Seconds until systemd kills it, as of this read."`
	killAt      time.Time
}

// PowerScheduled is a shutdown planned for later (`shutdown -r HH:MM`).
type PowerScheduled struct {
	Action PowerAction `json:"action" enum:"reboot,poweroff"`
	At     time.Time   `json:"at"`
}

// PowerStatus is the shutdown under way and what a page waiting for the host
// needs to know.
type PowerStatus struct {
	Action    PowerAction `json:"action,omitempty" enum:"reboot,poweroff" doc:"The shutdown under way; empty when none is."`
	Phase     PowerPhase  `json:"phase" enum:"idle,stopping_stacks,unmounting,going_down,failed"`
	StartedAt *time.Time  `json:"started_at,omitempty"`
	Steps     []PowerStep `json:"steps" doc:"The stacks stopped, then the pools and partitions unmounted, in order."`
	Error     string      `json:"error,omitempty" doc:"Why the shutdown did not start (phase failed)."`
	Busy      []string    `json:"busy" doc:"Work a shutdown would interrupt now: stack operations, an update, backups. Only while idle."`
	Frozen    []string    `json:"frozen" doc:"The frozen stacks. A shutdown asks for thaw (they stop cleanly) or force (they are killed, and what they held is lost). Only while idle."`
	BootID    string      `json:"boot_id" doc:"This boot's id. A page waiting through a reboot knows the host is back when it changes."`
	// ExpectedSeconds is the countdown a page shows.
	ExpectedSeconds int             `json:"expected_seconds" doc:"How long the last reboot took, from the shutdown to the daemon answering again; 120 before one was measured."`
	Scheduled       *PowerScheduled `json:"scheduled,omitempty" doc:"A reboot or poweroff planned for later; it stops the stacks first like any other."`
}

// PowerRequest asks for a reboot or a poweroff.
type PowerRequest struct {
	Action PowerAction `json:"action" enum:"reboot,poweroff"`
	Force  bool        `json:"force,omitempty" doc:"Go ahead even when it interrupts stack operations, an update or backups. Without thaw, frozen stacks are killed: what they held is lost."`
	Thaw   bool        `json:"thaw,omitempty" doc:"Thaw the frozen stacks, so they stop cleanly. Without thaw or force a request with frozen stacks is refused with stacks_frozen."`
}
