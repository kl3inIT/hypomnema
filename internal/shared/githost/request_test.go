package githost

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1053): the control plane reads a pull request it follows. On GitHub one GraphQL query gives
// its state, head, conflict and the required checks of its head: a required check that failed is named, by its
// newest run, so one rerun green after a failure is not; a check that is not required, or a pending one, is not.
// GitLab's merge request gives its state, conflict and its head pipeline. The token goes as a bearer to the host's
// API alone, and an answer that is not 200 names only its status.
func TestReadRequest(t *testing.T) {
	t.Parallel()

	rollup := `{"data":{"repository":{"pullRequest":{"state":"OPEN","mergeable":"CONFLICTING",
		"headRefOid":"abc1234def","commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
		{"__typename":"CheckRun","name":"Required","status":"COMPLETED","conclusion":"FAILURE",
		 "startedAt":"2026-11-22T10:00:00Z","isRequired":true},
		{"__typename":"CheckRun","name":"Race","status":"COMPLETED","conclusion":"FAILURE",
		 "startedAt":"2026-11-22T10:00:00Z","isRequired":true},
		{"__typename":"CheckRun","name":"Race","status":"COMPLETED","conclusion":"SUCCESS",
		 "startedAt":"2026-11-22T10:20:00Z","isRequired":true},
		{"__typename":"CheckRun","name":"Web","status":"IN_PROGRESS","conclusion":null,
		 "startedAt":"2026-11-22T10:00:00Z","isRequired":true},
		{"__typename":"StatusContext","context":"CodeRabbit","state":"FAILURE",
		 "createdAt":"2026-11-22T10:00:00Z","isRequired":false}
		]}}}}]}}}}}`

	host := &answering{body: rollup}
	r := Request{Repo: Repo{Host: GitHubHost, Path: "kl3inIT/hypomnema"}, Number: 1054}

	got, err := ReadRequest(t.Context(), &http.Client{Transport: host}, GitHub, r, "the-token")
	require.NoError(t, err)
	assert.Equal(t, RequestState{Status: RequestOpen, Head: "abc1234def", Conflict: true, Failed: []string{"Required"}},
		got, "Race passed on its rerun; Web is pending; CodeRabbit is not required")

	assert.Equal(t, "https://api.github.com/graphql", host.asked.URL.String())
	assert.Equal(t, http.MethodPost, host.asked.Method)
	assert.Equal(t, "Bearer the-token", host.asked.Header.Get("Authorization"))

	body, err := io.ReadAll(host.asked.Body)
	require.NoError(t, err)

	var sent struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Contains(t, sent.Query, "isRequired(pullRequestNumber: $number)")
	assert.Equal(t, map[string]any{"owner": "kl3inIT", "name": "hypomnema", "number": float64(1054)}, sent.Variables)

	host = &answering{body: `{"data":{"repository":{"pullRequest":{"state":"MERGED","mergeable":"UNKNOWN",
		"headRefOid":"abc1234def","commits":{"nodes":[]}}}}}`}
	r.Repo.Host = "github.company.example"
	got, err = ReadRequest(t.Context(), &http.Client{Transport: host}, GitHub, r, "the-token")
	require.NoError(t, err)
	assert.Equal(t, RequestState{Status: RequestMerged, Head: "abc1234def"}, got)
	assert.Equal(t, "https://github.company.example/api/graphql", host.asked.URL.String())

	host = &answering{body: `{"state":"opened","has_conflicts":false,"sha":"0123abcd",
		"head_pipeline":{"status":"failed"}}`}
	gl := Request{Repo: Repo{Host: "gitlab.company.example", Path: "team/platform/api"}, Number: 7}
	got, err = ReadRequest(t.Context(), &http.Client{Transport: host}, GitLab, gl, "the-token")
	require.NoError(t, err)
	assert.Equal(t, RequestState{Status: RequestOpen, Head: "0123abcd", Failed: []string{"pipeline"}}, got)
	assert.Equal(t, "https://gitlab.company.example/api/v4/projects/team%2Fplatform%2Fapi/merge_requests/7",
		host.asked.URL.String())

	host = &answering{body: `{"state":"merged","has_conflicts":false,"sha":"0123abcd","head_pipeline":null}`}
	got, err = ReadRequest(t.Context(), &http.Client{Transport: host}, GitLab, gl, "the-token")
	require.NoError(t, err)
	assert.Equal(t, RequestMerged, got.Status)

	host = &answering{status: http.StatusUnauthorized, body: "bad credentials: the-token"}
	_, err = ReadRequest(t.Context(), &http.Client{Transport: host}, GitLab, gl, "the-token")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "the-token")
	assert.Contains(t, err.Error(), "HTTP 401")
}

// paging answers GitHub's GraphQL by the cursor asked for: the first page without one, the next with it.
type paging struct {
	pages  map[string]string
	afters []string
}

func (p *paging) RoundTrip(r *http.Request) (*http.Response, error) {
	var sent struct {
		Variables map[string]any `json:"variables"`
	}

	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &sent)

	after, _ := sent.Variables["after"].(string)
	p.afters = append(p.afters, after)

	return (&answering{body: p.pages[after]}).RoundTrip(r)
}

// Scenario (issue #1053, review round 2): a pull request with more than 100 checks has them on several pages. The
// read follows the end cursor until there is no next page, as gh does, so a required check that failed on the
// second page is named.
func TestReadRequestReadsEveryPageOfChecks(t *testing.T) {
	t.Parallel()

	page := func(check, next string) string {
		info := `{"hasNextPage":false,"endCursor":null}`
		if next != "" {
			info = `{"hasNextPage":true,"endCursor":"` + next + `"}`
		}

		return `{"data":{"repository":{"pullRequest":{"state":"OPEN","mergeable":"MERGEABLE","headRefOid":"abc1234",
			"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":` + info + `,"nodes":[` +
			check + `]}}}}]}}}}}`
	}

	host := &paging{pages: map[string]string{
		"": page(`{"__typename":"CheckRun","name":"Lint","status":"COMPLETED","conclusion":"SUCCESS",
			"startedAt":"2026-11-22T10:00:00Z","isRequired":true}`, "Y3Vyc29yOjEwMA=="),
		"Y3Vyc29yOjEwMA==": page(`{"__typename":"CheckRun","name":"Required","status":"COMPLETED",
			"conclusion":"FAILURE","startedAt":"2026-11-22T10:00:00Z","isRequired":true}`, ""),
	}}
	r := Request{Repo: Repo{Host: GitHubHost, Path: "kl3inIT/hypomnema"}, Number: 1054}

	got, err := ReadRequest(t.Context(), &http.Client{Transport: host}, GitHub, r, "the-token")
	require.NoError(t, err)
	assert.Equal(t, []string{"Required"}, got.Failed)
	assert.Equal(t, []string{"", "Y3Vyc29yOjEwMA=="}, host.afters, "two pages, the second by the first's cursor")
}
