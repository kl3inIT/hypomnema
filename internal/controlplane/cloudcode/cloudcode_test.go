package cloudcode_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
)

// Scenario: the IDE's update manifest cannot be read, then names an older
// release, then a newer one. The user agent starts at the built-in release
// with its build number, never goes back, moves to the newer release (whose
// build number is not known, so none is claimed), and the manifest is read
// again only after six hours.
func TestTheUserAgentFollowsTheUpdateManifest(t *testing.T) {
	t.Parallel()

	var (
		answer atomic.Value
		reads  atomic.Int64
	)

	answer.Store("")

	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		assert.Equal(t, "electron-builder", r.Header.Get("User-Agent"))

		body, _ := answer.Load().(string)
		if body == "" {
			http.Error(w, "down", http.StatusServiceUnavailable)

			return
		}

		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(manifest.Close)

	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	identity := cloudcode.NewIdentity(manifest.URL, func() time.Time { return now })

	const builtIn = "antigravity/hub/2.29.0 (aidev_client; os_type=darwin; arch=arm64; cl=1013060670)"

	assert.Equal(t, builtIn, identity.UserAgent(t.Context()))
	assert.Equal(t, builtIn, identity.UserAgent(t.Context()))
	assert.EqualValues(t, 1, reads.Load(), "a manifest that failed is not asked again at once")

	now = now.Add(7 * time.Hour)
	answer.Store("version: 2.9.9\n")
	assert.Equal(t, builtIn, identity.UserAgent(t.Context()), "an older release is not taken")

	now = now.Add(7 * time.Hour)
	answer.Store("path: Antigravity.zip\nversion: '2.31.4'\nreleaseDate: x\n")
	assert.Equal(t, "antigravity/hub/2.31.4 (aidev_client; os_type=darwin; arch=arm64)", identity.UserAgent(t.Context()))
	assert.EqualValues(t, 3, reads.Load())
}
