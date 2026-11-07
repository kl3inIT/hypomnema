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
)

// Run is one objective from the owner.
type Run struct {
	ID         string
	Objective  string
	Status     RunStatus
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

// Task is one unit of work inside a Run.
type Task struct {
	ID            string
	RunID         string
	Position      int
	Role          string
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
}

// NewTask describes a Task to create. Ref names it within one CreateTasks
// call; DependsOn lists Refs of earlier Tasks in the same call or ids of
// Tasks already in the Run. Runtime, Model and Effort are optional; empty
// means the role's default.
type NewTask struct {
	Ref       string
	Role      string
	Spec      string
	Runtime   string
	Model     string
	Effort    string
	DependsOn []string
}
