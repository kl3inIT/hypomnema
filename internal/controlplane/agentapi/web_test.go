package agentapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// fakeWeb stands for the web: it records whose call asked what (a
// Dispatch's, or a chat's), and fails with err when it is set.
type fakeWeb struct {
	calls []string
	err   error
}

func (f *fakeWeb) Search(_ context.Context, who agentapi.Who, query string, limit int, provider string,
) (websearch.Answer, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s search %q %d %q", whose(who), query, limit, provider))

	return websearch.Answer{Provider: "exa", Query: query, Results: []websearch.Result{
		{Title: "Go", URL: "https://go.dev", Snippet: "The Go language."},
	}}, f.err
}

func (f *fakeWeb) Fetch(_ context.Context, who agentapi.Who, url string) (websearch.Page, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s fetch %s", whose(who), url))

	return websearch.Page{URL: url, Title: "Go", Content: "# Go"}, f.err
}

// whose names the Dispatch or the chat a call is recorded for.
func whose(who agentapi.Who) string {
	if who.DispatchID != "" {
		return who.DispatchID
	}

	return "chat " + who.ChatID
}

// fakeImages stands for the gateway's images: it records which Dispatch
// asked what, and fails with err when it is set.
type fakeImages struct {
	asked []string
	err   error
}

func (f *fakeImages) Make(_ context.Context, who agentapi.Who, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	refs := make([]string, 0, len(req.Images))
	for _, img := range req.Images {
		refs = append(refs, img.MIME+":"+string(img.Data))
	}

	f.asked = append(f.asked, fmt.Sprintf("%s %q n=%d size=%s quality=%s refs=%v", whose(who), req.Prompt, req.N,
		req.Size, req.Quality, refs))

	if f.err != nil {
		return modelgateway.ImageResult{}, f.err
	}

	return modelgateway.ImageResult{
		Provider: "antigravity", Model: "gemini-3.1-flash-image", Size: "1024x1536",
		Images: []modelgateway.GeneratedImage{{B64JSON: base64.StdEncoding.EncodeToString([]byte("PNG")),
			MIME: "image/png"}},
		Errors: []modelgateway.ImageFailure{{Provider: "codex", Model: "gpt-image-2", Message: "rate limited"}},
	}, nil
}

// webWorld is a control plane whose workers run the runtimes named, with the
// web and the gateway's images, and with the browser or without it.
func webWorld(t *testing.T, withBrowser bool, runtimes map[string]string) (*world, *fakeWeb) {
	t.Helper()

	w, web, _ := gatewayWorld(t, withBrowser, runtimes)

	return w, web
}

func gatewayWorld(t *testing.T, withBrowser bool, runtimes map[string]string) (*world, *fakeWeb, *fakeImages) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	w := &world{t: t, svc: orchestration.New(db, platform.SystemClock{}, platform.NewID), logs: &bytes.Buffer{},
		browser: &sharedBrowser{}}
	web, images := &fakeWeb{}, &fakeImages{}

	api := agentapi.New(w.svc, nil, slog.New(slog.NewTextHandler(w.logs, nil))).
		WithRuntimes(func(_ context.Context, dispatchID string) (string, error) {
			runtime, ok := runtimes[dispatchID]
			if !ok {
				return "", orchestration.ErrNotFound
			}

			return runtime, nil
		}).WithWeb(web).WithImages(images).WithRoutines(nowRunner{w.svc})
	if withBrowser {
		api.WithBrowser(w.browser)
	}

	w.api = api

	mux := http.NewServeMux()
	api.Register(mux)

	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)

	return w, web, images
}

func (w *world) toolNames(key string) []string {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathTools, key, nil)
	require.Equal(w.t, http.StatusOK, status, string(body))

	var list agenttool.BrowserToolList
	require.NoError(w.t, json.Unmarshal(body, &list))

	names := []string{}
	for _, tool := range list.Data {
		names = append(names, tool.Name)
	}

	return names
}

// Scenario: each worker is offered the gateway's tools its runtime gets,
// before the browser's: a Codex worker web_fetch and generate_image, its own
// search being the gateway's; Claude Code, omp and opencode all three, their
// own being off (issue #492). A coordinator gets none: a Bot has its CLI.
func TestEachRuntimeIsOfferedItsWebTools(t *testing.T) {
	t.Parallel()

	runtimes := map[string]string{"d_claude": "claude", "d_codex": "codex", "d_omp": "omp", "d_oc": "opencode"}
	w, _ := webWorld(t, true, runtimes)
	ctx := t.Context()

	want := map[string][]string{
		"d_claude": {"memory", "show_artifact", "web_search", "web_fetch", "generate_image", "agent_browser_open"},
		"d_codex":  {"memory", "show_artifact", "web_fetch", "generate_image", "agent_browser_open"},
		"d_omp":    {"memory", "show_artifact", "web_search", "web_fetch", "generate_image", "agent_browser_open"},
		"d_oc":     {"memory", "show_artifact", "web_search", "web_fetch", "generate_image", "agent_browser_open"},
	}

	for dispatch, names := range want {
		key, err := w.svc.AddAgentSession(ctx, dispatch, nil)
		require.NoError(t, err)
		assert.Equal(t, names, w.toolNames(key), dispatch)
	}

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	assert.Empty(t, w.toolNames(coordinator))

	var list agenttool.BrowserToolList

	key, err := w.svc.AddAgentSession(ctx, "d_omp", nil)
	require.NoError(t, err)

	_, body := w.call(http.MethodGet, agenttool.PathTools, key, nil)
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Data, 6)
	assert.JSONEq(t, `["query"]`, mustField(t, list.Data[2].InputSchema, "required"))
	assert.JSONEq(t, `["prompt"]`, mustField(t, list.Data[4].InputSchema, "required"))
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
// search: web_search is no tool of its, and without a browser it has no
// such tool at all. What the web could not do is the tool's
// error, for the agent to read. Without the browser the web tools are still
// listed and run.
func TestAWorkerSearchesAndReadsThroughTheAgentAPI(t *testing.T) {
	t.Parallel()

	w, web := webWorld(t, false, map[string]string{"d_omp": "omp", "d_codex": "codex"})
	ctx := t.Context()

	omp, err := w.svc.AddAgentSession(ctx, "d_omp", nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"memory", "show_artifact", "web_search", "web_fetch", "generate_image"},
		w.toolNames(omp), "no browser, still the gateway's tools")

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
	assert.Equal(t, http.StatusNotFound, status, "a Codex worker's web_search is not ours")
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

// Scenario: a Claude Code worker makes an image from a description and then
// edits one. Each call reaches the gateway as its Dispatch's, so that it is
// recorded on its key, makes one image, and answers what made it, the
// providers that failed first included, then the image as MCP image
// content. What no provider could make is the tool's error, for the agent to
// read; a call the tool cannot read says what it takes.
func TestAWorkerMakesAnImageThroughTheAgentAPI(t *testing.T) {
	t.Parallel()

	w, _, images := gatewayWorld(t, false, map[string]string{"d_claude": "claude"})

	key, err := w.svc.AddAgentSession(t.Context(), "d_claude", nil)
	require.NoError(t, err)

	status, body := w.browse(key, "generate_image", `{"prompt":"a red fox","size":"1024x1536","quality":"high"}`)
	require.Equal(t, http.StatusOK, status, body)

	var result struct {
		Content []struct{ Type, Text, Data, MimeType string } `json:"content"`
		IsError bool                                          `json:"isError"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 2)
	assert.Equal(t, "Made by antigravity with gemini-3.1-flash-image at 1024x1536, after codex failed (rate limited).",
		result.Content[0].Text)
	assert.Equal(t, "image", result.Content[1].Type)
	assert.Equal(t, "image/png", result.Content[1].MimeType)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("PNG")), result.Content[1].Data)

	ref := base64.StdEncoding.EncodeToString([]byte("GIF89a"))
	status, _ = w.browse(key, "generate_image",
		`{"prompt":"make it blue","images":[{"mime_type":"image/gif","data":"`+ref+`"}]}`)
	require.Equal(t, http.StatusOK, status)

	assert.Equal(t, []string{
		`d_claude "a red fox" n=1 size=1024x1536 quality=high refs=[]`,
		`d_claude "make it blue" n=1 size= quality= refs=[image/gif:GIF89a]`,
	}, images.asked)
	assert.Contains(t, w.logs.String(), "agentapi.image_call")
	assert.NotContains(t, w.logs.String(), "fox", "what a worker asks for is not logged")

	images.err = &modelgateway.ImageError{Failures: []modelgateway.ImageFailure{
		{Provider: "codex", Model: "gpt-image-2", Status: http.StatusTooManyRequests, Message: "rate limited"},
	}}

	status, body = w.browse(key, "generate_image", `{"prompt":"a red fox"}`)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "no image provider made the image: codex gpt-image-2: rate limited")

	status, body = w.browse(key, "generate_image", `{"prompt":"x","images":["fox.png"]}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "generate_image takes")
	assert.Len(t, images.asked, 3, "a call the tool cannot read reaches no provider")
}

// gatewayNames lists the gateway's tools among those an agent is offered.
func (w *world) gatewayNames(key string) ([]string, []agenttool.BrowserTool) {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathTools, key, nil)
	require.Equal(w.t, http.StatusOK, status, string(body))

	var list agenttool.BrowserToolList
	require.NoError(w.t, json.Unmarshal(body, &list))

	names, tools := []string{}, []agenttool.BrowserTool{}

	for _, tool := range list.Data {
		if slices.Contains([]string{"web_search", "web_fetch", "generate_image"}, tool.Name) {
			names, tools = append(names, tool.Name), append(tools, tool)
		}
	}

	return names, tools
}

// Scenario: a Bot's pi mounts the tools server (issue #492). It is offered
// the gateway's three tools, and its generate_image says how to send the
// image into its chat. Its calls are its chat's, so that they are recorded on the key of
// the chat's running pi as its `hypomnema web` and `hypomnema image` are. A
// worker is offered its runtime's there too; a Routine's script and the
// coordinator token, which have no MCP, none; a tool an agent was not given
// is not run.
func TestABotsPiIsOfferedTheGatewaysTools(t *testing.T) {
	t.Parallel()

	w, web, images := gatewayWorld(t, true, map[string]string{"d_codex": "codex"})
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	names, tools := w.gatewayNames(pi)
	assert.Equal(t, []string{"web_search", "web_fetch", "generate_image"}, names)
	assert.Contains(t, tools[2].Description, "MEDIA line", "a Bot is told how to send the image")

	codex, err := w.svc.AddAgentSession(ctx, "d_codex", nil)
	require.NoError(t, err)

	names, tools = w.gatewayNames(codex)
	assert.Equal(t, []string{"web_fetch", "generate_image"}, names)
	assert.NotContains(t, tools[1].Description, "MEDIA", "a worker has no chat to send into")

	names, _ = w.gatewayNames(coordinator)
	assert.Empty(t, names)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	_, err = w.svc.RunRoutineNow(ctx, routine.ID) // a script's token is its live run's (#1023)
	require.NoError(t, err)
	script, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)

	names, _ = w.gatewayNames(script)
	assert.Empty(t, names, "a Routine's script keeps its commands")

	call := func(key, tool, arguments string) (int, string) {
		status, body := w.call(http.MethodPost, agenttool.PathToolsCall, key,
			agenttool.BrowserCall{Tool: tool, Arguments: json.RawMessage(arguments)})

		return status, string(body)
	}

	status, body := call(pi, "web_search", `{"query":"go"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "EXTERNAL_UNTRUSTED_CONTENT")

	status, _ = call(pi, "generate_image", `{"prompt":"a red fox"}`)
	require.Equal(t, http.StatusOK, status)

	assert.Equal(t, []string{`chat ` + chat.ID + ` search "go" 0 ""`}, web.calls)
	assert.Equal(t, []string{`chat ` + chat.ID + ` "a red fox" n=1 size= quality= refs=[]`}, images.asked)

	_, _ = call(codex, "web_search", `{"query":"go"}`) // the browser's to refuse: the fake one runs anything

	status, _ = call(script, "web_search", `{"query":"go"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Len(t, web.calls, 1, "a Codex worker's search is its own, and a script has none")

	status, _ = w.call(http.MethodGet, agenttool.PathTools, "", nil)
	assert.Equal(t, http.StatusUnauthorized, status)
}
