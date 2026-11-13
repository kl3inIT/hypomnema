package clientcli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// theWeb stands for the control plane's web: it records which chat asked
// what, and fails with err when it is set.
type theWeb struct {
	calls []string
	err   error
}

func (w *theWeb) Search(_ context.Context, chatID, query string, limit int, provider string,
) (websearch.Answer, error) {
	w.calls = append(w.calls, fmt.Sprintf("%s search %q %d %q", chatID, query, limit, provider))

	return websearch.Answer{
		Provider: "parallel", Query: query,
		Results: []websearch.Result{{
			Title: "Go 1.27", URL: "https://go.dev/blog/go1.27", Snippet: "Go 1.27 is out.", PublishedAt: "2026-08-12",
		}},
		Errors: []websearch.Failure{{Provider: "exa", Reason: "public MCP: rate limited (429)"}},
	}, w.err
}

func (w *theWeb) Fetch(_ context.Context, chatID, url string) (websearch.Page, error) {
	w.calls = append(w.calls, fmt.Sprintf("%s fetch %s", chatID, url))

	return websearch.Page{URL: url, Title: "Go", Content: "# Go\n\nA language.", Truncated: true}, w.err
}

// Scenario: a Bot's pi searches the web and reads a page with the CLI. The
// call is its chat's, so that the control plane records it on that chat's
// key. What it prints is what the agent reads: the results between the
// outside-content markers, and which provider answered after which failed.
// --json prints the API's answer.
func TestABotSearchesTheWebAndReadsAPage(t *testing.T) {
	t.Parallel()

	web := &theWeb{}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithWeb(web) })
	ctx := t.Context()

	_, _, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "news")
	require.NoError(t, err)

	piToken, err := w.svc.AddBotToken(ctx, "pi", chat.ID, "coordinator")
	require.NoError(t, err)
	w.loginWith(t, piToken)

	out, err := w.cmd(t, "web", "search", "go", "release", "--max-results", "3", "--provider", "parallel")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, `Search results from parallel for "go release":`), out)
	assert.Contains(t, out, "<<<EXTERNAL_UNTRUSTED_CONTENT")
	assert.Contains(t, out, "1. Go 1.27\n   https://go.dev/blog/go1.27\n   Published: 2026-08-12\n   Go 1.27 is out.")
	assert.Contains(t, out, "Answered by parallel after exa failed (public MCP: rate limited (429)).")

	var answer openapi.WebSearchAnswer

	w.jsonCmd(t, &answer, "", "web", "search", "go")
	assert.Equal(t, "parallel", answer.Provider)
	require.Len(t, answer.Results, 1)
	assert.Equal(t, "https://go.dev/blog/go1.27", answer.Results[0].Url)

	out, err = w.cmd(t, "web", "fetch", "https://go.dev")
	require.NoError(t, err)
	assert.Contains(t, out, "Page https://go.dev\nTitle: Go")
	assert.Contains(t, out, "Source: Web Fetch\n---\n# Go\n\nA language.")
	assert.Contains(t, out, "The page was cut at 20000 characters.")

	assert.Equal(t, []string{
		chat.ID + ` search "go release" 3 "parallel"`, chat.ID + ` search "go" 0 ""`, chat.ID + " fetch https://go.dev",
	}, web.calls)
}

// Scenario: the owner's token searches too, as no chat's. A search every
// provider failed is an error with the reasons; one every provider rests
// for says when to retry; a control plane without the web says so.
func TestAWebSearchThatFailsSaysWhy(t *testing.T) {
	t.Parallel()

	web := &theWeb{}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithWeb(web) })
	w.login(t, bearertoken.Owner)

	web.err = fmt.Errorf("%w: exa (public MCP: answered 500); parallel (public MCP: answered 502)", websearch.ErrFailed)

	_, err := w.cmd(t, "web", "search", "go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exa (public MCP: answered 500)")
	assert.Contains(t, err.Error(), "unavailable")
	assert.Equal(t, ` search "go" 0 ""`, web.calls[0], "the owner's search is no chat's")

	web.err = &websearch.RestingError{Until: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}

	_, err = w.cmd(t, "web", "search", "go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "try again after 2030-01-01T00:00:00Z")

	web.err = fmt.Errorf("%w: 169.254.169.254:80 is the cloud metadata service", websearch.ErrRefused)

	_, err = w.cmd(t, "web", "fetch", "http://169.254.169.254/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")

	none := newWorld(t)
	none.login(t, bearertoken.Owner)

	_, err = none.cmd(t, "web", "fetch", "https://go.dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this control plane has no web search")
}
