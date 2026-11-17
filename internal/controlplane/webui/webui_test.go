package webui_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/webui"
)

var build = fstest.MapFS{
	"index.html":           {Data: []byte("<!doctype html><title>Hypomnema</title>")},
	"assets/index-a1.js":   {Data: []byte("console.log(1)")},
	"manifest.webmanifest": {Data: []byte(`{"name":"Hypomnema","start_url":"/"}`)},
	"apple-touch-icon.png": {Data: []byte("\x89PNG\r\n\x1a\n")},
	".gitkeep":             {},
}

func get(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	webui.Handler(build).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, nil))

	return rec
}

// Scenario: the browser loads the client. A page path such as /chats/x gets
// index.html so the client's router takes it; built assets are cached for a
// year; what is not in the build, or belongs to the server, is a 404.
func TestHandlerServesTheClient(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/", "/chats/cht_1", "/index.html"} {
		rec := get(t, http.MethodGet, target)
		require.Equal(t, http.StatusOK, rec.Code, target)
		assert.Contains(t, rec.Body.String(), "<title>Hypomnema</title>", target)
		assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"), target)
		assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'", target)
	}

	rec := get(t, http.MethodGet, "/assets/index-a1.js")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	missing := []string{"/assets/gone.js", "/favicon.ico", "/.gitkeep", "/node/v1/other", "/api/x", "/readyz"}
	for _, target := range missing {
		assert.Equal(t, http.StatusNotFound, get(t, http.MethodGet, target).Code, target)
	}

	assert.Equal(t, http.StatusMethodNotAllowed, get(t, http.MethodPost, "/").Code)

	head := get(t, http.MethodHead, "/chats/cht_1")
	assert.Equal(t, http.StatusOK, head.Code)
	assert.Empty(t, head.Body.String())
}

// Scenario: someone opens a view-only link's page without signing in (issue #745). It is the client's page, never
// indexed, kept, framed or sent as a referrer; the app's other pages are cached as before.
func TestHandlerServesASharedPageNeverIndexedOrKept(t *testing.T) {
	t.Parallel()

	rec := get(t, http.MethodGet, "/s/abc_DEF-123")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<title>Hypomnema</title>")
	assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")

	app := get(t, http.MethodGet, "/sessions")
	assert.Empty(t, app.Header().Get("X-Robots-Tag"))
	assert.Equal(t, "no-cache", app.Header().Get("Cache-Control"))
}

// Scenario: a phone adds the client to its home screen, and reads the web
// manifest and the touch icon with their types, whatever the host's mime
// database holds (issue #728).
func TestHandlerServesTheHomeScreenIcon(t *testing.T) {
	t.Parallel()

	types := map[string]string{
		"/manifest.webmanifest": "application/manifest+json; charset=utf-8",
		"/apple-touch-icon.png": "image/png",
	}
	for target, contentType := range types {
		rec := get(t, http.MethodGet, target)
		require.Equal(t, http.StatusOK, rec.Code, target)
		assert.Equal(t, contentType, rec.Header().Get("Content-Type"), target)
		assert.Equal(t, string(build[target[1:]].Data), rec.Body.String(), target)
	}
}

// Scenario: a binary built before `pnpm build` holds only dist/.gitkeep, and
// serves no client.
func TestAvailableNeedsIndex(t *testing.T) {
	t.Parallel()

	assert.True(t, webui.Available(build))
	assert.False(t, webui.Available(fstest.MapFS{".gitkeep": {}}))
}

// Scenario: a browser that accepts gzip gets the build gzipped, which it reads
// back as the same file; one that does not gets it as it is (issue #510).
func TestHandlerGzipsTheBuildForABrowserThatAcceptsIt(t *testing.T) {
	t.Parallel()

	files := map[string]string{"/": "index.html", "/assets/index-a1.js": "assets/index-a1.js"}
	for target, file := range files {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		rec := httptest.NewRecorder()
		webui.Handler(build).ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, target)
		require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), target)
		zr, err := gzip.NewReader(rec.Body)
		require.NoError(t, err, target)
		body, err := io.ReadAll(zr)
		require.NoError(t, err, target)
		assert.Equal(t, string(build[file].Data), string(body), target)
	}

	plain := get(t, http.MethodGet, "/assets/index-a1.js")
	assert.Empty(t, plain.Header().Get("Content-Encoding"))
	assert.Equal(t, "console.log(1)", plain.Body.String())
}
