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
