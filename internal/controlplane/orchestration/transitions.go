package orchestration

import "slices"

// taskTransitions lists every allowed Task status change, from -> to. Every
// write that changes a status is a guarded UPDATE whose allowed "from" values
// come from here, and a test checks every pair (as Coder's transition matrix,
// coderd/x/chatd/chatstate/transition.go).
var taskTransitions = map[TaskStatus][]TaskStatus{
	TaskPending: {TaskReady, TaskBlocked, TaskCanceled},
	// running: a Dispatch was created for it (issue #19, PR 2).
	TaskReady: {TaskRunning, TaskBlocked, TaskCanceled},
	// ready: a retryable failure; blocked: a Gate opened or the Dispatch is unknown.
	TaskRunning: {TaskSucceeded, TaskFailed, TaskReady, TaskBlocked, TaskCanceled},
	// ready: the owner answered the Gate or the coordinator settled the unknown Dispatch.
	TaskBlocked: {TaskReady, TaskFailed, TaskCanceled},
}

// runTransitions lists every allowed Run status change.
var runTransitions = map[RunStatus][]RunStatus{
	RunOpen: {RunFinished, RunCanceled},
}

// canTask reports whether a Task may go from one status to another.
func canTask(from, to TaskStatus) bool {
	return slices.Contains(taskTransitions[from], to)
}

// canRun reports whether a Run may go from one status to another.
func canRun(from, to RunStatus) bool {
	return slices.Contains(runTransitions[from], to)
}

// taskFrom returns the statuses a Task may move to "to" from, as strings for
// the guarded UPDATE.
func taskFrom(to TaskStatus) []string {
	var from []string

	for _, f := range taskOrder {
		if canTask(f, to) {
			from = append(from, string(f))
		}
	}

	return from
}

// taskOrder fixes the iteration order over statuses.
var taskOrder = []TaskStatus{
	TaskPending, TaskReady, TaskRunning, TaskBlocked, TaskSucceeded, TaskFailed, TaskCanceled,
}

// terminal reports whether a Task status is final.
func (s TaskStatus) terminal() bool {
	return len(taskTransitions[s]) == 0
}

// unfinished lists the statuses that are not final.
func unfinished() []string {
	var out []string

	for _, s := range taskOrder {
		if !s.terminal() {
			out = append(out, string(s))
		}
	}

	return out
}
