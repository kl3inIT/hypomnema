package clientapi

import (
	"context"
	"errors"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The web's search and a page's reading for any token (issue #354), a Bot's
// above all: a Bot has no tool of its own for the web and uses
// `hypomnema web search` and `hypomnema web fetch`, as 9router teaches agents
// its /v1/search with a skill (skills/9router-web-search/SKILL.md). The
// answer is the model gateway's /v1/search and /v1/web/fetch in this API's
// shape.

// Web searches the web and reads pages. chatID is the chat of the Bot's pi
// that asks, empty for another token: the control plane's wiring records the
// call on that chat's gateway key.
type Web interface {
	Search(ctx context.Context, chatID, query string, limit int, provider string) (websearch.Answer, error)
	Fetch(ctx context.Context, chatID, url string) (websearch.Page, error)
}

// errNoWeb is a control plane that wired no web search.
var errNoWeb = errors.New("this control plane has no web search")

// WithWeb gives the API the web's search and pages.
func (s *Server) WithWeb(w Web) *Server {
	s.web = w

	return s
}

// chatOfCaller is the chat a Bot's pi token is bound to; empty for any other
// token, a Routine's script's included.
func chatOfCaller(ctx context.Context) string {
	if tok := principal(ctx); tok.Kind == bearertoken.Bot {
		return tok.Chat
	}

	return ""
}

// SearchWeb searches the web.
func (s *Server) SearchWeb(ctx context.Context, req openapi.SearchWebRequestObject,
) (openapi.SearchWebResponseObject, error) {
	if s.web == nil {
		return nil, errNoWeb
	}

	provider := ""
	if req.Body.Provider != nil {
		provider = string(*req.Body.Provider)
	}

	a, err := s.web.Search(ctx, chatOfCaller(ctx), req.Body.Query, val(req.Body.MaxResults), provider)
	if err != nil {
		return nil, err
	}

	out := openapi.SearchWeb200JSONResponse{
		Provider: a.Provider, Query: a.Query, Cached: a.Cached,
		Results: make([]openapi.WebSearchResult, 0, len(a.Results)),
	}

	for _, r := range a.Results {
		res := openapi.WebSearchResult{Title: r.Title, Url: r.URL, Snippet: r.Snippet}
		if r.PublishedAt != "" {
			res.PublishedAt = ptr(r.PublishedAt)
		}

		out.Results = append(out.Results, res)
	}

	if len(a.Errors) > 0 {
		failures := make([]openapi.WebSearchError, 0, len(a.Errors))
		for _, f := range a.Errors {
			failures = append(failures, openapi.WebSearchError{Provider: f.Provider, Message: f.Reason})
		}

		out.Errors = &failures
	}

	return out, nil
}

// FetchWeb reads a public page as markdown.
func (s *Server) FetchWeb(ctx context.Context, req openapi.FetchWebRequestObject,
) (openapi.FetchWebResponseObject, error) {
	if s.web == nil {
		return nil, errNoWeb
	}

	p, err := s.web.Fetch(ctx, chatOfCaller(ctx), req.Body.Url)
	if err != nil {
		return nil, err
	}

	return openapi.FetchWeb200JSONResponse{Url: p.URL, Title: p.Title, Content: p.Content, Truncated: p.Truncated}, nil
}

// webCode is the code of the web's errors; ok is false for any other error.
func webCode(err error) (openapi.ErrorCode, bool) {
	var resting *websearch.RestingError

	switch {
	case errors.As(err, &resting):
		return openapi.RateLimited, true
	case errors.Is(err, websearch.ErrInvalid):
		return openapi.InvalidRequest, true
	case errors.Is(err, websearch.ErrRefused):
		return openapi.Forbidden, true
	case errors.Is(err, websearch.ErrFailed), errors.Is(err, websearch.ErrUnreadable), errors.Is(err, errNoWeb):
		return openapi.Unavailable, true
	}

	return "", false
}

// restingWait is how long a search refused because every provider rests
// should wait, for its Retry-After.
func (s *Server) restingWait(err error) (string, bool) {
	var resting *websearch.RestingError
	if !errors.As(err, &resting) {
		return "", false
	}

	return retryAfter(resting.Until.Sub(s.now())), true
}
