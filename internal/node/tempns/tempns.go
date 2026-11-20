// Package tempns gives a worker its Dispatch's temp directory as /tmp and
// /var/tmp (issue #895). TMPDIR already points there (internal/node/runner,
// temp.go), but a path a worker types itself (/tmp/pgdata, /tmp/refs.x)
// does not follow it, and nothing removed what it left. On Linux the
// worker, and its worktree's setup script, run inside bubblewrap with only
// those two directories bound over: `bwrap --dev-bind / / --bind T /tmp
// --bind T /var/tmp -- argv`, as the Bots' launcher binds a private /tmp
// (deploy/coordinator/hypomnema-pi). Everything else stays the host's: no
// PID or network namespace, no --die-with-parent, no new session, so the
// shim's group kill still reaches every process. What lands in /tmp is in
// the Dispatch's temp directory, which CleanTemp removes after the stop.
//
// The bwrap is the one on PATH, else the one Codex ships beside its binary
// (codex-rs/linux-sandbox/README.md: "prefers the first bwrap found on PATH
// ... falls back to the bundled codex-resources/bwrap"). A short probe at
// the node's start checks that it can make the namespace, as Codex probes
// its system bwrap (codex-rs/sandboxing/src/bwrap.rs). When none can, the
// worker runs as before, with TMPDIR only, and the node says why.
package tempns

import "github.com/kl3inIT/hypomnema/internal/shared/nodeproto"

// Isolation is whether the node's workers get their temp directory as /tmp.
type Isolation struct {
	// Bwrap is the bubblewrap that made the namespace when probed; empty: off.
	Bwrap string
	// Reason says why it is off.
	Reason string
}

// On says the workers run inside bubblewrap.
func (i Isolation) On() bool { return i.Bwrap != "" }

// Wrap is argv run with temp as /tmp and /var/tmp; argv itself when the
// isolation is off or there is no temp directory.
func (i Isolation) Wrap(argv []string, temp string) []string {
	if !i.On() || temp == "" || len(argv) == 0 {
		return argv
	}

	return append(i.args(temp), argv...)
}

func (i Isolation) args(temp string) []string {
	return []string{i.Bwrap, "--dev-bind", "/", "/", "--bind", temp, "/tmp", "--bind", temp, "/var/tmp", "--"}
}

// Hello is what the node says of it at its hello.
func (i Isolation) Hello() *nodeproto.TempIsolation {
	if i.On() {
		return &nodeproto.TempIsolation{On: true}
	}

	return &nodeproto.TempIsolation{Reason: i.Reason}
}
