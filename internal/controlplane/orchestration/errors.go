package orchestration

import "errors"

// Errors callers match with errors.Is.
var (
	// ErrNotFound: the Run, Task, Dispatch or Conversation does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the record is not in a status that allows the change, or a
	// node call names a stale epoch or reuses a request id with another payload.
	ErrConflict = errors.New("conflict")
	// ErrInvalid: the request itself is wrong (empty field, unknown dependency).
	ErrInvalid = errors.New("invalid request")
	// ErrCoolingDown: the slot (node, runtime, account) hit a rate limit and
	// takes no Dispatch yet. The scheduler waits and escalates after an hour.
	ErrCoolingDown = errors.New("slot is cooling down")
)
