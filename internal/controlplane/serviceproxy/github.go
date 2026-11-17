package serviceproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// GitHub through the proxy (issue #685): a git host's Connection, the token
// workers are given per Dispatch, used by a Bot's pi with the `gh` command.
// The catalog is Onyx's GitHub list (backend/onyx/external_apps/providers/
// github.py at da837e6, MIT, notice in third_party/onyx/NOTICE): its REST
// routes and its GraphQL operations, recognized by the operation's type and
// root field, as its GraphQLOp is. Added to it: the reads of Actions runs,
// jobs and logs, check runs and commit statuses (github-mcp-server's actions
// toolset), pull requests' files, reviews and comments, and the GraphQL root
// fields node and nodes, which `gh pr checks` asks (read off GH_DEBUG=api with
// gh 2.91.0 on 2026-11-17). Reads run; the five writes ask (Onyx's defaults);
// anything else asks as github.unlisted.

// GitHubID is the Integration a Bot reads GitHub through: github.com, whose
// Connections are the git host's.
const GitHubID = "github.com"

// maxGraphQLTokens bounds the parse of one query: gh's longest, `gh issue view`'s, is a few hundred tokens. A
// longer one is not read and asks.
const maxGraphQLTokens = 20000

// githubAPI is the host gh and the REST and GraphQL APIs are on.
const githubAPI = "api.github.com"

// githubLogHosts are where GitHub sends a log's download (a redirect from
// .../logs): read off `gh api .../actions/jobs/<id>/logs` and
// `.../actions/runs/<id>/logs` on 2026-11-17. The address is signed; no token
// is added there.
var (
	githubResults = "results-receiver.actions.githubusercontent.com"
	githubBlob    = regexp.MustCompile(`^productionresultssa[0-9]{1,3}\.blob\.core\.windows\.net$`)
)

// Operation recognizes a GraphQL request as an action by the type of an
// operation and one of its root fields (Onyx's GraphQLOp).
type Operation struct {
	Type  string
	Field string
}

func githubCatalog() Catalog {
	const repo = "/repos/{owner}/{repo}"

	var (
		get = []string{http.MethodGet}
		at  = func(methods []string, paths ...string) []Route { return routes(githubAPI, methods, paths...) }
	)

	c := Catalog{Integration: "github", Actions: []Action{
		{
			ID: "github.logs", Plumbing: true, Kind: Reads,
			Routes: append(routes(githubResults, get, "/rest/runs/{rest...}"),
				Route{Method: http.MethodGet, Path: "/actions-results/{rest...}"}),
		},
		{
			ID: "github.user.read", Service: "GitHub", Name: "Read the connected user", Kind: Reads,
			Routes:     at(get, "/user"),
			Operations: []Operation{{"query", "viewer"}},
		},
		{
			ID: "github.repos.read", Service: "GitHub", Name: "Read repositories", Kind: Reads,
			Text:   "List repositories and read one",
			Routes: at(get, "/user/repos", "/orgs/{org}/repos", "/users/{user}/repos", repo),
		},
		{
			ID: "github.issues.read", Service: "GitHub", Name: "Read issues", Kind: Reads,
			Text:   "Issues, their comments, labels and milestones",
			Routes: at(get, repo+"/issues", repo+"/issues/{rest...}", repo+"/labels", repo+"/milestones"),
		},
		{
			ID: "github.pulls.read", Service: "GitHub", Name: "Read pull requests", Kind: Reads,
			Text:   "Pull requests, their diff, files, commits, reviews and comments",
			Routes: at(get, repo+"/pulls", repo+"/pulls/{rest...}"),
		},
		{
			ID: "github.actions.read", Service: "GitHub", Name: "Read CI runs, checks and logs", Kind: Reads,
			Text: "Workflow runs, jobs and their logs, check runs and commit statuses",
			Routes: at(get, repo+"/actions/{rest...}", repo+"/commits/{ref}/check-runs",
				repo+"/commits/{ref}/check-suites", repo+"/commits/{ref}/status", repo+"/commits/{ref}/statuses",
				repo+"/check-runs/{rest...}", repo+"/check-suites/{rest...}"),
		},
		{
			ID: "github.search.read", Service: "GitHub", Name: "Search", Kind: Reads,
			Text:       "Search repositories, issues, pull requests and code",
			Routes:     at(get, "/search/{rest...}"),
			Operations: []Operation{{"query", "search"}},
		},
		{
			ID: "github.graphql.repo.read", Service: "GitHub", Name: "Read through gh's GraphQL queries", Kind: Reads,
			Text: "What `gh issue view`, `gh pr view`, `gh pr checks` and their lists read",
			Operations: []Operation{
				{"query", "repository"}, {"query", "repositoryOwner"}, {"query", "node"}, {"query", "nodes"},
			},
		},
		{
			ID: "github.refs.read", Service: "GitHub", Name: "Read branches and refs", Kind: Reads,
			Routes: at(get, repo+"/branches", repo+"/branches/{branch...}", repo+"/git/ref/{ref...}",
				repo+"/git/refs", repo+"/git/refs/{ref...}", repo+"/git/matching-refs/{ref...}", repo+"/tags"),
		},
		{
			ID: "github.contents.read", Service: "GitHub", Name: "Read file contents", Kind: Reads,
			Routes: at(get, repo+"/contents", repo+"/contents/{path...}", repo+"/readme", repo+"/readme/{dir...}"),
		},
		{
			ID: "github.git_data.read", Service: "GitHub", Name: "Read commits and git objects", Kind: Reads,
			Routes: at(get, repo+"/git/trees/{sha}", repo+"/git/blobs/{sha}", repo+"/git/commits/{sha}",
				repo+"/commits", repo+"/commits/{ref...}", repo+"/compare/{range...}"),
		},
		{
			ID: "github.releases.read", Service: "GitHub", Name: "Read releases", Kind: Reads,
			Routes: at(get, repo+"/releases", repo+"/releases/{release...}"),
		},
		{
			ID: "github.issues.create", Service: "GitHub", Name: "Open an issue", Kind: NeedsCare,
			Routes:     at([]string{http.MethodPost}, repo+"/issues"),
			Operations: []Operation{{"mutation", "createIssue"}},
		},
		{
			ID: "github.comments.create", Service: "GitHub", Name: "Comment on an issue or a pull request",
			Kind:       NeedsCare,
			Routes:     at([]string{http.MethodPost}, repo+"/issues/{number}/comments"),
			Operations: []Operation{{"mutation", "addComment"}},
		},
		{
			ID: "github.contents.write", Service: "GitHub", Name: "Create, change or delete files", Kind: NeedsCare,
			Text:   "A commit of one file, straight to a branch",
			Routes: at([]string{http.MethodPut, http.MethodDelete}, repo+"/contents/{path...}"),
		},
		{
			ID: "github.refs.write", Service: "GitHub", Name: "Create, move or delete branches", Kind: NeedsCare,
			Routes: append(at([]string{http.MethodPost}, repo+"/git/refs"),
				at([]string{http.MethodPatch, http.MethodDelete}, repo+"/git/refs/{ref...}")...),
		},
		{
			ID: "github.pulls.create", Service: "GitHub", Name: "Open a pull request", Kind: NeedsCare,
			Routes:     at([]string{http.MethodPost}, repo+"/pulls"),
			Operations: []Operation{{"mutation", "createPullRequest"}},
		},
	}}

	c.Reaches = func(host string) bool {
		return host == githubAPI || host == githubResults || githubBlob.MatchString(host)
	}
	c.Name = func(req *http.Request, body []byte, host string) Action { return githubAction(c, req, body, host) }

	return c
}

// githubAction is the action a request to GitHub is. A log's download is matched on its host's path alone (its
// storage account's number changes); a POST to /graphql by every operation in its body.
func githubAction(c Catalog, req *http.Request, body []byte, host string) Action {
	path := req.URL.EscapedPath()

	switch {
	case githubBlob.MatchString(host):
		host = ""
	case host == githubAPI && req.Method == http.MethodPost && path == "/graphql":
		return c.graphql(body)
	}

	return c.Match(req.Method, host, path)
}

// graphql is the action of a GraphQL request: of all the root fields its operations ask, the action that needs
// the most care. A field no action names, or a body that is not a GraphQL request, is unlisted and asks (Onyx's
// engine: a batched request is governed by its strictest action; graphql_parsing.py resolves fragments at the
// root, so that no field hides in one).
func (c Catalog) graphql(body []byte) Action {
	invocations, ok := graphqlInvocations(body)
	if !ok || len(invocations) == 0 {
		return unlisted(c.Integration)
	}

	rank := map[Kind]int{Reads: 0, Changes: 1, NeedsCare: 2}

	var governing *Action

	for _, op := range invocations {
		i := -1

		for j, a := range c.Actions {
			for _, o := range a.Operations {
				if o == op {
					i = j
				}
			}
		}

		if i < 0 {
			return unlisted(c.Integration)
		}

		if governing == nil || rank[c.Actions[i].Kind] > rank[governing.Kind] {
			governing = &c.Actions[i]
		}
	}

	return *governing
}

// graphqlInvocations reads the operation type and root field of every operation of a GraphQL request's body:
// one request, or a batch of them. ok is false for a body that is not one, so that it asks.
func graphqlInvocations(body []byte) ([]Operation, bool) {
	trimmed := bytes.TrimSpace(body)

	var documents []json.RawMessage
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &documents); err != nil {
			return nil, false
		}
	} else {
		documents = []json.RawMessage{trimmed}
	}

	var out []Operation

	for _, raw := range documents {
		var request struct {
			Query *string `json:"query"`
		}

		if err := json.Unmarshal(raw, &request); err != nil || request.Query == nil {
			return nil, false
		}

		document, err := parser.ParseQueryWithTokenLimit(&ast.Source{Input: *request.Query}, maxGraphQLTokens)
		if err != nil {
			return nil, false
		}

		for _, operation := range document.Operations {
			for _, field := range rootFields(operation.SelectionSet, document.Fragments, map[string]bool{}) {
				out = append(out, Operation{Type: string(operation.Operation), Field: field})
			}
		}
	}

	return out, true
}

// rootFields names the fields directly under a selection set, with those of its inline fragments and of the
// fragments it spreads, each fragment once.
func rootFields(set ast.SelectionSet, fragments ast.FragmentDefinitionList, seen map[string]bool) []string {
	var out []string

	for _, selection := range set {
		switch s := selection.(type) {
		case *ast.Field:
			out = append(out, s.Name)
		case *ast.InlineFragment:
			out = append(out, rootFields(s.SelectionSet, fragments, seen)...)
		case *ast.FragmentSpread:
			if seen[s.Name] {
				continue
			}

			seen[s.Name] = true

			if f := fragments.ForName(s.Name); f != nil {
				out = append(out, rootFields(f.SelectionSet, fragments, seen)...)
			}
		}
	}

	return out
}
