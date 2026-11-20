package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
