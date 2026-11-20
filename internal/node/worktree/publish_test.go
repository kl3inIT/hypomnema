package worktree

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
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
	src := Source{URL: bare, Repo: githost.Repo{Host: githost.GitHubHost, Path: "kl3inIT/second-bite"}}
	maker := Maker{Root: filepath.Join(root, "node")}
	require.NoError(t, maker.Ensure(t.Context(), src, "hyp/tsk_old", "tsk_new", worktree))
	tracking := "refs/remotes/origin/hyp/tsk_old"
	base := run(t, worktree, "rev-parse", tracking)
	run(t, worktree, "commit", "-q", "--allow-empty", "-m", "fix the review")
	fix := run(t, worktree, "rev-parse", "HEAD")

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
	require.ErrorContains(t, err, "`git fetch origin refs/heads/hyp/tsk_old && git merge FETCH_HEAD`")
	assert.Equal(t, theirs, run(t, bare, "rev-parse", "refs/heads/hyp/tsk_old"), "never forced")

	run(t, worktree, "fetch", "-q", "origin", "refs/heads/hyp/tsk_old")
	assert.Equal(t, base, run(t, worktree, "rev-parse", tracking), "the bare mirror has no fetch mapping")
	assert.Equal(t, theirs, run(t, worktree, "rev-parse", "FETCH_HEAD"))
	run(t, worktree, "merge", "-q", "--no-edit", "FETCH_HEAD")
	sha, err = Publish(t.Context(), src, worktree, "tsk_new", "hyp/tsk_old")
	require.NoError(t, err)
	assert.Equal(t, sha, run(t, bare, "rev-parse", "refs/heads/hyp/tsk_old"))
}

func TestNonFastForwardDoesNotMistakeHostPolicyForDivergence(t *testing.T) {
	t.Parallel()

	const refspec = "head:refs/heads/hyp/tsk_old"
	assert.True(t, nonFastForward("!\t"+refspec+"\t[rejected] (fetch first)\n", refspec))
	assert.True(t, nonFastForward("!\t"+refspec+"\t[rejected] (non-fast-forward)\n", refspec))
	assert.False(t, nonFastForward("!\t"+refspec+"\t[remote rejected] (pre-receive hook declined)\n", refspec))
}
