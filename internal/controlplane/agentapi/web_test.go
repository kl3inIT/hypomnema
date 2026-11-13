package agentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// fakeWeb stands for the web: it records which Dispatch asked what, and
// fails with err when it is set.
type fakeWeb struct {
	calls []string
	err   error
}

func (f *fakeWeb) Search(_ context.Context, dispatchID, query string, limit int, provider string,
) (websearch.Answer, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s search %q %d %q", dispatchID, query, limit, provider))

	return websearch.Answer{Provider: "exa", Query: query, Results: []websearch.Result{
		{Title: "Go", URL: "https://go.dev", Snippet: "The Go language."},
	}}, f.err
}

func (f *fakeWeb) Fetch(_ context.Context, dispatchID, url string) (websearch.Page, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s fetch %s", dispatchID, url))

	return websearch.Page{URL: url, Title: "Go", Content: "# Go"}, f.err
}

// webWorld is a control plane whose workers run the runtimes named, with the
// web, and with the browser or without it.
func webWorld(t *testing.T, withBrowser bool, runtimes map[string]string) (*world, *fakeWeb) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	w := &world{t: t, svc: orchestration.New(db, platform.SystemClock{}, platform.NewID), logs: &bytes.Buffer{},
		browser: &sharedBrowser{}}
	web := &fakeWeb{}

	api := agentapi.New(w.svc, nil, slog.New(slog.NewTextHandler(w.logs, nil))).
		WithWeb(web, func(_ context.Context, dispatchID string) (string, error) {
			runtime, ok := runtimes[dispatchID]
			if !ok {
				return "", orchestration.ErrNotFound
			}

			return runtime, nil
		})
	if withBrowser {
		api.WithBrowser(w.browser)
	}

	mux := http.NewServeMux()
	api.Register(mux)

	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)

	return w, web
}

func (w *world) toolNames(key string) []string {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathBrowserTools, key, nil)
	require.Equal(w.t, http.StatusOK, status, string(body))

	var list agenttool.BrowserToolList
	require.NoError(w.t, json.Unmarshal(body, &list))

	names := []string{}
	for _, tool := range list.Data {
		names = append(names, tool.Name)
	}

	return names
}

// Scenario: each worker is offered the web's tools its runtime gets, after
// the browser's: a Claude Code worker none, a Codex worker web_fetch, omp
// and opencode both. A coordinator gets none: a Bot has its CLI.
func TestEachRuntimeIsOfferedItsWebTools(t *testing.T) {
	t.Parallel()

	runtimes := map[string]string{"d_claude": "claude", "d_codex": "codex", "d_omp": "omp", "d_oc": "opencode"}
	w, _ := webWorld(t, true, runtimes)
	ctx := t.Context()

	want := map[string][]string{
		"d_claude": {"agent_browser_open"},
		"d_codex":  {"agent_browser_open", "web_fetch"},
		"d_omp":    {"agent_browser_open", "web_search", "web_fetch"},
		"d_oc":     {"agent_browser_open", "web_search", "web_fetch"},
	}

	for dispatch, names := range want {
		key, err := w.svc.AddAgentSession(ctx, dispatch, nil)
		require.NoError(t, err)
		assert.Equal(t, names, w.toolNames(key), dispatch)
	}

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	assert.Equal(t, []string{"agent_browser_open"}, w.toolNames(coordinator))

	var list agenttool.BrowserToolList

	key, err := w.svc.AddAgentSession(ctx, "d_omp", nil)
	require.NoError(t, err)

	_, body := w.call(http.MethodGet, agenttool.PathBrowserTools, key, nil)
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Data, 3)
	assert.JSONEq(t, `["query"]`, mustField(t, list.Data[1].InputSchema, "required"))
}

func mustField(t *testing.T, raw json.RawMessage, field string) string {
	t.Helper()

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))

	return string(fields[field])
}

// Scenario: an omp worker searches and reads a page. Each call reaches the
// web as its Dispatch's, so that it is recorded on its key, and its answer
// is the tool's text, marked as outside content. A Codex worker may not
// search: web_search is no tool of its, and is passed to the browser,
// which does not have it either. What the web could not do is the tool's
// error, for the agent to read. Without the browser the web tools are still
// listed and run.
func TestAWorkerSearchesAndReadsThroughTheAgentAPI(t *testing.T) {
	t.Parallel()

	w, web := webWorld(t, false, map[string]string{"d_omp": "omp", "d_codex": "codex"})
	ctx := t.Context()

	omp, err := w.svc.AddAgentSession(ctx, "d_omp", nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"web_search", "web_fetch"}, w.toolNames(omp), "no browser, still the web")

	status, body := w.browse(omp, "web_search", `{"query":"go","max_results":3,"provider":"exa"}`)
	require.Equal(t, http.StatusOK, status, body)

	var result struct {
		Content []struct{ Type, Text string } `json:"content"`
		IsError bool                          `json:"isError"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, `Search results from exa for "go"`)
	assert.Contains(t, result.Content[0].Text, "<<<EXTERNAL_UNTRUSTED_CONTENT")
	assert.Contains(t, result.Content[0].Text, "https://go.dev")

	status, body = w.browse(omp, "web_fetch", `{"url":"https://go.dev"}`)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "Source: Web Fetch")

	assert.Equal(t, []string{`d_omp search "go" 3 "exa"`, "d_omp fetch https://go.dev"}, web.calls)
	assert.Contains(t, w.logs.String(), "agentapi.web_call")
	assert.NotContains(t, w.logs.String(), "go.dev", "what a worker reads is not logged")

	codex, err := w.svc.AddAgentSession(ctx, "d_codex", nil)
	require.NoError(t, err)

	status, _ = w.browse(codex, "web_search", `{"query":"go"}`)
	assert.Equal(t, http.StatusServiceUnavailable, status, "a Codex worker's web_search is not ours")
	assert.Len(t, web.calls, 2)

	web.err = fmt.Errorf("%w: exa (public MCP: rate limited (429))", websearch.ErrFailed)

	status, body = w.browse(omp, "web_search", `{"query":"go"}`)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "rate limited")

	status, _ = w.browse(omp, "web_fetch", `["not an object"]`)
	assert.Equal(t, http.StatusBadRequest, status)
}
