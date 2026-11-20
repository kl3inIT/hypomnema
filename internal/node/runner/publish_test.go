package runner

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
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

// envRuntime is a Runtime that keeps the git host environment the Supervisor gives its launch, then stops the
// start there.
type envRuntime struct {
	fake
	hostEnv []string
}

var errStopAtLaunch = errors.New("stopped at the launch")

func (rt *envRuntime) Prepare(context.Context, statedb.Dispatch) (Launch, error) {
	return Launch{Write: func(_ string, hostEnv []string) (shim.Spec, error) {
		rt.hostEnv = hostEnv

		return shim.Spec{}, errStopAtLaunch
	}}, nil
}

// Scenario (issue #991): the Supervisor starts the worker of an offer whose token's account is octo (id 1): the
// launch the worker's process is written from gets the four variables, after the token's own; an offer without a
// token or an account gives none of them.
func TestTheWorkerIsLaunchedWithTheOffersAccount(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	origin := filepath.Join(t.TempDir(), "origin")
	repo := filepath.Join(origin, "kl3inIT", "second-bite")
	require.NoError(t, os.MkdirAll(repo, 0o700))

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "first"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // the test's own git commands
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	launched := func(name, email, token, task string) []string {
		home := t.TempDir()
		s := &Supervisor{
			Home: home, Logger: slog.New(slog.DiscardHandler),
			Worktrees: worktree.Maker{Root: home, URL: filepath.ToSlash(origin) + "/%s"},
		}

		d := offered()
		d.ID, d.Offer.DispatchID, d.Offer.TaskID = "dsp_"+task, "dsp_"+task, task
		d.Offer.Repo, d.Offer.RepoHost, d.Offer.RepoKind, d.Offer.Base = "kl3inIT/second-bite", "github.com", "github",
			"main"
		d.Offer.RepoToken, d.Offer.GitName, d.Offer.GitEmail = token, name, email
		d.Offer.Worktree = filepath.Join(home, "wt", task)

		rt := &envRuntime{}
		require.ErrorIs(t, s.Start(t.Context(), rt, d, newReporter()), errStopAtLaunch)

		return rt.hostEnv
	}

	env := launched("octo", "1+octo@users.noreply.github.com", "ghp_x", "tsk_1")
	require.Greater(t, len(env), 4, "the token's variables come first")
	assert.Equal(t, []string{
		"GIT_AUTHOR_NAME=octo", "GIT_AUTHOR_EMAIL=1+octo@users.noreply.github.com",
		"GIT_COMMITTER_NAME=octo", "GIT_COMMITTER_EMAIL=1+octo@users.noreply.github.com",
	}, env[len(env)-4:])

	for _, v := range launched("", "", "", "tsk_2") {
		assert.False(t, strings.HasPrefix(v, "GIT_AUTHOR_") || strings.HasPrefix(v, "GIT_COMMITTER_"), v)
	}
}
