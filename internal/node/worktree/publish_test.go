package worktree

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishOnlyAttestsAnAcceptedBranchUpdate(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	origin(t, root)
	remote := filepath.Join(root, "kl3inIT", "second-bite")
	worktree := filepath.Join(root, "worker")
	bare := filepath.Join(root, "assigned.git")
	run(t, root, "clone", "--bare", remote, bare)
	run(t, root, "clone", remote, worktree)
	const task = "tsk_publish"
	branch := Branch(task)
	ref := "refs/heads/" + branch
	run(t, worktree, "switch", "-c", branch)
	run(t, worktree, "push", "origin", branch)
	src := Source{URL: bare}
	first := run(t, worktree, "rev-parse", "HEAD")
	sha, err := Publish(t.Context(), src, worktree, task)
	require.NoError(t, err)
	assert.Equal(t, first, sha)
	assert.Equal(t, first, run(t, bare, "rev-parse", ref))

	sha, err = Publish(t.Context(), src, worktree, task)
	require.NoError(t, err)
	assert.Empty(t, sha, "a ref already on the host is not a push")

	run(t, worktree, "commit", "--allow-empty", "-m", "follow-up")
	second := run(t, worktree, "rev-parse", "HEAD")
	sha, err = Publish(t.Context(), src, worktree, task)
	require.NoError(t, err)
	assert.Equal(t, second, sha)
	assert.Equal(t, second, run(t, bare, "rev-parse", ref))
	assert.Equal(t, first, run(t, remote, "rev-parse", ref), "the worker's origin was not used")

	run(t, worktree, "reset", "--hard", first)
	sha, err = Publish(t.Context(), src, worktree, task)
	require.Error(t, err)
	assert.Empty(t, sha, "a non-fast-forward push was rejected")
	assert.Equal(t, second, run(t, bare, "rev-parse", ref))
}
