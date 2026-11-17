// Package selfupdate puts a newer release of the control plane in place on its machine (issue #279), as Beszel's
// `beszel update` does for its hub (internal/ghupdate/ghupdate.go) and OpenMausBot's deploy/update.sh does for its
// server: run as root by a systemd oneshot that a timer starts, one at a time under a lock, it reads the release
// manifest, downloads the binary into a root-owned folder, requires the manifest's SHA-256 and the version the
// binary reports, and then has the NEW binary install itself (`hypomnema update install`), so that the steps and the
// launchers are always the release's own: stop the control plane, back up its database, put the binary in place,
// migrate as the control plane's user, start it, wait for /readyz, and only then run the release's
// setup-coordinator.sh. A control plane that does not become ready is put back as it was, binary and database.
package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// Options say which control plane is updated, from where.
type Options struct {
	// Current is the installed release, the version of the binary that runs the update.
	Current string
	// User runs the control plane; Home holds bin/hypomnema, env and data/ (~/hypomnema-cp).
	User string
	Home string
	// Unit is the control plane's systemd unit.
	Unit string
	// ReleaseURL holds the release's manifests (the latest GitHub release's downloads).
	ReleaseURL string
	// State is a root-owned folder for the lock and the downloads.
	State string
	// Ready is the control plane's /readyz.
	Ready string
	// ReadyWithin is how long a started control plane has to become ready.
	ReadyWithin time.Duration
	// TurnsWithin is how long the update waits for Bots' turns before it stops the control plane anyway;
	// DefaultTurnsWithin when zero (issue #716).
	TurnsWithin time.Duration
	// TurnsEvery is how often it asks; a second when zero.
	TurnsEvery time.Duration

	HTTP *http.Client
	// Run runs a command, its output to Out; nil runs it.
	Run func(ctx context.Context, name string, args ...string) error
	// Reports is the version a binary reports; nil runs `<binary> version`.
	Reports func(ctx context.Context, path string) (string, error)
	Out     io.Writer
}

// Defaults for zm's layout (docs: the deploy notes).
const (
	DefaultUnit       = "hypomnema-cp.service"
	DefaultReleaseURL = "https://github.com/kl3inIT/hypomnema/releases/latest/download"
	DefaultState      = "/var/lib/hypomnema-update"
	DefaultReady      = "http://127.0.0.1:18990/readyz"
)

// DefaultTurnsWithin bounds the update's wait for Bots' turns (issue #716): a few minutes, as the owner asked
// (2026-11-17), where Hermes waits up to its restart_after_turn_timeout before a restart interrupts what still runs.
// A turn still running then is cut, and the control plane continues it when it starts again.
const DefaultTurnsWithin = 5 * time.Minute

// The control plane's control socket (issue #716): in its data folder, next to its database, it answers GET
// PathTurns with Turns, the chats whose Bot's turn runs now.
const (
	ControlSocket = "control.sock"
	PathTurns     = "/turns"
	// UpdatingFile, in the same folder, names the process of a running update (marker_unix.go).
	UpdatingFile = "updating"
)

// Turns is the control socket's answer to GET PathTurns.
type Turns struct {
	// Chats are the chats whose Bot works on a turn.
	Chats []string `json:"chats"`
}

// ErrBusy means another update holds the lock.
var ErrBusy = errors.New("another update is running")
