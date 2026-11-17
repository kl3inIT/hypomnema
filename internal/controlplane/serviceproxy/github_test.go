package serviceproxy_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// graphqlBody is a GraphQL request as gh sends one.
func graphqlBody(t *testing.T, query string) []byte {
	t.Helper()

	raw, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"number": 7}})
	require.NoError(t, err)

	return raw
}

// Scenario: what gh asks GitHub is an action of the catalog (issue #685). The REST reads of an issue, a pull
// request's diff, a run's logs and its checks are reads, and so are the GraphQL queries gh issue view, gh pr view
// and gh pr checks send (read off GH_DEBUG=api with gh 2.91.0). Opening an issue, commenting and opening a pull
// request are their own actions by REST and by GraphQL. A mutation the catalog does not name, a query whose root
// field it does not name (even beside one it does, or hidden in a fragment), and a body that is not a GraphQL
// request are unlisted. A log's download is plumbing, on the hosts GitHub sends it to.
func TestWhatGhAsksGitHubIsAnActionOfTheCatalog(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.GitHubID)
	require.True(t, ok)

	for _, host := range []string{"api.github.com", "results-receiver.actions.githubusercontent.com",
		"productionresultssa6.blob.core.windows.net"} {
		assert.True(t, catalog.Has(host), host)
	}

	for _, host := range []string{"github.com", "uploads.github.com", "evil.blob.core.windows.net", "example.com"} {
		assert.False(t, catalog.Has(host), host)
	}

	rest := []struct{ method, host, path, action string }{
		{http.MethodGet, "api.github.com", "/repos/o/r/issues/7", "github.issues.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/issues/7/comments", "github.issues.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/pulls/684", "github.pulls.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/pulls/684/files", "github.pulls.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/actions/runs/43902950344/jobs", "github.actions.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/actions/jobs/133424122651/logs", "github.actions.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/commits/abc123/check-runs", "github.actions.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/commits/abc123", "github.git_data.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/contents/docs/a.md", "github.contents.read"},
		{http.MethodGet, "api.github.com", "/search/issues", "github.search.read"},
		{http.MethodPost, "api.github.com", "/repos/o/r/issues", "github.issues.create"},
		{http.MethodPost, "api.github.com", "/repos/o/r/issues/7/comments", "github.comments.create"},
		{http.MethodPost, "api.github.com", "/repos/o/r/pulls", "github.pulls.create"},
		{http.MethodPut, "api.github.com", "/repos/o/r/contents/docs/a.md", "github.contents.write"},
		{http.MethodDelete, "api.github.com", "/repos/o/r/git/refs/heads/x", "github.refs.write"},
		{http.MethodPut, "api.github.com", "/repos/o/r/pulls/684/merge", "github.unlisted"},
		{http.MethodPatch, "api.github.com", "/repos/o/r/issues/7", "github.unlisted"},
		{http.MethodPost, "api.github.com", "/repos/o/r/actions/workflows/ci.yml/dispatches", "github.unlisted"},
		{http.MethodGet, "api.github.com", "/graphql", "github.unlisted"},
		{http.MethodGet, "results-receiver.actions.githubusercontent.com", "/rest/runs/10e8/logs", "github.logs"},
		{http.MethodGet, "productionresultssa6.blob.core.windows.net", "/actions-results/10e8/job/logs.txt",
			"github.logs"},
		{http.MethodPut, "productionresultssa6.blob.core.windows.net", "/actions-results/10e8", "github.unlisted"},
	}

	for _, c := range rest {
		req, err := http.NewRequestWithContext(t.Context(), c.method, c.path, nil)
		require.NoError(t, err)
		assert.Equal(t, c.action, catalog.Of(req, nil, c.host).ID, "%s %s%s", c.method, c.host, c.path)
	}

	graphql := []struct{ body, action string }{
		{`query IssueByNumber($owner: String!, $repo: String!, $number: Int!) { repository(owner: $owner, name: ` +
			`$repo) { issue: issueOrPullRequest(number: $number) { __typename } } }`, "github.graphql.repo.read"},
		{`query PullRequestStatusChecks($id: ID!) { node(id: $id) { id } }`, "github.graphql.repo.read"},
		{`fragment issue on Issue { number } query IssueList { repository(owner: "o", name: "r") { issues(first: 2)` +
			` { nodes { ...issue } } } }`, "github.graphql.repo.read"},
		{`{ viewer { login } }`, "github.user.read"},
		{`mutation { addComment(input: {subjectId: "I_1", body: "hi"}) { clientMutationId } }`,
			"github.comments.create"},
		{`mutation { createIssue(input: {repositoryId: "R_1", title: "t"}) { issue { number } } }`,
			"github.issues.create"},
		{`query { viewer { login } } mutation M { createPullRequest(input: {}) { clientMutationId } }`,
			"github.pulls.create"},
		{`mutation { mergePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`, "github.unlisted"},
		{`query { repository(owner: "o", name: "r") { id } enterprise(slug: "e") { id } }`, "github.unlisted"},
		{`query { ...F } fragment F on Query { rateLimit { limit } }`, "github.unlisted"},
		{`subscription { viewer { login } }`, "github.unlisted"},
		{`query { repository(`, "github.unlisted"},
		// gh's feature detection (#706): an alias at the root is matched by its field.
		{`query SearchType_enumValues{SearchType: __type(name: "SearchType"){enumValues(includeDeprecated: true)` +
			`{name}}}`, "github.schema.read"},
		{`{ __schema { queryType { name } } }`, "github.schema.read"},
		{`{ __typename }`, "github.schema.read"},
		{`{ __typename viewer { login } }`, "github.user.read"},
		{`{ t: __type(name: "Issue") { name } repository(owner: "o", name: "r") { id } }`,
			"github.graphql.repo.read"},
		{`query { __type(name: "Issue") { name } } mutation { addComment(input: {}) { clientMutationId } }`,
			"github.comments.create"},
		{`mutation { __typename addComment(input: {}) { clientMutationId } }`, "github.unlisted"},
		{`query { __type(name: "Issue") { name } } mutation { mergePullRequest(input: {}) { clientMutationId } }`,
			"github.unlisted"},
		{`{ __type(name: "Enterprise") { name } enterprise(slug: "e") { id } }`, "github.unlisted"},
		{`subscription { __typename }`, "github.unlisted"},
	}

	for _, c := range graphql {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql", nil)
		require.NoError(t, err)
		assert.Equal(t, c.action, catalog.Of(req, graphqlBody(t, c.body), "api.github.com").ID, c.body)
	}

	batch := []byte(`[{"query":"{ viewer { login } }"},` +
		`{"query":"mutation { addComment(input: {}) { clientMutationId } }"}]`)

	for body, action := range map[string]string{
		string(batch):      "github.comments.create",
		`not json`:         "github.unlisted",
		`{"variables":{}}`: "github.unlisted",
		``:                 "github.unlisted",
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql", nil)
		require.NoError(t, err)
		assert.Equal(t, action, catalog.Of(req, []byte(body), "api.github.com").ID, body)
	}

	for _, a := range catalog.Listed() {
		want := orchestration.PolicyAllow
		if a.Kind == serviceproxy.NeedsCare {
			want = orchestration.PolicyAsk
		}

		assert.Equal(t, want, a.Default(), a.ID)
	}
}

// Scenario: a Bot's pi reads an issue with gh, given the git host's Connection github.com:default, the token
// workers use. gh holds a placeholder; GitHub receives the host's token. A comment waits for the owner and reaches
// GitHub only once he approves. gh's feature detection, a query of the schema, runs on its own (#706). A log's
// download carries no token.
func TestABotReadsGitHubWithTheHostsTokenAndACommentAsks(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()

	_, err := w.svc.SetConnection(ctx, serviceproxy.GitHubID, "default", "", "conn-github-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(ctx, serviceproxy.GitHubID, "default", true)
	require.NoError(t, err)

	gh, refused := w.openTo(serviceproxy.GitHubID, w.pi, "default", "api.github.com", w.chat)
	require.Zero(t, refused)

	got := gh.do(http.MethodPost, "/graphql", "application/json",
		graphqlBody(t, `query { repository(owner: "o", name: "r") { issue(number: 7) { title } } }`))
	require.Equal(t, http.StatusOK, got.Status)

	read := gh.do(http.MethodGet, "/repos/o/r/actions/runs/1/jobs", "", nil)
	require.Equal(t, http.StatusOK, read.Status)

	schema := gh.do(http.MethodPost, "/graphql", "application/json", graphqlBody(t, `query SearchType_enumValues{`+
		`SearchType: __type(name: "SearchType"){enumValues(includeDeprecated: true){name}}}`))
	require.Equal(t, http.StatusOK, schema.Status, "gh's feature detection reads the schema (#706)")

	received := w.service.received()
	require.Len(t, received, 3)
	assert.Equal(t, "https://api.github.com/graphql", received[0].URL)
	assert.Equal(t, "Bearer "+realToken, received[0].Authorization)
	assert.Equal(t, "Bearer "+realToken, received[1].Authorization)
	assert.Empty(t, w.approvals(""), "reading asks nobody")

	comment := graphqlBody(t, `mutation { addComment(input: {subjectId: "I_1", body: "Looking"}) { clientMutationId } }`)
	answered := make(chan answer, 1)

	go func() { answered <- gh.do(http.MethodPost, "/graphql", "application/json", comment) }()

	asked := w.pending()
	assert.Equal(t, "github.comments.create", asked.Action)
	assert.Equal(t, serviceproxy.GitHubID, asked.Integration)
	assert.Len(t, w.service.received(), 3, "nothing is sent while the owner has not answered")

	_, err = w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, (<-answered).Status)

	received = w.service.received()
	require.Len(t, received, 4)
	assert.True(t, bytes.Equal(comment, received[3].Body), "the request goes on as gh made it")

	logs, refused := w.openTo(serviceproxy.GitHubID, w.pi, "default", "productionresultssa6.blob.core.windows.net", w.chat)
	require.Zero(t, refused)
	require.Equal(t, http.StatusOK, logs.do(http.MethodGet, "/actions-results/10e8/logs.txt?sig=abc", "", nil).Status)

	received = w.service.received()
	require.Len(t, received, 5)
	assert.Empty(t, received[4].Authorization, "a signed download carries no token")

	_, refused = w.openTo(serviceproxy.GitHubID, w.pi, "default", "github.com", w.chat)
	assert.Equal(t, http.StatusForbidden, refused, "only the API's hosts are reached")
	assert.NotContains(t, w.logs.String(), realToken)
}
