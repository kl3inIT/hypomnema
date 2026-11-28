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
	// Repo and Base are where the Run's workers make their worktrees: Repo is
	// the repository's path on RepoHost (owner/name on github.com).
	Repo     string
	RepoHost string
	Base     string
	// CoordinatorTokenID is the coordinator token that coordinates the Run;
	// empty when the owner created it.
	CoordinatorTokenID string
	// ChatID is the chat whose coordinator created the Run, if any.
	ChatID string
	// Bot is the Bot of that chat; empty for a Run without a chat. Filled by
	// GetRun and ListRuns.
	Bot string
	// RoutedBy is why the Run went to its chat (issue #1334); empty for a Run made before, or without a chat.
	RoutedBy RoutedBy
	// Uses are the Connections the Run's workers may use ("google:work").
	Uses        []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time
	ReviewStall *ReviewStall
}

// Task is one unit of work inside a Run.
type Task struct {
	ID            string
	RunID         string
	Position      int
	Profile       string
	Kind          string
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
	// WaitingReason is why a ready Task has no Dispatch yet, as the scheduler's
	// last pass found: a Reason code (dispatch_types.go). Empty for a Task that
	// is not ready, or that no pass has looked at yet (issue #660).
	WaitingReason string
	// Continues is the Task whose branch it works on and PushBranch that branch, which its publish_branch pushes
	// to, empty for its own hyp/<id>; StartFrom is the branch its worktree starts from and PRBase the base of the
	// pull request it opens, empty for the Run's base (issue #690).
	Continues  string
	PushBranch string
	StartFrom  string
	PRBase     string
}

// NewTask describes a Task to create. Ref names it within one CreateTasks
// call; DependsOn lists Refs of earlier Tasks in the same call or ids of
// Tasks already in the Run. Profile defaults to DefaultProfile. Runtime,
// Model and Effort are optional; empty means the profile's.
type NewTask struct {
	Ref       string
	Profile   string
	Kind      string `json:",omitempty"`
	ReviewOf  string `json:",omitempty"`
	Spec      string
	Runtime   string
	Model     string
	Effort    string
	DependsOn []string
	// Continues is the id of a Task of the same repository, of any Run, whose branch this one works on and pushes
	// into, so that Task's pull request takes its commits (issue #690). StartFrom is a branch the worktree starts
	// from instead of the Run's base; PRBase, PRBaseStartFrom (the default with StartFrom) or PRBaseRunBase, is the
	// base of the pull request it opens.
	Continues string `json:",omitempty"`
	StartFrom string `json:",omitempty"`
	PRBase    string `json:",omitempty"`
}

// NewRun is a Run to create. Repo is a registered repository's name,
// owner/name on GitHub, or a repository's URL on a known host; Base defaults
// to the registered repository's, else DefaultBase.
// CoordinatorTokenID names the coordinator token that coordinates it, if any.
type NewRun struct {
	Objective          string
	CreatedBy          string
	Repo               string
	Base               string
	CoordinatorTokenID string
	// ChatID names the chat whose coordinator creates it, if any.
	ChatID string
	// ChatNamed says the caller named ChatID itself (`runs create --chat`), rather than it being the chat of the Bot
	// whose token creates the Run: a named chat is kept whatever Bot owns the Run's repository (issue #1334).
	ChatNamed bool
	// Uses are the Connections the Run's workers may use ("google:work").
	Uses []string
	// Bot is the Bot whose token creates it, if any: it gives the Run only
	// Connections it may use itself (issue #169).
	Bot string
}

// RunFilter selects Runs; empty fields match everything.
type RunFilter struct {
	Status RunStatus
	// ChatID keeps the Runs a coordinator created from that chat.
	ChatID string
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
