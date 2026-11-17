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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// release is a release mirror on this machine: a manifest for this
// platform and the binary it names.
type release struct {
	t      *testing.T
	srv    *httptest.Server
	binary []byte

	mu       sync.Mutex
	manifest string
	// reads counts the reads of the manifest.
	reads atomic.Int32
}

// publish has the mirror name another manifest from now on.
func (r *release) publish(manifest string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.manifest = manifest
}

func newRelease(t *testing.T, version string, binary []byte) *release {
	t.Helper()

	r := &release{t: t, binary: binary}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /"+runtime.GOOS+"-"+runtime.GOARCH+".txt", func(w http.ResponseWriter, _ *http.Request) {
		r.reads.Add(1)
		r.mu.Lock()
		defer r.mu.Unlock()

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

// controlPlane is what a control plane said at its hellos, for the update loop.
type controlPlane struct {
	url     string
	version atomic.Pointer[string]
	hellos  chan struct{}
}

func newControlPlane(url, version string) *controlPlane {
	cp := &controlPlane{url: url, hellos: make(chan struct{}, 1)}
	cp.version.Store(&version)

	return cp
}

func (c *controlPlane) ReleaseURL() string          { return c.url }
func (c *controlPlane) ControlPlaneVersion() string { return *c.version.Load() }
func (c *controlPlane) Hellos() <-chan struct{}     { return c.hellos }

// hello is a hello at which the control plane says version.
func (c *controlPlane) hello(version string) {
	c.version.Store(&version)
	c.hellos <- struct{}{}
}

// fast are loop waits for a test where every wait is short.
var fast = loopWaits{
	first: 20 * time.Millisecond, every: 20 * time.Millisecond,
	soon: 20 * time.Millisecond, recheck: 20 * time.Millisecond,
}

// waits are loop waits for a test: the first check and the regular interval
// far away, so that only a hello or soon makes the loop look.
func waits(soon time.Duration) loopWaits {
	return loopWaits{first: time.Hour, every: time.Hour, soon: soon, recheck: 20 * time.Millisecond}
}

// Scenario: the node's regular check is one to two days apart, at a random
// moment so nodes do not look together (Nezha's 1440 to 2880 minutes), or
// the interval its configuration sets.
func TestUpdateChecksAreOneToTwoDaysApart(t *testing.T) {
	t.Parallel()

	for range 1000 {
		wait := nextUpdateCheck(0)
		assert.GreaterOrEqual(t, wait, 24*time.Hour)
		assert.Less(t, wait, 48*time.Hour)
	}

	assert.Equal(t, 15*time.Minute, nextUpdateCheck(15*time.Minute))
}

// Scenario: the control plane took a new release and restarted; the node
// says hello again and hears its version. The node looks for its release at
// once rather than in a day, and takes it (issue #783).
func TestNodeLooksAtOnceWhenTheControlPlaneSaysANewerRelease(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the newer node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")
	cp := newControlPlane(r.srv.URL, "v0.4.0")
	done := make(chan error, 1)

	go func() {
		done <- u.updateLoop(t.Context(), cp, func() bool { return false }, waits(time.Hour), slog.New(slog.DiscardHandler))
	}()

	cp.hello("v0.4.1")

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrUpdated)
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not look when the control plane said a newer release")
	}

	assert.Equal(t, "the newer node", contentOf(t, exe))
}

// Scenario: hellos from a control plane at the node's own version, an older
// one, or a build of a commit. The node does not look: only its timer does.
func TestNodeDoesNotLookAtAHelloThatIsNotNewer(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the newer node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")
	cp := newControlPlane(r.srv.URL, "")
	done := make(chan error, 1)

	go func() {
		done <- u.updateLoop(t.Context(), cp, func() bool { return false }, waits(time.Hour), slog.New(slog.DiscardHandler))
	}()

	for _, version := range []string{"v0.4.0", "v0.3.9", "v0.4.0-3-gabc1234", "dev", ""} {
		cp.hello(version)
	}

	select {
	case err := <-done:
		t.Fatalf("the loop ended: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	assert.Zero(t, r.reads.Load())
	assert.Equal(t, "the running node", contentOf(t, exe))
}

// Scenario: the control plane says a newer release whose manifest is not
// published yet, and the node's link keeps failing, so it says hello again
// every few seconds, each naming the same release. The node reads its
// release once per retry wait, not at each hello; a hello naming a newer
// release still makes it look at once (review of #788).
func TestHellosNamingTheSameReleaseKeepTheRetryWait(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		http.NotFound(w, nil)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u, exe := installed(t, "v0.4.0", "v0.4.1")
	cp := newControlPlane(srv.URL, "v0.4.0")
	done := make(chan error, 1)

	go func() {
		done <- u.updateLoop(t.Context(), cp, func() bool { return false }, waits(time.Hour), slog.New(slog.DiscardHandler))
	}()

	for range 5 {
		cp.hello("v0.4.1")
		require.Eventually(t, func() bool { return len(cp.hellos) == 0 }, 10*time.Second, time.Millisecond)
	}

	require.Eventually(t, func() bool { return reads.Load() >= 1 }, 10*time.Second, time.Millisecond)

	select {
	case err := <-done:
		t.Fatalf("the loop ended: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	assert.Equal(t, int32(1), reads.Load(), "hellos naming the same release read it again within the retry wait")

	cp.hello("v0.4.2")
	require.Eventually(t, func() bool { return reads.Load() == 2 }, 10*time.Second, time.Millisecond)
	assert.Equal(t, "the running node", contentOf(t, exe))
}

// Scenario: the control plane says a newer release, but the release the node
// reads is not there yet, or a terminal is open. The node looks again soon,
// not in a day, and takes the release once it is there and the node is free.
func TestNodeLooksAgainSoonWhileTheControlPlaneIsNewer(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.0", []byte("the same node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")
	cp := newControlPlane(r.srv.URL, "v0.4.0")

	var busy atomic.Bool

	done := make(chan error, 1)

	go func() {
		done <- u.updateLoop(t.Context(), cp, busy.Load, waits(20*time.Millisecond), slog.New(slog.DiscardHandler))
	}()

	cp.hello("v0.4.1")

	// The manifest still names v0.4.0: read again and again, nothing replaced.
	require.Eventually(t, func() bool { return r.reads.Load() >= 3 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, "the running node", contentOf(t, exe))

	// A terminal opens, then the release appears: no restart while it is open.
	busy.Store(true)

	newer := newRelease(t, "v0.4.1", []byte("the newer node"))
	r.publish(newer.manifest)

	select {
	case err := <-done:
		t.Fatalf("the node ended while a terminal was open: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	busy.Store(false)

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrUpdated)
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not take the release once it was free")
	}

	assert.Equal(t, "the newer node", contentOf(t, exe))
}

// Scenario: the node runs its update loop. It does not end while a terminal
// is open, and once it is free and a newer release exists it replaces its
// binary and ends, for its service manager to start the new one.
func TestUpdateLoopEndsTheNodeOnlyAfterItUpdated(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the newer node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")

	var busy atomic.Bool

	busy.Store(true)

	done := make(chan error, 1)
	cp := newControlPlane(r.srv.URL, "v0.4.0")

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	loop := func() {
		done <- u.updateLoop(ctx, cp, busy.Load, fast, slog.New(slog.DiscardHandler))
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

	busy.Store(false)

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

// Scenario: the phone is asked to start while the node downloads its update
// (issue #545). The node puts the new binary in place but does not end
// until the phone's start is over: a restart would cut the boot short.
func TestUpdateLoopWaitsForWhatBecameBusyDuringTheDownload(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v0.4.1", []byte("the newer node"))
	u, exe := installed(t, "v0.4.0", "v0.4.1")

	var busy atomic.Bool

	// The download is checked by running it; the phone's start is asked meanwhile.
	u.Reports = func(context.Context, string) (string, error) {
		busy.Store(true)

		return "v0.4.1", nil
	}

	done := make(chan error, 1)

	go func() {
		done <- u.updateLoop(t.Context(), newControlPlane(r.srv.URL, "v0.4.0"), busy.Load,
			fast,
			slog.New(slog.DiscardHandler))
	}()

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(exe) //nolint:gosec // a file this test wrote

		return err == nil && string(data) == "the newer node" // Windows refuses a read during the rename.
	}, 10*time.Second, 10*time.Millisecond)

	select {
	case err := <-done:
		t.Fatalf("the node ended while its phone was starting: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	busy.Store(false)

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrUpdated)
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not end once its phone had started")
	}
}
