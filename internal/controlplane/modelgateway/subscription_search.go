package modelgateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/antigravity"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/claude"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// A subscription's own web search as a search provider (issue #354, owner
// 2026-11-13): the web search package orders the providers, and asks a
// Codex, Claude or Antigravity account through the calls the gateway already
// makes to them, with the account's token, its refresh after a refusal and
// the accounts taken in turn (underAccounts). What each asks and reads is its
// package's (codex/search.go, claude/search.go, antigravity/search.go).
//
// A search never changes what chat knows of an account: a limit or a refusal
// it meets cools nothing down and parks nothing, and an answer clears no
// failure. OmniRoute keeps a search's 429 on the search alone
// (open-sse/handlers/search/borrowedKeyLock.ts, codexSearch.ts coolDown) and
// says "search success is not evidence that the shared Codex chat account
// recovered". The web search package rests the provider instead.

// maxSearchAnswer bounds a subscription's search answer, OmniRoute's
// CODEX_SEARCH_MAX_BYTES.
const maxSearchAnswer = 4 << 20

// SubscriptionSearch is the search of the accounts logged in on the control
// plane, for the web search package.
func (s *Server) SubscriptionSearch() websearch.Subscriptions { return subscriptionSearch{s} }

type subscriptionSearch struct{ s *Server }

// searchAttempt is a search under one account: its results, or the refusal
// that stands for its failure in OpenAI's shape.
type searchAttempt func(token AccountToken) ([]websearch.Result, *http.Response, error)

func (g subscriptionSearch) Search(ctx context.Context, provider, query string, limit int) ([]websearch.Result, error) {
	s := g.s
	if s.accounts == nil {
		return nil, websearch.ErrNoAccount
	}

	var (
		runtime string
		attempt searchAttempt
	)

	switch provider {
	case websearch.ProviderCodex:
		runtime = codexRuntime
		attempt = func(token AccountToken) ([]websearch.Result, *http.Response, error) {
			return s.codexSearch(ctx, token, query, limit)
		}
	case websearch.ProviderClaude:
		runtime = claudeAccountRuntime
		attempt = func(token AccountToken) ([]websearch.Result, *http.Response, error) {
			return s.claudeSearch(ctx, token, query, limit)
		}
	case websearch.ProviderAntigravity:
		runtime = antigravityRuntime
		attempt = func(token AccountToken) ([]websearch.Result, *http.Response, error) {
			return s.antigravitySearch(ctx, token, query, limit)
		}
	default:
		return nil, fmt.Errorf("%w: no subscription search %q", websearch.ErrInvalid, provider)
	}

	return s.searchUnder(ctx, runtime, attempt)
}

// searchUnder runs a search under the accounts of a runtime in their order,
// the next one asked when one is refused as chat moves on. A refusal is
// given to the web search package with its status and wait, for its rest.
func (s *Server) searchUnder(ctx context.Context, runtime string, attempt searchAttempt) ([]websearch.Result, error) {
	var found []websearch.Result

	resp, err := s.underAccounts(ctx, runtime, "", nil, func(token AccountToken) (*http.Response, error) {
		results, refusal, err := attempt(token)
		if err != nil || refusal != nil {
			return refusal, err
		}

		found = results

		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	switch {
	case errors.Is(err, ErrNoAccount):
		return nil, fmt.Errorf("%w: %w", websearch.ErrNoAccount, err)
	case err != nil:
		return nil, err
	}

	defer resp.Body.Close() //nolint:errcheck // The body is made here or was read; closing adds nothing.

	if resp.StatusCode == http.StatusOK {
		return found, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))

	var wait time.Duration
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		wait = time.Duration(secs) * time.Second
	}

	return nil, websearch.Refused(resp.StatusCode, wait, errorText(body))
}

// readSearch reads a search's answer, within maxSearchAnswer.
func readSearch(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchAnswer+1))
	if err != nil {
		return nil, fmt.Errorf("reading the search: %w", err)
	}

	if len(raw) > maxSearchAnswer {
		return nil, fmt.Errorf("the search's answer is larger than %d bytes", maxSearchAnswer)
	}

	return raw, nil
}

// codexSearch searches under a Codex account, through the call chat makes.
func (s *Server) codexSearch(
	ctx context.Context, token AccountToken, query string, limit int,
) ([]websearch.Result, *http.Response, error) {
	resp, err := s.codexCall(ctx, &token, codex.Request{Body: codex.SearchBody(query, limit)})
	if err != nil {
		return nil, nil, err
	}

	now := s.svc.clock.Now()
	status := resp.StatusCode
	retry := resp.Header.Get("Retry-After")

	raw, err := readSearch(resp)
	if err != nil {
		return nil, nil, err
	}

	if status != http.StatusOK {
		failure := codex.ParseFailure(status, raw)
		if secs, err := strconv.Atoi(retry); err == nil && secs > 0 && failure.ResetsAt.IsZero() {
			failure.ResetsAt = now.Add(time.Duration(secs) * time.Second)
		}

		return nil, codexRefusal(failure, now), nil
	}

	found, failure := codex.ParseSearch(raw, limit)
	if failure != nil {
		return nil, codexRefusal(failure, now), nil
	}

	out := make([]websearch.Result, 0, len(found))
	for _, r := range found {
		out = append(out, websearch.Result{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
	}

	return out, nil, nil
}

// claudeSearch searches under a Claude account, with the headers chat sends
// and omp's search beta.
func (s *Server) claudeSearch(
	ctx context.Context, token AccountToken, query string, limit int,
) ([]websearch.Result, *http.Response, error) {
	send := func(token AccountToken) (*http.Response, error) {
		body, err := claude.SearchBody(query, token.AccessToken, "")
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.claudeURL+"/v1/messages?beta=true",
			bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("building the search to Claude: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", claudeUserAgent)
		req.Header.Set("Anthropic-Version", claudeAnthropicVersion)
		req.Header.Set("Anthropic-Beta", mergeBetas(claudeBeta, claude.SearchBeta))
		req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
		req.Header.Set("X-App", claudeApp)
		req.Header.Set("X-Stainless-Retry-Count", "0")

		return s.claudeDo(token, req)
	}

	resp, err := send(token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		drain(resp)

		if token, err = s.accounts.TokenAfterRefusal(ctx, token); err == nil {
			resp, err = send(token)
		}
	}

	if err != nil {
		return nil, nil, err
	}

	now := s.svc.clock.Now()
	resetsAt := claudeResetsAt(resp, now)
	status := resp.StatusCode

	raw, err := readSearch(resp)
	if err != nil {
		return nil, nil, err
	}

	if status != http.StatusOK {
		failure := claude.ParseFailure(status, raw)
		failure.ResetsAt = resetsAt

		return nil, claudeRefusal(failure, now), nil
	}

	found, err := claude.ParseSearch(raw, limit)
	if err != nil {
		return nil, nil, err
	}

	out := make([]websearch.Result, 0, len(found))
	for _, r := range found {
		out = append(out, websearch.Result{Title: r.Title, URL: r.URL, Snippet: r.Snippet, PublishedAt: r.PublishedAt})
	}

	return out, nil, nil
}

// antigravitySearch searches under an Antigravity account, with the IDE's
// user agent chat sends, not streamed (9router's generateContent).
func (s *Server) antigravitySearch(
	ctx context.Context, token AccountToken, query string, limit int,
) ([]websearch.Result, *http.Response, error) {
	if token.Project == "" {
		// 9router refuses it the same way: the backend refuses a request without one.
		return nil, openAIError(http.StatusBadRequest, typeInvalidRequest, "no_project",
			"the Antigravity account has no project; log it in again", 0), nil
	}

	send := func(token AccountToken) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			s.antigravityURL+"/"+cloudcode.APIVersion+":generateContent",
			bytes.NewReader(antigravity.SearchBody(query, token.Project)))
		if err != nil {
			return nil, fmt.Errorf("building the search to Antigravity: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", s.antigravity.identity.UserAgent(ctx))

		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling Antigravity: %w", err)
		}

		return resp, nil
	}

	resp, err := send(token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		drain(resp)

		if token, err = s.accounts.TokenAfterRefusal(ctx, token); err == nil {
			resp, err = send(token)
		}
	}

	if err != nil {
		return nil, nil, err
	}

	status := resp.StatusCode

	raw, err := readSearch(resp)
	if err != nil {
		return nil, nil, err
	}

	if status != http.StatusOK {
		f := antigravity.ParseFailure(status, raw)

		kind := typeServer
		if f.Status == http.StatusTooManyRequests {
			kind = typeRateLimit
		}

		return nil, openAIError(f.Status, kind, f.Code, f.Message, f.RetryAfter), nil
	}

	found, err := antigravity.ParseSearch(raw, limit)
	if err != nil {
		return nil, nil, err
	}

	out := make([]websearch.Result, 0, len(found))
	for _, r := range found {
		out = append(out, websearch.Result{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
	}

	return out, nil, nil
}
