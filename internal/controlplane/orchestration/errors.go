package orchestration

import (
	"errors"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

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

// ErrNodeOffline is a node the control plane holds no socket for now, with ErrUnavailable: one that has not
// reconnected yet after the control plane restarted, while its last heartbeat still lists it as connected (issue
// #1060).
var ErrNodeOffline = errors.New("the machine is offline")
