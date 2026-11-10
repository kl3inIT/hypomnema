// Package cloudcode holds what the control plane presents to Google's Cloud
// Code backend when it works under an Antigravity account: the hosts, and
// the user agent of the Antigravity IDE with its current version (issue #20,
// step 4). The login and the gateway's adapter both use it.
//
// The backend refuses newer models to a client whose version is too old, so
// the version is read from the IDE's own update manifest, as CLIProxyAPI
// (internal/misc/antigravity_version.go, commit c5251b5, MIT), omp
// (packages/catalog/src/wire/gemini-headers.ts) and opencode-antigravity-auth
// (src/plugin/version.ts) do; read on 2026-11-10.
package cloudcode

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DailyHost serves chat for Antigravity accounts in every reference.
	DailyHost = "https://daily-cloudcode-pa.googleapis.com"
	// ProdHost is the host Google's own Gemini CLI calls. 9router finds a
	// project there when the daily host refuses the call.
	ProdHost = "https://cloudcode-pa.googleapis.com"
	// APIVersion is the path every method is under.
	APIVersion = "v1internal"

	// fallbackVersion is used until the manifest was read, or when it cannot
	// be. The backend refuses newer models below 2.23.0.
	fallbackVersion = "2.29.0"
	// platform is what every reference reports, on any machine.
	platform = "os_type=darwin; arch=arm64"
	// manifestURL is the IDE's update manifest for that platform.
	manifestURL = "https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
	// versionTTL is how long a version read from the manifest is used.
	versionTTL      = 6 * time.Hour
	manifestTimeout = 10 * time.Second
	maxManifest     = 4 << 10
)

// changelists are the build numbers of the releases they are known for; a
// release that is not here is reported without one (CLIProxyAPI).
var changelists = map[string]string{
	"2.23.0": "998057262",
	"2.24.0": "1000335682",
	"2.29.0": "1013060670",
}

var versionLine = regexp.MustCompile(`^version:\s*['"]?(\d+\.\d+\.\d+)['"]?\s*$`)

// Identity gives the user agent of the Antigravity IDE at its current version.
type Identity struct {
	client   *http.Client
	manifest string
	now      func() time.Time

	mu      sync.Mutex
	version string
	readAt  time.Time
}

// NewIdentity returns an Identity that reads the version from the IDE's
// update manifest. manifest is empty outside tests.
func NewIdentity(manifest string, now func() time.Time) *Identity {
	if manifest == "" {
		manifest = manifestURL
	}

	return &Identity{
		client: &http.Client{Timeout: manifestTimeout}, manifest: manifest, now: now, version: fallbackVersion,
	}
}

// UserAgent is the IDE's user agent. It reads the manifest when the version
// it has is older than six hours, and keeps the one it has when that fails.
func (i *Identity) UserAgent(ctx context.Context) string {
	version := i.currentVersion(ctx)

	metadata := "aidev_client; " + platform
	if cl := changelists[version]; cl != "" {
		metadata += "; cl=" + cl
	}

	return "antigravity/hub/" + version + " (" + metadata + ")"
}

func (i *Identity) currentVersion(ctx context.Context) string {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.readAt.IsZero() && i.now().Sub(i.readAt) < versionTTL {
		return i.version
	}

	// Whatever the outcome, the manifest is not asked again for a while.
	i.readAt = i.now()

	if version, ok := i.readManifest(ctx); ok && !older(version, i.version) {
		i.version = version
	}

	return i.version
}

func (i *Identity) readManifest(ctx context.Context) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.manifest, nil)
	if err != nil {
		return "", false
	}

	// The updater the IDE itself uses.
	req.Header.Set("User-Agent", "electron-builder")

	resp, err := i.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close() //nolint:errcheck // Read to the end or abandoned; closing adds nothing.

	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	lines := bufio.NewScanner(io.LimitReader(resp.Body, maxManifest))
	for lines.Scan() {
		if m := versionLine.FindStringSubmatch(strings.TrimSpace(lines.Text())); m != nil {
			return m[1], true
		}
	}

	return "", false
}

// older reports whether version a is lower than b; both are x.y.z.
func older(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")

	for n := range min(len(as), len(bs)) {
		if len(as[n]) != len(bs[n]) {
			return len(as[n]) < len(bs[n])
		}

		if as[n] != bs[n] {
			return as[n] < bs[n]
		}
	}

	return false
}
