//go:build !linux

package memlimit

import (
	"context"
	"errors"
)

// Probe says the limit is off: systemd and cgroup v2 are Linux's. A worker on macOS runs without a ceiling, as
// before (issue #1313).
func Probe(context.Context) Limit {
	return Limit{Reason: "the machine is not Linux: there is no systemd slice to limit the workers' memory"}
}

// OwnOOMKills is never asked off Linux, where no worker runs in a scope.
func OwnOOMKills() (int64, error) { return 0, errors.New("cgroups are Linux's") }
