package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func TestPublishRequiresTheRunRepositoryEvenOverSSH(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		remote string
		valid  bool
	}{
		{"", true},
		{"git@github.com:owner/repo.git", true},
		{"ssh://git@ssh.github.com:443/owner/repo.git", true},
		{"git@github.com:someone/repo.git", false},
		{"git@mirror.example:owner/repo.git", false},
	} {
		offer := nodeproto.Offer{Repo: "owner/repo", RepoHost: "github.com", RepoSSHURL: scenario.remote}
		assert.Equal(t, scenario.valid, githubPushRemote(offer), scenario.remote)
	}
}

// Scenario (issue #690): an offer of a control plane before #690, or of a plain Task, names no branch: the worktree
// starts from the Run's base, the commits go to the Task's own branch and its pull request targets the base. A Task
// that continues another or is stacked on one gets the branches its offer names.
func TestTheOfferNamesTheTasksBranches(t *testing.T) {
	t.Parallel()

	plain := nodeproto.Offer{TaskID: "tsk_1", Base: "main"}
	assert.Equal(t, [3]string{"main", "hyp/tsk_1", "main"},
		[3]string{startBranch(plain), pushBranch(plain), prBase(plain)})

	continued := nodeproto.Offer{TaskID: "tsk_2", Base: "main", PushBranch: "hyp/tsk_1", StartFrom: "hyp/tsk_1"}
	assert.Equal(t, [3]string{"hyp/tsk_1", "hyp/tsk_1", "main"},
		[3]string{startBranch(continued), pushBranch(continued), prBase(continued)})

	stacked := nodeproto.Offer{TaskID: "tsk_3", Base: "main", StartFrom: "hyp/tsk_1", PRBase: "hyp/tsk_1"}
	assert.Equal(t, [3]string{"hyp/tsk_1", "hyp/tsk_3", "hyp/tsk_1"},
		[3]string{startBranch(stacked), pushBranch(stacked), prBase(stacked)})
}

// gitRun runs git in dir as the test's own setup, with an identity of its own.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // the test's own git commands
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// Scenario (issue #991), end to end on the node: the node user's ~/.gitconfig names someone else, as on the node
// where a worker committed as its user. The Supervisor starts the worker of an offer whose token's account is octo
// (id 1): the repository's setup script commits in the new worktree, then the worker's own process commits there,
// with only the environment the node started it with. Both commits are authored and committed by octo.
//
// Not parallel: the test gives the node a HOME of its own.
func TestTheWorkersCommitsNameTheTokensAccount(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[user]\n\tname = tinzisreal\n\temail = 163243197+tinzisreal@users.noreply.github.com\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}

	origin := filepath.Join(t.TempDir(), "origin")
	repo := filepath.Join(origin, "kl3inIT", "second-bite")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	gitRun(t, repo, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, worktree.SetupFile),
		[]byte("scripts:\n  setup: git commit -q --allow-empty -m setup\n"), 0o600))
	gitRun(t, repo, "add", worktree.SetupFile)
	gitRun(t, repo, "commit", "-q", "-m", "first")

	g := newRig(t, "commit")
	g.sup.Worktrees = worktree.Maker{Root: t.TempDir(), URL: filepath.ToSlash(origin) + "/%s"}

	d := g.dispatch()
	d.Offer.Repo, d.Offer.RepoHost, d.Offer.RepoKind, d.Offer.Base = "kl3inIT/second-bite", "github.com", "github",
		"main"
	d.Offer.RepoToken, d.Offer.GitName, d.Offer.GitEmail = "ghp_x", "octo", "1+octo@users.noreply.github.com"

	report := newReporter()
	require.NoError(t, g.sup.Start(t.Context(), g.rt, d, report))
	t.Cleanup(func() { _ = shim.KillRecorded(g.sup.dir("dsp_1")) })
	require.Eventually(t, func() bool { return report.startedCount() == 1 }, 20*time.Second, 20*time.Millisecond)

	failed, err := os.ReadFile(filepath.Join(g.worktree, "commit-failed.txt"))
	require.ErrorIs(t, err, os.ErrNotExist, "the worker's commit failed: %s", failed)

	assert.Equal(t, "tinzisreal", gitRun(t, g.worktree, "config", "user.name"),
		"the node user's configuration names someone else")

	const asOcto = "octo <1+octo@users.noreply.github.com> / octo <1+octo@users.noreply.github.com>"

	who := func(rev string) string {
		return gitRun(t, g.worktree, "log", "-1", "--format=%s: %an <%ae> / %cn <%ce>", rev)
	}
	assert.Equal(t, "worker: "+asOcto, who("HEAD"), "the worker's own process commits as the token's account")
	assert.Equal(t, "setup: "+asOcto, who("HEAD~1"), "so does the repository's setup script")

	g.sup.Stop("dsp_1")
	report.wait(t)
}
