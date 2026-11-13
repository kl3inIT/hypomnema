package webui_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/webui"
)

var build = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>Hypomnema</title>")},
	"assets/index-a1.js": {Data: []byte("console.log(1)")},
	".gitkeep":           {},
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

// Scenario: a binary built before `pnpm build` holds only dist/.gitkeep, and
// serves no client.
func TestAvailableNeedsIndex(t *testing.T) {
	t.Parallel()

	assert.True(t, webui.Available(build))
	assert.False(t, webui.Available(fstest.MapFS{".gitkeep": {}}))
}
