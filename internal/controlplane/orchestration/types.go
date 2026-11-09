package orchestration

import "time"

// RunStatus is where a Run stands.
type RunStatus string

// Run statuses.
const (
	RunOpen     RunStatus = "open"
	RunFinished RunStatus = "finished"
	RunCanceled RunStatus = "canceled"
)

// TaskStatus is where a Task stands.
type TaskStatus string

// Task statuses. A Task is ready when every Task it depends on succeeded.
const (
	TaskPending   TaskStatus = "pending"
	TaskReady     TaskStatus = "ready"
	TaskRunning   TaskStatus = "running"
	TaskBlocked   TaskStatus = "blocked"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
	TaskCanceled  TaskStatus = "canceled"
)

// BlockedReason says why a blocked Task cannot go on.
type BlockedReason string

// Reasons a Task is blocked.
const (
	BlockedNone             BlockedReason = ""
	BlockedDependencyFailed BlockedReason = "dependency_failed"
	BlockedGate             BlockedReason = "gate"
	BlockedDispatchUnknown  BlockedReason = "dispatch_unknown"
	// BlockedNeedsAuth: the worker's account needs a login; never retried automatically.
	BlockedNeedsAuth BlockedReason = "needs_auth"
)

// Run is one objective from the owner.
type Run struct {
	ID        string
	Objective string
	Status    RunStatus
	CreatedBy string
	// Repo (owner/name) and Base are where the Run's workers make their worktrees.
	Repo string
	Base string
	// CoordinatorTokenID is the coordinator token that coordinates the Run;
	// empty when the owner created it.
	CoordinatorTokenID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	FinishedAt         *time.Time
}

// Task is one unit of work inside a Run.
type Task struct {
	ID            string
	RunID         string
	Position      int
	Profile       string
	Spec          string
	Runtime       string
	Model         string
	Effort        string
	Status        TaskStatus
	BlockedReason BlockedReason
	FailureCount  int
	Result        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	FinishedAt    *time.Time
	// NotBefore: no Dispatch is created before it (back-off, or a rate limit's reset).
	NotBefore *time.Time
	// CancelRequestedAt: a cancel waits for the running Dispatch to stop.
	CancelRequestedAt *time.Time
}

// NewTask describes a Task to create. Ref names it within one CreateTasks
// call; DependsOn lists Refs of earlier Tasks in the same call or ids of
// Tasks already in the Run. Profile defaults to DefaultProfile. Runtime,
// Model and Effort are optional; empty means the profile's.
type NewTask struct {
	Ref       string
	Profile   string
	Spec      string
	Runtime   string
	Model     string
	Effort    string
	DependsOn []string
}

// NewRun is a Run to create. Repo is owner/name; Base defaults to DefaultBase.
// CoordinatorTokenID names the coordinator token that coordinates it, if any.
type NewRun struct {
	Objective          string
	CreatedBy          string
	Repo               string
	Base               string
	CoordinatorTokenID string
}

// RunFilter selects Runs; empty fields match everything.
type RunFilter struct {
	Status RunStatus
}

// RunCursor is where a page of Runs ends; the next page starts after it.
type RunCursor struct {
	CreatedAt time.Time
	ID        string
}

// RunPage is one page of Runs, newest first. Next is set when more follow.
type RunPage struct {
	Runs []Run
	Next *RunCursor
}

// DefaultBase is the branch a Run's worktrees start from unless it names one.
const DefaultBase = "main"
