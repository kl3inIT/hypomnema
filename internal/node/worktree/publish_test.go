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
	sha, err := Publish(t.Context(), src, worktree, task, "")
	require.NoError(t, err)
	assert.Equal(t, first, sha)
	assert.Equal(t, first, run(t, bare, "rev-parse", ref))

	sha, err = Publish(t.Context(), src, worktree, task, "")
	require.NoError(t, err)
	assert.Empty(t, sha, "a ref already on the host is not a push")

	run(t, worktree, "commit", "--allow-empty", "-m", "follow-up")
	second := run(t, worktree, "rev-parse", "HEAD")
	sha, err = Publish(t.Context(), src, worktree, task, "")
	require.NoError(t, err)
	assert.Equal(t, second, sha)
	assert.Equal(t, second, run(t, bare, "rev-parse", ref))
	assert.Equal(t, first, run(t, remote, "rev-parse", ref), "the worker's origin was not used")

	run(t, worktree, "reset", "--hard", first)
	sha, err = Publish(t.Context(), src, worktree, task, "")
	require.Error(t, err)
	assert.Empty(t, sha, "a non-fast-forward push was rejected")
	assert.Equal(t, second, run(t, bare, "rev-parse", ref))
}

// Scenario (issue #690): a Task continues the earlier Task tsk_old, whose pull request is on hyp/tsk_old. Its commits,
// on its own branch, are pushed to hyp/tsk_old, never forced: when someone pushed there meanwhile, the host refuses
// and the error says to merge the branch first; after the merge the push goes through.
func TestPublishPushesIntoTheBranchItContinues(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	origin(t, root)
	remote := filepath.Join(root, "kl3inIT", "second-bite")
	bare := filepath.Join(root, "host.git")
	run(t, root, "clone", "-q", "--bare", remote, bare)
	run(t, bare, "branch", "hyp/tsk_old", "main")

	worktree := filepath.Join(root, "worker")
	run(t, root, "clone", "-q", bare, worktree)
	run(t, worktree, "switch", "-q", "-c", "hyp/tsk_new", "origin/hyp/tsk_old")
	run(t, worktree, "commit", "-q", "--allow-empty", "-m", "fix the review")
	fix := run(t, worktree, "rev-parse", "HEAD")

	src := Source{URL: bare}
	sha, err := Publish(t.Context(), src, worktree, "tsk_new", "hyp/tsk_old")
	require.NoError(t, err)
	assert.Equal(t, fix, sha)
	assert.Equal(t, fix, run(t, bare, "rev-parse", "refs/heads/hyp/tsk_old"), "the earlier Task's branch moved")
	assert.NotContains(t, run(t, bare, "branch", "--list", "hyp/tsk_new"), "hyp/tsk_new",
		"no new branch, so no new pull request")

	other := filepath.Join(root, "other")
	run(t, root, "clone", "-q", "-b", "hyp/tsk_old", bare, other)
	run(t, other, "commit", "-q", "--allow-empty", "-m", "someone else")
	run(t, other, "push", "-q", "origin", "hyp/tsk_old")
	theirs := run(t, other, "rev-parse", "HEAD")

	run(t, worktree, "commit", "-q", "--allow-empty", "-m", "second round")
	_, err = Publish(t.Context(), src, worktree, "tsk_new", "hyp/tsk_old")
	require.ErrorContains(t, err, "merge `origin/hyp/tsk_old` into your branch")
	assert.Equal(t, theirs, run(t, bare, "rev-parse", "refs/heads/hyp/tsk_old"), "never forced")

	run(t, worktree, "fetch", "-q", "origin", "hyp/tsk_old")
	run(t, worktree, "merge", "-q", "--no-edit", "origin/hyp/tsk_old")
	sha, err = Publish(t.Context(), src, worktree, "tsk_new", "hyp/tsk_old")
	require.NoError(t, err)
	assert.Equal(t, sha, run(t, bare, "rev-parse", "refs/heads/hyp/tsk_old"))
}
