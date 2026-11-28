package githost

import (
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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
	assert.Equal(t, RequestState{
		Status: RequestOpen, Head: "abc1234def", Conflict: true, Failed: []string{"Required"},
		Checks: 3, Pending: []string{"Web"}, Failing: []string{"Required"},
	}, got, "Race passed on its rerun; Web is pending; CodeRabbit is not required")

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

// hosting answers GitHub's REST list of a branch's pull requests and its GraphQL read of one, by number.
type hosting struct {
	open  map[string]int // head "owner:branch" → its open pull request
	pulls map[int]string // number → its head branch
	asked []string       // what was asked, in order
	state map[int]string // number → its state, OPEN when not set
	armed map[int]bool   // number → auto-merge armed
}

func (h *hosting) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet {
		head := r.URL.Query().Get("head")
		h.asked = append(h.asked, r.URL.Host+r.URL.Path+"?head="+head+"&state="+r.URL.Query().Get("state"))

		if n, ok := h.open[head]; ok {
			return (&answering{body: `[{"number":` + strconv.Itoa(n) + `,"html_url":"x"}]`}).RoundTrip(r)
		}

		return (&answering{body: `[]`}).RoundTrip(r)
	}

	var sent struct {
		Variables struct {
			Number int `json:"number"`
		} `json:"variables"`
	}

	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &sent)
	n := sent.Variables.Number
	h.asked = append(h.asked, "graphql #"+strconv.Itoa(n))

	state, armed := cmp.Or(h.state[n], "OPEN"), "null"
	if h.armed[n] {
		armed = `{"enabledAt":"2026-11-25T10:00:00Z"}`
	}

	return (&answering{body: `{"data":{"repository":{"pullRequest":{"state":"` + state + `","mergeable":"MERGEABLE",
		"headRefOid":"abc1234","headRefName":"` + h.pulls[n] + `","autoMergeRequest":` + armed + `,
		"commits":{"nodes":[]}}}}}`}).RoundTrip(r)
}

// Scenario (issue #1168): the control plane finds a Task's pull request by its branch, as Coder's gitsync does. A
// hint whose head is the Task's branch is read alone, even once it merged and left the branch's open list; a hint
// naming a pull request of another branch is dropped for the branch's own, looked up with head=owner:branch; a
// branch with no open pull request is ErrNoRequest. Whether auto-merge is armed is read from the host.
func TestFindRequestFindsTheBranchsPullRequest(t *testing.T) {
	t.Parallel()

	repo := Repo{Host: GitHubHost, Path: "kl3inIT/hypomnema"}
	host := &hosting{
		open:  map[string]int{"kl3inIT:hyp/tsk_a": 1160},
		pulls: map[int]string{1160: "hyp/tsk_a", 1156: "hyp/tsk_b", 1150: "hyp/tsk_a"},
		state: map[int]string{1150: "MERGED"},
		armed: map[int]bool{1160: true},
	}
	client := &http.Client{Transport: host}

	r, st, err := FindRequest(t.Context(), client, GitHub, repo, "hyp/tsk_a", nil, "the-token")
	require.NoError(t, err)
	assert.Equal(t, Request{Repo: repo, Number: 1160}, r)
	assert.Equal(t, RequestState{Status: RequestOpen, Head: "abc1234", Branch: "hyp/tsk_a", AutoMerge: true}, st)
	assert.Equal(t, []string{
		"api.github.com/repos/kl3inIT/hypomnema/pulls?head=kl3inIT:hyp/tsk_a&state=open", "graphql #1160",
	}, host.asked)

	host.asked = nil
	other := Request{Repo: repo, Number: 1156}
	r, _, err = FindRequest(t.Context(), client, GitHub, repo, "hyp/tsk_a", &other, "the-token")
	require.NoError(t, err)
	assert.Equal(t, 1160, r.Number, "the hint's head is another branch: the branch's own pull request wins")
	assert.Equal(t, []string{
		"graphql #1156", "api.github.com/repos/kl3inIT/hypomnema/pulls?head=kl3inIT:hyp/tsk_a&state=open",
		"graphql #1160",
	}, host.asked)

	host.asked = nil
	merged := Request{Repo: repo, Number: 1150}
	r, st, err = FindRequest(t.Context(), client, GitHub, repo, "hyp/tsk_a", &merged, "the-token")
	require.NoError(t, err)
	assert.Equal(t, 1150, r.Number)
	assert.Equal(t, RequestMerged, st.Status)
	assert.False(t, st.AutoMerge)
	assert.Equal(t, []string{"graphql #1150"}, host.asked, "a hint of the branch is read alone")

	_, _, err = FindRequest(t.Context(), client, GitHub, repo, "hyp/tsk_c", nil, "the-token")
	require.ErrorIs(t, err, ErrNoRequest)

	host.asked = nil
	enterprise := Repo{Host: "github.company.example", Path: "team/app"}
	_, _, err = FindRequest(t.Context(), client, GitHub, enterprise, "hyp/tsk_a", nil, "the-token")
	require.ErrorIs(t, err, ErrNoRequest)
	assert.Equal(t, []string{"github.company.example/api/v3/repos/team/app/pulls?head=team:hyp/tsk_a&state=open"},
		host.asked)
}

// Scenario (issue #1168): on GitLab the merge request of a branch is its open one by source_branch, and its head
// branch and auto-merge are source_branch and merge_when_pipeline_succeeds.
func TestFindRequestOnGitLab(t *testing.T) {
	t.Parallel()

	repo := Repo{Host: "gitlab.company.example", Path: "team/platform/api"}

	list := &answering{body: `[{"iid":7,"web_url":"x"}]`}
	r, err := branchRequest(t.Context(), &http.Client{Transport: list}, GitLab, repo, "hyp/tsk_a", "the-token")
	require.NoError(t, err)
	assert.Equal(t, Request{Repo: repo, Number: 7}, r)
	assert.Equal(t, "https://gitlab.company.example/api/v4/projects/team%2Fplatform%2Fapi/merge_requests?"+
		"order_by=updated_at&per_page=1&sort=desc&source_branch=hyp%2Ftsk_a&state=opened", list.asked.URL.String())

	mr := &answering{body: `{"state":"opened","has_conflicts":false,"sha":"0123abcd","source_branch":"hyp/tsk_a",
		"merge_when_pipeline_succeeds":true,"head_pipeline":null}`}
	st, err := ReadRequest(t.Context(), &http.Client{Transport: mr}, GitLab, r, "the-token")
	require.NoError(t, err)
	assert.Equal(t, RequestState{Status: RequestOpen, Head: "0123abcd", Branch: "hyp/tsk_a", AutoMerge: true}, st)
}
