//go:build !linux

package tempns

import "context"

// Probe says the isolation is off: bubblewrap is Linux's. A worker on
// macOS keeps TMPDIR only, a limit accepted in issue #874.
func Probe(context.Context, string) Isolation {
	return Isolation{Reason: "bubblewrap runs on Linux only; workers use TMPDIR"}
}
