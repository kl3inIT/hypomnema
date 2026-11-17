package serviceproxy

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// GitLab through the proxy (issue #740, step 2 of #713; what #705 asked): a GitLab host's Connection, the token
// workers are given per Dispatch, used by a Bot's pi with the `glab` command. A GitLab host is gitlab.com or a
// company's server the owner declared with the kind gitlab (internal/shared/githost), each an Integration of its own
// named by its host; the catalog's routes carry no host, so that the Connection's own host is the one reached, as
// Superset's are.
//
// The catalog is generated from the route table of client-go (gitlab.com/gitlab-org/api/client-go, Apache-2.0,
// notice in third_party/gitlab/NOTICE), the Go client glab is built on, at the version glab pins: every route is
// registered with route("…") so that "Routes and MatchRoute describe the whole client" (route.go), and each call
// names its method beside the route (withMethod, withPath). gitlab_operations.json keeps each method and route;
// catalogsgen writes it from the module catalog_sources.json pins. GitLab's own OpenAPI description is not used: it
// lacks notes, discussions, labels and /user, which glab sends.
//
// A request is one of the declaration's when its method and path match a route under /api/v4/. Its action is a
// service, then read, write or delete, as GitHub's: the service is the resource the path names first after a
// project, a group or a user (client-go's first resource segment), read off the request's own path, so that a route
// whose resource is a parameter (projects/:id/:type/:iid/award_emoji) is the resource the request names; a GET
// reads, a DELETE deletes, anything else writes (github-mcp-server's ReadOnlyHint, zereight's GitLab server, which
// sends only GET in read-only mode). Reads run; the rest asks. Merging a merge request and turning auto-merge on or
// off are actions of their own (t3code's merge, enable-auto-merge and disable-auto-merge): on GitLab both are the
// same request, PUT …/merge_requests/:iid/merge, or adding the merge request to a merge train, told apart by their
// auto-merge flags (gitlabMergeFlags, autoMerge), and auto-merge is canceled by POST …/cancel_auto_merge or
// …/cancel_merge_when_pipeline_succeeds. `glab mr merge` asks for auto-merge by default when the merge request has a
// pipeline (mr_merge.go). An auto-merge is sent with all its flags set, which every version of GitLab reads
// (gitlabAutoMergeBody). A GraphQL query on /api/graphql reads (glab sends only queries: its work items); a mutation,
// or a body GitLab could read as another request than the proxy does (gitlabGraphQLBody), asks as unlisted.

//go:embed gitlab_operations.json
var gitlabOperationsFile []byte

// gitlabDeclaration is gitlab_operations.json: the glab version it was generated for (agenttool.GitLabCLI's), the
// client-go version glab pins, and every operation of client-go.
type gitlabDeclaration struct {
	Glab       string `json:"glab"`
	ClientGo   string `json:"client_go"`
	Operations []struct {
		Method string `json:"method"`
		Route  string `json:"route"`
	} `json:"operations"`
}

// The paths of GitLab's REST and GraphQL APIs (client-go gitlab.go apiVersionPath, graphql.go GraphQLAPIEndpoint).
const (
	gitlabREST    = "/api/v4/"
	gitlabGraphQL = "/api/graphql"
)

// gitlabService is a part of GitLab as the owner reads it: the resources of client-go's routes that are it, by the
// same services as GitHub's (github-mcp-server's toolsets). A resource is the segment after a scope's id
// (projects/:id/<resource>); one at the root is written "/<resource>", and one of a scope only "<scope>/<resource>".
type gitlabService struct {
	key, name, things, text string
	resources               []string
}

var gitlabServices = []gitlabService{
	{"repos", "Repositories", "projects and repositories",
		"Projects and their repositories: files, branches, tags, commits, protection, forks, mirrors, wikis, " +
			"snippets, webhooks and settings",
		[]string{"projects", "/projects", "repository", "protected_branches", "protected_tags", "push_rule", "fork",
			"forks", "star", "unstar", "starrers", "languages", "archive", "unarchive", "transfer", "restore",
			"share", "mirror", "remote_mirrors", "housekeeping", "export", "import", "/import", "uploads", "avatar",
			"badges", "wikis", "snippets", "/snippets", "pages", "/pages", "statistics", "templates", "storage",
			"deploy_keys", "deploy_tokens", "hooks", "integrations", "services", "repository_storage_moves",
			"/project_aliases"}},
	{"issues", "Issues", "issues", "Issues, their notes and discussions, labels, milestones, boards and epics",
		[]string{"issues", "/issues", "issues_statistics", "/issues_statistics", "labels", "milestones", "boards",
			"epics", "epic_boards", "iterations"}},
	{"merge_requests", "Merge requests", "merge requests",
		"Merge requests, their diffs, commits, notes, discussions, approvals and merge trains",
		[]string{"merge_requests", "/merge_requests", "merge_trains", "approval_rules", "approvals",
			"merge_request_approval_setting", "external_status_checks"}},
	{"ci", "CI/CD", "CI/CD",
		"Pipelines, jobs and their logs and artifacts, schedules, runners, variables, environments and deployments",
		[]string{"pipelines", "/pipelines", "pipeline", "pipeline_schedules", "jobs", "/job", "runners", "/runners",
			"trigger", "triggers", "variables", "environments", "deployments", "freeze_periods", "resource_groups",
			"protected_environments", "secure_files", "artifacts", "ci", "statuses", "job_token_scope",
			"cluster_agents", "clusters", "terraform", "feature_flags", "feature_flags_user_lists", "dora"}},
	{"releases", "Releases and packages", "releases and packages",
		"Releases and their links, packages and the container registry",
		[]string{"releases", "packages", "registry", "/registry", "dependency_proxy"}},
	{"search", "Search", "search results", "Search projects, issues, merge requests, code and users",
		[]string{"search", "/search", "/orbit"}},
	{"security", "Security", "security findings and settings",
		"Vulnerabilities, dependencies, security settings, attestations, audit events and errors",
		[]string{"vulnerabilities", "dependencies", "dependency_firewall", "security_settings",
			"/dependency_list_exports", "attestations", "audit_events", "/audit_events", "error_tracking",
			"alert_management_alerts"}},
	{"orgs", "Groups and members", "groups and members",
		"Groups and subgroups, their settings, members, invitations, access requests, access tokens and links",
		[]string{"groups", "/groups", "members", "access_requests", "invitations", "invited_groups",
			"shared_projects", "subgroups", "descendant_groups", "ldap_group_links", "ldap_sync", "saml_group_links",
			"saml_users", "scim", "billable_members", "enterprise_users", "provisioned_users", "service_accounts",
			"/service_accounts", "access_tokens", "member_roles", "/member_roles", "transfer_locations",
			"placeholder_reassignments", "ssh_certificates", "manage", "/namespaces", "/bulk_imports", "users",
			"groups/hooks", "groups/badges", "groups/integrations", "groups/avatar", "groups/export",
			"groups/export_relations", "groups/archive", "groups/unarchive", "groups/restore", "groups/transfer",
			"groups/share", "groups/uploads", "groups/push_rule"}},
	{"account", "Account", "the account",
		"The connected user and other users, keys and tokens, emails, notification settings, to-do items and events",
		[]string{"/user", "/users", "/keys", "/personal_access_tokens", "/notification_settings",
			"notification_settings", "events", "/events", "/todos", "emails", "gpg_keys", "keys", "status",
			"memberships", "contributed_projects", "starred_projects", "project_deploy_keys", "associations_count",
			"personal_access_tokens", "/markdown", "/version", "/metadata", "/avatar", "/templates"}},
	{"admin", "Administration", "the instance",
		"The instance's settings, users' state, system hooks, Geo, licenses, broadcast messages and storage moves",
		[]string{"/admin", "/application", "/applications", "/broadcast_messages", "/geo_nodes", "/geo_sites",
			"/features", "/license", "/usage_data", "/hooks", "/sidekiq", "/runner_controllers", "/topics", "/analytics",
			"/deploy_keys", "/deploy_tokens", "/test_only", "/group_repository_storage_moves",
			"/project_repository_storage_moves", "/snippet_repository_storage_moves", "custom_attributes",
			"activate", "approve", "ban", "block", "deactivate", "disable_two_factor", "reject", "unban", "unblock",
			"identities", "impersonation_tokens"}},
}

// gitlabOther is the service of what no service names: a route whose resource is a parameter, read without a
// request's path.
var gitlabOther = gitlabService{key: "other", name: "Everything else", things: "anything else",
	text: "A request whose part of GitLab no part above names"}

// gitlabScopes are the root segments whose next segment is an id, so that the one after names the resource.
var gitlabScopes = []string{"projects", "groups", "users"}

// The named actions: merge, auto-merge and GraphQL's queries.
const (
	gitlabMerge     = "gitlab.merge_requests.merge"
	gitlabAutoMerge = "gitlab.merge_requests.automerge"
	gitlabGraphRead = "gitlab.graphql.read"
)

// gitlabNamedActions are the actions that are no service's read, write or delete, each in its service.
var gitlabNamedActions = []Action{
	{
		ID: gitlabMerge, Service: "Merge requests", Name: "Merge a merge request", Kind: NeedsCare,
		Text: "Merge now (`glab mr merge --auto-merge=false`), or add it to the merge train, which merges it once " +
			"the train's pipeline passes; the project's rules, such as pipelines and approvals, still apply",
	},
	{
		ID: gitlabAutoMerge, Service: "Merge requests", Name: "Turn auto-merge on or off", Kind: NeedsCare,
		Text: "GitLab merges the merge request, or adds it to the merge train, once its pipeline succeeds, at " +
			"once when it already has (`glab mr merge`, which asks for auto-merge when the merge request has a " +
			"pipeline), or no longer does",
	},
	{
		ID: gitlabGraphRead, Service: "Everything else", Name: "Read through GraphQL queries", Kind: Reads,
		Text: "What glab's work items commands read",
	},
}

// gitlabRoute is a declared operation, its route split into segments.
type gitlabRoute struct {
	method   string
	segments []string
}

// gitlabTable is the declaration read once, and the actions every GitLab host's catalog lists.
type gitlabTable struct {
	routes  []gitlabRoute
	actions []Action
}

var gitlabTableOf = sync.OnceValue(func() gitlabTable {
	// The file is this package's own; one that does not read is a broken build, which the catalog's tests catch, and
	// a catalog without its operations then asks about everything.
	d, _ := readGitLabDeclaration(gitlabOperationsFile)

	return gitlabTableFrom(d)
})

func readGitLabDeclaration(raw []byte) (gitlabDeclaration, error) {
	var d gitlabDeclaration

	err := json.Unmarshal(raw, &d)

	return d, err
}

func gitlabTableFrom(d gitlabDeclaration) gitlabTable {
	t := gitlabTable{routes: make([]gitlabRoute, 0, len(d.Operations))}

	for _, op := range d.Operations {
		t.routes = append(t.routes, gitlabRoute{method: op.Method, segments: routeSegments(op.Route)})
	}

	for _, s := range append(slices.Clone(gitlabServices), gitlabOther) {
		t.actions = append(t.actions, Action{
			ID: "gitlab." + s.key + "." + githubRead, Service: s.name, Name: "Read " + s.things, Text: s.text,
			Kind: Reads,
		})

		for _, a := range gitlabNamedActions {
			if a.Service == s.name {
				t.actions = append(t.actions, a)
			}
		}

		t.actions = append(t.actions,
			Action{
				ID: "gitlab." + s.key + "." + githubWrite, Service: s.name, Name: "Create or change " + s.things,
				Kind: NeedsCare, Text: "Any request there that is not a GET and that no action above names",
			},
			Action{
				ID: "gitlab." + s.key + "." + githubDelete, Service: s.name, Name: "Delete " + s.things,
				Kind: NeedsCare, Text: "A DELETE request there",
			},
		)
	}

	return t
}

// GitLabIntegration is the name of GitLab's catalog: its actions are gitlab.<service>.<read|write|delete>, whichever
// GitLab host's Connection asks.
const GitLabIntegration = "gitlab"

// gitlabCatalog is the catalog of one GitLab host: it reaches that host only. A project's id is its path with its
// slashes encoded (group%2Fproject), as glab sends it, and a file's path is too, so an encoded slash stays in its
// segment there (GitLab's "URL-encoded path of the project"; issue #617 for Search Console's).
func gitlabCatalog(host string) Catalog {
	t := gitlabTableOf()

	c := Catalog{Integration: GitLabIntegration, Actions: t.actions, SlashInSegment: []string{host}}
	c.Reaches = func(h string) bool { return h == host }
	c.Rewrite = gitlabAutoMergeBody
	c.Name = func(req *http.Request, body []byte, _ string) Action {
		return t.actionOf(req.Method, req.URL.EscapedPath(), req.URL.RawQuery, req.Header.Get("Content-Type"), body)
	}

	return c
}

// CatalogIn is CatalogOf, with the git hosts: a host whose kind is gitlab, gitlab.com or one the owner declared,
// has GitLab's catalog (issue #740). kinds says a host's kind (orchestration's KindOfHost).
func CatalogIn(ctx context.Context, kinds HostKinds, integration string) (Catalog, bool) {
	if c, ok := CatalogOf(integration); ok {
		return c, true
	}

	if !githost.ValidHost(integration) || kinds == nil {
		return Catalog{}, false
	}

	if kind, ok := kinds.KindOfHost(ctx, integration); ok && kind == githost.GitLab {
		return gitlabCatalog(integration), true
	}

	return Catalog{}, false
}

// HostKinds says the kind of a git host, and false for a host that is not known.
type HostKinds interface {
	KindOfHost(ctx context.Context, host string) (githost.Kind, bool)
}

// actionOf is the action of a request to a GitLab host.
func (t gitlabTable) actionOf(method, path, query, contentType string, body []byte) Action {
	if path == gitlabGraphQL {
		if method == http.MethodPost && query == "" && gitlabGraphQLBody(contentType, body) {
			return t.graphql(body)
		}

		return unlisted(GitLabIntegration)
	}

	segments, ok := gitlabSegments(path)
	if !ok {
		return unlisted(GitLabIntegration)
	}

	if !slices.ContainsFunc(t.routes, func(r gitlabRoute) bool { return r.method == method && r.matches(segments) }) {
		return unlisted(GitLabIntegration)
	}

	ids := gitlabActionIDs(method, segments, query, contentType, body)
	if len(ids) == 0 {
		return unlisted(GitLabIntegration)
	}

	governing := t.action(ids[0])
	for _, id := range ids[1:] {
		governing.With = append(governing.With, t.action(id))
	}

	return governing
}

// gitlabSegments are the segments of a path under /api/v4/, each read as GitLab reads it, decoded: merg%65 is
// merge. A segment's encoded slash stays in it.
func gitlabSegments(path string) ([]string, bool) {
	rest, ok := strings.CutPrefix(path, gitlabREST)
	if !ok {
		return nil, false
	}

	segments := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	for i, s := range segments {
		decoded, err := url.PathUnescape(s)
		if err != nil {
			return nil, false
		}

		segments[i] = decoded
	}

	return segments, true
}

// action is a listed action by its id.
func (t gitlabTable) action(id string) Action {
	for _, a := range t.actions {
		if a.ID == id {
			return a
		}
	}

	return unlisted(GitLabIntegration)
}

// gitlabGraphQLKeys are the keys of a GraphQL request GitLab reads (app/controllers/graphql_controller.rb at
// v18.5.0, permitted_params and permitted_multiplex_params), and that glab sends (internal/commands/api/http.go,
// groupGraphQLVariables).
var gitlabGraphQLKeys = []string{"query", "operationName", "variables"}

// gitlabGraphQLBody is whether a GraphQL request is read by GitLab only as the proxy reads it: a JSON body, with no
// parameter in the URL, which Rails would merge into its params; one request, or an array of them (GitLab's
// multiplex, params[:_json]); each an object with no key but those GitLab reads, none twice and none that differs
// from another only by case (graphqlObject). Anything else asks as unlisted: a "_json" key in an object would be run
// as a multiplex in place of the query the proxy reads.
func gitlabGraphQLBody(contentType string, body []byte) bool {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil || media != "application/json" {
		return false
	}

	trimmed := bytes.TrimSpace(body)

	// A fresh slice: decoding into one that held trimmed would write over the body itself.
	var documents []json.RawMessage
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &documents); err != nil {
			return false
		}
	} else {
		documents = []json.RawMessage{trimmed}
	}

	for _, raw := range documents {
		fields, ok := graphqlObject(raw)
		if !ok {
			return false
		}

		for key := range fields {
			if !slices.Contains(gitlabGraphQLKeys, key) {
				return false
			}
		}
	}

	return true
}

// graphql is a GraphQL request's action: a read when every operation in it is a query.
func (t gitlabTable) graphql(body []byte) Action {
	invocations, ok := graphqlInvocations(body)
	if !ok || len(invocations) == 0 {
		return unlisted(GitLabIntegration)
	}

	for _, op := range invocations {
		if op.Type != "query" {
			return unlisted(GitLabIntegration)
		}
	}

	return t.action(gitlabGraphRead)
}

// matches is whether a path's segments are the route's: a fmt verb is a parameter that matches any segment, one
// in a segment ("archive%s") matches what starts with the segment's text before it, and the rest is literal.
func (r gitlabRoute) matches(segments []string) bool {
	if len(segments) != len(r.segments) {
		return false
	}

	for i, want := range r.segments {
		got := segments[i]

		switch {
		case got == "":
			return false
		case want == "%s" || want == "%d" || want == "%v":
		case strings.Contains(want, "%"):
			prefix, _, _ := strings.Cut(want, "%")
			if !strings.HasPrefix(got, prefix) {
				return false
			}
		case want != got:
			return false
		}
	}

	return true
}

// gitlabActionIDs are the ids of a request's actions, from its method and its path's segments (under /api/v4/):
// the first names it, and a request that may do more than one thing names the others too (a merge that may also turn
// auto-merge on), so that the strictest of the owner's answers governs it (Action.With). None is an unlisted request.
func gitlabActionIDs(method string, segments []string, query, contentType string, body []byte) []string {
	if flags := gitlabMergeFlags(method, segments); flags != nil {
		switch mergeIntentOf(flags, query, contentType, body) {
		case mergeNow:
			return []string{gitlabMerge}
		case mergeAuto:
			return []string{gitlabAutoMerge}
		case mergeEither:
			return []string{gitlabAutoMerge, gitlabMerge}
		default:
			return nil
		}
	}

	if len(segments) == 5 && segments[0] == "projects" && segments[2] == "merge_requests" &&
		method == http.MethodPost &&
		(segments[4] == "cancel_auto_merge" || segments[4] == "cancel_merge_when_pipeline_succeeds") {
		return []string{gitlabAutoMerge}
	}

	what := githubWrite

	switch method {
	case http.MethodGet, http.MethodHead:
		what = githubRead
	case http.MethodDelete:
		what = githubDelete
	}

	return []string{"gitlab." + gitlabServiceOf(segments).key + "." + what}
}

// gitlabMergeFlags are the parameters that ask for auto-merge on a request that merges a merge request, and nil for
// a request that does not. Two requests merge: accepting it, PUT projects/:id/merge_requests/:iid/merge
// (lib/api/merge_requests.rb), and adding it to a merge train, POST projects/:id/merge_trains/merge_requests/:iid,
// which GitLab's "Merge" does in a project with merge trains and which merges once the train's pipeline passes
// (ee/lib/api/merge_trains.rb, ee/app/services/merge_trains/add_merge_request_service.rb: STRATEGY_MERGE_TRAIN, or
// STRATEGY_ADD_TO_MERGE_TRAIN_WHEN_CHECKS_PASS when asked). Each has a deprecated flag and auto_merge.
func gitlabMergeFlags(method string, segments []string) []string {
	switch {
	case method == http.MethodPut && len(segments) == 5 && segments[0] == "projects" &&
		segments[2] == "merge_requests" && segments[4] == "merge":
		return []string{"auto_merge", "merge_when_pipeline_succeeds"}
	case method == http.MethodPost && len(segments) == 5 && segments[0] == "projects" &&
		segments[2] == "merge_trains" && segments[3] == "merge_requests":
		return []string{"auto_merge", "when_pipeline_succeeds"}
	}

	return nil
}

// mergeIntent is what a request that merges asks for, as far as the proxy can tell.
type mergeIntent int

const (
	// mergeUnread is a request whose flags cannot be read for certain: it asks as unlisted.
	mergeUnread mergeIntent = iota
	// mergeNow names no auto-merge flag, or names each plainly false.
	mergeNow
	// mergeAuto names its flags only in a JSON body, each plainly true: it is sent on with all of them set.
	mergeAuto
	// mergeEither is any other request with a flag that is not plainly false (in the query, in a form, a flag true
	// beside one false, a value such as 1): GitLab may turn auto-merge on, or merge at once, whichever its version
	// reads. It needs both answers.
	mergeEither
)

// mergeIntentOf reads the auto-merge flags of a request that merges, wherever GitLab may read them: the query, a
// JSON body or a form body (Grape merges all three into params), and fails closed (review 2 of #777): a flag that is
// not plainly false anywhere may turn auto-merge on, and a body that cannot be read is no request the proxy names.
//
// Which flag a GitLab reads depends on its version: 17.10 reads only merge_when_pipeline_succeeds and merges at
// once, pipeline running or not, when it is absent (lib/api/merge_requests.rb at v17.10.0, lines 756-781), while
// 17.11 and later read either, ORed (v18.5.0, lines 767-792), as the merge trains' API reads when_pipeline_succeeds
// or auto_merge. glab sends auto_merge alone in JSON (mr_merge.go, mergeOpts.AutoMerge). So a plain JSON auto-merge
// is sent on with every flag of its pair set to true (gitlabAutoMergeBody), which every version reads as
// auto-merge; any other form of it needs both the merge's and the auto-merge's answer.
func mergeIntentOf(flags []string, query, contentType string, body []byte) mergeIntent {
	q, err := url.ParseQuery(query)
	if err != nil {
		return mergeUnread
	}

	var inQuery []string
	for _, name := range flags {
		inQuery = append(inQuery, q[name]...)
	}

	var (
		inBody   []string
		fromJSON = true
	)

	if len(bytes.TrimSpace(body)) > 0 {
		media, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return mergeUnread
		}

		switch media {
		case "application/json":
			fields, ok := jsonObject(contentType, body)
			if !ok {
				return mergeUnread
			}

			for _, name := range flags {
				if raw, present := fields[name]; present {
					inBody = append(inBody, jsonFlag(raw))
				}
			}
		case "application/x-www-form-urlencoded":
			form, err := url.ParseQuery(string(body))
			if err != nil {
				return mergeUnread
			}

			fromJSON = false

			for _, name := range flags {
				inBody = append(inBody, form[name]...)
			}
		default:
			return mergeUnread
		}
	}

	all := append(slices.Clone(inQuery), inBody...)
	if !slices.ContainsFunc(all, func(v string) bool { return v != "false" }) {
		return mergeNow
	}

	if fromJSON && len(inQuery) == 0 && !slices.ContainsFunc(inBody, func(v string) bool { return v != "true" }) {
		return mergeAuto
	}

	return mergeEither
}

// jsonFlag is a JSON flag's value as a query would carry it: "true" and "false" for a boolean or a string of
// either, and the raw JSON otherwise, which is neither.
func jsonFlag(raw json.RawMessage) string {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b)
	}

	var s string
	if json.Unmarshal(raw, &s) == nil && (s == "true" || s == "false") {
		return s
	}

	return string(raw)
}

// jsonObject reads a JSON object body; an empty body is an empty object.
func jsonObject(contentType string, body []byte) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]json.RawMessage{}, true
	}

	media, _, err := mime.ParseMediaType(contentType)
	if err != nil || media != "application/json" {
		return nil, false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, false
	}

	return fields, true
}

// gitlabAutoMergeBody is the body an auto-merge is sent on with: every flag of its pair set to true, so that the
// host reads it as auto-merge whatever its version (mergeIntentOf). Any other request is sent as it came.
func gitlabAutoMergeBody(req *http.Request, body []byte, action Action) []byte {
	// A request that needs the merge's answer too (mergeEither) is sent as it came: it was asked as what it is.
	if action.ID != gitlabAutoMerge || len(action.With) > 0 {
		return body
	}

	segments, ok := gitlabSegments(req.URL.EscapedPath())
	if !ok {
		return body
	}

	flags := gitlabMergeFlags(req.Method, segments)
	if flags == nil {
		return body
	}

	fields, ok := jsonObject(req.Header.Get("Content-Type"), body)
	if !ok {
		return body
	}

	for _, name := range flags {
		fields[name] = json.RawMessage("true")
	}

	written, err := json.Marshal(fields)
	if err != nil {
		return body
	}

	return written
}

// gitlabServiceOf is the service of a path: its resource's, else its scope's, else Everything else.
func gitlabServiceOf(segments []string) gitlabService {
	var keys []string

	if len(segments) >= 3 && slices.Contains(gitlabScopes, segments[0]) {
		keys = []string{segments[0] + "/" + segments[2], segments[2], "/" + segments[0]}
	} else {
		keys = []string{"/" + segments[0], segments[0]}
	}

	for _, key := range keys {
		for _, s := range gitlabServices {
			if slices.Contains(s.resources, key) {
				return s
			}
		}
	}

	return gitlabOther
}

// GitLabActionIDs names the action of every operation of a gitlab_operations.json, keyed "METHOD route": catalogsgen
// prints which ones a regeneration adds, removes or moves. A route's parameters are read as a parameter's value.
func GitLabActionIDs(operations []byte) (map[string]string, error) {
	d, err := readGitLabDeclaration(operations)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(d.Operations))

	for _, op := range d.Operations {
		out[op.Method+" "+op.Route] = gitlabActionIDs(op.Method, gitlabSample(op.Route), "", "", nil)[0]
	}

	return out, nil
}

// routeSegments splits a route into its segments; a few of client-go's are written with a leading slash
// (/sidekiq/job_stats).
func routeSegments(route string) []string { return strings.Split(strings.Trim(route, "/"), "/") }

// gitlabSample is a path a route matches: each parameter is "1".
func gitlabSample(route string) []string {
	segments := routeSegments(route)

	for i, s := range segments {
		if strings.Contains(s, "%") {
			prefix, _, _ := strings.Cut(s, "%")
			segments[i] = prefix + "1"
		}
	}

	return segments
}
