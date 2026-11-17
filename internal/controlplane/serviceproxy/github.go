package serviceproxy

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// GitHub through the proxy (issue #685): a git host's Connection, the token workers are given per Dispatch, used by
// a Bot's pi with the `gh` command. The catalog is generated (issue #739, step 1 of #713): every operation of
// GitHub's REST description and every root field of its GraphQL schema is an action, so that nothing gh sends is
// unlisted. github_operations.json keeps, of github/rest-api-description (descriptions/api.github.com, MIT), each
// operation's method, host, path, x-github category and operationId, and of the public schema
// (docs.github.com/public/fpt/schema.docs.graphql) each query and mutation with its @docsCategory, as Superset's
// catalog keeps three fields of its description; catalogsgen writes it from the sources catalog_sources.json pins,
// and the notice is in third_party/github/NOTICE.
//
// The categories fold into the services the owner reads (github-mcp-server's toolsets), each with a read, a write
// and a delete: a GET or a query reads, a DELETE or a mutation named delete… deletes, anything else writes
// (github-mcp-server's ReadOnlyHint, Onyx's defaults). Reads run; the rest asks. The actions the catalog named by
// hand before (Onyx's GitHub list, backend/onyx/external_apps/providers/github.py at da837e6, MIT, notice in
// third_party/onyx/NOTICE, and the reads added to it) keep their ids, so that the owner's answers stay: the operations
// they named are mapped to them by githubNamed, and their GET routes stay for what the description does not have.
// Merging a pull request and turning auto-merge on or off are actions of their own (t3code's merge,
// enable-auto-merge and disable-auto-merge), so that the owner can allow the host to merge once checks pass
// without allowing a merge now (#714). The introspection root fields __type, __schema and __typename read only the
// schema (GraphQL spec, "Introspection"; gh asks __type to detect features, issue #706).

// GitHubID is the Integration a Bot reads GitHub through: github.com, whose
// Connections are the git host's.
const GitHubID = "github.com"

// maxGraphQLTokens bounds the parse of one query: gh's longest, `gh issue view`'s, is a few hundred tokens. A
// longer one is not read and asks.
const maxGraphQLTokens = 20000

// githubAPI is the host gh and the REST and GraphQL APIs are on; githubUploads is where a release's asset is
// uploaded (the operation's own servers in the description).
const (
	githubAPI     = "api.github.com"
	githubUploads = "uploads.github.com"
)

// githubLogHosts are where GitHub sends a log's download (a redirect from
// .../logs): read off `gh api .../actions/jobs/<id>/logs` and
// `.../actions/runs/<id>/logs` on 2026-11-17. The address is signed; no token
// is added there.
var (
	githubResults = "results-receiver.actions.githubusercontent.com"
	githubBlob    = regexp.MustCompile(`^productionresultssa[0-9]{1,3}\.blob\.core\.windows\.net$`)
)

//go:embed github_operations.json
var githubOperationsFile []byte

// githubDeclaration is github_operations.json: the gh version it was generated for (agenttool.GitHubCLI's), every
// REST operation and every GraphQL root field.
type githubDeclaration struct {
	GH   string `json:"gh"`
	REST []struct {
		Method    string `json:"method"`
		Host      string `json:"host"`
		Path      string `json:"path"`
		Category  string `json:"category"`
		Operation string `json:"operation"`
	} `json:"rest"`
	GraphQL []struct {
		Type     string `json:"type"`
		Field    string `json:"field"`
		Category string `json:"category"`
	} `json:"graphql"`
}

// Operation recognizes a GraphQL request as an action by the type of an
// operation and one of its root fields (Onyx's GraphQLOp).
type Operation struct {
	Type  string
	Field string
}

// githubService is a part of GitHub as the owner reads it: the categories of the description that are it, as
// github-mcp-server groups its tools in toolsets (pkg/github/tools.go: repos and git, issues and labels,
// pull_requests, actions, code_security, secret_protection and dependabot, orgs, users and context, copilot).
type githubService struct {
	key, name, things, text string
	categories              []string
}

var githubServices = []githubService{
	{"repos", "Repositories", "repositories",
		"Repositories, their settings, branches' protection, commits, collaborators, keys, deployments and Pages",
		[]string{"repos", "branches", "commits", "git", "collaborators", "deploy-keys", "deployments", "pages",
			"metrics", "licenses", "codes-of-conduct", "gitignore"}},
	{"issues", "Issues", "issues", "Issues, their comments, labels, milestones and reactions",
		[]string{"issues", "reactions"}},
	{"pulls", "Pull requests", "pull requests", "Pull requests, their diff, files, commits, reviews and comments",
		[]string{"pulls"}},
	{"actions", "Actions and checks", "Actions and checks",
		"Workflows, runs, jobs and their logs, runners, secrets' names and variables, check runs and suites",
		[]string{"actions", "checks"}},
	{"releases", "Releases", "releases", "Releases and their assets", []string{"releases"}},
	{"search", "Search", "search results", "Search repositories, issues, pull requests, code, commits and users",
		[]string{"search"}},
	{"projects", "Projects", "projects", "Projects, their fields and items", []string{"projects", "projects-classic"}},
	{"discussions", "Discussions", "discussions", "Discussions and their comments", []string{"discussions"}},
	{"gists", "Gists", "gists", "Gists and their comments", []string{"gists"}},
	{"security", "Security", "security alerts and settings",
		"Code scanning, secret scanning, Dependabot, advisories and the dependency graph",
		[]string{"code-scanning", "code-security", "code-quality", "dependabot", "dependency-graph",
			"secret-scanning", "security-advisories", "private-registries"}},
	{"orgs", "Organizations and teams", "organizations and teams",
		"Organizations, their members, teams, settings and billing, enterprises and migrations",
		[]string{"orgs", "teams", "migrations", "interactions", "campaigns", "billing", "classroom", "credentials",
			"enterprise-admin"}},
	{"account", "Account", "the account",
		"Users, the account's keys, notifications, stars and follows, apps, codespaces, packages and sponsors",
		[]string{"users", "activity", "apps", "codespaces", "packages", "markdown", "meta", "emojis", "rate-limit",
			"sponsors"}},
	{"copilot", "Copilot and agents", "Copilot and agents", "Copilot's settings and seats, spaces and agents' tasks",
		[]string{"copilot", "copilot-spaces", "agents", "agent-automations", "agent-tasks"}},
}

// githubOther is the service of what has no category: the queries, whose only category is the root type's (meta),
// and a category no service names. Every mutation of the schema read on 2026-11-17 has its own.
var githubOther = githubService{key: "other", name: "Everything else", things: "anything else",
	text: "What GitHub's description puts in no part of the API, such as the GraphQL queries of an organization"}

// The three things a service's operations are.
const (
	githubRead   = "read"
	githubWrite  = "write"
	githubDelete = "delete"
)

// githubNamed maps chosen operations, by REST operationId or GraphQL type and field, to the actions the catalog
// named before it was generated, and to merge and auto-merge. Every other operation is its service's read, write
// or delete. The reads are the operations the hand-written routes matched where the service's read would be
// another id (a branch, a file, a commit), so that an answer the owner gave keeps applying to them.
var githubNamed = map[string]string{
	"rest users/get-authenticated": "github.user.read",
	"query viewer":                 "github.user.read",
	"query search":                 "github.search.read",

	"query repository":      "github.graphql.repo.read",
	"query repositoryOwner": "github.graphql.repo.read",
	"query node":            "github.graphql.repo.read",
	"query nodes":           "github.graphql.repo.read",

	"rest repos/list-branches":                             "github.refs.read",
	"rest repos/get-branch":                                "github.refs.read",
	"rest repos/get-branch-protection":                     "github.refs.read",
	"rest repos/get-admin-branch-protection":               "github.refs.read",
	"rest repos/get-pull-request-review-protection":        "github.refs.read",
	"rest repos/get-commit-signature-protection":           "github.refs.read",
	"rest repos/get-status-checks-protection":              "github.refs.read",
	"rest repos/get-all-status-check-contexts":             "github.refs.read",
	"rest repos/get-access-restrictions":                   "github.refs.read",
	"rest repos/get-apps-with-access-to-protected-branch":  "github.refs.read",
	"rest repos/get-teams-with-access-to-protected-branch": "github.refs.read",
	"rest repos/get-users-with-access-to-protected-branch": "github.refs.read",
	"rest git/list-matching-refs":                          "github.refs.read",
	"rest git/get-ref":                                     "github.refs.read",
	"rest repos/list-tags":                                 "github.refs.read",
	"rest repos/get-content":                               "github.contents.read",
	"rest repos/get-readme":                                "github.contents.read",
	"rest repos/get-readme-in-directory":                   "github.contents.read",
	"rest repos/list-commits":                              "github.git_data.read",
	"rest repos/list-branches-for-head-commit":             "github.git_data.read",
	"rest repos/list-comments-for-commit":                  "github.git_data.read",
	"rest repos/list-pull-requests-associated-with-commit": "github.git_data.read",
	"rest repos/get-commit":                                "github.git_data.read",
	"rest repos/compare-commits":                           "github.git_data.read",
	"rest git/get-blob":                                    "github.git_data.read",
	"rest git/get-commit":                                  "github.git_data.read",
	"rest git/get-tree":                                    "github.git_data.read",
	"rest repos/get-combined-status-for-ref":               "github.actions.read",
	"rest repos/list-commit-statuses-for-ref":              "github.actions.read",
	"rest reactions/list-for-pull-request-review-comment":  "github.pulls.read",
	"rest reactions/list-for-release":                      "github.releases.read",
	"rest issues/create":                                   "github.issues.create",
	"mutation createIssue":                                 "github.issues.create",
	"rest issues/create-comment":                           "github.comments.create",
	"mutation addComment":                                  "github.comments.create",
	"rest repos/create-or-update-file-contents":            "github.contents.write",
	"rest repos/delete-file":                               "github.contents.write",
	"rest git/create-ref":                                  "github.refs.write",
	"rest git/update-ref":                                  "github.refs.write",
	"rest git/delete-ref":                                  "github.refs.write",
	"rest pulls/create":                                    "github.pulls.create",
	"mutation createPullRequest":                           "github.pulls.create",
	"rest pulls/merge":                                     "github.pulls.merge",
	"rest pulls/merge-async":                               "github.pulls.merge",
	"mutation mergePullRequest":                            "github.pulls.merge",
	"mutation enablePullRequestAutoMerge":                  "github.pulls.automerge",
	"mutation disablePullRequestAutoMerge":                 "github.pulls.automerge",
}

// githubNamedActions are the actions githubNamed maps to that are no service's read, write or delete, each in its
// service.
var githubNamedActions = []Action{
	{ID: "github.user.read", Service: "Account", Name: "Read the connected user", Kind: Reads},
	{
		ID: "github.graphql.repo.read", Service: "Repositories", Name: "Read through gh's GraphQL queries", Kind: Reads,
		Text: "What `gh issue view`, `gh pr view`, `gh pr checks` and their lists read",
	},
	{
		ID: "github.refs.read", Service: "Repositories", Name: "Read branches and refs", Kind: Reads,
		Text: "Branches and their protection, refs and tags",
	},
	{ID: "github.contents.read", Service: "Repositories", Name: "Read file contents", Kind: Reads},
	{ID: "github.git_data.read", Service: "Repositories", Name: "Read commits and git objects", Kind: Reads},
	{
		ID: "github.schema.read", Service: "Everything else", Name: "Read the API's schema", Kind: Reads,
		Text: "GraphQL introspection, which gh asks to learn what the server supports; no account's data",
	},
	{ID: "github.issues.create", Service: "Issues", Name: "Open an issue", Kind: NeedsCare},
	{
		ID: "github.comments.create", Service: "Issues", Name: "Comment on an issue or a pull request",
		Kind: NeedsCare,
	},
	{
		ID: "github.contents.write", Service: "Repositories", Name: "Create, change or delete files", Kind: NeedsCare,
		Text: "A commit of one file, straight to a branch",
	},
	{ID: "github.refs.write", Service: "Repositories", Name: "Create, move or delete branches", Kind: NeedsCare},
	{ID: "github.pulls.create", Service: "Pull requests", Name: "Open a pull request", Kind: NeedsCare},
	{
		ID: "github.pulls.merge", Service: "Pull requests", Name: "Merge a pull request", Kind: NeedsCare,
		Text: "Merge now (`gh pr merge`); the repository's rules, such as required checks and reviews, still apply",
	},
	{
		ID: "github.pulls.automerge", Service: "Pull requests", Name: "Turn auto-merge on or off", Kind: NeedsCare,
		Text: "GitHub merges the pull request once its required checks pass (`gh pr merge --auto`, " +
			"`gh pr merge --disable-auto`)",
	},
}

// githubHandReads are the GET routes the catalog named by hand before it was generated. The description names
// every one of them more exactly; they stay for a request it does not have (an endpoint newer than the pinned
// description), so that what read before still reads, as Superset's and Lark's catalogs keep a part of the API's
// GETs for what their description lacks.
func githubHandReads() map[string][]Route {
	const repo = "/repos/{owner}/{repo}"

	at := func(paths ...string) []Route { return routes(githubAPI, []string{http.MethodGet}, paths...) }

	return map[string][]Route{
		"github.user.read":  at("/user"),
		"github.repos.read": at("/user/repos", "/orgs/{org}/repos", "/users/{user}/repos", repo),
		"github.issues.read": at(repo+"/issues", repo+"/issues/{rest...}", repo+"/labels",
			repo+"/milestones"),
		"github.pulls.read": at(repo+"/pulls", repo+"/pulls/{rest...}"),
		"github.actions.read": at(repo+"/actions/{rest...}", repo+"/commits/{ref}/check-runs",
			repo+"/commits/{ref}/check-suites", repo+"/commits/{ref}/status", repo+"/commits/{ref}/statuses",
			repo+"/check-runs/{rest...}", repo+"/check-suites/{rest...}"),
		"github.search.read": at("/search/{rest...}"),
		"github.refs.read": at(repo+"/branches", repo+"/branches/{branch...}", repo+"/git/ref/{ref...}",
			repo+"/git/refs", repo+"/git/refs/{ref...}", repo+"/git/matching-refs/{ref...}", repo+"/tags"),
		"github.contents.read": at(repo+"/contents", repo+"/contents/{path...}", repo+"/readme",
			repo+"/readme/{dir...}"),
		"github.git_data.read": at(repo+"/git/trees/{sha}", repo+"/git/blobs/{sha}", repo+"/git/commits/{sha}",
			repo+"/commits", repo+"/commits/{ref...}", repo+"/compare/{range...}"),
		"github.releases.read": at(repo+"/releases", repo+"/releases/{release...}"),
	}
}

// githubSlashParams are the parameters that may hold slashes when they end a path: a file's path, a ref, a branch,
// a range of two refs. They become "{name...}", as the hand-written routes had them.
var githubSlashParams = []string{"{path}", "{ref}", "{branch}", "{basehead}"}

// githubServiceOf is the service a category is in; one that none names is in Everything else.
func githubServiceOf(category string) githubService {
	for _, s := range githubServices {
		if slices.Contains(s.categories, category) {
			return s
		}
	}

	return githubOther
}

// githubRESTAction is the id of a REST operation's action.
func githubRESTAction(method, category, operation string) string {
	if id, ok := githubNamed["rest "+operation]; ok {
		return id
	}

	what := githubWrite

	switch method {
	case http.MethodGet:
		what = githubRead
	case http.MethodDelete:
		what = githubDelete
	}

	return "github." + githubServiceOf(category).key + "." + what
}

// githubFieldAction is the id of a GraphQL root field's action. A query reads; a mutation named delete… deletes, as
// gh's deleteIssue does; any other mutation writes.
func githubFieldAction(typ, field, category string) string {
	if id, ok := githubNamed[typ+" "+field]; ok {
		return id
	}

	what := githubRead

	if typ == string(ast.Mutation) {
		what = githubWrite
		if strings.HasPrefix(field, "delete") {
			what = githubDelete
		}
	}

	return "github." + githubServiceOf(category).key + "." + what
}

// githubTemplate is a description's path as a route's: a parameter that may hold slashes, at the end, takes them.
func githubTemplate(path string) string {
	i := strings.LastIndex(path, "/")
	if last := path[i+1:]; slices.Contains(githubSlashParams, last) {
		return path[:i+1] + strings.TrimSuffix(last, "}") + "...}"
	}

	return path
}

func readGitHubDeclaration(raw []byte) (githubDeclaration, error) {
	var d githubDeclaration

	err := json.Unmarshal(raw, &d)

	return d, err
}

// GitHubActionIDs names the action of every operation of a github_operations.json, keyed "GET api.github.com/path"
// or "mutation field": catalogsgen prints which ones a regeneration adds, removes or moves.
func GitHubActionIDs(operations []byte) (map[string]string, error) {
	d, err := readGitHubDeclaration(operations)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(d.REST)+len(d.GraphQL))

	for _, op := range d.REST {
		out[op.Method+" "+op.Host+op.Path] = githubRESTAction(op.Method, op.Category, op.Operation)
	}

	for _, f := range d.GraphQL {
		out[f.Type+" "+f.Field] = githubFieldAction(f.Type, f.Field, f.Category)
	}

	return out, nil
}

var githubCatalog = sync.OnceValue(func() Catalog {
	// The file is this package's own; one that does not read is a broken build, which the catalog's tests catch, and
	// a catalog without its operations then asks about everything.
	d, _ := readGitHubDeclaration(githubOperationsFile)

	return githubCatalogOf(d)
})

func githubCatalogOf(d githubDeclaration) Catalog {
	routesOf, operationsOf := githubHandReads(), map[string][]Operation{
		"github.schema.read": {{"query", "__type"}, {"query", "__schema"}, {"query", "__typename"}},
	}

	// The description's routes go before the hand-written ones, so that between two that name as much of a path,
	// the description's wins.
	for _, op := range d.REST {
		id := githubRESTAction(op.Method, op.Category, op.Operation)
		routesOf[id] = append([]Route{{Method: op.Method, Host: op.Host, Path: githubTemplate(op.Path)}},
			routesOf[id]...)
	}

	for _, f := range d.GraphQL {
		id := githubFieldAction(f.Type, f.Field, f.Category)
		operationsOf[id] = append(operationsOf[id], Operation{Type: f.Type, Field: f.Field})
	}

	c := Catalog{Integration: "github", Actions: []Action{{
		ID: "github.logs", Plumbing: true, Kind: Reads,
		Routes: append(routes(githubResults, []string{http.MethodGet}, "/rest/runs/{rest...}"),
			Route{Method: http.MethodGet, Path: "/actions-results/{rest...}"}),
	}}}

	for _, s := range append(slices.Clone(githubServices), githubOther) {
		actions := []Action{{
			ID: "github." + s.key + "." + githubRead, Service: s.name, Name: "Read " + s.things, Text: s.text,
			Kind: Reads,
		}}

		for _, a := range githubNamedActions {
			if a.Service == s.name {
				actions = append(actions, a)
			}
		}

		actions = append(actions,
			Action{
				ID: "github." + s.key + "." + githubWrite, Service: s.name, Name: "Create or change " + s.things,
				Kind: NeedsCare,
				Text: "Any request there that is not a GET, and any GraphQL mutation, that no action above names",
			},
			Action{
				ID: "github." + s.key + "." + githubDelete, Service: s.name, Name: "Delete " + s.things,
				Kind: NeedsCare, Text: "A DELETE request there, or a GraphQL mutation whose name starts with delete",
			},
		)

		for _, a := range actions {
			a.Routes, a.Operations = routesOf[a.ID], operationsOf[a.ID]
			if len(a.Routes)+len(a.Operations) > 0 {
				c.Actions = append(c.Actions, a)
			}
		}
	}

	c.Reaches = func(host string) bool {
		return host == githubAPI || host == githubUploads || host == githubResults || githubBlob.MatchString(host)
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
// the most care, with the others the request names beside it (With), so that the strictest of the owner's answers
// governs it (Onyx's engine: a batched request is governed by its strictest action, AllMatchedActions sorted by
// policy; graphql_parsing.py resolves fragments at the root, so that no field hides in one). A field no action
// names, or a body that is not a GraphQL request, is unlisted and asks. An introspection field governs only a
// request that asks nothing else: beside another field, that field's action names the request.
func (c Catalog) graphql(body []byte) Action {
	invocations, ok := graphqlInvocations(body)
	if !ok || len(invocations) == 0 {
		return unlisted(c.Integration)
	}

	asksMore := slices.ContainsFunc(invocations, func(op Operation) bool { return !introspection(op.Field) })

	var named []Action

	for _, op := range invocations {
		a, found := c.operation(op)
		if !found {
			return unlisted(c.Integration)
		}

		if asksMore && introspection(op.Field) {
			continue
		}

		if !slices.ContainsFunc(named, func(n Action) bool { return n.ID == a.ID }) {
			named = append(named, a)
		}
	}

	rank := map[Kind]int{Reads: 0, Changes: 1, NeedsCare: 2}

	slices.SortStableFunc(named, func(a, b Action) int { return rank[b.Kind] - rank[a.Kind] })

	governing := named[0]
	governing.With = named[1:]

	return governing
}

// operation is the action that names a GraphQL operation.
func (c Catalog) operation(op Operation) (Action, bool) {
	for _, a := range c.Actions {
		if slices.Contains(a.Operations, op) {
			return a, true
		}
	}

	return Action{}, false
}

// introspection is whether a root field is one of GraphQL's introspection fields (__type, __schema, __typename).
func introspection(field string) bool { return strings.HasPrefix(field, "__") }

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
