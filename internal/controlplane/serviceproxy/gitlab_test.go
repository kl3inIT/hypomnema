package serviceproxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// gitlabHost is a company's GitLab server, declared by the owner.
const gitlabHost = "git.example.com"

// kinds says the kind of the hosts it names.
type kinds map[string]githost.Kind

func (k kinds) KindOfHost(_ context.Context, host string) (githost.Kind, bool) {
	kind, ok := k[host]

	return kind, ok
}

func gitlabCatalog(t *testing.T) serviceproxy.Catalog {
	t.Helper()

	catalog, ok := serviceproxy.CatalogIn(t.Context(), kinds{gitlabHost: githost.GitLab}, gitlabHost)
	require.True(t, ok)

	return catalog
}

// gitlabDeclaration is the catalog's embedded declaration, read from the package's own file.
type gitlabDeclaration struct {
	Glab       string `json:"glab"`
	ClientGo   string `json:"client_go"`
	Operations []struct {
		Method, Route string
	} `json:"operations"`
}

func readGitLabDeclaration(t *testing.T) gitlabDeclaration {
	t.Helper()

	raw, err := os.ReadFile("gitlab_operations.json")
	require.NoError(t, err)

	var d gitlabDeclaration
	require.NoError(t, json.Unmarshal(raw, &d))
	require.NotEmpty(t, d.Operations)

	return d
}

// gitlabPath fills a route's parameters, so that the path is one a request could have: a parameter is "1", one in
// a segment ("archive%s") is ".zip".
func gitlabPath(route string) string {
	segments := strings.Split(strings.Trim(route, "/"), "/")

	for i, s := range segments {
		switch {
		case s == "%s" || s == "%d" || s == "%v":
			segments[i] = "1"
		case strings.Contains(s, "%"):
			prefix, _, _ := strings.Cut(s, "%")
			segments[i] = prefix + ".zip"
		}
	}

	return "/api/v4/" + strings.Join(segments, "/")
}

// gitlabOf is the action of a request to the GitLab host.
func gitlabOf(t *testing.T, catalog serviceproxy.Catalog, method, target, contentType, body string,
) serviceproxy.Action {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	require.NoError(t, err)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	return catalog.Of(req, []byte(body), gitlabHost)
}

// Scenario: the catalog has not fallen behind client-go's route table (#740, decision 9a of #713). Every operation
// of the embedded declaration is an action, never gitlab.unlisted; a GET or a HEAD is a read, anything else is not.
// Every resource a route names is in a part of GitLab the owner reads: only a route whose resource is a parameter,
// told by the request's own path, falls to Everything else here.
func TestEveryOperationOfGitLabsDeclarationIsANamedAction(t *testing.T) {
	t.Parallel()

	catalog := gitlabCatalog(t)

	for _, op := range readGitLabDeclaration(t).Operations {
		a := gitlabOf(t, catalog, op.Method, gitlabPath(op.Route), "", "")
		_, listed := catalog.Action(a.ID)

		if !assert.True(t, listed, "%s %s is %s", op.Method, op.Route, a.ID) {
			continue
		}

		read := op.Method == http.MethodGet || op.Method == http.MethodHead
		assert.Equal(t, read, a.Kind == serviceproxy.Reads, "%s %s is %s", op.Method, op.Route, a.ID)

		segments := strings.Split(strings.Trim(op.Route, "/"), "/")
		resourceIsAParameter := strings.HasPrefix(segments[0], "%") ||
			(len(segments) > 2 && strings.HasPrefix(segments[2], "%"))

		if !resourceIsAParameter {
			assert.NotContains(t, a.ID, "gitlab.other.", "the resource of %s is in no part of GitLab", op.Route)
		}
	}
}

// Scenario: the catalog is generated for the glab agenttool pins, at the client-go version glab pins (#740, decision
// 9b of #713). Bumping glab fails here until `make catalogs-generate SERVICE=gitlab` has been run for the new
// version.
func TestGitLabsCatalogIsForTheGlabAgenttoolPins(t *testing.T) {
	t.Parallel()

	want := agenttool.GitLabCLI().Version
	d := readGitLabDeclaration(t)

	assert.Equal(t, want, d.Glab, "gitlab_operations.json")

	raw, err := os.ReadFile("catalog_sources.json")
	require.NoError(t, err)

	var sources struct {
		GitLab struct {
			CLI struct {
				Version string `json:"version"`
			} `json:"cli"`
			Client struct {
				Version string `json:"version"`
			} `json:"client"`
		} `json:"gitlab"`
	}

	require.NoError(t, json.Unmarshal(raw, &sources))
	assert.Equal(t, want, sources.GitLab.CLI.Version, "catalog_sources.json")
	assert.Equal(t, d.ClientGo, sources.GitLab.Client.Version, "the declaration is of the client-go pinned")
}

// Scenario: merging a merge request and turning auto-merge on or off are actions of their own (#740, decision 8 of
// #713), as glab sends them: `glab mr merge` is PUT …/merge, with auto_merge true when the merge request has a
// pipeline; auto-merge is canceled by its own POST. Adding a merge request to a merge train merges it too, now or,
// when asked, once its checks pass (review of #777). Auto-merge is read only from a JSON body, where every version of
// GitLab is sent it the same way (TestAnAutoMergeIsSentAsEveryGitLabReadsIt); a body that cannot be read, a flag in
// the query or a form, or one that is not plainly true, is a merge. Both ask until the owner says otherwise. A note
// on a merge request is a write of merge requests, its deletion a delete; a segment is read decoded, and a project's
// encoded path stays one segment. A GraphQL query reads; a mutation, a body GitLab could read as another request
// (a key twice or by another case, a multiplex in "_json", a query in the URL, a form), or a path client-go does not
// call, asks as unlisted.
func TestGlabsMergeAndAutoMergeAreActionsOfTheirOwn(t *testing.T) {
	t.Parallel()

	catalog := gitlabCatalog(t)

	for _, id := range []string{"gitlab.merge_requests.merge", "gitlab.merge_requests.automerge"} {
		a, listed := catalog.Action(id)
		require.True(t, listed, id)
		assert.Equal(t, orchestration.PolicyAsk, a.Default(), id)
	}

	const (
		mr      = "/api/v4/projects/group%2Fproject/merge_requests/7"
		train   = "/api/v4/projects/group%2Fproject/merge_trains/merge_requests/7"
		graphql = "/api/graphql"
		json    = "application/json"
		form    = "application/x-www-form-urlencoded"
	)

	cases := []struct {
		name, method, target, contentType, body, action string
	}{
		{
			name: "auto_merge true in JSON", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"auto_merge":true,"sha":"abc"}`, action: "gitlab.merge_requests.automerge",
		},
		{
			name: "merge_when_pipeline_succeeds true in JSON", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"merge_when_pipeline_succeeds":true}`, action: "gitlab.merge_requests.automerge",
		},
		{
			name: "auto_merge as the string true", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"auto_merge":"true"}`, action: "gitlab.merge_requests.automerge",
		},
		{
			name: "auto_merge in the query is a merge", method: http.MethodPut, target: mr + "/merge?auto_merge=true", contentType: "",
			body: "", action: "gitlab.merge_requests.merge",
		},
		{
			name: "auto_merge in a form is a merge", method: http.MethodPut, target: mr + "/merge", contentType: form,
			body: "auto_merge=true", action: "gitlab.merge_requests.merge",
		},
		{
			name: "no flag is a merge", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"squash":true}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "no body is a merge", method: http.MethodPut, target: mr + "/merge", contentType: "",
			body: "", action: "gitlab.merge_requests.merge",
		},
		{
			name: "auto_merge false", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"auto_merge":false}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "one flag true, the other false", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"auto_merge":true,"merge_when_pipeline_succeeds":false}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "auto_merge 1 is not plainly true", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"auto_merge":1}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "a body that cannot be read", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"auto_merge":tru`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "a body of another type", method: http.MethodPut, target: mr + "/merge", contentType: "text/plain",
			body: "auto_merge=true", action: "gitlab.merge_requests.merge",
		},
		{
			name: "a segment read decoded", method: http.MethodPut, target: mr + "/merg%65", contentType: json,
			body: `{}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "only a differently cased flag", method: http.MethodPut, target: mr + "/merge", contentType: json,
			body: `{"Auto_Merge":true}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "cancel auto-merge", method: http.MethodPost, target: mr + "/cancel_auto_merge", contentType: "",
			body: "", action: "gitlab.merge_requests.automerge",
		},
		{
			name: "cancel merge when pipeline succeeds", method: http.MethodPost, target: mr + "/cancel_merge_when_pipeline_succeeds", contentType: "",
			body: "", action: "gitlab.merge_requests.automerge",
		},
		{
			name: "add to a merge train", method: http.MethodPost, target: train, contentType: json,
			body: `{}`, action: "gitlab.merge_requests.merge",
		},
		{
			name: "add to a merge train with no body", method: http.MethodPost, target: train, contentType: "",
			body: "", action: "gitlab.merge_requests.merge",
		},
		{
			name: "add to a merge train when checks pass", method: http.MethodPost, target: train, contentType: json,
			body: `{"when_pipeline_succeeds":true}`, action: "gitlab.merge_requests.automerge",
		},
		{
			name: "add to a merge train with auto_merge", method: http.MethodPost, target: train, contentType: json,
			body: `{"auto_merge":true}`, action: "gitlab.merge_requests.automerge",
		},
		{
			name: "merge train flag in the query is a merge", method: http.MethodPost, target: train + "?auto_merge=true", contentType: "",
			body: "", action: "gitlab.merge_requests.merge",
		},
		{
			name: "a note on a merge request", method: http.MethodPost, target: mr + "/notes", contentType: json,
			body: `{"body":"Looking"}`, action: "gitlab.merge_requests.write",
		},
		{
			name: "a note deleted", method: http.MethodDelete, target: mr + "/notes/3", contentType: "",
			body: "", action: "gitlab.merge_requests.delete",
		},
		{
			name: "an emoji on a note", method: http.MethodPost, target: mr + "/notes/3/award_emoji", contentType: json,
			body: `{"name":"thumbsup"}`, action: "gitlab.merge_requests.write",
		},
		{
			name: "a merge request read", method: http.MethodGet, target: mr, contentType: "",
			body: "", action: "gitlab.merge_requests.read",
		},
		{
			name: "merge requests listed", method: http.MethodGet, target: "/api/v4/projects/group%2Fproject/merge_requests?state=opened", contentType: "",
			body: "", action: "gitlab.merge_requests.read",
		},
		{
			name: "a note on an issue", method: http.MethodPost, target: "/api/v4/projects/1/issues/4/notes", contentType: json,
			body: `{"body":"x"}`, action: "gitlab.issues.write",
		},
		{
			name: "a pipeline's jobs", method: http.MethodGet, target: "/api/v4/projects/1/pipelines/9/jobs", contentType: "",
			body: "", action: "gitlab.ci.read",
		},
		{
			name: "a job retried", method: http.MethodPost, target: "/api/v4/projects/1/jobs/9/retry", contentType: "",
			body: "", action: "gitlab.ci.write",
		},
		{
			name: "a file's head", method: http.MethodHead, target: "/api/v4/projects/1/repository/files/a%2Fb.md", contentType: "",
			body: "", action: "gitlab.repos.read",
		},
		{
			name: "an archive", method: http.MethodGet, target: "/api/v4/projects/1/repository/archive.zip", contentType: "",
			body: "", action: "gitlab.repos.read",
		},
		{
			name: "a generic package uploaded", method: http.MethodPut, target: "/api/v4/projects/1/packages/generic/pkg/1.0/app.bin", contentType: "application/octet-stream",
			body: "binary", action: "gitlab.releases.write",
		},
		{
			name: "a generic package downloaded", method: http.MethodGet, target: "/api/v4/projects/1/packages/generic/pkg/1.0/app.bin", contentType: "",
			body: "", action: "gitlab.releases.read",
		},
		{
			name: "the user", method: http.MethodGet, target: "/api/v4/user", contentType: "",
			body: "", action: "gitlab.account.read",
		},
		{
			name: "a project deleted", method: http.MethodDelete, target: "/api/v4/projects/1", contentType: "",
			body: "", action: "gitlab.repos.delete",
		},
		{
			name: "a path client-go does not call", method: http.MethodGet, target: "/api/v4/projects/1/no_such_thing", contentType: "",
			body: "", action: "gitlab.unlisted",
		},
		{
			name: "a method client-go does not send", method: http.MethodPatch, target: mr + "/merge", contentType: json,
			body: `{}`, action: "gitlab.unlisted",
		},
		{
			name: "a page, not the API", method: http.MethodGet, target: "/group/project/-/merge_requests/7", contentType: "",
			body: "", action: "gitlab.unlisted",
		},
		{
			name: "a GraphQL query", method: http.MethodPost, target: graphql, contentType: json,
			body: `{"query":"query { project(fullPath: \"g/p\") { name } }"}`, action: "gitlab.graphql.read",
		},
		{
			name: "a batch of GraphQL queries", method: http.MethodPost, target: graphql, contentType: json,
			body: `[{"query":"query { currentUser { id } }"},{"query":"query { a: currentUser { id } }"}]`, action: "gitlab.graphql.read",
		},
		{
			name: "a GraphQL mutation", method: http.MethodPost, target: graphql, contentType: json,
			body: `{"query":"mutation { createNote(input: {}) { note { id } } }"}`, action: "gitlab.unlisted",
		},
		{
			name: "a mutation hidden behind Query", method: http.MethodPost, target: graphql, contentType: json,
			body: `{"query":"mutation { createNote(input: {}) { note { id } } }","Query":"query { __typename }"}`, action: "gitlab.unlisted",
		},
		{
			name: "a query key given twice", method: http.MethodPost, target: graphql, contentType: json,
			body: `{"query":"mutation { createNote(input: {}) { note { id } } }","query":"query { __typename }"}`, action: "gitlab.unlisted",
		},
		{
			name: "a multiplex hidden in _json", method: http.MethodPost, target: graphql, contentType: json,
			body: `{"query":"query { __typename }","_json":[{"query":"mutation { createNote(input: {}) { note { id } } }"}]}`, action: "gitlab.unlisted",
		},
		{
			name: "a query in the URL", method: http.MethodPost, target: graphql + "?query=mutation%7BcreateNote%7D", contentType: json,
			body: `{"query":"query { __typename }"}`, action: "gitlab.unlisted",
		},
		{
			name: "a GraphQL form", method: http.MethodPost, target: graphql, contentType: form,
			body: "query=query+%7B+__typename+%7D", action: "gitlab.unlisted",
		},
		{
			name: "a body that is no GraphQL", method: http.MethodPost, target: graphql, contentType: json,
			body: `not graphql`, action: "gitlab.unlisted",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, c.action, gitlabOf(t, catalog, c.method, c.target, c.contentType, c.body).ID)
		})
	}
}

// Scenario: a GitLab host's catalog is chosen by the host's kind (#740): gitlab.com and a server the owner declared
// with the kind gitlab have it, and reach only their own host; a host of another kind, one not known, or a name
// that is no host, has none. github.com keeps GitHub's.
func TestAHostOfAnotherKindGetsNoGitLabCatalog(t *testing.T) {
	t.Parallel()

	known := kinds{gitlabHost: githost.GitLab, "gitlab.com": githost.GitLab, "ghe.example.com": githost.GitHub}

	for _, host := range []string{gitlabHost, "gitlab.com"} {
		catalog, ok := serviceproxy.CatalogIn(t.Context(), known, host)
		require.True(t, ok, host)
		assert.Equal(t, serviceproxy.GitLabIntegration, catalog.Integration, host)
		assert.True(t, catalog.Has(host), host)
		assert.False(t, catalog.Has("api.github.com"), host)
	}

	gitlab, _ := serviceproxy.CatalogIn(t.Context(), known, gitlabHost)
	assert.False(t, gitlab.Has("gitlab.com"), "a host's catalog reaches that host only")

	for _, name := range []string{"ghe.example.com", "unknown.example.com", "gitlab"} {
		_, ok := serviceproxy.CatalogIn(t.Context(), known, name)
		assert.False(t, ok, name)
	}

	github, ok := serviceproxy.CatalogIn(t.Context(), known, serviceproxy.GitHubID)
	require.True(t, ok)
	assert.Equal(t, "github", github.Integration)
}

// Scenario: a Bot's pi reads a merge request on the owner's GitLab server with glab, given that host's Connection
// git.example.com:default, the token workers use (#740). glab holds a placeholder, sent as Private-Token; GitLab
// receives the host's token instead and never the placeholder. Asking for auto-merge waits for the owner as
// auto-merge, and reaches GitLab only once he approves. Another host is not reached.
func TestABotReadsAGitLabHostWithItsTokenAndAutoMergeAsks(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()

	_, err := w.svc.AddGitHost(ctx, gitlabHost, githost.GitLab)
	require.NoError(t, err)

	_, err = w.svc.SetConnection(ctx, gitlabHost, "default", "", "conn-git-example-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(ctx, gitlabHost, "default", true)
	require.NoError(t, err)

	glab, refused := w.openTo(gitlabHost, w.pi, "default", gitlabHost, w.chat)
	require.Zero(t, refused)

	read := glab.doGlab(http.MethodGet, "/api/v4/projects/group%2Fproject/merge_requests/7", "", nil)
	require.Equal(t, http.StatusOK, read.Status)

	received := w.service.received()
	require.Len(t, received, 1)
	assert.Equal(t, "https://"+gitlabHost+"/api/v4/projects/group%2Fproject/merge_requests/7", received[0].URL)
	assert.Equal(t, "Bearer "+realToken, received[0].Authorization)
	assert.Empty(t, w.approvals(""), "reading asks nobody")

	answered := make(chan answer, 1)

	go func() {
		answered <- glab.doGlab(http.MethodPut, "/api/v4/projects/group%2Fproject/merge_requests/7/merge",
			"application/json", []byte(`{"auto_merge":true}`))
	}()

	asked := w.pending()
	assert.Equal(t, "gitlab.merge_requests.automerge", asked.Action)
	assert.Equal(t, gitlabHost, asked.Integration)
	assert.Len(t, w.service.received(), 1, "nothing is sent while the owner has not answered")

	_, err = w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, (<-answered).Status)

	received = w.service.received()
	require.Len(t, received, 2)
	assert.Equal(t, http.MethodPut, received[1].Method)

	for _, r := range received {
		assert.Empty(t, r.Header.Get("Private-Token"), "glab's placeholder is never passed on")
	}

	_, refused = w.openTo(gitlabHost, w.pi, "default", "gitlab.com", w.chat)
	assert.Equal(t, http.StatusForbidden, refused, "only the Connection's own host is reached")

	_, refused = w.openTo("unknown.example.com", w.pi, "default", "unknown.example.com", w.chat)
	assert.Equal(t, http.StatusForbidden, refused, "a host that is not declared has no catalog")
	assert.NotContains(t, w.logs.String(), realToken)
}

// doGlab sends a request as glab does: its token, the placeholder, as Private-Token (client-go
// AccessTokenAuthSource).
func (tl *tool) doGlab(method, path, contentType string, body []byte) answer {
	tl.t.Helper()

	req, err := http.NewRequestWithContext(tl.t.Context(), method, "https://service"+path, bytes.NewReader(body))
	require.NoError(tl.t, err)

	req.Header.Set("Private-Token", agenttool.Placeholder)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	require.NoError(tl.t, req.Write(tl.conn))

	resp, err := http.ReadResponse(tl.reader, req)
	require.NoError(tl.t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(tl.t, err)
	require.NoError(tl.t, resp.Body.Close())

	return answer{Status: resp.StatusCode, Body: string(raw)}
}

// openGitLab is a pi's glab tunnel to the declared GitLab host, its Connection on for chats.
func openGitLab(t *testing.T, w *world) *tool {
	t.Helper()

	ctx := t.Context()

	_, err := w.svc.AddGitHost(ctx, gitlabHost, githost.GitLab)
	require.NoError(t, err)

	_, err = w.svc.SetConnection(ctx, gitlabHost, "default", "", "conn-git-example-com-default")
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(ctx, gitlabHost, "default", true)
	require.NoError(t, err)

	glab, refused := w.openTo(gitlabHost, w.pi, "default", gitlabHost, w.chat)
	require.Zero(t, refused)

	return glab
}

// Scenario (review of #777): glab asks for auto-merge with auto_merge alone, which GitLab 17.10 does not read: it
// merges at once (lib/api/merge_requests.rb at v17.10.0). An auto-merge the owner allowed is sent with every flag of
// its pair set, merge_when_pipeline_succeeds beside auto_merge, and a merge train's when_pipeline_succeeds beside
// auto_merge, which every version reads as auto-merge; the rest of the body is kept. A merge is sent as it came.
func TestAnAutoMergeIsSentAsEveryGitLabReadsIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()
	glab := openGitLab(t, w)

	require.NoError(t, w.svc.SetActionPolicy(ctx, gitlabHost, "default", "gitlab.merge_requests.automerge",
		orchestration.PolicyAllow))
	require.NoError(t, w.svc.SetActionPolicy(ctx, gitlabHost, "default", "gitlab.merge_requests.merge",
		orchestration.PolicyAllow))

	const project = "/api/v4/projects/group%2Fproject"

	for _, c := range []struct {
		name, method, path, body string
		want                     map[string]any
	}{
		{
			name: "glab mr merge", method: http.MethodPut, path: project + "/merge_requests/7/merge",
			body: `{"auto_merge":true,"sha":"abc"}`,
			want: map[string]any{"auto_merge": true, "merge_when_pipeline_succeeds": true, "sha": "abc"},
		},
		{
			name: "a merge train when checks pass", method: http.MethodPost,
			path: project + "/merge_trains/merge_requests/7", body: `{"when_pipeline_succeeds":true}`,
			want: map[string]any{"auto_merge": true, "when_pipeline_succeeds": true},
		},
		{
			name: "a merge now", method: http.MethodPut, path: project + "/merge_requests/7/merge",
			body: `{"squash":true}`, want: map[string]any{"squash": true},
		},
	} {
		before := len(w.service.received())

		require.Equal(t, http.StatusOK, glab.doGlab(c.method, c.path, "application/json", []byte(c.body)).Status,
			c.name)

		received := w.service.received()
		require.Len(t, received, before+1, c.name)

		var sent map[string]any
		require.NoError(t, json.Unmarshal(received[before].Body, &sent), c.name)
		assert.Equal(t, c.want, sent, c.name)
	}
}

// Scenario (review of #777): with writes of merge requests allowed and merge and auto-merge set to never, adding a
// merge request to a merge train is refused: it merges. A GraphQL mutation behind a "Query" key that Go's decoder
// would read waits for the owner as unlisted, and reaches GitLab only once he approves.
func TestAMergeTrainAndAHiddenMutationAreNotWrites(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()
	glab := openGitLab(t, w)

	for action, policy := range map[string]string{
		"gitlab.merge_requests.write":     orchestration.PolicyAllow,
		"gitlab.merge_requests.merge":     orchestration.PolicyNever,
		"gitlab.merge_requests.automerge": orchestration.PolicyNever,
	} {
		require.NoError(t, w.svc.SetActionPolicy(ctx, gitlabHost, "default", action, policy))
	}

	note := glab.doGlab(http.MethodPost, "/api/v4/projects/1/merge_requests/7/notes", "application/json",
		[]byte(`{"body":"Looking"}`))
	require.Equal(t, http.StatusOK, note.Status, "a note is a write")

	for _, body := range []string{`{}`, `{"auto_merge":true}`} {
		train := glab.doGlab(http.MethodPost, "/api/v4/projects/1/merge_trains/merge_requests/7", "application/json",
			[]byte(body))
		assert.Equal(t, http.StatusForbidden, train.Status, body)
	}

	assert.Len(t, w.service.received(), 1, "no merge train reached GitLab")

	hidden := []byte(`{"query":"mutation { createNote(input: {}) { note { id } } }","Query":"query { __typename }"}`)
	answered := make(chan answer, 1)

	go func() { answered <- glab.doGlab(http.MethodPost, "/api/graphql", "application/json", hidden) }()

	asked := w.pending()
	assert.Equal(t, "gitlab.unlisted", asked.Action)
	assert.Len(t, w.service.received(), 1, "nothing is sent while the owner has not answered")

	_, err := w.proxy.Decide(ctx, asked.ID, false, orchestration.ScopeOnce)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, (<-answered).Status)
	assert.Len(t, w.service.received(), 1)
}
