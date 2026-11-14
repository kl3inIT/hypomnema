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

// theChain stands for the web search chain and what the owner sets of it.
type theChain struct {
	off     map[string]bool
	searxng string
	key     *vaultOfOne
}

func (c *theChain) Chain(context.Context) ([]websearch.Link, error) {
	return []websearch.Link{
		{Provider: "exa", Source: websearch.SourceKey, On: !c.off["exa"], Usable: c.key.value != ""},
		{Provider: "searxng", Source: websearch.SourceSelfHosted, On: !c.off["searxng"], Usable: c.searxng != "",
			Address: c.searxng},
		{Provider: "exa", Source: websearch.SourcePublic, On: !c.off["exa"], Usable: true},
		{Provider: "parallel", Source: websearch.SourcePublic, On: !c.off["parallel"], Usable: true,
			RestingUntil: time.Date(2030, 1, 1, 9, 30, 0, 0, time.Local)},
	}, nil
}

func (c *theChain) SetSearchProvider(_ context.Context, provider string, on bool) error {
	c.off[provider] = !on

	return nil
}

func (c *theChain) SetSearXNG(_ context.Context, address string) error {
	c.searxng = address

	return nil
}

// vaultOfOne keeps one secret's value.
type vaultOfOne struct {
	name, value string
}

func (v *vaultOfOne) List(context.Context) ([]clientapi.SecretInfo, error) { return nil, nil }

func (v *vaultOfOne) Value(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (v *vaultOfOne) Put(_ context.Context, name string, values map[string]string) (clientapi.SecretInfo, error) {
	v.name, v.value = name, values["value"]

	return clientapi.SecretInfo{Name: name}, nil
}

func (v *vaultOfOne) Delete(context.Context, string) error {
	v.value = ""

	return nil
}

// Scenario: the owner looks after the web search chain from the terminal,
// as on Settings → Gateway → Web search: the providers in their order with
// each one's source and state, a switch, Exa's key read from standard input
// into the vault, and SearXNG's address.
func TestTheOwnerLooksAfterTheWebSearchChain(t *testing.T) {
	t.Parallel()

	vault := &vaultOfOne{}
	chain := &theChain{off: map[string]bool{}, key: vault}
	w := newWorldWith(t, nil, func(s *clientapi.Server) {
		s.WithSecrets(vault).WithWebSearch(chain, "web-search-exa")
	})
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "web", "providers")
	require.NoError(t, err)
	assert.Equal(t, "1\texa\twith your key\tnot set up\n2\tsearxng\tself-hosted\tnot set up\n"+
		"3\texa\tpublic, no key\tready\n4\tparallel\tpublic, no key\tresting until 09:30\n", out)

	out, err = w.cmd(t, "web", "providers", "set", "parallel", "off")
	require.NoError(t, err)
	assert.Equal(t, "web search provider parallel is off\n", out)
	assert.True(t, chain.off["parallel"])

	_, err = w.cmd(t, "web", "providers", "set", "parallel", "maybe")
	require.ErrorIs(t, err, ErrUsage)

	out, err = w.cmdIn(t, "exa-123\n", "web", "exa-key", "set", "--key-file", "-")
	require.NoError(t, err)
	assert.Equal(t, "Exa's key is kept in the vault as web-search-exa\n", out)
	assert.Equal(t, vaultOfOne{name: "web-search-exa", value: "exa-123"}, *vault)

	out, err = w.cmd(t, "web", "searxng", "set", "http://10.0.0.5:8888")
	require.NoError(t, err)
	assert.Equal(t, "SearXNG is http://10.0.0.5:8888\n", out)

	out, err = w.cmd(t, "web", "providers")
	require.NoError(t, err)
	assert.Contains(t, out, "1\texa\twith your key\tready\n2\tsearxng\tself-hosted\tready\n")
	assert.Contains(t, out, "4\tparallel\tpublic, no key\toff\n")

	_, err = w.cmd(t, "web", "exa-key", "rm")
	require.NoError(t, err)
	assert.Empty(t, vault.value)

	_, err = w.cmd(t, "web", "searxng", "rm")
	require.NoError(t, err)
	assert.Empty(t, chain.searxng)
}
