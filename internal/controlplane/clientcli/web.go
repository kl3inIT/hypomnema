package clientcli

import (
	"context"
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
