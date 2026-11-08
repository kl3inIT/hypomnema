package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // the test's own git commands
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// origin makes a repository owner/name under dir with one commit on main.
func origin(t *testing.T, dir string) {
	t.Helper()

	src := filepath.Join(dir, "kl3inIT", "second-bite")
	require.NoError(t, os.MkdirAll(src, 0o700))
	run(t, src, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(src, "README.md"), []byte("second-bite\n"), 0o600))
	run(t, src, "add", "README.md")
	run(t, src, "commit", "-q", "-m", "first")
}

// Scenario: the first Dispatch of a Task makes a worktree on its own branch
// from the base; a continued one reuses it; a retry after the worktree was
// removed gets the branch back with what was committed on it.
func TestWorktreeIsMadeReusedAndRemade(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	origin(t, filepath.Join(dir, "origin"))

	m := Maker{Root: filepath.Join(dir, "node"), URL: filepath.ToSlash(filepath.Join(dir, "origin")) + "/%s"}
	path := filepath.Join(dir, "wt", "run_1", "tsk_1")

	require.NoError(t, m.Ensure(t.Context(), "kl3inIT/second-bite", "main", "tsk_1", path))
	assert.Equal(t, "hyp/tsk_1", run(t, path, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.FileExists(t, filepath.Join(path, "README.md"))

	require.NoError(t, os.WriteFile(filepath.Join(path, "login.go"), []byte("package login\n"), 0o600))
	run(t, path, "add", "login.go")
	run(t, path, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "login")

	require.NoError(t, m.Ensure(t.Context(), "kl3inIT/second-bite", "main", "tsk_1", path), "a continued Conversation")
	assert.FileExists(t, filepath.Join(path, "login.go"))

	require.NoError(t, m.Remove(t.Context(), "kl3inIT/second-bite", path))
	assert.NoDirExists(t, path)

	require.NoError(t, m.Ensure(t.Context(), "kl3inIT/second-bite", "main", "tsk_1", path), "a retry")
	assert.FileExists(t, filepath.Join(path, "login.go"), "the branch keeps the earlier commit")

	require.ErrorContains(t, m.Ensure(t.Context(), "not a repo", "main", "tsk_2", filepath.Join(dir, "x")),
		"not owner/name")
}
