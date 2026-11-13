package modelgateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// searchGateway is a gateway whose subscriptions search through the fake
// backends cfg names, with no model of its own.
func searchGateway(t *testing.T, cfg modelgateway.Config) websearch.Subscriptions {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := modelgateway.New(db, platform.SystemClock{}, platform.NewID,
		func(context.Context, string) (modelgateway.ProviderInfo, error) {
			return modelgateway.ProviderInfo{}, modelgateway.ErrNotFound
		},
		func(context.Context, string) (string, error) { return "", nil })

	return modelgateway.NewServer(svc, platform.NewLogger(t.Output(), platform.LogConfig{}, "test"), cfg).
		SubscriptionSearch()
}

// codexSearchEvents is a Codex search's stream: the search's sources, the
// model's JSON answer (one row the search never saw), and the end.
var codexSearchEvents = []string{
	event(`{"type":"response.created","response":{"id":"resp_1"}}`),
	event(`{"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed",` +
		`"action":{"type":"search","query":"go 1.27","sources":[{"type":"url","url":"https://go.dev/blog/go1.27"},` +
		`{"type":"url","url":"https://www.tip.golang.org/doc/go1.27/?utm_source=openai"}]}}}`),
	event(`{"type":"response.output_text.done","text":"` + "```json\\n" + `{\"results\":[` +
		`{\"title\":\"Go 1.27 is released\",\"url\":\"https://go.dev/blog/go1.27\",\"description\":\"The release.\"},` +
		`{\"title\":\"Made up\",\"url\":\"https://invented.example/x\",\"description\":\"never seen\"}]}` +
		"\\n```" + `"}`),
	event(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":9}}}`),
}

// Scenario: a search reaches a Codex account. The backend is called as chat
// calls it, under the account, with OmniRoute's request: the small model,
// the hosted web_search tool required and its sources asked for. Of the
// model's rows only the one whose page the search saw is kept. Without an
// answer from the model, the sources are the results, each page once.
func TestACodexAccountSearchesTheWeb(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, stream(codexSearchEvents...))
	subs := &accounts{}
	search := searchGateway(t, modelgateway.Config{Subscriptions: subs, CodexBaseURL: backend.URL})

	results, err := search.Search(t.Context(), websearch.ProviderCodex, "go 1.27", 5)
	require.NoError(t, err)
	assert.Equal(t, []websearch.Result{
		{Title: "Go 1.27 is released", URL: "https://go.dev/blog/go1.27", Snippet: "The release."},
	}, results)

	header, body := got.last(t)
	assert.Equal(t, "Bearer access-0", header.Get("Authorization"))
	assert.Equal(t, "chatgpt-account", header.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "text/event-stream", header.Get("Accept"))

	var sent map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Equal(t, "gpt-6-luna", sent["model"])
	assert.Equal(t, []any{map[string]any{"type": "web_search"}}, sent["tools"])
	assert.Equal(t, "required", sent["tool_choice"])
	assert.Equal(t, []any{"web_search_call.action.sources"}, sent["include"])
	assert.Equal(t, map[string]any{"effort": "low"}, sent["reasoning"])
	assert.Equal(t, true, sent["stream"])
	assert.Equal(t, false, sent["store"])
	assert.Contains(t, sent["instructions"], "Return up to 5 relevant results.")
	assert.Contains(t, body, `"text":"go 1.27"`)

	sourcesOnly, _ := codexBackend(t, stream(codexSearchEvents[0], codexSearchEvents[1], codexSearchEvents[1],
		codexSearchEvents[3]))
	search = searchGateway(t, modelgateway.Config{Subscriptions: &accounts{}, CodexBaseURL: sourcesOnly.URL})

	results, err = search.Search(t.Context(), websearch.ProviderCodex, "go 1.27", 5)
	require.NoError(t, err)
	assert.Equal(t, []websearch.Result{
		{Title: "go.dev", URL: "https://go.dev/blog/go1.27"},
		{Title: "www.tip.golang.org", URL: "https://www.tip.golang.org/doc/go1.27/"},
	}, results, "the sources, without utm_source, each once")

	// A stream that never searched is a failure.
	noSearch, _ := codexBackend(t, stream(codexSearchEvents[0], codexSearchEvents[3]))
	search = searchGateway(t, modelgateway.Config{Subscriptions: &accounts{}, CodexBaseURL: noSearch.URL})

	_, err = search.Search(t.Context(), websearch.ProviderCodex, "go 1.27", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Codex web search failed")
}

// Scenario: the Codex account has reached its usage limit. The search fails
// with a 429, so the web search rests Codex's search: the next one is not
// sent. The account is not cooled down: its chat goes on. With no account
// logged in the provider is not there.
func TestACodexUsageLimitRestsTheSearchNotTheAccount(t *testing.T) {
	t.Parallel()

	backend, got := codexBackend(t, plain(http.StatusTooManyRequests,
		`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":90}}`))
	subs := &accounts{}
	web := websearch.New(websearch.Config{}).WithSubscriptions(
		searchGateway(t, modelgateway.Config{Subscriptions: subs, CodexBaseURL: backend.URL}))

	_, err := web.Search(t.Context(), "go", 0, websearch.ProviderCodex)
	require.ErrorIs(t, err, websearch.ErrFailed)
	assert.Contains(t, err.Error(), "codex (its subscription: rate limited (429))")

	_, err = web.Search(t.Context(), "go again", 0, websearch.ProviderCodex)

	var resting *websearch.RestingError
	require.ErrorAs(t, err, &resting)
	assert.Equal(t, 1, got.calls(), "a resting search is not sent")
	assert.True(t, subs.cooledUntil().IsZero(), "the account's chat is not cooled down")

	none := searchGateway(t, modelgateway.Config{Subscriptions: &accounts{none: true}, CodexBaseURL: backend.URL})

	_, err = none.Search(t.Context(), websearch.ProviderCodex, "go", 5)
	require.ErrorIs(t, err, websearch.ErrNoAccount)

	_, err = searchGateway(t, modelgateway.Config{}).Search(t.Context(), websearch.ProviderClaude, "go", 5)
	require.ErrorIs(t, err, websearch.ErrNoAccount, "a gateway without subscriptions has no account")
}

const claudeSearchAnswer = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5",
"content":[
 {"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go 1.27"}},
 {"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
  {"type":"web_search_result","title":"Go 1.27 is released","url":"https://go.dev/blog/go1.27",
   "encrypted_content":"x","page_age":"2 days ago"},
  {"type":"web_search_result","title":"Release notes","url":"https://go.dev/doc/go1.27","encrypted_content":"y"},
  {"type":"web_search_result","title":"Third","url":"https://third.example","encrypted_content":"z"}]},
 {"type":"text","text":"Go 1.27 is out.","citations":[{"type":"web_search_result_location",
  "url":"https://go.dev/blog/go1.27","title":"Go 1.27 is released","cited_text":"Today we release Go 1.27."}]}],
"usage":{"input_tokens":10,"output_tokens":5,"server_tool_use":{"web_search_requests":1}}}`

// Scenario: a search reaches a Claude account. The backend is called as omp
// calls it under a subscription: one Messages request, not streamed, on the
// small model, with the web_search_20250305 tool and its beta, wearing the
// disguise every subscription request wears. The pages are the tool's
// results, as many as asked, each with the passage the answer cited from it.
func TestAClaudeAccountSearchesTheWeb(t *testing.T) {
	t.Parallel()

	backend, got := claudeBackend(t, plain(http.StatusOK, claudeSearchAnswer))
	search := searchGateway(t, modelgateway.Config{Subscriptions: &accounts{}, ClaudeBaseURL: backend.URL})

	results, err := search.Search(t.Context(), websearch.ProviderClaude, "go 1.27", 2)
	require.NoError(t, err)
	assert.Equal(t, []websearch.Result{
		{Title: "Go 1.27 is released", URL: "https://go.dev/blog/go1.27", Snippet: "Today we release Go 1.27.",
			PublishedAt: "2 days ago"},
		{Title: "Release notes", URL: "https://go.dev/doc/go1.27"},
	}, results)

	header, body := got.last(t)
	assert.Equal(t, "Bearer access-0", header.Get("Authorization"))
	assert.Equal(t, "application/json", header.Get("Accept"))
	assert.Contains(t, header.Get("Anthropic-Beta"), "web-search-2025-03-05")
	assert.Contains(t, header.Get("Anthropic-Beta"), "oauth-2025-04-20")

	var sent struct {
		Model     string           `json:"model"`
		MaxTokens int              `json:"max_tokens"`
		Stream    *bool            `json:"stream"`
		Tools     []map[string]any `json:"tools"`
		System    []struct {
			Text string `json:"text"`
		} `json:"system"`
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &sent))
	assert.Equal(t, "claude-haiku-4-5", sent.Model)
	assert.Equal(t, 4096, sent.MaxTokens)
	assert.Nil(t, sent.Stream, "not streamed")
	assert.Equal(t, []map[string]any{{"type": "web_search_20250305", "name": "web_search"}}, sent.Tools)
	require.NotEmpty(t, sent.System)
	assert.True(t, strings.HasPrefix(sent.System[0].Text, "x-anthropic-billing-header:"), "the disguise is on")
	assert.NotEmpty(t, sent.Metadata.UserID)
}

// Scenario: Claude refuses the search with 429. The search fails with that
// status and its wait, for the web search's rest; the account is not cooled.
func TestAClaudeRefusalIsTheSearchsAlone(t *testing.T) {
	t.Parallel()

	backend, _ := claudeBackend(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "40")
		plain(http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`)(w, nil)
	})
	subs := &accounts{}
	web := websearch.New(websearch.Config{}).WithSubscriptions(
		searchGateway(t, modelgateway.Config{Subscriptions: subs, ClaudeBaseURL: backend.URL}))

	_, err := web.Search(t.Context(), "go", 0, websearch.ProviderClaude)
	require.ErrorIs(t, err, websearch.ErrFailed)
	assert.Contains(t, err.Error(), "rate limited (429)")

	_, err = web.Search(t.Context(), "go again", 0, websearch.ProviderClaude)

	var resting *websearch.RestingError
	require.ErrorAs(t, err, &resting)
	assert.True(t, subs.cooledUntil().IsZero())
}

const antigravitySearchAnswer = `{"response":{"candidates":[{"content":{"role":"model","parts":[
 {"text":"Go 1.27 was released in August. It is faster."}]},
 "groundingMetadata":{"groundingChunks":[
  {"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/a","title":"go.dev"}},
  {"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/b","title":"tip.golang.org"}},
  {"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/a","title":"go.dev"}}],
 "groundingSupports":[
  {"segment":{"startIndex":0,"endIndex":31,"text":"Go 1.27 was released in August."},"groundingChunkIndices":[0,2]},
  {"segment":{"startIndex":32,"endIndex":45,"text":"It is faster."},"groundingChunkIndices":[0]}]}}],
 "usageMetadata":{"totalTokenCount":120}}}`

// Scenario: a search reaches an Antigravity account. The backend is called
// as 9router calls it: generateContent, not streamed, under the account's
// project, the small Gemini model with Google Search grounding, in the IDE's
// search envelope. Each page grounded is a result once, its snippet the
// sentences it grounds; without grounding there is nothing.
func TestAnAntigravityAccountSearchesTheWeb(t *testing.T) {
	t.Parallel()

	got := &seen{}
	answer := antigravitySearchAnswer

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:generateContent" {
			http.NotFound(w, r)

			return
		}

		got.add(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(backend.Close)

	search := searchGateway(t, modelgateway.Config{
		Subscriptions: &googleAccount{}, AntigravityBaseURL: backend.URL, AntigravityManifestURL: backend.URL + "/manifest",
	})

	results, err := search.Search(t.Context(), websearch.ProviderAntigravity, "go 1.27", 5)
	require.NoError(t, err)
	assert.Equal(t, []websearch.Result{
		{Title: "go.dev", URL: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/a",
			Snippet: "Go 1.27 was released in August. | It is faster."},
		{Title: "tip.golang.org", URL: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/b",
			Snippet: "tip.golang.org"},
	}, results)

	header, body := got.last(t)
	assert.Equal(t, "Bearer access-0", header.Get("Authorization"))
	assert.NotEmpty(t, header.Get("User-Agent"))
	assert.JSONEq(t, `{"project":"bright-wave-1234","model":"gemini-2.5-flash","userAgent":"antigravity",
		"requestType":"search","request":{"contents":[{"role":"user","parts":[{"text":"go 1.27"}]}],
		"tools":[{"googleSearch":{}}],"generationConfig":{"temperature":1,"maxOutputTokens":8192}}}`, body)

	answer = `{"response":{"candidates":[{"content":{"parts":[{"text":"I do not know."}]}}]}}`

	results, err = search.Search(t.Context(), websearch.ProviderAntigravity, "nothing", 5)
	require.NoError(t, err)
	assert.Empty(t, results, "no grounding, no results: the web search asks the next provider")
}

// codexSearchAsks is Codex's standalone search request, as codex-rs sends it
// (codex-api/src/endpoint/search.rs, its test): the session, the model, the
// last turns, the commands and the settings.
const codexSearchAsks = `{"id":"search-session","model":"gpt-6.1-sol",
"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"find this"}]}],
"commands":{"search_query":[{"q":"hypomnema","recency":7,"domains":["example.com"]}],"response_length":"short"},
"settings":{"search_context_size":"low","allowed_callers":["direct"],"external_web_access":false},
"max_output_tokens":2500}`

// Scenario: a Codex worker's web.run tool posts its search to
// /v1/alpha/search with its gateway key. The gateway searches the one chain
// with the count response_length asks for and answers in Codex's
// SearchResponse shape: the output the model reads, marked as outside
// content, the results, and the queries used. The call is recorded on the
// key as a web search. Any command but search_query is refused plainly, as
// a request without a query is; without a key nothing is searched.
func TestCodexsStandaloneSearchIsAnsweredFromTheChain(t *testing.T) {
	t.Parallel()

	var asked string

	g, _ := webGateway(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		asked = string(raw)

		exaFound(w, r)
	})

	status, _, body := g.post(t, "/v1/alpha/search", g.key, codexSearchAsks)
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, asked, `"numResults":3`, "short is three results")

	var answer struct {
		EncryptedOutput *string          `json:"encrypted_output"`
		Output          string           `json:"output"`
		Results         []map[string]any `json:"results"`
		Usage           map[string]any   `json:"usage"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &answer))
	assert.Contains(t, body, `"encrypted_output":null`)
	assert.Nil(t, answer.EncryptedOutput)
	assert.Contains(t, answer.Output, `Search results from exa for "hypomnema":`)
	assert.Contains(t, answer.Output, "<<<EXTERNAL_UNTRUSTED_CONTENT")
	assert.Contains(t, answer.Output, "https://example.com/h")
	assert.Equal(t, []map[string]any{{"title": "Hypomnema", "url": "https://example.com/h", "snippet": "A gateway."}},
		answer.Results)
	assert.Equal(t, map[string]any{"queries_used": float64(1), "search_cost_usd": float64(0)}, answer.Usage)

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.ModelWebSearch, u.Model)
	assert.Equal(t, "exa", u.Provider)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)

	for _, refused := range []struct{ body, says string }{
		{`{"commands":{"search_query":[{"q":"x"}],"open":[{"ref_id":"https://example.com"}]}}`,
			"Unsupported operation for /v1/alpha/search: open"},
		{`{"commands":{"weather":[{"location":"Vietnam, Hanoi"}]}}`, "Unsupported operation for /v1/alpha/search: weather"},
		{`{"commands":{}}`, "commands.search_query must contain at least one query"},
		{`{"commands":{"search_query":[{"q":" "}]}}`, "commands.search_query[0].q is required"},
		{`{"commands":{"search_query":[{"q":"x"}],"response_length":"huge"}}`, "short, medium or long"},
		{`not json`, "Invalid JSON body"},
	} {
		status, _, body := g.post(t, "/v1/alpha/search", g.key, refused.body)
		assert.Equal(t, http.StatusBadRequest, status, refused.body)
		assert.Contains(t, body, refused.says)
	}

	status, _, _ = g.post(t, "/v1/alpha/search", "", codexSearchAsks)
	assert.Equal(t, http.StatusUnauthorized, status)
}
