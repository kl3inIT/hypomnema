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

// UpdateLoop checks for a newer release at start and then at each interval,
// and returns ErrUpdated once the binary was replaced. releaseURL gives the
// release the control plane named, "" until it has; busy says the node must
// not end now. every is the interval; zero is Nezha's random one to two days.
func (u Updater) UpdateLoop(
	ctx context.Context, releaseURL func() string, busy func() bool, every time.Duration, logger *slog.Logger,
) error {
	// The first check waits for the node's first hello, which names the release.
	return u.updateLoop(ctx, releaseURL, busy, 30*time.Second, every, logger)
}

func (u Updater) updateLoop(
	ctx context.Context, releaseURL func() string, busy func() bool, first, every time.Duration, logger *slog.Logger,
) error {
	// soon is the wait when there is nothing to check against yet or the node is busy.
	soon := 10 * time.Minute
	if every > 0 && every < soon {
		soon = every
	}

	wait := first

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		wait = every
		if wait <= 0 {
			wait = updateEveryMin + rand.N(updateEveryMax-updateEveryMin) //nolint:gosec // G404: spreading checks, not a secret
		}

		url := releaseURL()
		if url == "" || busy() {
			wait = soon

			continue
		}

		version, err := u.Update(ctx, url)
		if err != nil {
			logger.WarnContext(ctx, "the node did not update itself",
				slog.String("event", "node.update_failed"), slog.String("error", err.Error()))

			continue
		}

		if version != "" {
			logger.InfoContext(ctx, "the node updated itself and restarts",
				slog.String("event", "node.updated"), slog.String("from", u.Version), slog.String("to", version))

			return ErrUpdated
		}
	}
}
