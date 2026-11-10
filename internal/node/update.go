package node

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
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

	maxManifestBytes = 4 << 10
	maxBinaryBytes   = 256 << 20
	updateTimeout    = 10 * time.Minute
)

// ErrUpdated ends the node after it replaced its own binary; its service
// manager starts the new one.
var ErrUpdated = errors.New("the node updated itself and restarts")

var (
	releasedVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	hexSHA256       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Updater replaces the node's binary with a newer release.
type Updater struct {
	// Exe is the running binary's path and Version its version.
	Exe     string
	Version string
	HTTP    *http.Client
	// Reports returns the version a binary reports; nil runs `<binary> version`.
	Reports func(ctx context.Context, path string) (string, error)
}

// manifest is a release's manifest for one platform.
type manifest struct {
	version string
	url     string
	sha256  string
}

// CanUpdate says whether this binary is one that updates itself: a released
// version, not a build of a commit.
func (u Updater) CanUpdate() bool { return releasedVersion.MatchString(u.Version) }

// Update looks at the release at releaseURL and, when it is newer than this
// binary, downloads it, checks it and puts it in the binary's place. It
// returns the new version, or "" when there was nothing newer.
func (u Updater) Update(ctx context.Context, releaseURL string) (string, error) {
	if !u.CanUpdate() {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	m, err := u.manifest(ctx, strings.TrimSuffix(releaseURL, "/")+"/"+runtime.GOOS+"-"+runtime.GOARCH+".txt")
	if err != nil {
		return "", err
	}

	if !newer(m.version, u.Version) {
		return "", nil
	}

	staged := u.Exe + ".update"
	defer os.Remove(staged) //nolint:errcheck // gone already when it was renamed into place

	if err := u.download(ctx, m, staged); err != nil {
		return "", err
	}

	reports := u.Reports
	if reports == nil {
		reports = reportedVersion
	}

	got, err := reports(ctx, staged)
	if err != nil {
		return "", fmt.Errorf("the downloaded node does not run: %w", err)
	}

	if got != m.version {
		return "", fmt.Errorf("the downloaded node says it is %s, not the %s its manifest names", got, m.version)
	}

	// Renamed over the running binary, which keeps its old file until it ends.
	if err := os.Rename(staged, u.Exe); err != nil {
		return "", fmt.Errorf("replacing %s: %w", u.Exe, err)
	}

	return m.version, nil
}

func (u Updater) get(ctx context.Context, url string) (*http.Response, error) {
	if !allowedReleaseURL(url) {
		return nil, fmt.Errorf("release URL %q is not https", url)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", url, err)
	}

	client := u.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()

		return nil, fmt.Errorf("fetching %s: status %d", url, resp.StatusCode)
	}

	return resp, nil
}

// manifest reads a release manifest: three lines, each exactly once,
// version=, url= and sha256=, as the install script requires.
func (u Updater) manifest(ctx context.Context, url string) (manifest, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return manifest{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	var (
		m    manifest
		seen = map[string]int{}
	)

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxManifestBytes))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return manifest{}, errors.New("the release manifest has a line that is not understood")
		}

		seen[key]++

		switch key {
		case "version":
			m.version = value
		case "url":
			m.url = value
		case "sha256":
			m.sha256 = value
		default:
			return manifest{}, errors.New("the release manifest has a line that is not understood")
		}
	}

	if err := scanner.Err(); err != nil {
		return manifest{}, fmt.Errorf("reading the release manifest: %w", err)
	}

	switch {
	case seen["version"] != 1 || seen["url"] != 1 || seen["sha256"] != 1:
		return manifest{}, errors.New("the release manifest must name exactly one version, url and sha256")
	case !releasedVersion.MatchString(m.version):
		return manifest{}, fmt.Errorf("the release manifest's version %q is not a release", m.version)
	case !hexSHA256.MatchString(m.sha256):
		return manifest{}, errors.New("the release manifest's sha256 is not 64 lower-case hex characters")
	case !allowedReleaseURL(m.url):
		return manifest{}, errors.New("the release manifest's url is not allowed")
	}

	return m, nil
}

// download writes the release's binary to path as an executable, and fails
// unless it has the manifest's hash.
func (u Updater) download(ctx context.Context, m manifest, path string) error {
	resp, err := u.get(ctx, m.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) //nolint:gosec // an executable, owner-only
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	sum := sha256.New()

	n, err := io.Copy(io.MultiWriter(out, sum), io.LimitReader(resp.Body, maxBinaryBytes+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("downloading %s: %w", m.url, err)
	}

	if n > maxBinaryBytes {
		return fmt.Errorf("the download from %s is larger than %d bytes", m.url, maxBinaryBytes)
	}

	if got := hex.EncodeToString(sum.Sum(nil)); got != m.sha256 {
		return fmt.Errorf("the download's SHA-256 is %s, not the manifest's %s", got, m.sha256)
	}

	return nil
}

func reportedVersion(ctx context.Context, path string) (string, error) {
	// The binary just downloaded, whose hash was checked.
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

// allowedReleaseURL is the install script's rule: https, or plain http for a
// release mirror on this machine.
func allowedReleaseURL(url string) bool {
	return strings.HasPrefix(url, "https://") ||
		strings.HasPrefix(url, "http://127.0.0.1:") || strings.HasPrefix(url, "http://localhost:")
}

// newer says whether release version a is newer than b, both vX.Y.Z.
func newer(a, b string) bool {
	pa, pb := releasedVersion.FindStringSubmatch(a), releasedVersion.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return false
	}

	for i := 1; i <= 3; i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])

		if na != nb {
			return na > nb
		}
	}

	return false
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
