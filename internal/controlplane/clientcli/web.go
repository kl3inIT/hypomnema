package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The web from the terminal (issue #354): a Bot searches the web and reads a
// page with these, as 9router's agents call its /v1/search from a skill
// (skills/9router-web-search/SKILL.md). The text they print is what the
// agent reads, the results marked as outside content (OpenClaw's
// EXTERNAL_UNTRUSTED_CONTENT); --json prints the answer as the API gives it.

func webSearch(ctx context.Context, e *env, fs *flag.FlagSet) error {
	body := openapi.WebSearchRequest{Query: strings.Join(fs.Args(), " ")}

	if n := number(fs, "max-results"); n > 0 {
		body.MaxResults = &n
	}

	if p := str(fs, "provider"); p != "" {
		provider := openapi.WebSearchRequestProvider(p)
		body.Provider = &provider
	}

	resp, err := e.api.SearchWebWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("searching the web: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("searching the web", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s\n", answerOf(resp.JSON200).Text()) })
}

func webFetch(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.FetchWebWithResponse(ctx, openapi.WebFetchRequest{Url: fs.Arg(0)})
	if err != nil {
		return fmt.Errorf("reading the page: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the page", resp.StatusCode(), resp.JSONDefault)
	}

	p := resp.JSON200

	return e.print(fs, p, func() {
		e.printf("%s\n", websearch.Page{URL: p.Url, Title: p.Title, Content: p.Content, Truncated: p.Truncated}.Text())
	})
}

// answerOf is the API's answer as the search package words it for an agent.
func answerOf(a *openapi.WebSearchAnswer) websearch.Answer {
	out := websearch.Answer{Provider: a.Provider, Query: a.Query, Cached: a.Cached}

	for _, r := range a.Results {
		out.Results = append(out.Results, websearch.Result{
			Title: r.Title, URL: r.Url, Snippet: r.Snippet, PublishedAt: val(r.PublishedAt),
		})
	}

	for _, f := range val(a.Errors) {
		out.Errors = append(out.Errors, websearch.Failure{Provider: f.Provider, Reason: f.Message})
	}

	return out
}

// The web search chain from the terminal (issue #354): what Settings →
// Gateway → Web search shows and changes, as every page of the web client
// has its commands.

func webProviders(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetWebSearchWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the web search providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the web search providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printChain(resp.JSON200) })
}

// printChain prints a line a way of asking, in order: its turn, provider, source and state.
func (e *env) printChain(w *openapi.WebSearch) {
	sources := map[openapi.WebSearchLinkSource]string{
		openapi.Key: "with your key", openapi.SelfHosted: "self-hosted", openapi.Public: "public, no key",
		openapi.Subscription: "your subscription",
	}

	for i, l := range w.Chain {
		state := string(l.State)
		if l.State == openapi.WebSearchLinkStateResting && l.RestingUntil != nil {
			state = "resting until " + l.RestingUntil.Local().Format("15:04")
		}

		state = strings.ReplaceAll(state, "_", " ")
		e.printf("%d\t%s\t%s\t%s\n", i+1, l.Provider, sources[l.Source], state)
	}
}

func webProvidersSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, state := fs.Arg(0), fs.Arg(1)
	if state != "on" && state != "off" {
		return fmt.Errorf("%w: web providers set <provider> on|off", ErrUsage)
	}

	resp, err := e.api.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProvider(provider),
		openapi.WebSearchSwitch{On: state == "on"})
	if err != nil {
		return fmt.Errorf("switching %s: %w", provider, err)
	}

	if resp.JSON200 == nil {
		return apiError("switching "+provider, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("web search provider %s is %s\n", provider, state) })
}

func webExaKeySet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	file := str(fs, "key-file")
	if file == "" {
		return errors.New("pass --key-file with the file holding the key, or - to read it from standard input")
	}

	key, err := e.read(file)
	if err != nil {
		return err
	}

	key = strings.TrimSpace(key)

	resp, err := e.api.PutWebSearchExaKeyWithResponse(ctx, openapi.WebSearchExaKey{Key: &key})
	if err != nil {
		return fmt.Errorf("keeping Exa's key: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("keeping Exa's key", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		e.printf("Exa's key is kept in the vault as %s\n", resp.JSON200.ExaSecret)
	})
}

func webExaKeyRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.DeleteWebSearchExaKeyWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("removing Exa's key: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("removing Exa's key", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("Exa has no key; it is asked through its public MCP\n") })
}

func webSearXNGSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.PutWebSearchSearXNGWithResponse(ctx, openapi.WebSearchSearXNG{Url: fs.Arg(0)})
	if err != nil {
		return fmt.Errorf("setting SearXNG's address: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("setting SearXNG's address", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("SearXNG is %s\n", val(resp.JSON200.SearxngUrl)) })
}

func webSearXNGRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.DeleteWebSearchSearXNGWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("forgetting SearXNG's address: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("forgetting SearXNG's address", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("no SearXNG is set\n") })
}
