package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// Scenario (issue #991): an offer whose token's account is octo (id 1) gives the worker's git that account as
// author and committer, after the token's own variables; an offer without a token or an account gives none of them.
func TestTheWorkerCommitsAsTheOffersAccount(t *testing.T) {
	t.Parallel()

	s := &Supervisor{}
	offer := nodeproto.Offer{
		Repo: "kl3inIT/hypomnema", RepoHost: "github.com", RepoKind: "github", RepoToken: "ghp_x",
		GitName: "octo", GitEmail: "1+octo@users.noreply.github.com",
	}

	src, err := s.repoSource(offer)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"GIT_AUTHOR_NAME=octo", "GIT_AUTHOR_EMAIL=1+octo@users.noreply.github.com",
		"GIT_COMMITTER_NAME=octo", "GIT_COMMITTER_EMAIL=1+octo@users.noreply.github.com",
	}, src.WorkerEnv()[len(src.Env):])

	offer.RepoToken, offer.GitName, offer.GitEmail = "", "", ""
	src, err = s.repoSource(offer)
	require.NoError(t, err)
	assert.Empty(t, src.WorkerEnv(), "no token, no account: nothing is set")
}
