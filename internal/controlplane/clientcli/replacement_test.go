package clientcli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func TestRunOwnsReplacement(t *testing.T) {
	t.Parallel()

	const prURL = "https://github.com/owner/repo/pull/827"
	repo, host := "owner/repo", "github.com"
	sha := strings.Repeat("a", 40)
	otherSHA := strings.Repeat("b", 40)
	pushed := []openapi.PublishedHead{{Branch: "hyp/tsk_mine", Sha: sha}}
	evidence := &openapi.Dispatch{Result: &openapi.DispatchResult{
		Evidence: []openapi.Evidence{{Kind: "pull_request", Ref: prURL}},
	}}
	for _, scenario := range []struct {
		name, head, path, sha string
		dispatch              *openapi.Dispatch
		recorded              bool
		owned                 bool
	}{
		{name: "matching name without a push", path: repo, head: "hyp/tsk_mine", sha: sha},
		{name: "matching name with only worker evidence", path: repo, head: "hyp/tsk_mine", sha: sha, dispatch: evidence},
		{name: "branch and SHA pushed by the Run", path: repo, head: "hyp/tsk_mine", sha: sha, recorded: true, owned: true},
		{name: "branch moved since the recorded push", path: repo, head: "hyp/tsk_mine", sha: otherSHA, recorded: true},
		{name: "other Run branch", path: repo, head: "hyp/tsk_other", sha: sha, recorded: true},
		{name: "different repository", path: "owner/other", head: "hyp/tsk_mine", sha: sha, recorded: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			run := openapi.RunDetail{Repo: &scenario.path, RepoHost: &host, Tasks: []openapi.TaskDetail{
				{Id: "tsk_mine", LatestDispatch: scenario.dispatch},
			}}
			if scenario.recorded {
				run.PublishedHeads = &pushed
			}
			assert.Equal(t, scenario.owned, runOwnsReplacement(run, prURL, scenario.head, scenario.sha))
		})
	}
}
