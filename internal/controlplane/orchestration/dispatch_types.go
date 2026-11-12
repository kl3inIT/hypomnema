package orchestration

import "time"

// DispatchStatus is where a Dispatch, one attempt at a Task, stands.
type DispatchStatus string

// Dispatch statuses. unknown and stop_unknown are not final: a report from
// the proven epoch, a reattach, a settlement or an abandon ends them.
const (
	DispatchQueued      DispatchStatus = "queued"
	DispatchStarting    DispatchStatus = "starting"
	DispatchRunning     DispatchStatus = "running"
	DispatchStopping    DispatchStatus = "stopping"
	DispatchUnknown     DispatchStatus = "unknown"
	DispatchStopUnknown DispatchStatus = "stop_unknown"
	DispatchSucceeded   DispatchStatus = "succeeded"
	DispatchFailed      DispatchStatus = "failed"
	DispatchCanceled    DispatchStatus = "canceled"
	DispatchExpired     DispatchStatus = "expired"
	DispatchAbandoned   DispatchStatus = "abandoned"
	// DispatchGated: the worker ended to wait for a decision (a Gate); the Conversation can continue.
	DispatchGated DispatchStatus = "gated"
)

// FailureClass says why a Dispatch failed, which decides what the Task does next.
type FailureClass string

// Failure classes.
const (
	// FailureNone: the Dispatch succeeded.
	FailureNone FailureClass = ""
	// FailureStartFailed: the launch failed before the CLI ran. Counts toward the breaker.
	FailureStartFailed FailureClass = "start_failed"
	// FailureNeedsAuth: the account needs a login. The Task is blocked, never retried automatically.
	FailureNeedsAuth FailureClass = "needs_auth"
	// FailureRateLimited: the account hit a limit. Not counted; the account cools down on every node.
	FailureRateLimited FailureClass = "rate_limited"
	// FailureWorkerError: the worker reported a failure. Counts toward the breaker.
	FailureWorkerError FailureClass = "worker_error"
	// FailureLost: an unknown Dispatch was settled as gone. Counts toward the breaker.
	FailureLost FailureClass = "lost"
)

// UnknownCause says why a Dispatch's outcome is unknown.
type UnknownCause string

// Causes of an unknown outcome.
const (
	UnknownNone UnknownCause = ""
	// UnknownLeaseLost: the node stopped renewing the lease; the worker may still run.
	UnknownLeaseLost UnknownCause = "lease_lost"
	// UnknownExitedUnreported: the process exited without a report.
	UnknownExitedUnreported UnknownCause = "exited_unreported"
	// UnknownStopTimeout: a stop was not confirmed in time.
	UnknownStopTimeout UnknownCause = "stop_timeout"
)

// ConversationStatus is whether a Conversation can be continued.
type ConversationStatus string

// Conversation statuses.
const (
	ConversationOpen   ConversationStatus = "open"
	ConversationClosed ConversationStatus = "closed"
)

// CloseReason says why a Conversation was closed.
type CloseReason string

// Reasons a Conversation is closed.
const (
	CloseNone CloseReason = ""
	// CloseInterrupted: a Dispatch ended without a first-hand report; the next one starts fresh.
	CloseInterrupted CloseReason = "interrupted"
	// CloseNeverStarted: its only Dispatch expired before the CLI session existed.
	CloseNeverStarted CloseReason = "never_started"
	// CloseSuperseded: a new Conversation was opened for the Task.
	CloseSuperseded CloseReason = "superseded"
)

// RecoveryNote is what a worker is told when its Dispatch opens a new
// Conversation after an interrupted one (Omnigent's recovery turn).
const RecoveryNote = "The previous attempt was interrupted. Check the outcome of any interrupted operation " +
	"before repeating it."

// Settled reports whether the Dispatch has ended for good.
func (d Dispatch) Settled() bool { return d.Status.settled() }

// Dispatch is one attempt at a Task, on one node, inside one Conversation.
type Dispatch struct {
	ID             string
	TaskID         string
	ConversationID string
	NodeID         string
	Status         DispatchStatus
	// Epoch grows each time a node takes the Dispatch; node calls must name the current one.
	Epoch        int
	FailureClass FailureClass
	UnknownCause UnknownCause
	// Recovery: the worker gets RecoveryNote with its instructions.
	Recovery        bool
	Result          *Result
	ResetsAt        *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       *time.Time
	LeaseExpiresAt  *time.Time
	ProgressAt      *time.Time
	StopRequestedAt *time.Time
	StalledAt       *time.Time
	OverrunAt       *time.Time
	SettledAt       *time.Time
	// WaitingSince is set while the worker waits for an answer; Waited is the time spent waiting.
	WaitingSince *time.Time
	Waited       time.Duration
	// GateQuestionID is the answered Gate this Dispatch resumes from.
	GateQuestionID string
	// Model and Effort are what the Dispatch was asked to run with: the Task's
	// own, or its profile's; empty leaves the runtime's default.
	Model  string
	Effort string
	// ProfileVersionID is the profile version it ran with; empty before profiles.
	ProfileVersionID string
	// FallbackFor is the profile the Task asked for, when the Dispatch ran
	// under a backup of it because every account of that profile's runtime
	// waited for a limit to reset; FallbackUntil is when the first was to be
	// free (issue #171).
	FallbackFor   string
	FallbackUntil *time.Time
	// Handoff: the Dispatch takes over a Task another worker began and left
	// when its account met a limit (issue #187). It runs in that worker's
	// worktree with a session of its own, and its worker is told so.
	Handoff bool
	// Activity is one line about the newest thing the worker said or did.
	Activity string
}

// Conversation is the worker's CLI session for one Task: one node, worktree,
// runtime and account. A later Dispatch can continue it.
type Conversation struct {
	ID            string
	TaskID        string
	NodeID        string
	Worktree      string
	Runtime       string
	AccountRef    string
	CLISessionRef string
	Status        ConversationStatus
	CloseReason   CloseReason
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Result is what a worker reports when it finishes.
type Result struct {
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Exit     Exit       `json:"exit"`
	Usage    *Usage     `json:"usage,omitempty"`
}

// Evidence points at something the work produced (a pull request, a commit, a CI run).
type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Exit is how the worker process ended.
type Exit struct {
	Kind   string `json:"kind"`
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

// Usage is what the runtime reports it used, when it reports it.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// DispatchSpec says where a new Dispatch runs. Continue asks to continue the
// Task's open Conversation; otherwise a new Conversation is opened on NodeID,
// Worktree, Runtime and AccountRef.
type DispatchSpec struct {
	Continue   bool
	NodeID     string
	Worktree   string
	Runtime    string
	AccountRef string
}

// NodeCall identifies a call from a node about one Dispatch. RequestID makes
// a repeated call return the stored response instead of acting twice.
type NodeCall struct {
	NodeID     string
	DispatchID string
	Epoch      int
	RequestID  string
}

// Outcome is a worker's final report: success when Class is FailureNone.
// ResetsAt is when a rate limit ends, if the runtime said.
type Outcome struct {
	Class    FailureClass
	ResetsAt *time.Time
	Result   Result
	// Gate: the worker ended to wait for this decision.
	Gate *Ask
}

// Accepted is the reply to AcceptDispatch and ReattachDispatch.
type Accepted struct {
	Epoch          int       `json:"epoch"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Recovery       bool      `json:"recovery"`
	CLISessionRef  string    `json:"cli_session_ref,omitempty"`
	// Gate is the answered decision this Dispatch resumes from, for the worker's instructions.
	Gate *Question `json:"gate,omitempty"`
}

// Beat is the reply to a heartbeat. StopRequested tells the node to stop the worker.
type Beat struct {
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	StopRequested  bool      `json:"stop_requested"`
	// Answers are answered questions the node has not acknowledged yet.
	Answers []Question `json:"answers"`
}

// EffectPhase is whether an outside effect is about to happen or has happened.
type EffectPhase string

// Effect phases.
const (
	EffectIntent EffectPhase = "intent"
	EffectDone   EffectPhase = "done"
)

// Effect is an outside effect of a Dispatch (push, pull request, message).
// An intent without a done is what the coordinator checks when settling an
// unknown Dispatch; effects are never replayed automatically.
type Effect struct {
	Seq     int
	Kind    string
	Ref     string
	Phase   EffectPhase
	Payload string
}

// Resource is something a Dispatch holds on its node.
type Resource struct {
	Kind       string
	Ref        string
	Custody    string
	ReleasedAt *time.Time
}

// Settlement is the coordinator's decision about an unknown Dispatch, after
// checking its effects: it succeeded (Result says how) or it is lost.
type Settlement struct {
	Succeeded bool
	Result    Result
}

// EscalationKind names something the coordinator or the owner must look at.
type EscalationKind string

// Escalations raised by the reaper.
const (
	// EscalationStalled: a running Dispatch made no progress for StallAfter.
	EscalationStalled EscalationKind = "stalled"
	// EscalationOverrun: a Dispatch has run for MaxDuration.
	EscalationOverrun EscalationKind = "overrun"
	// EscalationNodeUnavailable: the node let ExpiriesBeforeUnavailable Dispatches in a row expire.
	EscalationNodeUnavailable EscalationKind = "node_unavailable"
	// EscalationQuestionReminder: a question to the owner is still open (after ReminderFirst, then every ReminderEvery).
	EscalationQuestionReminder EscalationKind = "question_reminder"
	// EscalationUnplaceable: a ready Task found no node or no account for UnplaceableAfter.
	EscalationUnplaceable EscalationKind = "unplaceable"
	// EscalationAccountCooling: a rate limit cooled an account for more than
	// AccountCoolingAlert while every other account of its runtime cools too.
	EscalationAccountCooling EscalationKind = "account_cooling"
	// EscalationFallback: a Task runs under a backup of its profile, whose
	// accounts wait for a limit to reset. Once per Run for each stretch of
	// the limit, as OpenMausBot tells a bot's owner once (limit-notices.ts).
	EscalationFallback EscalationKind = "fallback"
)

// Reasons a Task is unplaceable.
const (
	ReasonNoNode    = "no_node"
	ReasonNoAccount = "no_account"
	// ReasonAccountsCooling: every account of the runtime cools down. It is a
	// profile's reason only; a Task that waits for one is not escalated by it.
	ReasonAccountsCooling = "accounts_cooling"
	// ReasonNoNodeReaches is followed by the git host: "no_node_reaches:gitlab.company.example".
	ReasonNoNodeReaches = "no_node_reaches:"
	// ReasonNoNodeHasTool is followed by the tool: "no_node_has_tool:gws". The
	// Run uses an account whose tool no connected node has installed yet.
	ReasonNoNodeHasTool = "no_node_has_tool:"
)

// Escalation is one thing the reaper or the scheduler found.
type Escalation struct {
	Kind       EscalationKind
	DispatchID string
	TaskID     string
	NodeID     string
	QuestionID string
	AccountRef string
	Reason     string
	// Profile, Fallback and Until say which profile was out of quota, which
	// one took the Task, and when the first is free again.
	Profile  string
	Fallback string
	Until    int64
}

// Reaped is what one Reap did.
type Reaped struct {
	Expired     []string
	Unknown     []string
	StopUnknown []string
	Escalations []Escalation
}
