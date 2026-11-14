package orchestration

import "github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"

// Errors callers match with errors.Is. Each is storeerr's, so a domain store
// gives the same one; storeerr says what each means.
var (
	ErrNotFound    = storeerr.ErrNotFound
	ErrConflict    = storeerr.ErrConflict
	ErrInvalid     = storeerr.ErrInvalid
	ErrCoolingDown = storeerr.ErrCoolingDown
	ErrUnavailable = storeerr.ErrUnavailable
	ErrForbidden   = storeerr.ErrForbidden
)
