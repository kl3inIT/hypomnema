package orchestration

import "errors"

// Errors callers match with errors.Is.
var (
	// ErrNotFound: the Run or Task does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict: the Run or Task is not in a status that allows the change.
	ErrConflict = errors.New("conflict")
	// ErrInvalid: the request itself is wrong (empty field, unknown dependency).
	ErrInvalid = errors.New("invalid request")
)
