// Package release reads a Hypomnema release the way the install script does (nodeapi/install.sh.tmpl): a manifest
// for one platform names a version, a URL and a SHA-256, and a download is kept only when it has that hash. The node
// updates itself with it (issue #69, after Nezha's agent) and so does the control plane (issue #279, after Beszel's
// `update`, internal/ghupdate/ghupdate.go).
package release

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxManifestBytes = 4 << 10
	// MaxBinaryBytes is the largest download kept.
	MaxBinaryBytes = 256 << 20
)

var (
	version   = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	described = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)-\d+-g[0-9a-f]+$`)
	hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Manifest is a release's manifest for one platform.
type Manifest struct {
	Version string
	URL     string
	SHA256  string
}

// IsRelease says whether v is a released version, vX.Y.Z, not a build of a commit.
func IsRelease(v string) bool { return version.MatchString(v) }

// AllowedURL is the install script's rule: https, or plain http for a release mirror on this machine.
func AllowedURL(url string) bool {
	return strings.HasPrefix(url, "https://") ||
		strings.HasPrefix(url, "http://127.0.0.1:") || strings.HasPrefix(url, "http://localhost:")
}

// Newer says whether the release a is newer than b. b is a release, or a build of a commit as `git describe` names
// it (vX.Y.Z-N-gHASH), which comes after vX.Y.Z: a release is newer than it only from the next version on. Anything
// else is never older than a release.
func Newer(a, b string) bool {
	pa := version.FindStringSubmatch(a)
	if pa == nil {
		return false
	}

	pb := version.FindStringSubmatch(b)
	if pb == nil {
		pb = described.FindStringSubmatch(b)
	}

	if pb == nil {
		return false
	}

	for i := 1; i <= 3; i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])

		if na != nb {
			return na > nb
		}
	}

	// The same version: a release is not newer than itself or than a commit after it.
	return false
}

func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	if !AllowedURL(url) {
		return nil, fmt.Errorf("release URL %q is not https", url)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", url, err)
	}

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

// ReadManifest reads a release manifest: three lines, each exactly once, version=, url= and sha256=, as the install
// script requires.
func ReadManifest(ctx context.Context, client *http.Client, url string) (Manifest, error) {
	resp, err := get(ctx, client, url)
	if err != nil {
		return Manifest{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	var (
		m    Manifest
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
			return Manifest{}, errors.New("the release manifest has a line that is not understood")
		}

		seen[key]++

		switch key {
		case "version":
			m.Version = value
		case "url":
			m.URL = value
		case "sha256":
			m.SHA256 = value
		default:
			return Manifest{}, errors.New("the release manifest has a line that is not understood")
		}
	}

	if err := scanner.Err(); err != nil {
		return Manifest{}, fmt.Errorf("reading the release manifest: %w", err)
	}

	switch {
	case seen["version"] != 1 || seen["url"] != 1 || seen["sha256"] != 1:
		return Manifest{}, errors.New("the release manifest must name exactly one version, url and sha256")
	case !IsRelease(m.Version):
		return Manifest{}, fmt.Errorf("the release manifest's version %q is not a release", m.Version)
	case !hexSHA256.MatchString(m.SHA256):
		return Manifest{}, errors.New("the release manifest's sha256 is not 64 lower-case hex characters")
	case !AllowedURL(m.URL):
		return Manifest{}, errors.New("the release manifest's url is not allowed")
	}

	return m, nil
}

// Download writes the release's binary to path as an owner-only executable, and fails unless it has the manifest's
// hash.
func Download(ctx context.Context, client *http.Client, m Manifest, path string) error {
	resp, err := get(ctx, client, m.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) //nolint:gosec // an executable, owner-only
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	sum := sha256.New()

	n, err := io.Copy(io.MultiWriter(out, sum), io.LimitReader(resp.Body, MaxBinaryBytes+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("downloading %s: %w", m.URL, err)
	}

	if n > MaxBinaryBytes {
		return fmt.Errorf("the download from %s is larger than %d bytes", m.URL, MaxBinaryBytes)
	}

	if got := hex.EncodeToString(sum.Sum(nil)); got != m.SHA256 {
		return fmt.Errorf("the download's SHA-256 is %s, not the manifest's %s", got, m.SHA256)
	}

	return nil
}

// Reported is the version a binary reports with `<binary> version`.
func Reported(ctx context.Context, path string) (string, error) {
	// A binary just downloaded, whose hash was checked.
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", fmt.Errorf("running %s version: %w", path, err)
	}

	return strings.TrimSpace(string(out)), nil
}
