// Package storeerr holds the errors every orchestration store returns, so a
// domain store and the root package give the same ones (Omnara's
// storage/storeerr). The root package re-exports them.
package storeerr

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
	// ErrForbidden: the caller may not do this, such as a Bot giving work to
	// a profile it may not give work to.
	ErrForbidden = errors.New("forbidden")
)
