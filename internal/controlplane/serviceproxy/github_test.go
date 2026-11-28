package serviceproxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
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
// request are their own actions by REST and by GraphQL. Since the catalog is generated (#739), a merge, an issue's
// change and a workflow's dispatch are named too. A root field the schema does not have (even hidden in a fragment),
// a subscription and a body that is not a GraphQL request are unlisted. A log's download is plumbing, on the hosts
// GitHub sends it to.
func TestWhatGhAsksGitHubIsAnActionOfTheCatalog(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.GitHubID)
	require.True(t, ok)

	for _, host := range []string{"api.github.com", "uploads.github.com",
		"results-receiver.actions.githubusercontent.com", "productionresultssa6.blob.core.windows.net"} {
		assert.True(t, catalog.Has(host), host)
	}

	for _, host := range []string{"github.com", "evil.blob.core.windows.net", "example.com"} {
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
		// Named since the catalog is generated (#739); unlisted before, so no answer was kept for them.
		{http.MethodPut, "api.github.com", "/repos/o/r/pulls/684/merge", "github.pulls.merge"},
		{http.MethodPatch, "api.github.com", "/repos/o/r/issues/7", "github.issues.write"},
		{http.MethodPost, "api.github.com", "/repos/o/r/actions/workflows/ci.yml/dispatches", "github.actions.write"},
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
		{`mutation { mergePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`, "github.pulls.merge"},
		{`mutation { closePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`, "github.pulls.write"},
		{`query { repository(owner: "o", name: "r") { id } enterprise(slug: "e") { id } }`,
			"github.graphql.repo.read"},
		{`query { ...F } fragment F on Query { rateLimit { limit } }`, "github.other.read"},
		{`query { ...F } fragment F on Query { notAField { id } }`, "github.unlisted"},
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
			"github.pulls.merge"},
		{`{ __type(name: "Enterprise") { name } enterprise(slug: "e") { id } }`, "github.other.read"},
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

// The coordinator's CodeRabbit trigger reaches GitHub with the Connection token without an approval (issue #1339).
func TestCoordinatorRequestsCodeRabbitWithoutApproval(t *testing.T) {
	t.Parallel()

	w, gh := githubWorld(t, 100*time.Millisecond)
	got := gh.do(http.MethodPost, "/repos/example/project/issues/7/comments", "application/json",
		[]byte(`{"body":"@coderabbitai review"}`))
	require.Equal(t, http.StatusOK, got.Status)
	received := w.service.received()
	require.Len(t, received, 1)
	assert.Equal(t, "Bearer "+realToken, received[0].Authorization)
	assert.Empty(t, w.approvals(""))
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

// githubDeclaration is the catalog's embedded declaration, read from the package's own file.
type githubDeclaration struct {
	GH   string `json:"gh"`
	REST []struct {
		Method, Host, Path, Category, Operation string
	} `json:"rest"`
	GraphQL []struct {
		Type, Field, Category string
	} `json:"graphql"`
}

func readGitHubDeclaration(t *testing.T) githubDeclaration {
	t.Helper()

	raw, err := os.ReadFile("github_operations.json")
	require.NoError(t, err)

	var d githubDeclaration
	require.NoError(t, json.Unmarshal(raw, &d))
	require.NotEmpty(t, d.REST)
	require.NotEmpty(t, d.GraphQL)

	return d
}

// placeholder fills a path's parameters, so that the path is one a request could have.
var placeholder = regexp.MustCompile(`\{[^/}]+\}`)

// Scenario: the catalog has not fallen behind GitHub's declaration (#739, decision 9a of #713). Every REST operation
// and every GraphQL root field of the embedded declaration is an action, never github.unlisted; a GET or a query is a
// read, anything else is not. Every category of the description is in a part of GitHub the owner reads: a new one
// is placed by hand, not left to Everything else, where only what has no category goes.
func TestEveryOperationOfGitHubsDeclarationIsANamedAction(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.GitHubID)
	require.True(t, ok)

	d := readGitHubDeclaration(t)

	for _, op := range d.REST {
		req, err := http.NewRequestWithContext(t.Context(), op.Method, placeholder.ReplaceAllString(op.Path, "x1"), nil)
		require.NoError(t, err)

		a := catalog.Of(req, nil, op.Host)
		_, listed := catalog.Action(a.ID)

		if !assert.True(t, listed, "%s %s (%s) is %s", op.Method, op.Path, op.Operation, a.ID) {
			continue
		}

		assert.Equal(t, op.Method == http.MethodGet, a.Kind == serviceproxy.Reads, "%s %s is %s", op.Method, op.Path,
			a.ID)
		assert.NotContains(t, a.ID, "github.other.", "category %q of %s is in no part of GitHub", op.Category,
			op.Operation)
	}

	for _, f := range d.GraphQL {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql", nil)
		require.NoError(t, err)

		a := catalog.Of(req, graphqlBody(t, f.Type+" { "+f.Field+" }"), "api.github.com")
		_, listed := catalog.Action(a.ID)

		if !assert.True(t, listed, "%s %s is %s", f.Type, f.Field, a.ID) {
			continue
		}

		assert.Equal(t, f.Type == "query", a.Kind == serviceproxy.Reads, "%s %s is %s", f.Type, f.Field, a.ID)

		if f.Type == "mutation" && f.Category != "" {
			assert.NotContains(t, a.ID, "github.other.", "category %q of %s is in no part of GitHub", f.Category,
				f.Field)
		}
	}
}

// Scenario: the catalog is generated for the gh agenttool pins (#739, decision 9b of #713). Bumping gh fails here
// until `make catalogs-generate` has been run for the new version.
func TestGitHubsCatalogIsForTheGhAgenttoolPins(t *testing.T) {
	t.Parallel()

	want := agenttool.GitHubCLI().Version

	assert.Equal(t, want, readGitHubDeclaration(t).GH, "github_operations.json")

	raw, err := os.ReadFile("catalog_sources.json")
	require.NoError(t, err)

	var sources struct {
		GitHub struct {
			CLI struct {
				Version string `json:"version"`
			} `json:"cli"`
		} `json:"github"`
	}

	require.NoError(t, json.Unmarshal(raw, &sources))
	assert.Equal(t, want, sources.GitHub.CLI.Version, "catalog_sources.json")
}

// Scenario: merging a pull request and turning auto-merge on or off are actions of their own (#739, decision 8 of
// #713), as gh sends them: `gh pr merge` by mergePullRequest, `--auto` by enablePullRequestAutoMerge,
// `--disable-auto` by disablePullRequestAutoMerge, and REST's merge and merge-async; both ask until the owner says
// otherwise. `gh issue delete` (deleteIssue) and a DELETE are deletes. A release's asset goes to uploads.github.com.
// A trailing file path or ref takes its slashes. Several actions in one GraphQL request: the one that needs the most
// care names it, and the others go with it.
func TestMergeAndAutoMergeAreActionsOfTheirOwn(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.GitHubID)
	require.True(t, ok)

	for _, id := range []string{"github.pulls.merge", "github.pulls.automerge"} {
		a, listed := catalog.Action(id)
		require.True(t, listed, id)
		assert.Equal(t, orchestration.PolicyAsk, a.Default(), id)
	}

	rest := []struct{ method, host, path, action string }{
		{http.MethodPut, "api.github.com", "/repos/o/r/pulls/7/merge", "github.pulls.merge"},
		{http.MethodPut, "api.github.com", "/repos/o/r/pulls/7/merge-async", "github.pulls.merge"},
		{http.MethodGet, "api.github.com", "/repos/o/r/pulls/7/merge", "github.pulls.read"},
		{http.MethodPost, "api.github.com", "/repos/o/r/merges", "github.repos.write"},
		{http.MethodDelete, "api.github.com", "/repos/o/r/issues/7/labels/bug", "github.issues.delete"},
		{http.MethodDelete, "api.github.com", "/repos/o/r", "github.repos.delete"},
		{http.MethodPost, "api.github.com", "/repos/o/r/actions/runs/9/rerun", "github.actions.write"},
		{http.MethodPost, "uploads.github.com", "/repos/o/r/releases/3/assets", "github.releases.write"},
		{http.MethodPost, "api.github.com", "/repos/o/r/releases/3/assets", "github.unlisted"},
		{http.MethodPut, "api.github.com", "/repos/o/r/contents/a/b/c.md", "github.contents.write"},
		{http.MethodGet, "api.github.com", "/repos/o/r/compare/main...feat/x", "github.git_data.read"},
		{http.MethodPatch, "api.github.com", "/repos/o/r/git/refs/heads/feat/x", "github.refs.write"},
		// A GET the pinned description does not have keeps the read the hand-written routes gave it.
		{http.MethodGet, "api.github.com", "/repos/o/r/git/refs", "github.refs.read"},
		{http.MethodGet, "api.github.com", "/repos/o/r/actions/something-new", "github.actions.read"},
	}

	for _, c := range rest {
		req, err := http.NewRequestWithContext(t.Context(), c.method, c.path, nil)
		require.NoError(t, err)
		assert.Equal(t, c.action, catalog.Of(req, nil, c.host).ID, "%s %s%s", c.method, c.host, c.path)
	}

	graphql := []struct {
		body, action string
		with         []string
	}{
		{`mutation PullRequestAutoMerge($input: EnablePullRequestAutoMergeInput!) { enablePullRequestAutoMerge(` +
			`input: $input) { clientMutationId } }`, "github.pulls.automerge", nil},
		{`mutation { disablePullRequestAutoMerge(input: {pullRequestId: "PR_1"}) { clientMutationId } }`,
			"github.pulls.automerge", nil},
		{`mutation { deleteIssue(input: {issueId: "I_1"}) { clientMutationId } }`, "github.issues.delete", nil},
		{`mutation { closeIssue(input: {issueId: "I_1"}) { clientMutationId } }`, "github.issues.write", nil},
		{`query { viewer { login } } mutation { mergePullRequest(input: {}) { clientMutationId } }`,
			"github.pulls.merge", []string{"github.user.read"}},
		{`mutation { addComment(input: {}) { clientMutationId } mergePullRequest(input: {}) { clientMutationId } }`,
			"github.comments.create", []string{"github.pulls.merge"}},
		{`mutation { a: addComment(input: {}) { clientMutationId } b: addComment(input: {}) { clientMutationId } }`,
			"github.comments.create", nil},
	}

	for _, c := range graphql {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql", nil)
		require.NoError(t, err)

		a := catalog.Of(req, graphqlBody(t, c.body), "api.github.com")
		assert.Equal(t, c.action, a.ID, c.body)

		var with []string
		for _, w := range a.With {
			with = append(with, w.ID)
		}

		assert.Equal(t, c.with, with, c.body)
	}
}

// Scenario: one GraphQL request that comments and merges is governed by the strictest of the owner's answers
// (Onyx's AllMatchedActions): with comments allowed, it still waits for the owner, as a merge; with merges set to
// never, it is refused.
func TestAGraphQLRequestIsGovernedByItsStrictestAnswer(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()

	_, err := w.svc.SetConnection(ctx, serviceproxy.GitHubID, "default", "", "conn-github-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(ctx, serviceproxy.GitHubID, "default", true)
	require.NoError(t, err)

	require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.GitHubID, "default", "github.comments.create",
		orchestration.PolicyAllow))

	gh, refused := w.openTo(serviceproxy.GitHubID, w.pi, "default", "api.github.com", w.chat)
	require.Zero(t, refused)

	comment := graphqlBody(t, `mutation { addComment(input: {subjectId: "I_1", body: "hi"}) { clientMutationId } }`)
	require.Equal(t, http.StatusOK, gh.do(http.MethodPost, "/graphql", "application/json", comment).Status,
		"a comment alone is allowed")

	both := graphqlBody(t, `mutation { addComment(input: {subjectId: "I_1", body: "hi"}) { clientMutationId } `+
		`mergePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`)
	answered := make(chan answer, 1)

	go func() { answered <- gh.do(http.MethodPost, "/graphql", "application/json", both) }()

	asked := w.pending()
	assert.Equal(t, "github.pulls.merge", asked.Action, "the merge beside an allowed comment asks")
	assert.Len(t, w.service.received(), 1, "nothing is sent while the owner has not answered")

	_, err = w.proxy.Decide(ctx, asked.ID, false, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, (<-answered).Status)

	require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.GitHubID, "default", "github.pulls.merge",
		orchestration.PolicyNever))

	got := gh.do(http.MethodPost, "/graphql", "application/json", both)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Len(t, w.service.received(), 1, "a request with a merge set to never is refused")
}

// Scenario (review of #746): a comment approved for the session does not carry a merge sent in the same GraphQL
// request. Both ask by default; the owner approves a comment for the session, and a request that comments and
// merges then waits for an approval of the merge, is refused while it waits, and GitHub is reached once, by the
// comment alone. Approved once, the merge runs with it.
func TestASessionGrantOfACommentDoesNotCarryAMerge(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()

	_, err := w.svc.SetConnection(ctx, serviceproxy.GitHubID, "default", "", "conn-github-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(ctx, serviceproxy.GitHubID, "default", true)
	require.NoError(t, err)

	gh, refused := w.openTo(serviceproxy.GitHubID, w.pi, "default", "api.github.com", w.chat)
	require.Zero(t, refused)

	comment := graphqlBody(t, `mutation { addComment(input: {subjectId: "I_1", body: "hi"}) { clientMutationId } }`)
	commented := make(chan answer, 1)

	go func() { commented <- gh.do(http.MethodPost, "/graphql", "application/json", comment) }()

	asked := w.pending()
	require.Equal(t, "github.comments.create", asked.Action)

	_, err = w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeSession)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, (<-commented).Status)
	require.Len(t, w.service.received(), 1)

	both := graphqlBody(t, `mutation { addComment(input: {subjectId: "I_1", body: "hi"}) { clientMutationId } `+
		`mergePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`)
	merged := make(chan answer, 1)

	go func() { merged <- gh.do(http.MethodPost, "/graphql", "application/json", both) }()

	merge := w.pending()
	assert.Equal(t, "github.pulls.merge", merge.Action, "the merge asks, whatever the comment's grant")
	assert.Len(t, w.service.received(), 1, "nothing is sent while the merge waits")

	_, err = w.proxy.Decide(ctx, merge.ID, false, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, (<-merged).Status)
	assert.Len(t, w.service.received(), 1, "GitHub was reached once, by the comment alone")

	go func() { merged <- gh.do(http.MethodPost, "/graphql", "application/json", both) }()

	again := w.pending()
	assert.Equal(t, "github.pulls.merge", again.Action)

	_, err = w.proxy.Decide(ctx, again.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, (<-merged).Status)
	assert.Len(t, w.service.received(), 2, "approved once, the merge runs with the comment")
}

// grantBetween is the store with one thing done between the proxy's reads: right after it has read that the merge
// has no standing grant, the owner approves another request's comment for the session, as he may at any time.
type grantBetween struct {
	serviceproxy.Store

	svc  *orchestration.Service
	once sync.Once
	err  error
}

func (g *grantBetween) StandingGrant(ctx context.Context, n orchestration.NewApproval) (bool, error) {
	granted, err := g.Store.StandingGrant(ctx, n)

	if n.Action == "github.pulls.merge" {
		g.once.Do(func() {
			comment := n
			comment.Action, comment.ActionName, comment.Title = "github.comments.create", "Comment", "Comment"
			comment.AskedBy, comment.MatchHash = "pi", "another request"

			asked, _, err := g.svc.AskApproval(ctx, comment)
			if err == nil {
				_, err = g.svc.DecideApproval(ctx, asked.ID, true, orchestration.ScopeSession)
			}

			g.err = err
		})
	}

	return granted, err
}

// Scenario (second review of #746): the owner approves a comment for the session while a request that comments and
// merges is being decided, between the proxy's check of each action's grant and its ask. The comment's grant does
// not carry the merge: the request waits for an approval of the merge, is refused while it waits, and reaches
// GitHub only once the merge itself is approved.
func TestACommentGrantedWhileARequestIsDecidedDoesNotCarryItsMerge(t *testing.T) {
	t.Parallel()

	var between *grantBetween

	w := newWorldWith(t, time.Minute, func(s serviceproxy.Store) serviceproxy.Store {
		svc, ok := s.(*orchestration.Service)
		require.True(t, ok)

		between = &grantBetween{Store: s, svc: svc}

		return between
	})
	ctx := t.Context()

	_, err := w.svc.SetConnection(ctx, serviceproxy.GitHubID, "default", "", "conn-github-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(ctx, serviceproxy.GitHubID, "default", true)
	require.NoError(t, err)

	gh, refused := w.openTo(serviceproxy.GitHubID, w.pi, "default", "api.github.com", w.chat)
	require.Zero(t, refused)

	both := graphqlBody(t, `mutation { addComment(input: {subjectId: "I_1", body: "hi"}) { clientMutationId } `+
		`mergePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`)
	merged := make(chan answer, 1)

	go func() { merged <- gh.do(http.MethodPost, "/graphql", "application/json", both) }()

	merge := pendingMerge(t, w)
	require.NoError(t, between.err)
	assert.Equal(t, "github.pulls.merge", merge.Action, "the merge asks, though the comment was granted meanwhile")
	assert.Empty(t, w.service.received(), "nothing is sent while the merge waits")

	_, err = w.proxy.Decide(ctx, merge.ID, false, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, (<-merged).Status)
	assert.Empty(t, w.service.received(), "a refused merge sends nothing")

	go func() { merged <- gh.do(http.MethodPost, "/graphql", "application/json", both) }()

	again := w.pending()
	assert.Equal(t, "github.pulls.merge", again.Action)

	_, err = w.proxy.Decide(ctx, again.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, (<-merged).Status)
	assert.Len(t, w.service.received(), 1, "approved, the merge runs with the comment")
}

// pendingMerge waits for the merge's approval. The comment's own approval is pending for a moment too, until the
// owner approves it in grantBetween; it is not the one waited for.
func pendingMerge(t *testing.T, w *world) orchestration.Approval {
	t.Helper()

	var found orchestration.Approval

	require.Eventually(t, func() bool {
		for _, a := range w.approvals(orchestration.ApprovalPending) {
			if a.Action == "github.pulls.merge" {
				found = a

				return true
			}
		}

		return false
	}, 5*time.Second, 10*time.Millisecond, "the merge should wait for the owner")

	return found
}

// githubRepositories answers GitHub's GET /repositories/{id} as GitHub would for an account that can read
// kl3inIT/hypomnema (1447911125) only: another id is 404, and 77 is answered with another repository's id.
func githubRepositories(r *http.Request) (int, string, bool) {
	if r.Host != "api.github.com" && r.URL.Host != "api.github.com" {
		return 0, "", false
	}

	switch r.URL.Path {
	case "/repositories/1447911125":
		return http.StatusOK, `{"id":1447911125,"full_name":"kl3inIT/hypomnema","private":false}`, true
	case "/repositories/77":
		return http.StatusOK, `{"id":78,"full_name":"kl3inIT/hypomnema"}`, true
	case "/repositories/42":
		return http.StatusNotFound, `{"message":"Not Found"}`, true
	}

	return 0, "", false
}

// githubWorld is a world with the git host's Connection github.com:default on for chats, GitHub answering
// /repositories/{id} as githubRepositories does, and gh's connection to api.github.com.
func githubWorld(t *testing.T, hold time.Duration) (*world, *tool) {
	t.Helper()

	w := newWorld(t, hold)
	w.service.answers = githubRepositories

	_, err := w.svc.SetConnection(t.Context(), serviceproxy.GitHubID, "default", "", "conn-github-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(t.Context(), serviceproxy.GitHubID, "default", true)
	require.NoError(t, err)

	gh, refused := w.openTo(serviceproxy.GitHubID, w.pi, "default", "api.github.com", w.chat)
	require.Zero(t, refused)

	return w, gh
}

// Scenario (issue #802): `gh api --paginate` lists a pull request's files. The first page is asked by
// /repos/{owner}/{repo}; GitHub's Link sends the next ones to /repositories/{id}. The proxy resolves the id through
// the same Connection (GitHub's GET /repositories/{id}, with the account's token), reads the page as the /repos
// form's read and sends it on unchanged; nobody is asked. The id's name is kept: a third page looks nothing up.
func TestAPagedListByARepositorysIDIsReadWithoutAsking(t *testing.T) {
	t.Parallel()

	w, gh := githubWorld(t, time.Minute)

	require.Equal(t, http.StatusOK, gh.do(http.MethodGet, "/repos/kl3inIT/hypomnema/pulls/786/files?per_page=100", "",
		nil).Status)
	require.Equal(t, http.StatusOK, gh.do(http.MethodGet,
		"/repositories/1447911125/pulls/786/files?per_page=100&page=2", "", nil).Status)
	require.Equal(t, http.StatusOK, gh.do(http.MethodGet,
		"/repositories/1447911125/pulls/786/files?per_page=100&page=3", "", nil).Status)

	assert.Empty(t, w.approvals(""), "reading a page asks nobody")

	received := w.service.received()
	require.Len(t, received, 4, "the first page, the id's lookup, the second and the third page")
	assert.Equal(t, "https://api.github.com/repositories/1447911125", received[1].URL)
	assert.Equal(t, "Bearer "+realToken, received[1].Authorization, "the id is looked up through the Connection")
	assert.Equal(t, "https://api.github.com/repositories/1447911125/pulls/786/files?page=2&per_page=100",
		received[2].URL, "the page is sent on as gh asked it")
	assert.Equal(t, "Bearer "+realToken, received[2].Authorization)
	assert.Equal(t, "https://api.github.com/repositories/1447911125/pulls/786/files?page=3&per_page=100",
		received[3].URL)
	assert.Contains(t, w.logs.String(), "action=github.pulls.read")
	assert.NotContains(t, w.logs.String(), realToken)
}

// Scenario (issue #802): an id the account cannot read (GitHub answers 404), and one GitHub answers with another
// repository's id, are not resolved: the request stays github.unlisted and asks, and nothing is sent until the
// owner answers. Rejected, it is never sent.
func TestARepositorysIDThatIsNotResolvedAsks(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"42", "77"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			w, gh := githubWorld(t, time.Minute)
			answered := make(chan answer, 1)

			go func() { answered <- gh.do(http.MethodGet, "/repositories/"+id+"/pulls/1/files?page=2", "", nil) }()

			asked := w.pending()
			assert.Equal(t, "github.unlisted", asked.Action)

			received := w.service.received()
			require.Len(t, received, 1, "only the id's lookup reaches GitHub")
			assert.Equal(t, "https://api.github.com/repositories/"+id, received[0].URL)

			_, err := w.proxy.Decide(t.Context(), asked.ID, false, "")
			require.NoError(t, err)
			assert.Equal(t, http.StatusForbidden, (<-answered).Status)
			assert.Len(t, w.service.received(), 1)
		})
	}
}

// Scenario (issue #802): a request by a repository's id takes the action of its /repos/{owner}/{repo} form, whatever
// its method: a comment, a merge, a branch's deletion and a change of the repository ask as they do by name, never as
// a read, and the owner is shown which repository the id is.
func TestAWriteByARepositorysIDTakesTheActionOfItsReposForm(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.GitHubID)
	require.True(t, ok)

	writes := []struct{ method, rest string }{
		{http.MethodPost, "/issues/1/comments"},
		{http.MethodPut, "/pulls/1/merge"},
		{http.MethodDelete, "/git/refs/heads/topic"},
		{http.MethodPatch, ""},
		{http.MethodPost, "/pulls"},
	}

	for _, write := range writes {
		t.Run(write.method+write.rest, func(t *testing.T) {
			t.Parallel()

			want := catalog.Match(write.method, "api.github.com", "/repos/kl3inIT/hypomnema"+write.rest)
			require.NotEqual(t, "github.unlisted", want.ID)
			require.NotEqual(t, serviceproxy.Reads, want.Kind)

			w, gh := githubWorld(t, time.Minute)
			answered := make(chan answer, 1)

			go func() {
				answered <- gh.do(write.method, "/repositories/1447911125"+write.rest, "application/json",
					[]byte(`{"body":"hi"}`))
			}()

			asked := w.pending()
			assert.Equal(t, want.ID, asked.Action)
			assert.Contains(t, asked.Payload, orchestration.Field{Label: "Repository",
				Value: "kl3inIT/hypomnema (id 1447911125)"})
			assert.Len(t, w.service.received(), 1, "only the id's lookup is sent while the owner has not answered")

			_, err := w.proxy.Decide(t.Context(), asked.ID, true, orchestration.ScopeOnce)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, (<-answered).Status)

			received := w.service.received()
			require.Len(t, received, 2)
			assert.Equal(t, write.method, received[1].Method)
			assert.Equal(t, "https://api.github.com/repositories/1447911125"+write.rest, received[1].URL,
				"the request is sent on unchanged")
		})
	}
}

// named answers GitHub's GET /repositories/1447911125 with fullName as the repository's name, and the rest as
// githubRepositories does.
func named(fullName string) func(r *http.Request) (int, string, bool) {
	return func(r *http.Request) (int, string, bool) {
		if r.URL.Path == "/repositories/1447911125" {
			return http.StatusOK, `{"id":1447911125,"full_name":"` + fullName + `"}`, true
		}

		return githubRepositories(r)
	}
}

// answering makes GitHub answer as answers does from now on.
func (w *world) answering(answers func(r *http.Request) (int, string, bool)) {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()

	w.service.answers = answers
}

// Scenario (review of #813): the id's lookup is a read of the repository, GET /repos/{owner}/{repo}'s, and is decided
// as that read is before it is sent. Set to never, a request by the id is refused and nothing reaches GitHub, as
// the same read by name is. Set to ask, the request asks with the read beside it, and nothing is sent, not even the
// lookup, until the owner answers; rejected, nothing is sent at all.
func TestTheLookupOfARepositorysIDIsDecidedAsItsRead(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf(serviceproxy.GitHubID)
	require.True(t, ok)

	read := catalog.Match(http.MethodGet, "api.github.com", "/repos/kl3inIT/hypomnema")
	require.Equal(t, "github.repos.read", read.ID)

	t.Run("never", func(t *testing.T) {
		t.Parallel()

		w, gh := githubWorld(t, time.Minute)
		require.NoError(t, w.svc.SetActionPolicy(t.Context(), serviceproxy.GitHubID, "default", read.ID,
			orchestration.PolicyNever))

		assert.Equal(t, http.StatusForbidden, gh.do(http.MethodGet, "/repos/kl3inIT/hypomnema", "", nil).Status)
		assert.Equal(t, http.StatusForbidden,
			gh.do(http.MethodGet, "/repositories/1447911125/pulls/786/files?page=2", "", nil).Status)
		assert.Empty(t, w.service.received(), "nothing reaches GitHub, not even the lookup")
		assert.Empty(t, w.approvals(""))
	})

	t.Run("ask", func(t *testing.T) {
		t.Parallel()

		w, gh := githubWorld(t, time.Minute)
		require.NoError(t, w.svc.SetActionPolicy(t.Context(), serviceproxy.GitHubID, "default", read.ID,
			orchestration.PolicyAsk))

		answered := make(chan answer, 1)

		go func() {
			answered <- gh.do(http.MethodGet, "/repositories/1447911125/pulls/786/files?page=2", "", nil)
		}()

		asked := w.pending()
		assert.Equal(t, read.ID, asked.Action, "the owner is asked for the read that learns the repository")
		assert.Contains(t, asked.Payload, orchestration.Field{Label: "Request",
			Value: "GET /repositories/1447911125/pulls/786/files"})
		assert.Empty(t, w.service.received(), "nothing is sent while the owner has not answered")

		_, err := w.proxy.Decide(t.Context(), asked.ID, false, "")
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, (<-answered).Status)
		assert.Empty(t, w.service.received())
	})
}

// Scenario (review of #813): GitHub's answer names an owner or a repository that would step out of the path, "."
// or "..": the /repos form refuses such a path, so the id is not resolved and the request asks as github.unlisted.
func TestARepositorysNameThatStepsOutOfThePathIsNotResolved(t *testing.T) {
	t.Parallel()

	for _, fullName := range []string{"../hypomnema", "./hypomnema", "kl3inIT/..", "kl3inIT/.", "o/r/x", "/r", "o/"} {
		t.Run(fullName, func(t *testing.T) {
			t.Parallel()

			w, gh := githubWorld(t, time.Minute)
			w.answering(named(fullName))

			answered := make(chan answer, 1)

			go func() {
				answered <- gh.do(http.MethodGet, "/repositories/1447911125/pulls/786/files?page=2", "", nil)
			}()

			asked := w.pending()
			assert.Equal(t, "github.unlisted", asked.Action)
			assert.Len(t, w.service.received(), 1, "only the lookup reaches GitHub")

			_, err := w.proxy.Decide(t.Context(), asked.ID, false, "")
			require.NoError(t, err)
			assert.Equal(t, http.StatusForbidden, (<-answered).Status)
		})
	}
}

// Scenario (review of #813): a name kept from an earlier read is not shown as the name of a request that asks. The
// repository is renamed and transferred after a page was read by its id; a merge by the id then asks, and the owner
// is shown the name GitHub gives now, read again for him; when it cannot be read now, only the id.
func TestARequestThatAsksShowsTheRepositorysNameAsItIsNow(t *testing.T) {
	t.Parallel()

	w, gh := githubWorld(t, time.Minute)

	require.Equal(t, http.StatusOK, gh.do(http.MethodGet,
		"/repositories/1447911125/pulls/786/files?page=2", "", nil).Status)

	w.answering(named("new-owner/new-name"))

	merge := func() orchestration.Approval {
		answered := make(chan answer, 1)

		go func() { answered <- gh.do(http.MethodPut, "/repositories/1447911125/pulls/786/merge", "", nil) }()

		asked := w.pending()

		_, err := w.proxy.Decide(t.Context(), asked.ID, false, "")
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, (<-answered).Status)

		return asked
	}

	asked := merge()
	assert.Equal(t, "github.pulls.merge", asked.Action)
	assert.Contains(t, asked.Payload,
		orchestration.Field{Label: "Repository", Value: "new-owner/new-name (id 1447911125)"})

	w.answering(func(r *http.Request) (int, string, bool) {
		if r.URL.Path == "/repositories/1447911125" {
			return http.StatusInternalServerError, `{}`, true
		}

		return 0, "", false
	})

	asked = merge()
	assert.Contains(t, asked.Payload, orchestration.Field{Label: "Repository",
		Value: "the repository with id 1447911125 (its name could not be read now)"})

	for _, got := range w.service.received() {
		assert.NotEqual(t, http.MethodPut, got.Method, "no merge was sent")
	}
}

// Scenario (second review of #813): a request by a repository's id is never looser than its /repos form when the
// read that learns the repository asks. The owner approves that read; only then is the id looked up, and the request
// is decided as what it is: a merge or a read of files set to never is refused and never sent, and a read of files
// that is allowed runs.
func TestARequestByIDIsDecidedAsWhatItIsOnceItsLookupIsApproved(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, method, path, never string
		status                    int
	}{
		{"merge set to never", http.MethodPut, "/repositories/1447911125/pulls/786/merge", "github.pulls.merge",
			http.StatusForbidden},
		{"files set to never", http.MethodGet, "/repositories/1447911125/pulls/786/files?page=2", "github.pulls.read",
			http.StatusForbidden},
		{"files allowed", http.MethodGet, "/repositories/1447911125/pulls/786/files?page=2", "", http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			w, gh := githubWorld(t, time.Minute)
			ctx := t.Context()

			require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.GitHubID, "default", "github.repos.read",
				orchestration.PolicyAsk))

			if c.never != "" {
				require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.GitHubID, "default", c.never,
					orchestration.PolicyNever))
			}

			answered := make(chan answer, 1)

			go func() { answered <- gh.do(c.method, c.path, "", nil) }()

			asked := w.pending()
			require.Equal(t, "github.repos.read", asked.Action)
			assert.Empty(t, w.service.received())

			_, err := w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
			require.NoError(t, err)
			assert.Equal(t, c.status, (<-answered).Status)

			received := w.service.received()
			require.NotEmpty(t, received)
			assert.Equal(t, "https://api.github.com/repositories/1447911125", received[0].URL,
				"the id is looked up once the read is approved")

			if c.status != http.StatusOK {
				assert.Len(t, received, 1, "the request itself is never sent")

				return
			}

			require.Len(t, received, 2)
			assert.Equal(t, "https://api.github.com"+c.path, received[1].URL)
		})
	}
}

// Scenario (second review of #813): every owner GitHub allows is resolved, such as the user "-" (id 75544), who
// owns -/test (repository id 45484796; both read with gh api on 2026-11-18): a page of its files by the id reads as
// its /repos form does, without asking.
func TestAnOwnerNamedWithAHyphenIsResolved(t *testing.T) {
	t.Parallel()

	w, gh := githubWorld(t, time.Minute)
	w.answering(func(r *http.Request) (int, string, bool) {
		if r.URL.Path == "/repositories/45484796" {
			return http.StatusOK, `{"id":45484796,"full_name":"-/test"}`, true
		}

		return 0, "", false
	})

	require.Equal(t, http.StatusOK, gh.do(http.MethodGet, "/repos/-/test/pulls/1/files", "", nil).Status)
	require.Equal(t, http.StatusOK, gh.do(http.MethodGet, "/repositories/45484796/pulls/1/files?page=2", "",
		nil).Status)
	assert.Empty(t, w.approvals(""), "nobody is asked")
	assert.Len(t, w.service.received(), 3)
}
