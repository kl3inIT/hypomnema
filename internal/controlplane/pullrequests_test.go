package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Scenario (issue #1053): a pull request left with auto-merge on is read at its host with the token of its
// repository's Connection, the one its worker was given: GitHub's GraphQL answer names a required check that
// failed. A repository without a token is not asked at all: the control plane cannot follow its pull request
// (issue #1053, review round 2), and says so.
func TestAFollowedPullRequestIsReadWithItsRepositorysToken(t *testing.T) {
	t.Parallel()

	plain, _, _ := tokensWorld(t, true)
	ctx := t.Context()

	hosts := &fakeHosts{users: map[string]string{
		"ghp_octo": `{"data":{"repository":{"pullRequest":{"state":"OPEN","mergeable":"MERGEABLE",
			"headRefOid":"b5abccde","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
			{"__typename":"CheckRun","name":"Required","status":"COMPLETED","conclusion":"FAILURE",
			 "startedAt":"2026-11-22T10:00:00Z","isRequired":true}]}}}}]}}}}}`,
	}}
	tokens, _ := withIdentities(plain, hosts)

	f := orchestration.FollowedRequest{
		Kind: githost.GitHub, URL: "https://github.com/kl3inIT/hypomnema/pull/1054", Head: "b5abccde",
		Request: githost.Request{Repo: githost.Repo{Host: githost.GitHubHost, Path: "kl3inIT/hypomnema"}, Number: 1054},
	}

	_, err := tokens.readPullRequest(ctx, f)
	require.ErrorIs(t, err, orchestration.ErrCannotFollow, "no token, no read: the control plane cannot follow it")
	assert.Empty(t, hosts.askedSoFar())

	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "default", "ghp_octo"))

	st, err := tokens.readPullRequest(ctx, f)
	require.NoError(t, err)
	assert.Equal(t, githost.RequestState{Status: githost.RequestOpen, Head: "b5abccde", Failed: []string{"Required"}}, st)
	assert.Contains(t, hosts.askedSoFar(), "api.github.com/graphql ghp_octo")
}
