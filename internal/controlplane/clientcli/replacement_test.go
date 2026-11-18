package clientcli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func TestRunOwnsReplacement(t *testing.T) {
	t.Parallel()

	const prURL = "https://github.com/owner/repo/pull/827"
	repo, host := "owner/repo", "github.com"
	evidence := &openapi.Dispatch{Result: &openapi.DispatchResult{
		Evidence: []openapi.Evidence{{Kind: "pull_request", Ref: prURL}},
	}}
	for _, scenario := range []struct {
		name, head, path, profile string
		dispatch                  *openapi.Dispatch
		owned                     bool
	}{
		{name: "same-repository Task branch", path: repo, head: "hyp/tsk_mine", owned: true},
		{name: "other Run branch", path: repo, head: "hyp/tsk_other"},
		{name: "different repository", path: "owner/other", head: "hyp/tsk_mine"},
		{name: "Task PR evidence", path: repo, profile: "implementer", dispatch: evidence, owned: true},
		{name: "review evidence is not authorship", path: repo, profile: "reviewer-claude", dispatch: evidence},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			run := openapi.RunDetail{Repo: &scenario.path, RepoHost: &host, Tasks: []openapi.TaskDetail{
				{Id: "tsk_mine", Profile: scenario.profile, LatestDispatch: scenario.dispatch},
			}}
			assert.Equal(t, scenario.owned, runOwnsReplacement(run, prURL, scenario.head))
		})
	}
}
