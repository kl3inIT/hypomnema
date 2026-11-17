package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	releases "github.com/kl3inIT/hypomnema/internal/shared/release"
)

// The node updates itself as Nezha's agent does (cmd/agent/main.go, doSelfUpdate and its loop in run): only a
// released version, at start and then at a random interval of one to two days, to a version newer than its own,
// and at once when its control plane says at a hello that it runs a newer release, as Tailscale's control says
// ClientVersion and Coder's server its build version (issue #783, docs/research/2026-11-17-node-update-interval.md),
// and after replacing its binary it ends with a failure so that its service manager starts the new one. The
// agent's own configuration turns it off (Nezha: disable_auto_update) or sets the interval (self_update_period).
//
// What is downloaded is what the install script downloads, checked the same way (nodeapi/install.sh.tmpl): the
// release manifest of this platform names a version, a URL and a SHA-256; the download must have that hash and
// must itself report that version before it replaces anything.
//
// Changed from Nezha: the node updates only when systemd runs it, since ending is a restart only there; and not
// while a terminal is open, whose shell would end with the node. Workers are kept across a restart by their shims.

const (
	// The interval between checks is random in this range (Nezha: 1440 to 2880 minutes).
	updateEveryMin = 24 * time.Hour
	updateEveryMax = 48 * time.Hour

	updateTimeout = 10 * time.Minute
)

// ErrUpdated ends the node after it replaced its own binary; its service
// manager starts the new one.
var ErrUpdated = errors.New("the node updated itself and restarts")

// Updater replaces the node's binary with a newer release.
type Updater struct {
	// Exe is the running binary's path and Version its version.
	Exe     string
	Version string
	HTTP    *http.Client
	// Reports returns the version a binary reports; nil runs `<binary> version`.
	Reports func(ctx context.Context, path string) (string, error)
}

// CanUpdate says whether this binary is one that updates itself: a released
// version, not a build of a commit.
func (u Updater) CanUpdate() bool { return releases.IsRelease(u.Version) }

// Update looks at the release at releaseURL and, when it is newer than this
// binary, downloads it, checks it and puts it in the binary's place. It
// returns the new version, or "" when there was nothing newer.
func (u Updater) Update(ctx context.Context, releaseURL string) (string, error) {
	if !u.CanUpdate() {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	m, err := releases.ReadManifest(ctx, u.HTTP,
		strings.TrimSuffix(releaseURL, "/")+"/"+runtime.GOOS+"-"+runtime.GOARCH+".txt")
	if err != nil {
		return "", err
	}

	if !releases.Newer(m.Version, u.Version) {
		return "", nil
	}

	staged := u.Exe + ".update"
	defer os.Remove(staged) //nolint:errcheck // gone already when it was renamed into place

	if err := releases.Download(ctx, u.HTTP, m, staged); err != nil {
		return "", err
	}

	reports := u.Reports
	if reports == nil {
		reports = releases.Reported
	}

	got, err := reports(ctx, staged)
	if err != nil {
		return "", fmt.Errorf("the downloaded node does not run: %w", err)
	}

	if got != m.Version {
		return "", fmt.Errorf("the downloaded node says it is %s, not the %s its manifest names", got, m.Version)
	}

	// Renamed over the running binary, which keeps its old file until it ends.
	if err := os.Rename(staged, u.Exe); err != nil {
		return "", fmt.Errorf("replacing %s: %w", u.Exe, err)
	}

	return m.Version, nil
}

// ControlPlane is what the control plane said at its last hello: where the node's releases are, its own version,
// and a signal after each hello. The node's daemon is one.
type ControlPlane interface {
	// ReleaseURL is "" until the first hello.
	ReleaseURL() string
	// ControlPlaneVersion is "" until the first hello.
	ControlPlaneVersion() string
	// Hellos receives after each hello.
	Hellos() <-chan struct{}
}

// UpdateLoop checks for a newer release at start, at each interval, and at once after a hello whose control plane
// is newer than this binary (issue #783), and returns ErrUpdated once the binary was replaced. busy says the node
// must not end now. every is the interval; zero is Nezha's random one to two days.
func (u Updater) UpdateLoop(
	ctx context.Context, cp ControlPlane, busy func() bool, every time.Duration, logger *slog.Logger,
) error {
	// The first check waits for the node's first hello, which names the release. soon is the wait when there is
	// nothing to check against yet, the node is busy, or the control plane runs a newer release than the node
	// found; recheck the wait, after the binary was replaced, for what keeps the node busy.
	w := loopWaits{first: 30 * time.Second, every: every, soon: 10 * time.Minute, recheck: 5 * time.Second}
	if every > 0 {
		w.soon = min(w.soon, every)
		w.recheck = min(w.recheck, every)
	}

	return u.updateLoop(ctx, cp, busy, w, logger)
}

// loopWaits are the update loop's waits; every zero is a random one to two days.
type loopWaits struct {
	first, every, soon, recheck time.Duration
}

// behind says whether the control plane said a version newer than this binary's: the release it runs is one the
// node has not taken yet.
func (u Updater) behind(cp ControlPlane) bool {
	return releases.Newer(cp.ControlPlaneVersion(), u.Version)
}

// nextUpdateCheck is the wait to the next check: every, or a random one to two days when it is zero.
func nextUpdateCheck(every time.Duration) time.Duration {
	if every > 0 {
		return every
	}

	return updateEveryMin + rand.N(updateEveryMax-updateEveryMin) //nolint:gosec // G404: spreading checks, not a secret
}

func (u Updater) updateLoop(
	ctx context.Context, cp ControlPlane, busy func() bool, w loopWaits, logger *slog.Logger,
) error {
	timer := time.NewTimer(w.first)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		case <-cp.Hellos():
			// A hello whose control plane is not newer leaves the timer as it was.
			if !u.behind(cp) {
				continue
			}

			logger.InfoContext(ctx, "the control plane runs a newer release; the node looks for it",
				slog.String("event", "node.update_told"), slog.String("version", u.Version),
				slog.String("control_plane_version", cp.ControlPlaneVersion()))
		}

		version, err := u.check(ctx, cp, busy, logger)
		if err != nil {
			return err
		}

		if version != "" {
			// What became busy during the download is waited for too: the new binary is in place, and the node
			// ends once it is free (issue #545).
			if err := waitFree(ctx, busy, w.recheck); err != nil {
				return err
			}

			logger.InfoContext(ctx, "the node updated itself and restarts",
				slog.String("event", "node.updated"), slog.String("from", u.Version), slog.String("to", version))

			return ErrUpdated
		}

		wait := nextUpdateCheck(w.every)
		if cp.ReleaseURL() == "" || busy() || u.behind(cp) {
			wait = w.soon
		}

		timer.Reset(wait)
	}
}

// check looks for a newer release once, unless there is nowhere to look yet or the node is busy, and returns the
// version it put in place, "" when none. A failed check is logged and is not an error.
func (u Updater) check(ctx context.Context, cp ControlPlane, busy func() bool, logger *slog.Logger) (string, error) {
	url := cp.ReleaseURL()
	if url == "" || busy() {
		return "", nil
	}

	version, err := u.Update(ctx, url)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		logger.WarnContext(ctx, "the node did not update itself",
			slog.String("event", "node.update_failed"), slog.String("error", err.Error()))

		return "", nil
	}

	return version, nil
}

// waitFree returns once busy says the node may end, looking every interval.
func waitFree(ctx context.Context, busy func() bool, every time.Duration) error {
	for busy() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}

	return nil
}
