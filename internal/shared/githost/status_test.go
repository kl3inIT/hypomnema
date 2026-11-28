package githost

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1279): the control plane posts a commit status on a pull request's head. On GitHub it is
// POST /repos/:owner/:repo/statuses/:sha with its state, context, description and link, under /api/v3 on a GitHub
// Enterprise host; on GitLab POST /projects/:id/statuses/:sha, whose failure is "failed" and whose context is
// "name". A description longer than GitHub takes is cut, and an answer that is not 2xx names only its status.
func TestPostStatus(t *testing.T) {
	t.Parallel()

	const sha = "0c6ca51b4e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b"

	status := Status{
		Context: "review", State: StatusFailure, Description: "incorrect: " + strings.Repeat("é", 200),
		TargetURL: "https://hypomnema.example/team?task=tsk_1",
	}
	sent := func(t *testing.T, r *http.Request) map[string]string {
		t.Helper()

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var got map[string]string
		require.NoError(t, json.Unmarshal(body, &got))

		return got
	}

	host := &answering{status: http.StatusCreated, body: `{"id":1}`}
	repo := Repo{Host: GitHubHost, Path: "kl3inIT/hypomnema"}
	require.NoError(t, PostStatus(t.Context(), &http.Client{Transport: host}, GitHub, repo, sha, status, "the-token"))
	assert.Equal(t, http.MethodPost, host.asked.Method)
	assert.Equal(t, "https://api.github.com/repos/kl3inIT/hypomnema/statuses/"+sha, host.asked.URL.String())
	assert.Equal(t, "Bearer the-token", host.asked.Header.Get("Authorization"))

	got := sent(t, host.asked)
	assert.Equal(t, "failure", got["state"])
	assert.Equal(t, "review", got["context"])
	assert.Equal(t, status.TargetURL, got["target_url"])
	assert.Len(t, []rune(got["description"]), maxStatusDescription)
	assert.True(t, strings.HasSuffix(got["description"], "…"))

	host = &answering{status: http.StatusCreated, body: `{"id":1}`}
	repo.Host = "github.company.example"
	require.NoError(t, PostStatus(t.Context(), &http.Client{Transport: host}, GitHub, repo, sha, status, "the-token"))
	assert.Equal(t, "https://github.company.example/api/v3/repos/kl3inIT/hypomnema/statuses/"+sha,
		host.asked.URL.String())

	host = &answering{status: http.StatusCreated, body: `{"id":1}`}
	gl := Repo{Host: "gitlab.company.example", Path: "team/platform/api"}
	require.NoError(t, PostStatus(t.Context(), &http.Client{Transport: host}, GitLab, gl, sha, status, "the-token"))
	assert.Equal(t, "https://gitlab.company.example/api/v4/projects/team%2Fplatform%2Fapi/statuses/"+sha,
		host.asked.URL.String())

	got = sent(t, host.asked)
	assert.Equal(t, "failed", got["state"])
	assert.Equal(t, "review", got["name"])

	host = &answering{status: http.StatusForbidden, body: `{"message":"the-token is not allowed"}`}
	err := PostStatus(t.Context(), &http.Client{Transport: host}, GitHub, repo, sha, status, "the-token")
	require.EqualError(t, err, "github.company.example answered HTTP 403 to a commit status")
}
