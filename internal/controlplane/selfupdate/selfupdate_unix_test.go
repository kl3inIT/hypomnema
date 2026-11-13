//go:build unix

package selfupdate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
)

// commands records the commands an update runs, and fails the ones fail names.
type commands struct {
	mu   sync.Mutex
	ran  []string
	fail map[string]error
}

func (c *commands) run(_ context.Context, name string, args ...string) error {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.ran = append(c.ran, line)
	for prefix, err := range c.fail {
		if strings.HasPrefix(line, prefix) {
			return err
		}
	}

	return nil
}

func (c *commands) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.ran...)
}

// releaseServer serves a release manifest for this platform and the binary it names.
func releaseServer(t *testing.T, version string, binary []byte) string {
	t.Helper()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(binary)
	manifest := fmt.Sprintf("version=%s\nurl=%s/hypomnema\nsha256=%s\n", version, srv.URL, hex.EncodeToString(sum[:]))
	mux.HandleFunc("GET /controlplane-"+runtime.GOOS+"-"+runtime.GOARCH+".txt",
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("GET /hypomnema", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) })

	return srv.URL
}

// installed is a control plane's folder as on zm, owned by the user running the test.
func installed(t *testing.T) selfupdate.Options {
	t.Helper()

	me, err := user.Current()
	require.NoError(t, err)

	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "data"), 0o700))
	write(t, filepath.Join(home, "bin", "hypomnema"), "the old control plane")
	write(t, filepath.Join(home, "data", "hypomnema.db"), "the old database")

	return selfupdate.Options{
		Current: "v0.9.4", User: me.Username, Home: home, Unit: "hypomnema-cp.service", State: t.TempDir(),
		ReadyWithin: 3 * time.Second,
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // a file of this test
	require.NoError(t, err)

	return string(data)
}

func ready(t *testing.T, status int) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)

	return srv.URL + "/readyz"
}

// Scenario: a newer release is published. The update downloads it into its own folder, finds the hash its manifest
// names and the version it reports, and has that binary install itself.
func TestUpdateHasANewerReleaseInstallItself(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.ReleaseURL = releaseServer(t, "v0.9.5", []byte("the new control plane"))
	o.Reports = func(context.Context, string) (string, error) { return "v0.9.5", nil }
	o.Ready = "http://127.0.0.1:18990/readyz"
	cmds := &commands{}
	o.Run = cmds.run

	require.NoError(t, selfupdate.Update(t.Context(), o))

	staged := filepath.Join(o.State, "hypomnema-v0.9.5")
	assert.Equal(t, "the new control plane", read(t, staged))
	assert.Equal(t, []string{staged + " update install --from " + staged + " --user " + o.User + " --home " + o.Home +
		" --unit hypomnema-cp.service --state " + o.State + " --ready http://127.0.0.1:18990/readyz"}, cmds.lines())
}

// Scenario: the release is the one installed, or older than the build of a commit that runs. Nothing is downloaded
// or run.
func TestUpdateDoesNothingWithoutANewerRelease(t *testing.T) {
	t.Parallel()

	for _, current := range []string{"v0.9.5", "v0.9.5-3-gabcdef0"} {
		o := installed(t)
		o.Current = current
		o.ReleaseURL = releaseServer(t, "v0.9.5", []byte("the new control plane"))
		cmds := &commands{}
		o.Run = cmds.run

		require.NoError(t, selfupdate.Update(t.Context(), o))
		assert.Empty(t, cmds.lines(), current)
		assert.NoFileExists(t, filepath.Join(o.State, "hypomnema-v0.9.5"))
	}
}

// Scenario: a download that says it is another version is not run.
func TestUpdateRefusesABinaryThatIsNotTheRelease(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.ReleaseURL = releaseServer(t, "v0.9.5", []byte("the new control plane"))
	o.Reports = func(context.Context, string) (string, error) { return "v0.9.4", nil }
	cmds := &commands{}
	o.Run = cmds.run

	require.ErrorContains(t, selfupdate.Update(t.Context(), o), "says it is v0.9.4")
	assert.Empty(t, cmds.lines())
}

// Scenario: two updates start at once. The second one gives up while the first holds the lock.
func TestASecondUpdateGivesUpWhileOneRuns(t *testing.T) {
	t.Parallel()

	o := installed(t)
	f, err := os.OpenFile(filepath.Join(o.State, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))

	require.ErrorIs(t, selfupdate.Update(t.Context(), o), selfupdate.ErrBusy)
}

// Scenario: the new release installs itself. The control plane is stopped, its database backed up, the binary put in
// place with the old one kept, the schema migrated as the control plane's user, the control plane started and found
// ready, and only then the release's coordinator set up.
func TestInstallPutsTheReleaseInPlaceThenSetsUpTheCoordinator(t *testing.T) {
	t.Parallel()

	o := installed(t)
	// The control plane's /readyz answers 204.
	o.Ready = ready(t, http.StatusNoContent)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")
	cmds := &commands{}
	o.Run = cmds.run

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	assert.Equal(t, "the new control plane", read(t, filepath.Join(o.Home, "bin", "hypomnema")))
	assert.Equal(t, "the old control plane", read(t, filepath.Join(o.Home, "bin", "hypomnema.previous")))

	backups, err := filepath.Glob(filepath.Join(o.Home, "data", "predeploy-*", "hypomnema.db"))
	require.NoError(t, err)
	require.Len(t, backups, 1)
	assert.Equal(t, "the old database", read(t, backups[0]))

	ran := cmds.lines()
	require.Len(t, ran, 4)
	assert.Equal(t, "systemctl stop hypomnema-cp.service", ran[0])
	assert.True(t, strings.HasPrefix(ran[1], "runuser -u "+o.User+" -- sh -c"), ran[1])
	assert.True(t, strings.HasSuffix(ran[1], "migrate "+o.Home), ran[1])
	assert.Equal(t, "systemctl start hypomnema-cp.service", ran[2])
	script, args, _ := strings.Cut(strings.TrimPrefix(ran[3], "sh "), " ")
	assert.Equal(t, filepath.Join(o.State, filepath.Base(filepath.Dir(script)), "setup-coordinator.sh"), script)
	assert.True(t, strings.HasPrefix(filepath.Base(filepath.Dir(script)), "coordinator-"), script)
	assert.Equal(t, o.User+" "+filepath.Dir(script), args)

	entries, err := os.ReadDir(o.State)
	require.NoError(t, err)

	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), "coordinator-"), "the coordinator's folder is removed")
	}
}

// Scenario: seven backups of earlier updates are in data. After an update that worked, the five newest backups are
// kept, the new one among them; the rest are removed, and what is not a backup is left alone.
func TestInstallKeepsTheFiveNewestBackups(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusNoContent)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")
	o.Run = (&commands{}).run

	data := filepath.Join(o.Home, "data")
	old := time.Now().Add(-time.Hour)

	for i := range 7 {
		dir := filepath.Join(data, fmt.Sprintf("predeploy-old-%d", i))
		require.NoError(t, os.MkdirAll(dir, 0o700))
		at := old.Add(time.Duration(i) * time.Minute)
		require.NoError(t, os.Chtimes(dir, at, at))
	}

	write(t, filepath.Join(data, "notes.txt"), "the owner's")

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	backups, err := filepath.Glob(filepath.Join(data, "predeploy-*"))
	require.NoError(t, err)
	require.Len(t, backups, 5)

	kept := map[string]bool{}
	for _, b := range backups {
		kept[filepath.Base(b)] = true
	}

	for _, gone := range []string{"predeploy-old-0", "predeploy-old-1", "predeploy-old-2"} {
		assert.False(t, kept[gone], gone)
	}

	assert.True(t, kept["predeploy-old-6"])
	assert.FileExists(t, filepath.Join(data, "notes.txt"))
}

// Scenario: the new release does not become ready. The old binary and database are put back and started again, and
// the coordinator is not touched.
func TestInstallPutsTheOldControlPlaneBackWhenTheNewOneIsNotReady(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusServiceUnavailable)
	o.ReadyWithin = 1500 * time.Millisecond
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")
	cmds := &commands{}
	o.Run = func(ctx context.Context, name string, args ...string) error {
		// The migration changes the database; the rollback must undo it.
		if name == "runuser" {
			write(t, filepath.Join(o.Home, "data", "hypomnema.db"), "the migrated database")
		}

		return cmds.run(ctx, name, args...)
	}

	err := selfupdate.Install(t.Context(), o, from)
	require.ErrorContains(t, err, "did not become ready")

	assert.Equal(t, "the old control plane", read(t, filepath.Join(o.Home, "bin", "hypomnema")))
	assert.Equal(t, "the old database", read(t, filepath.Join(o.Home, "data", "hypomnema.db")))

	for _, line := range cmds.lines() {
		assert.NotContains(t, line, "setup-coordinator.sh")
	}

	ran := cmds.lines()
	assert.Equal(t, "systemctl start hypomnema-cp.service", ran[len(ran)-1])
}
