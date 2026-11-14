package clientcli

import (
	"context"
	"fmt"
	"maps"
	"slices"
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

// theChain stands for the web search chain and what the owner sets of it:
// its Exa keys are the vault's secrets.
type theChain struct {
	off   map[string]bool
	order []string
	vault *vaultOfKeys
}

func (c *theChain) Chain(context.Context) ([]websearch.Link, error) {
	exa := websearch.Link{Provider: "exa", Source: websearch.SourceKey, On: !c.off["exa"], Keys: []websearch.KeyState{}}

	for _, name := range c.vault.names() {
		exa.Usable = true
		exa.Keys = append(exa.Keys, websearch.KeyState{Name: name, Usage: websearch.Count{
			Today: websearch.Tally{Answered: 2}, Month: websearch.Tally{Answered: 9, Failed: 1}, MonthDollars: 0.045,
		}})
	}

	tavily := websearch.Link{Provider: "tavily", Source: websearch.SourceKey, On: !c.off["tavily"], Usable: true,
		Keys: []websearch.KeyState{{
			Name: "web-search-tavily", RestingUntil: time.Date(2030, 1, 1, 10, 0, 0, 0, time.Local),
			Rest: websearch.RestNoCredits, Balance: &websearch.Balance{Used: 1000, Limit: 1000},
		}},
		RestingUntil: time.Date(2030, 1, 1, 10, 0, 0, 0, time.Local), Rest: websearch.RestNoCredits,
	}

	keyed := []websearch.Link{exa, tavily}
	if len(c.order) > 0 && c.order[0] == "tavily" {
		keyed = []websearch.Link{tavily, exa}
	}

	return append(keyed,
		websearch.Link{Provider: "parallel", Source: websearch.SourcePublic, On: !c.off["parallel"], Usable: true,
			RestingUntil: time.Date(2030, 1, 1, 9, 30, 0, 0, time.Local), Rest: websearch.RestRateLimited},
	), nil
}

func (c *theChain) SetSearchProvider(_ context.Context, provider string, on bool) error {
	c.off[provider] = !on

	return nil
}

func (c *theChain) SetSearchOrder(_ context.Context, providers []string) error {
	c.order = providers

	return nil
}

// vaultOfKeys keeps secrets' values by name.
type vaultOfKeys struct {
	values map[string]string
}

func (v *vaultOfKeys) names() []string {
	return slices.Sorted(maps.Keys(v.values))
}

func (v *vaultOfKeys) List(context.Context) ([]clientapi.SecretInfo, error) {
	var out []clientapi.SecretInfo
	for _, name := range v.names() {
		out = append(out, clientapi.SecretInfo{Name: name})
	}

	return out, nil
}

func (v *vaultOfKeys) Value(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (v *vaultOfKeys) Put(_ context.Context, name string, values map[string]string) (clientapi.SecretInfo, error) {
	v.values[name] = values["value"]

	return clientapi.SecretInfo{Name: name}, nil
}

func (v *vaultOfKeys) Delete(_ context.Context, name string) error {
	delete(v.values, name)

	return nil
}

// Scenario: the owner looks after the web search chain from the terminal,
// as on Settings → Gateway → Web search: the providers in their order with
// each one's source, state and usage, each key under its provider; a
// switch; a second Exa key read from standard input into the vault, then
// the first removed; and the keyed providers put in another order.
func TestTheOwnerLooksAfterTheWebSearchChain(t *testing.T) {
	t.Parallel()

	vault := &vaultOfKeys{values: map[string]string{"web-search-exa": "exa-1"}}
	chain := &theChain{off: map[string]bool{}, vault: vault}
	w := newWorldWith(t, nil, func(s *clientapi.Server) {
		s.WithSecrets(vault).WithWebSearch(chain)
	})
	w.login(t, bearertoken.Owner)

	none := "today 0 answered, 0 failed; this month 0 answered, 0 failed"

	out, err := w.cmd(t, "web", "providers")
	require.NoError(t, err)
	assert.Equal(t, "1\texa\twith your keys\tready\t"+none+"; allowance unknown\n"+
		"\tkey web-search-exa\tready\ttoday 2 answered, 0 failed; this month 9 answered, 1 failed; "+
		"$0.045 this month; allowance unknown\n"+
		"2\ttavily\twith your keys\tout of credits until 10:00\t"+none+"; allowance unknown\n"+
		"\tkey web-search-tavily\tout of credits until 10:00\t"+none+"; 0 of 1000 credits left\n"+
		"3\tparallel\tpublic, no key\trate limited until 09:30\t"+none+"; allowance unknown\n", out)

	out, err = w.cmd(t, "web", "providers", "set", "parallel", "off")
	require.NoError(t, err)
	assert.Equal(t, "web search provider parallel is off\n", out)
	assert.True(t, chain.off["parallel"])

	_, err = w.cmd(t, "web", "providers", "set", "parallel", "maybe")
	require.ErrorIs(t, err, ErrUsage)

	out, err = w.cmdIn(t, "exa-2\n", "web", "keys", "add", "exa", "--key-file", "-")
	require.NoError(t, err)
	assert.Equal(t, "exa's key is kept in the vault as web-search-exa-2\n", out)
	assert.Equal(t, map[string]string{"web-search-exa": "exa-1", "web-search-exa-2": "exa-2"}, vault.values)

	out, err = w.cmd(t, "web", "keys", "rm", "exa", "web-search-exa")
	require.NoError(t, err)
	assert.Equal(t, "web-search-exa was removed from the vault\n", out)
	assert.Equal(t, map[string]string{"web-search-exa-2": "exa-2"}, vault.values)

	_, err = w.cmd(t, "web", "keys", "rm", "tavily", "web-search-exa-2")
	require.Error(t, err, "a key is removed under its own provider")

	out, err = w.cmd(t, "web", "order", "tavily", "exa", "firecrawl", "parallel", "tinyfish")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "1\ttavily\t"), out)
	assert.Equal(t, []string{"tavily", "exa", "firecrawl", "parallel", "tinyfish"}, chain.order)
}
