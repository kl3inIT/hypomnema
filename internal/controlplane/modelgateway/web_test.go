package modelgateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// webGateway is a gateway whose web search asks one fake provider, Exa's
// MCP, which answers as exaMCP does; and whose fetch may read
// the page server the test runs.
func webGateway(t *testing.T, exaMCP http.HandlerFunc) (chatGateway, *httptest.Server) {
	t.Helper()

	exa := httptest.NewServer(exaMCP)
	t.Cleanup(exa.Close)

	parallel := httptest.NewServer(plain(http.StatusTooManyRequests, `{"error":"slow down"}`))
	t.Cleanup(parallel.Close)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = io.WriteString(w, "# Hello\n\nFrom the page.")
	}))
	t.Cleanup(page.Close)

	pageURL, err := url.Parse(page.URL)
	require.NoError(t, err)

	allow, err := egress.ParseAllow([]string{pageURL.Host})
	require.NoError(t, err)

	// Only Exa's MCP is on among the keyless providers, so that the ring's moving start cannot change who answers;
	// the others are pointed at the 429 server, so no test reaches the internet.
	off := websearch.Settings{Off: []string{"parallel", "firecrawl", "tavily", "you"}}
	web := websearch.New(websearch.Config{
		ExaMCPURL: exa.URL, ParallelMCPURL: parallel.URL, TavilyURL: parallel.URL, FirecrawlURL: parallel.URL,
		YouMCPURL: parallel.URL, Rules: egress.Rules{Allow: allow},
		Settings: func(context.Context) (websearch.Settings, error) { return off, nil },
	})
	upstream, _ := provider(t, plain(http.StatusOK, answer))

	return newChatGateway(t, modelgateway.Config{Web: web}, upstream), page
}

func exaFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":`+
		`"Title: Hypomnema\nURL: https://example.com/h\nPublished: 2026-11-01\nText: A gateway."}]}}`)
}

func (g chatGateway) post(t *testing.T, path, key, body string) (int, http.Header, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)

	return rec.Code, rec.Header(), rec.Body.String()
}

// Scenario: a client searches with a gateway key that may use one chat
// model only: any key the gateway knows may search. The answer is in one
// shape whatever provider gave it, and the search is recorded on the key as
// model web/search with the provider that answered. A page is read the
// same way and recorded as web/fetch. Without a key nothing is asked.
func TestAKeySearchesTheWebAndTheCallIsRecorded(t *testing.T) {
	t.Parallel()

	g, page := webGateway(t, exaFound)

	status, _, body := g.post(t, "/v1/search", g.key, `{"query":"hypomnema","max_results":3}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `{"provider":"exa","query":"hypomnema","cached":false,"results":[
		{"title":"Hypomnema","url":"https://example.com/h","snippet":"A gateway.","published_at":"2026-11-01"}]}`, body)

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.ModelWebSearch, u.Model)
	assert.Equal(t, "exa", u.Provider)
	assert.Equal(t, "pi", u.KeyName)
	assert.Equal(t, modelgateway.OutcomeOK, u.Outcome)

	status, _, body = g.post(t, "/v1/web/fetch", g.key, `{"url":"`+page.URL+`/hello"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `{"url":"`+page.URL+`/hello","title":"","content":"# Hello\n\nFrom the page.","truncated":false}`,
		body)
	assert.Equal(t, modelgateway.ModelWebFetch, g.lastUsage(t).Model)

	status, _, _ = g.post(t, "/v1/search", "", `{"query":"hypomnema"}`)
	assert.Equal(t, http.StatusUnauthorized, status)
}

// Scenario: the gateway's refusals of a web call are OpenAI's errors: an
// empty query or an unknown provider 400, a private address 403, every
// provider failing 502 with each one's reason, and every provider resting
// 429 with when to retry. Each failure is recorded on the key.
func TestAWebCallThatFailsSaysWhy(t *testing.T) {
	t.Parallel()

	g, _ := webGateway(t, plain(http.StatusTooManyRequests, `{"error":"slow down"}`))

	for _, body := range []string{`{"query":" "}`, `{"query":"x","provider":"google"}`, `not json`} {
		status, _, answer := g.post(t, "/v1/search", g.key, body)
		assert.Equal(t, http.StatusBadRequest, status, body)
		assert.Contains(t, answer, `"type":"invalid_request_error"`)
	}

	status, _, answer := g.post(t, "/v1/web/fetch", g.key, `{"url":"http://169.254.169.254/latest/meta-data/"}`)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, answer, "address_refused")

	status, _, answer = g.post(t, "/v1/search", g.key, `{"query":"anything"}`)
	assert.Equal(t, http.StatusBadGateway, status)

	var failed struct {
		Error struct{ Code, Message string } `json:"error"`
	}

	require.NoError(t, json.Unmarshal([]byte(answer), &failed))
	assert.Equal(t, "upstream_failed", failed.Error.Code)
	assert.Contains(t, failed.Error.Message, "exa (public MCP: rate limited (429))")

	u := g.lastUsage(t)
	assert.Equal(t, modelgateway.ModelWebSearch, u.Model)
	assert.Equal(t, modelgateway.OutcomeUpstreamError, u.Outcome)
	assert.Contains(t, u.Error, "rate limited")

	status, header, _ := g.post(t, "/v1/search", g.key, `{"query":"again"}`)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.NotEmpty(t, header.Get("Retry-After"))
	assert.Equal(t, http.StatusTooManyRequests, g.lastUsage(t).Status)
}

// A gateway the control plane gave no web search says so.
func TestAGatewayWithoutWebSearchSaysSo(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	status, _, body := g.post(t, "/v1/search", g.key, `{"query":"x"}`)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, "web_unavailable")
}

// A Dispatch's key, which its web calls are recorded on, is its newest that
// works; a settled Dispatch has none.
func TestADispatchsKeyIsItsNewestThatWorks(t *testing.T) {
	t.Parallel()

	g := newGateway(t)
	ctx := t.Context()

	_, err := g.ApplyModel(ctx, solModel)
	require.NoError(t, err)

	_, err = g.DispatchKey(ctx, "dsp_1")
	require.ErrorIs(t, err, modelgateway.ErrNotFound)

	first, err := g.AddDispatchKey(ctx, "dsp_1", "gpt-sol")
	require.NoError(t, err)

	k, err := g.DispatchKey(ctx, "dsp_1")
	require.NoError(t, err)

	byFirst, err := g.Authenticate(ctx, first)
	require.NoError(t, err)
	assert.Equal(t, byFirst.ID, k.ID)
	assert.Equal(t, "dsp_1", k.DispatchID)

	require.NoError(t, g.RevokeDispatchKeys(ctx, "dsp_1"))

	_, err = g.DispatchKey(ctx, "dsp_1")
	require.ErrorIs(t, err, modelgateway.ErrNotFound)
}
