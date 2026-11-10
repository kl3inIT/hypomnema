package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// release is a release mirror on this machine: a manifest for this
// platform and the binary it names.
type release struct {
	t        *testing.T
	srv      *httptest.Server
	manifest string
	binary   []byte
}

func newRelease(t *testing.T, version string, binary []byte) *release {
	t.Helper()

	r := &release{t: t, binary: binary}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /"+runtime.GOOS+"-"+runtime.GOARCH+".txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(r.manifest))
	})
	mux.HandleFunc("GET /node", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(r.binary) })

	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)

	sum := sha256.Sum256(binary)
	r.manifest = fmt.Sprintf("version=%s\nurl=%s/node\nsha256=%s\n", version, r.srv.URL, hex.EncodeToString(sum[:]))

	return r
}

// installed is a node binary on disk at a version, with an Updater for it
// whose downloads report the version reports gives.
func installed(t *testing.T, version, reports string) (Updater, string) {
	t.Helper()

	exe := filepath.Join(t.TempDir(), "hypomnema-node")
	require.NoError(t, os.WriteFile(exe, []byte("the running node"), 0o700)) //nolint:gosec // G306: a binary is executable

	return Updater{
		Exe: exe, Version: version,
		Reports: func(context.Context, string) (string, error) { return reports, nil },
	}, exe
}

func contentOf(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // a file this test wrote
	require.NoError(t, err)

	return string(data)
}

// Scenario: a newer node is released. The node downloads it, finds the hash
// its manifest names and the version it reports, and puts it in its own
// place; nothing is left beside it.
func TestNodeReplacesItselfWithANewerRelease(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the newer node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")

	got, err := u.Update(t.Context(), r.srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "v0.4.1", got)
	assert.Equal(t, "the newer node", contentOf(t, exe))

	entries, err := os.ReadDir(filepath.Dir(exe))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

// Scenario: there is nothing to update to. A node at the released version,
// a node ahead of it, and a node built from a commit, which no release
// replaces, all keep their binary.
func TestNodeKeepsItsBinaryWhenNothingIsNewer(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the released node"))

	for _, version := range []string{"v0.4.1", "v0.10.0", "v0.4.0-12-gabc1234", "dev"} {
		u, exe := installed(t, version, "v0.4.1")

		got, err := u.Update(t.Context(), r.srv.URL)
		require.NoError(t, err, version)
		assert.Empty(t, got, version)
		assert.Equal(t, "the running node", contentOf(t, exe), version)
	}
}

// Scenario: the release cannot be trusted. A download whose hash is not the
// manifest's, a binary that reports another version than the manifest
// names, a manifest that points somewhere that is not https, and a manifest
// that is not understood: each is refused and the running binary stays.
func TestNodeRefusesAReleaseItCannotCheck(t *testing.T) {
	t.Parallel()

	tampered := newRelease(t, "v0.4.1", []byte("the released node"))
	tampered.binary = []byte("something else")

	lying := newRelease(t, "v0.4.1", []byte("an older node under a new name"))

	elsewhere := newRelease(t, "v0.4.1", []byte("x"))
	elsewhere.manifest = "version=v0.4.1\nurl=http://example.com/node\nsha256=" +
		"0000000000000000000000000000000000000000000000000000000000000000\n"

	garbled := newRelease(t, "v0.4.1", []byte("x"))
	garbled.manifest = "<html>not found</html>\n"

	for name, tc := range map[string]struct {
		r       *release
		reports string
		want    string
	}{
		"hash":     {tampered, "v0.4.1", "SHA-256"},
		"version":  {lying, "v0.3.0", "says it is v0.3.0"},
		"url":      {elsewhere, "v0.4.1", "url is not allowed"},
		"manifest": {garbled, "v0.4.1", "not understood"},
	} {
		u, exe := installed(t, "v0.4.0", tc.reports)

		got, err := u.Update(t.Context(), tc.r.srv.URL)
		require.ErrorContains(t, err, tc.want, name)
		assert.Empty(t, got, name)
		assert.Equal(t, "the running node", contentOf(t, exe), name)

		entries, err := os.ReadDir(filepath.Dir(exe))
		require.NoError(t, err)
		assert.Len(t, entries, 1, "%s: nothing is left beside the binary", name)
	}
}

// Scenario: the node runs its update loop. It does not end while a terminal
// is open, and once it is free and a newer release exists it replaces its
// binary and ends, for its service manager to start the new one.
func TestUpdateLoopEndsTheNodeOnlyAfterItUpdated(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the newer node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")

	busy := true
	done := make(chan error, 1)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	loop := func() {
		done <- u.updateLoop(ctx, func() string { return r.srv.URL }, func() bool { return busy },
			20*time.Millisecond, 20*time.Millisecond, slog.New(slog.DiscardHandler))
	}

	go loop()

	select {
	case err := <-done:
		t.Fatalf("the node ended while a terminal was open: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	assert.Equal(t, "the running node", contentOf(t, exe))

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	busy = false
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Second)

	defer cancel()

	go loop()

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrUpdated)
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not update itself")
	}

	assert.Equal(t, "the newer node", contentOf(t, exe))
}
