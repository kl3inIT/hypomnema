package worktree

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/diskguard"
)

func TestMain(tests *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == diskguard.ScopedSubcommand {
		if err := diskguard.ScopedExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(tests.Run())
}

// lockedBuffer is a log the node writes while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// declaring makes the repository kl3inIT/second-bite, whose hypomnema.yaml is
// file, and a Maker over it that logs to the returned buffer.
func declaring(t *testing.T, file string) (Maker, Source, string, *lockedBuffer) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	if _, err := setupShell(); err != nil {
		t.Skip("no shell is installed")
	}

	dir := t.TempDir()
	origin(t, filepath.Join(dir, "origin"))

	src := filepath.Join(dir, "origin", "kl3inIT", "second-bite")
	require.NoError(t, os.WriteFile(filepath.Join(src, SetupFile), []byte(file), 0o600))
	run(t, src, "add", SetupFile)
	run(t, src, "commit", "-q", "-m", "setup")

	logged := &lockedBuffer{}
	m := Maker{
		Root: filepath.Join(dir, "node"), URL: filepath.ToSlash(filepath.Join(dir, "origin")) + "/%s",
		Logger: slog.New(slog.NewTextHandler(logged, nil)),
	}

	repo, err := SourceOf("github.com", "github", "kl3inIT/second-bite", "")
	require.NoError(t, err)

	return m, repo, filepath.Join(dir, "wt", "run_1", "tsk_1"), logged
}

// Scenario: a repository declares a setup script. The node runs it in a Task's
// new worktree before the worker starts, with the worktree's path in its
// environment; a continued Conversation, which reuses the worktree, does not
// run it again; a retry, which makes the worktree anew, does.
func TestARepositorysSetupScriptRunsInANewWorktree(t *testing.T) {
	t.Parallel()

	m, repo, path, logged := declaring(t,
		"scripts:\n  setup: |\n    test -n \"$HYPOMNEMA_WORKTREE_PATH\" && echo ran >> setup.log\n")

	runs := func() int {
		raw, err := os.ReadFile(filepath.Join(path, "setup.log")) //nolint:gosec // the test's own worktree
		require.NoError(t, err)

		return strings.Count(string(raw), "ran")
	}

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	assert.Equal(t, 1, runs(), "the script ran in the worktree, which it was told the path of")

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path), "a continued Conversation")
	assert.Equal(t, 1, runs(), "a worktree that exists is not set up again")

	stopped := func() bool { return true }
	require.NoError(t, git(t.Context(), path, nil, "config", "status.showUntrackedFiles", "no"))
	require.ErrorContains(t, m.Clean(t.Context(), path, true, stopped), "untracked")
	assert.Equal(t, 1, runs(), "a dirty worktree is kept")
	require.NoError(t, os.WriteFile(filepath.Join(path, "ignored"), nil, 0o600))
	excludes := filepath.Join(t.TempDir(), "exclude")
	require.NoError(t, os.WriteFile(excludes, []byte("ignored\n"), 0o600))
	require.NoError(t, git(t.Context(), path, nil, "config", "core.excludesFile", excludes))
	require.NoError(t, os.Remove(filepath.Join(path, "setup.log")))
	require.ErrorContains(t, m.Clean(t.Context(), path, true, stopped), "ignored")
	require.FileExists(t, filepath.Join(path, "ignored"))
	require.NoError(t, os.Remove(filepath.Join(path, "ignored")))
	if !diskguard.ScopeAvailable() {
		t.Skip("Landlock is not available for the native command")
	}
	require.NoError(t, m.Clean(t.Context(), path, true, stopped))
	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path), "a retry")
	assert.Equal(t, 1, runs(), "a worktree made anew is set up again")

	assert.Empty(t, logged.String(), "a script that succeeds logs nothing")
}

// Scenario: a setup script that fails does not fail the Dispatch. The worktree
// stands, so the worker starts, and the node's log has what the script printed.
func TestASetupScriptThatFailsLeavesTheWorktree(t *testing.T) {
	t.Parallel()

	m, repo, path, logged := declaring(t, "scripts:\n  setup: |\n    echo no network >&2\n    exit 3\n")

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	assert.FileExists(t, filepath.Join(path, "README.md"))
	assert.Contains(t, logged.String(), "the setup script failed")
	assert.Contains(t, logged.String(), "no network")
}

// Scenario: a setup script that does not end is stopped when its time is up,
// and the worker starts all the same.
func TestASetupScriptThatDoesNotEndIsStopped(t *testing.T) {
	t.Parallel()

	m, repo, path, logged := declaring(t, "scripts:\n  setup: sleep 60\n")
	m.SetupTimeout = time.Second

	started := time.Now()

	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	assert.Less(t, time.Since(started), 30*time.Second, "the node did not wait for the script")
	assert.FileExists(t, filepath.Join(path, "README.md"))
	assert.Contains(t, logged.String(), "did not end within 1s")
}

// Scenario: a hypomnema.yaml the node cannot read as YAML is logged and the
// worktree stands; one that declares no script runs nothing.
func TestASetupFileThatCannotBeReadOrDeclaresNothing(t *testing.T) {
	t.Parallel()

	m, repo, path, logged := declaring(t, "scripts: [not, a, map\n")
	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	assert.Contains(t, logged.String(), "reading "+SetupFile)

	m, repo, path, logged = declaring(t, "scripts: {}\n")
	require.NoError(t, m.Ensure(t.Context(), repo, "main", "tsk_1", path))
	assert.Empty(t, logged.String())
}
