//go:build unix

package selfupdate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
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

// Scenario: a release fails to install (the control plane put back the old one). The next update does not try it
// again; a newer release is tried, and once it installs the block is gone.
func TestAReleaseThatFailedToInstallIsNotTriedAgain(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.ReleaseURL = releaseServer(t, "v0.9.5", []byte("the broken control plane"))
	o.Reports = func(context.Context, string) (string, error) { return "v0.9.5", nil }
	cmds := &commands{fail: map[string]error{filepath.Join(o.State, "hypomnema-v0.9.5"): errors.New("not ready")}}
	o.Run = cmds.run

	require.ErrorContains(t, selfupdate.Update(t.Context(), o), "not ready")
	require.Len(t, cmds.lines(), 1)

	require.NoError(t, selfupdate.Update(t.Context(), o))
	assert.Len(t, cmds.lines(), 1, "the failed release is not tried again")

	o.ReleaseURL = releaseServer(t, "v0.9.6", []byte("the fixed control plane"))
	o.Reports = func(context.Context, string) (string, error) { return "v0.9.6", nil }

	require.NoError(t, selfupdate.Update(t.Context(), o))
	assert.Len(t, cmds.lines(), 2, "a newer release is tried")
	assert.NoFileExists(t, filepath.Join(o.State, "blocked"))
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

// events is what a test's control plane was asked and what the update ran, in order.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.list = append(e.list, s)
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.list...)
}

// controlPlane answers the control socket in home's data folder as a control plane would (issue #716): the chats
// whose turn runs are busy's answer to each question, by its number from 0.
func controlPlane(t *testing.T, home string, log *events, busy func(n int) []string) {
	t.Helper()

	serveTurns(t, home, log, func(n int) selfupdate.Turns { return selfupdate.Turns{Chats: busy(n)} })
}

// serveTurns is controlPlane answering what runs, tool calls too.
func serveTurns(t *testing.T, home string, log *events, busy func(n int) selfupdate.Turns) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", filepath.Join(home, "data", selfupdate.ControlSocket))
	require.NoError(t, err)

	var (
		mu sync.Mutex
		n  int
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+selfupdate.PathTurns, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		running := busy(n)
		n++
		mu.Unlock()

		if running.ToolCalls > 0 {
			log.add(fmt.Sprintf("turns: %v, tool calls: %d", running.Chats, running.ToolCalls))
		} else {
			log.add(fmt.Sprintf("turns: %v", running.Chats))
		}

		_ = json.NewEncoder(w).Encode(running)
	})
	// The control plane marks the update itself, as the real one does under its coordinator's lock.
	mux.HandleFunc("POST "+selfupdate.PathUpdate, func(w http.ResponseWriter, r *http.Request) {
		var b selfupdate.Begin
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil ||
			selfupdate.WriteMarker(filepath.Join(home, "data"), b) != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		log.add(fmt.Sprintf("update began: %v", b.PID == os.Getpid()))
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() { _ = srv.Close() })
}

func installWaiting(t *testing.T, log *events) selfupdate.Options {
	t.Helper()

	o := installed(t)
	o.Ready = ready(t, http.StatusNoContent)
	o.TurnsEvery = 10 * time.Millisecond
	o.Run = func(_ context.Context, name string, args ...string) error {
		log.add(strings.TrimSpace(name + " " + strings.Join(args, " ")))

		return nil
	}

	return o
}

// Scenario (issue #716): a Bot's turn runs when the update comes. The update asks the control plane until no turn
// runs, and only then stops it.
func TestInstallWaitsForBotsTurnsBeforeItStops(t *testing.T) {
	t.Parallel()

	log := &events{}
	o := installWaiting(t, log)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	controlPlane(t, o.Home, log, func(n int) []string {
		if n < 3 {
			return []string{"cht_a"}
		}

		return nil
	})

	var out strings.Builder
	o.Out = &out

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	got := log.all()
	require.GreaterOrEqual(t, len(got), 6)
	assert.Equal(t, []string{
		"update began: true",
		"turns: [cht_a]", "turns: [cht_a]", "turns: [cht_a]", "turns: []", "systemctl stop hypomnema-cp.service",
	}, got[:6])
	assert.Contains(t, out.String(), "waiting up to 5m0s for Bots' turns and tool calls to end, in chats cht_a")
}

// Scenario (issue #1060): no Bot's turn runs, but an agent's tool call does, a worker's command on a node. The update
// waits for it too, within the same bound, before it stops the control plane.
func TestInstallWaitsForToolCallsBeforeItStops(t *testing.T) {
	t.Parallel()

	log := &events{}
	o := installWaiting(t, log)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	serveTurns(t, o.Home, log, func(n int) selfupdate.Turns {
		if n < 2 {
			return selfupdate.Turns{ToolCalls: 1}
		}

		return selfupdate.Turns{}
	})

	var out strings.Builder
	o.Out = &out

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	got := log.all()
	require.GreaterOrEqual(t, len(got), 5)
	assert.Equal(t, []string{
		"update began: true",
		"turns: [], tool calls: 1", "turns: [], tool calls: 1", "turns: []", "systemctl stop hypomnema-cp.service",
	}, got[:5])
	assert.Contains(t, out.String(), "waiting up to 5m0s for Bots' turns and tool calls to end, 1 tool call")
}

// Scenario: the turn does not end. The update waits as long as its bound, then stops the control plane anyway: the
// turn is cut, and the control plane continues it when it starts.
func TestInstallStopsWhenTheWaitForTurnsRunsOut(t *testing.T) {
	t.Parallel()

	log := &events{}
	o := installWaiting(t, log)
	o.TurnsWithin = 200 * time.Millisecond
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	controlPlane(t, o.Home, log, func(int) []string { return []string{"cht_a", "cht_b"} })

	var out strings.Builder
	o.Out = &out

	started := time.Now()

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	waited := time.Since(started)
	assert.GreaterOrEqual(t, waited, 200*time.Millisecond)
	assert.Less(t, waited, 5*time.Second, "the wait is bounded")

	got := log.all()
	stop := slices.Index(got, "systemctl stop hypomnema-cp.service")
	require.Positive(t, stop)

	assert.Equal(t, "update began: true", got[0], "the control plane marks the update first")

	for _, e := range got[1:stop] {
		assert.Equal(t, "turns: [cht_a cht_b]", e)
	}

	assert.Contains(t, out.String(), "still run after 200ms, in chats cht_a, cht_b")
}

// Scenario: the control plane does not answer on its control socket, one from before it: the update does not wait.
func TestInstallDoesNotWaitForAControlPlaneThatDoesNotSay(t *testing.T) {
	t.Parallel()

	log := &events{}
	o := installWaiting(t, log)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	var out strings.Builder
	o.Out = &out

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	assert.Equal(t, "systemctl stop hypomnema-cp.service", log.all()[0])
	assert.Contains(t, out.String(), "not waiting for Bots' turns")
	assert.Contains(t, out.String(), "the control plane did not mark the update", "the update marks itself then")
}

// Scenario (review of #725, finding 4): from before the update waits for Bots' turns until the release's coordinator
// is set up, the control plane is told an update runs, so it starts no pi: the old one while it is stopped, the new
// one before setup-coordinator.sh has put pi in place. After the update, nothing says so.
func TestTheControlPlaneKnowsAnUpdateRunsUntilItsCoordinatorIsSetUp(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusNoContent)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	data := filepath.Join(o.Home, "data")
	assert.False(t, selfupdate.Updating(data))

	seen := &events{}

	o.Run = func(_ context.Context, name string, _ ...string) error {
		seen.add(fmt.Sprintf("%s %v", name, selfupdate.Updating(data)))

		return nil
	}

	controlPlane(t, o.Home, &events{}, func(int) []string {
		seen.add(fmt.Sprintf("turns %v", selfupdate.Updating(data)))

		return nil
	})

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	assert.Equal(t, []string{
		"turns true", "systemctl true", "runuser true", "systemctl true", "sh true",
	}, seen.all(), "while it waits, stops, migrates, starts and sets up the coordinator")
	assert.False(t, selfupdate.Updating(data), "not after it")
}

// Scenario: an update was killed and left its marker; the process it names is gone, so no update runs.
func TestAMarkerOfAnUpdateThatIsGoneIsNoUpdate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := filepath.Join(dir, selfupdate.UpdatingFile)

	write(t, marker, fmt.Sprintf("%d\n", os.Getpid()))
	assert.True(t, selfupdate.Updating(dir), "a process that runs")

	if _, err := os.Stat("/proc/self/stat"); err == nil {
		write(t, marker, fmt.Sprintf("%d 1\n", os.Getpid()))
		assert.False(t, selfupdate.Updating(dir), "a process of that id that started at another time")
	}

	write(t, marker, "2147483646 1\n")
	assert.False(t, selfupdate.Updating(dir), "no such process")

	write(t, marker, "nonsense")
	assert.False(t, selfupdate.Updating(dir))
}

// Scenario (issue #1060, review round 1): the control plane answers slowly once the wait has begun. The last
// question is bounded by what is left of the wait, so the update stops it at the bound, not seconds after.
func TestTheWaitForTurnsEndsAtItsBoundWhenTheControlPlaneIsSlow(t *testing.T) {
	t.Parallel()

	log := &events{}
	o := installWaiting(t, log)
	o.TurnsWithin = 300 * time.Millisecond
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	serveTurns(t, o.Home, log, func(n int) selfupdate.Turns {
		if n > 0 {
			time.Sleep(3 * time.Second)
		}

		return selfupdate.Turns{ToolCalls: 1}
	})

	var out strings.Builder
	o.Out = &out

	started := time.Now()

	require.NoError(t, selfupdate.Install(t.Context(), o, from))

	assert.Less(t, time.Since(started), 2*time.Second, "the slow answer is not waited for past the bound")
	assert.Contains(t, out.String(), "still run after 300ms, 1 tool call")
}
