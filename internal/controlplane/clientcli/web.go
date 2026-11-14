package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

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

// The web search chain from the terminal (issues #354, #393): what Settings →
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

// printChain prints a line a way of asking, in order: its turn, provider,
// source, state and usage; a keyed provider's keys follow it, a line each.
func (e *env) printChain(w *openapi.WebSearch) {
	sources := map[openapi.WebSearchLinkSource]string{
		openapi.Key: "with your keys", openapi.Public: "public, no key", openapi.Subscription: "your subscription",
	}

	for i, l := range w.Chain {
		state := strings.ReplaceAll(string(l.State), "_", " ")
		if l.State == openapi.WebSearchLinkStateResting {
			state = restText(l.Rest, l.RestingUntil)
		}

		e.printf("%d\t%s\t%s\t%s\t%s\n", i+1, l.Provider, sources[l.Source], state, usageText(l.Usage, nil))

		for _, k := range val(l.Keys) {
			state := string(k.State)
			if k.State == openapi.WebSearchKeyStateResting {
				state = restText(k.Rest, k.RestingUntil)
			}

			e.printf("\tkey %s\t%s\t%s\n", k.Name, state, usageText(k.Usage, k.Balance))
		}
	}
}

// restText says why and until when a way rests.
func restText(why *openapi.WebSearchRest, until *time.Time) string {
	words := map[openapi.WebSearchRest]string{
		openapi.RateLimit: "rate limited", openapi.NoCredits: "out of credits",
		openapi.Refused: "refused", openapi.DailyLimit: "at its daily limit",
	}

	out := "resting"
	if why != nil {
		out = words[*why]
	}

	if until != nil {
		out += " until " + until.Local().Format("15:04")
	}

	return out
}

// usageText is what was used: the provider's balance when it tells it, the
// month's spend when it says it, this control plane's calls; never a cap
// that is not known.
func usageText(u openapi.WebSearchUsage, b *openapi.WebSearchBalance) string {
	out := fmt.Sprintf("today %d answered, %d failed; this month %d answered, %d failed",
		u.Today.Answered, u.Today.Failed, u.Month.Answered, u.Month.Failed)

	if u.MonthDollars != nil {
		out += fmt.Sprintf("; $%.3f this month", *u.MonthDollars)
	}

	if b != nil {
		return out + fmt.Sprintf("; %g of %g credits left", b.Remaining, b.Limit)
	}

	return out + "; allowance unknown"
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

func webKeysAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider := fs.Arg(0)

	file := str(fs, "key-file")
	if file == "" {
		return errors.New("pass --key-file with the file holding the key, or - to read it from standard input")
	}

	key, err := e.read(file)
	if err != nil {
		return err
	}

	key = strings.TrimSpace(key)

	resp, err := e.api.AddWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProvider(provider),
		openapi.WebSearchNewKey{Key: &key})
	if err != nil {
		return fmt.Errorf("keeping %s's key: %w", provider, err)
	}

	if resp.JSON201 == nil {
		return apiError("keeping "+provider+"'s key", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON201, func() {
		e.printf("%s's key is kept in the vault as %s\n", provider, resp.JSON201.Name)
	})
}

func webKeysRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	provider, name := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.RemoveWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProvider(provider), name)
	if err != nil {
		return fmt.Errorf("removing %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("removing "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s was removed from the vault\n", name) })
}

func webOrder(ctx context.Context, e *env, fs *flag.FlagSet) error {
	var body openapi.WebSearchOrder
	for _, p := range fs.Args() {
		body.Providers = append(body.Providers, openapi.WebSearchKeyedProvider(p))
	}

	resp, err := e.api.SetWebSearchOrderWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("ordering the keyed providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("ordering the keyed providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printChain(resp.JSON200) })
}
