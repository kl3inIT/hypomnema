package orchestration

import "slices"

// taskTransitions lists every allowed Task status change, from -> to. Every
// write that changes a status is a guarded UPDATE whose allowed "from" values
// come from here, and a test checks every pair (as Coder's transition matrix,
// coderd/x/chatd/chatstate/transition.go).
var taskTransitions = map[TaskStatus][]TaskStatus{
	TaskPending: {TaskReady, TaskBlocked, TaskCanceled},
	// running: a Dispatch was created for it.
	TaskReady: {TaskRunning, TaskBlocked, TaskCanceled},
	// ready: a retry; blocked: a Gate opened, the Dispatch is unknown, or the account needs a login.
	TaskRunning: {TaskSucceeded, TaskFailed, TaskReady, TaskBlocked, TaskCanceled},
	// pending: the failed dependency was reopened; running: the unknown Dispatch was reattached;
	// succeeded, failed, ready: the unknown Dispatch was settled.
	TaskBlocked: {TaskPending, TaskReady, TaskRunning, TaskSucceeded, TaskFailed, TaskCanceled},
	// ready: the owner reopened it.
	TaskFailed: {TaskReady},
}

// taskTerminal lists the statuses in which a Task has ended. A failed Task
// can still be reopened by the owner.
var taskTerminal = []TaskStatus{TaskSucceeded, TaskFailed, TaskCanceled}

// runTransitions lists every allowed Run status change.
var runTransitions = map[RunStatus][]RunStatus{
	RunOpen: {RunFinished, RunCanceled},
}

// dispatchTransitions lists every allowed Dispatch status change.
var dispatchTransitions = map[DispatchStatus][]DispatchStatus{
	DispatchQueued:   {DispatchStarting, DispatchExpired, DispatchCanceled},
	DispatchStarting: {DispatchRunning, DispatchFailed, DispatchStopping, DispatchUnknown},
	// gated: the worker ended to wait for a decision.
	DispatchRunning:  {DispatchSucceeded, DispatchFailed, DispatchGated, DispatchStopping, DispatchUnknown},
	DispatchStopping: {DispatchCanceled, DispatchStopUnknown},
	// running: reattached; abandoned: the coordinator or the owner gave up finding out.
	DispatchUnknown: {
		DispatchRunning, DispatchSucceeded, DispatchFailed, DispatchGated, DispatchCanceled, DispatchAbandoned,
	},
	DispatchStopUnknown: {DispatchSucceeded, DispatchFailed, DispatchGated, DispatchCanceled, DispatchAbandoned},
}

// dispatchOrder fixes the iteration order over Dispatch statuses.
var dispatchOrder = []DispatchStatus{
	DispatchQueued, DispatchStarting, DispatchRunning, DispatchStopping, DispatchUnknown, DispatchStopUnknown,
	DispatchSucceeded, DispatchFailed, DispatchCanceled, DispatchExpired, DispatchAbandoned, DispatchGated,
}

// canTask reports whether a Task may go from one status to another.
func canTask(from, to TaskStatus) bool {
	return slices.Contains(taskTransitions[from], to)
}

// canRun reports whether a Run may go from one status to another.
func canRun(from, to RunStatus) bool {
	return slices.Contains(runTransitions[from], to)
}

// canDispatch reports whether a Dispatch may go from one status to another.
func canDispatch(from, to DispatchStatus) bool {
	return slices.Contains(dispatchTransitions[from], to)
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

// dispatchFrom returns the statuses a Dispatch may move to "to" from.
func dispatchFrom(to DispatchStatus) []string {
	var from []string

	for _, f := range dispatchOrder {
		if canDispatch(f, to) {
			from = append(from, string(f))
		}
	}

	return from
}

// taskOrder fixes the iteration order over statuses.
var taskOrder = []TaskStatus{
	TaskPending, TaskReady, TaskRunning, TaskBlocked, TaskSucceeded, TaskFailed, TaskCanceled,
}

// terminal reports whether a Task has ended.
func (s TaskStatus) terminal() bool {
	return slices.Contains(taskTerminal, s)
}

// Terminal reports whether a Task in this status has ended for good.
func (s TaskStatus) Terminal() bool { return s.terminal() }

// settled reports whether a Dispatch has ended for good.
func (s DispatchStatus) settled() bool {
	return len(dispatchTransitions[s]) == 0
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
