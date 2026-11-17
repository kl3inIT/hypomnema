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
// pipeline; auto-merge is canceled by its own POST. A body that cannot be read, or a flag that is not plainly true,
// is a merge. Both ask until the owner says otherwise. A note on a merge request is a write of merge requests, its
// deletion a delete; a segment is read decoded, and a project's encoded path stays one segment. A GraphQL query
// reads; a mutation, or a path client-go does not call, asks as unlisted.
func TestGlabsMergeAndAutoMergeAreActionsOfTheirOwn(t *testing.T) {
	t.Parallel()

	catalog := gitlabCatalog(t)

	for _, id := range []string{"gitlab.merge_requests.merge", "gitlab.merge_requests.automerge"} {
		a, listed := catalog.Action(id)
		require.True(t, listed, id)
		assert.Equal(t, orchestration.PolicyAsk, a.Default(), id)
	}

	const (
		mr   = "/api/v4/projects/group%2Fproject/merge_requests/7"
		json = "application/json"
		form = "application/x-www-form-urlencoded"
	)

	cases := []struct{ method, target, contentType, body, action string }{
		{http.MethodPut, mr + "/merge", json, `{"auto_merge":true,"sha":"abc"}`, "gitlab.merge_requests.automerge"},
		{http.MethodPut, mr + "/merge", json, `{"merge_when_pipeline_succeeds":true}`,
			"gitlab.merge_requests.automerge"},
		{http.MethodPut, mr + "/merge?auto_merge=true", "", "", "gitlab.merge_requests.automerge"},
		{http.MethodPut, mr + "/merge", form, "auto_merge=true", "gitlab.merge_requests.automerge"},
		{http.MethodPut, mr + "/merge", json, `{"squash":true}`, "gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merge", "", "", "gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merge", json, `{"auto_merge":false}`, "gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merge", json, `{"auto_merge":true,"merge_when_pipeline_succeeds":false}`,
			"gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merge?auto_merge=1", "", "", "gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merge", json, `{"auto_merge":tru`, "gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merge", "text/plain", "auto_merge=true", "gitlab.merge_requests.merge"},
		{http.MethodPut, mr + "/merg%65", json, `{}`, "gitlab.merge_requests.merge"},
		{http.MethodPost, mr + "/cancel_auto_merge", "", "", "gitlab.merge_requests.automerge"},
		{http.MethodPost, mr + "/cancel_merge_when_pipeline_succeeds", "", "", "gitlab.merge_requests.automerge"},
		{http.MethodPost, mr + "/notes", json, `{"body":"Looking"}`, "gitlab.merge_requests.write"},
		{http.MethodDelete, mr + "/notes/3", "", "", "gitlab.merge_requests.delete"},
		{http.MethodPost, mr + "/notes/3/award_emoji", json, `{"name":"thumbsup"}`, "gitlab.merge_requests.write"},
		{http.MethodGet, mr, "", "", "gitlab.merge_requests.read"},
		{http.MethodGet, "/api/v4/projects/group%2Fproject/merge_requests?state=opened", "", "",
			"gitlab.merge_requests.read"},
		{http.MethodPost, "/api/v4/projects/1/issues/4/notes", json, `{"body":"x"}`, "gitlab.issues.write"},
		{http.MethodGet, "/api/v4/projects/1/pipelines/9/jobs", "", "", "gitlab.ci.read"},
		{http.MethodPost, "/api/v4/projects/1/jobs/9/retry", "", "", "gitlab.ci.write"},
		{http.MethodHead, "/api/v4/projects/1/repository/files/a%2Fb.md", "", "", "gitlab.repos.read"},
		{http.MethodGet, "/api/v4/projects/1/repository/archive.zip", "", "", "gitlab.repos.read"},
		{http.MethodGet, "/api/v4/user", "", "", "gitlab.account.read"},
		{http.MethodDelete, "/api/v4/projects/1", "", "", "gitlab.repos.delete"},
		{http.MethodGet, "/api/v4/projects/1/no_such_thing", "", "", "gitlab.unlisted"},
		{http.MethodPatch, mr + "/merge", json, `{}`, "gitlab.unlisted"},
		{http.MethodGet, "/group/project/-/merge_requests/7", "", "", "gitlab.unlisted"},
		{http.MethodPost, "/api/graphql", json, `{"query":"query { project(fullPath: \"g/p\") { name } }"}`,
			"gitlab.graphql.read"},
		{http.MethodPost, "/api/graphql", json, `{"query":"mutation { createNote(input: {}) { note { id } } }"}`,
			"gitlab.unlisted"},
		{http.MethodPost, "/api/graphql", json, `not graphql`, "gitlab.unlisted"},
	}

	for _, c := range cases {
		assert.Equal(t, c.action, gitlabOf(t, catalog, c.method, c.target, c.contentType, c.body).ID, "%s %s %s",
			c.method, c.target, c.body)
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
