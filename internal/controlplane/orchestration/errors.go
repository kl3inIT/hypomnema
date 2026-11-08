package orchestration

import "errors"

// Errors callers match with errors.Is.
var (
	// ErrNotFound: the Run, Task, Dispatch, Conversation, node or account does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the record is not in a status that allows the change, or a
	// node call names a stale epoch or reuses a request id with another payload.
	ErrConflict = errors.New("conflict")
	// ErrInvalid: the request itself is wrong (empty field, unknown dependency).
	ErrInvalid = errors.New("invalid request")
	// ErrCoolingDown: the account hit a rate limit and takes no Dispatch yet.
	ErrCoolingDown = errors.New("account is cooling down")
	// ErrUnavailable: no capacity now; the system, the node or the account
	// cannot take the Dispatch (at the limit, offline, drained or unknown).
	ErrUnavailable = errors.New("unavailable")
)
