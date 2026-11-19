package modelgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The web's search and a page's reading (issue #354), served with any key the
// gateway knows that is not limited to other kinds (issue #664), as OmniRoute serves POST /v1/search and POST /v1/web/fetch
// (open-sse/handlers/search.ts, src/app/api/v1/web/fetch/route.ts) and 9router
// its /v1/search: the provider answers in one shape whatever it is. Each call
// is recorded on the key that made it, as OmniRoute records a search's cost
// on its key (recordSearchUsageCost), with the model web/search or web/fetch
// and the provider that answered, so it shows on the Usage page.

// The models a web call is recorded under.
const (
	ModelWebSearch = "web/search"
	ModelWebFetch  = "web/fetch"
)

// maxWebRequestBytes bounds a web call's body: a query or an address.
const maxWebRequestBytes = 64 << 10

const typeRateLimit = "rate_limit_error"

// WebCall is one search or one page read, to be recorded on a key.
type WebCall struct {
	KeyID string
	// Model is ModelWebSearch or ModelWebFetch.
	Model string
	// Provider is the search provider that answered; empty for a page, or
	// for a search none answered.
	Provider string
	// Attempts is how many providers were tried, 1 for a page.
	Attempts int
	// Err is why the call failed; nil when it was answered.
	Err      error
	Duration time.Duration
}

// RecordWebCall records a web call on its key, as a request the gateway
// served, a search answered counting one search: a failure is an upstream error, a search refused because every
// provider rests is a 429, and a caller that left is a client gone.
func (s *Service) RecordWebCall(ctx context.Context, c WebCall) error {
	u := Usage{
		KeyID: c.KeyID, Model: c.Model, Provider: c.Provider, Attempts: max(c.Attempts, 1), Outcome: OutcomeOK,
		Status: http.StatusOK, Duration: c.Duration,
	}

	if c.Model == ModelWebSearch && c.Err == nil {
		// A search answered is one search, priced by the provider's query (issue #723).
		u.WebSearches = 1
	}

	if c.Err != nil {
		u.Outcome, u.Status, u.Error = OutcomeUpstreamError, 0, c.Err.Error()

		var resting *websearch.RestingError

		switch {
		case errors.As(c.Err, &resting):
			u.Status = http.StatusTooManyRequests
		case errors.Is(c.Err, context.Canceled):
			u.Outcome = OutcomeClientGone
		}
	}

	return s.recordUsage(ctx, u)
}

// DispatchKey is the key a Dispatch's worker uses now, so that what it does
// through the control plane is recorded with what it asked the gateway; an
// error wrapping ErrNotFound when the Dispatch has none that works.
func (s *Service) DispatchKey(ctx context.Context, dispatchID string) (Key, error) {
	row, err := store.New(s.db).GetLiveGatewayKeyOfDispatch(ctx, sql.NullString{String: dispatchID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, fmt.Errorf("%w: dispatch %s has no gateway key", ErrNotFound, dispatchID)
	}

	if err != nil {
		return Key{}, fmt.Errorf("reading the key of dispatch %s: %w", dispatchID, err)
	}

	return keyFromRow(row)
}

// ChatKey is the newest key of a chat's pi, working or not (issue #723): a
// voice message a Channel hears before the chat's pi starts is recorded on
// it. An error wrapping ErrNotFound when the chat never had one.
func (s *Service) ChatKey(ctx context.Context, chatID string) (Key, error) {
	row, err := store.New(s.db).GetLatestGatewayKeyOfChat(ctx, sql.NullString{String: chatID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, fmt.Errorf("%w: chat %s has no gateway key", ErrNotFound, chatID)
	}

	if err != nil {
		return Key{}, fmt.Errorf("reading the key of chat %s: %w", chatID, err)
	}

	return keyFromRow(row)
}

// ChatUsageKey is the key a chat's use outside its pi is recorded on (issue
// #723): its newest, working or not, as ChatKey; or, for a chat that never had
// one, a key made for the chat and its Bot and revoked at once, so that the
// use counts under the Bot and the key is never usable. Neither is made while
// another is: the chat's keys are read and the key made in one transaction,
// and no live key is revoked, so a pi starting meanwhile keeps its own.
func (s *Service) ChatUsageKey(ctx context.Context, chatID, botID string) (Key, error) {
	if chatID == "" {
		return Key{}, fmt.Errorf("%w: a chat's key needs the chat", ErrInvalid)
	}

	return s.agentUsageKey(ctx, keyOwner{chatID: chatID, botID: botID}, "chat")
}

func (s *Service) DispatchUsageKey(ctx context.Context, dispatchID string) (Key, error) {
	if dispatchID == "" {
		return Key{}, fmt.Errorf("%w: a dispatch's key needs the dispatch", ErrInvalid)
	}

	return s.agentUsageKey(ctx, keyOwner{dispatchID: dispatchID}, "dispatch")
}

func (s *Service) agentUsageKey(ctx context.Context, owner keyOwner, name string) (Key, error) {
	secret, err := bearertoken.Generate(bearertoken.Gateway)
	if err != nil {
		return Key{}, err
	}

	models, err := json.Marshal([]string{AllModels})
	if err != nil {
		return Key{}, fmt.Errorf("encoding the key's models: %w", err)
	}

	var id string

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		chat := sql.NullString{String: owner.chatID, Valid: owner.chatID != ""}
		dispatch := sql.NullString{String: owner.dispatchID, Valid: owner.dispatchID != ""}
		var row store.GatewayKey
		var err error
		if dispatch.Valid {
			row, err = q.GetLatestGatewayKeyOfDispatch(ctx, dispatch)
		} else {
			row, err = q.GetLatestGatewayKeyOfChat(ctx, chat)
		}

		switch {
		case err == nil:
			id = row.ID

			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("reading the %s usage key: %w", name, err)
		}

		id = s.newID("gwk")

		if err := q.InsertGatewayKey(ctx, store.InsertGatewayKeyParams{
			ID: id, Name: strings.ToLower(strings.ReplaceAll(s.newID(name), "_", "-")),
			KeyHash: bearertoken.Hash(secret), KeyPrefix: secret[:keyPrefixLength], Models: string(models),
			ChatID: chat, DispatchID: dispatch,
			BotID: sql.NullString{String: owner.botID, Valid: owner.botID != ""}, Now: now,
		}); err != nil {
			return fmt.Errorf("storing the %s usage key: %w", name, err)
		}

		if _, err := q.RevokeGatewayKey(ctx, store.RevokeGatewayKeyParams{
			ID: id, Now: sql.NullInt64{Int64: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("revoking the %s usage key: %w", name, err)
		}

		return nil
	})
	if err != nil {
		return Key{}, err
	}

	return s.getKey(ctx, id)
}

type webSearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	Provider   string `json:"provider"`
}

type webSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at,omitempty"`
}

type webSearchError struct {
	Provider string `json:"provider"`
	Message  string `json:"message"`
}

type webSearchResponse struct {
	Provider string            `json:"provider"`
	Query    string            `json:"query"`
	Results  []webSearchResult `json:"results"`
	Cached   bool              `json:"cached"`
	Errors   []webSearchError  `json:"errors,omitempty"`
}

type webFetchRequest struct {
	URL string `json:"url"`
}

type webFetchResponse struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// webSearch answers POST /v1/search.
func (s *Server) webSearch(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticateFor(w, r, EndpointSearch)
	if !ok {
		return
	}

	if s.web == nil {
		writeError(w, http.StatusServiceUnavailable, typeServer, "web_unavailable", "this gateway has no web search")

		return
	}

	var in webSearchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWebRequestBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request",
			`the body is {"query": "...", "max_results": n, "provider": "`+strings.Join(websearch.Providers(), "|")+`"}`)

		return
	}

	start := s.svc.clock.Now()
	answer, err := s.web.Search(r.Context(), in.Query, in.MaxResults, in.Provider)
	s.recordWeb(r, WebCall{
		KeyID: k.ID, Model: ModelWebSearch, Provider: answer.Provider, Attempts: len(answer.Errors) + 1, Err: err,
		Duration: s.svc.clock.Now().Sub(start),
	})

	if err != nil {
		s.webFailed(w, r, err)

		return
	}

	out := webSearchResponse{
		Provider: answer.Provider, Query: answer.Query, Cached: answer.Cached,
		Results: make([]webSearchResult, 0, len(answer.Results)),
	}

	for _, res := range answer.Results {
		out.Results = append(out.Results, webSearchResult{
			Title: res.Title, URL: res.URL, Snippet: res.Snippet, PublishedAt: res.PublishedAt,
		})
	}

	for _, f := range answer.Errors {
		out.Errors = append(out.Errors, webSearchError{Provider: f.Provider, Message: f.Reason})
	}

	writeJSON(w, http.StatusOK, out)
}

// webFetch answers POST /v1/web/fetch.
func (s *Server) webFetch(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticateFor(w, r, EndpointFetch)
	if !ok {
		return
	}

	if s.web == nil {
		writeError(w, http.StatusServiceUnavailable, typeServer, "web_unavailable", "this gateway has no web fetch")

		return
	}

	var in webFetchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWebRequestBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", `the body is {"url": "https://..."}`)

		return
	}

	start := s.svc.clock.Now()
	page, err := s.web.Fetch(r.Context(), in.URL)
	s.recordWeb(r, WebCall{
		KeyID: k.ID, Model: ModelWebFetch, Attempts: 1, Err: err, Duration: s.svc.clock.Now().Sub(start),
	})

	if err != nil {
		s.webFailed(w, r, err)

		return
	}

	writeJSON(w, http.StatusOK, webFetchResponse{
		URL: page.URL, Title: page.Title, Content: page.Content, Truncated: page.Truncated,
	})
}

// recordWeb records a web call, whether or not the client stayed for it.
func (s *Server) recordWeb(r *http.Request, c WebCall) {
	if err := s.svc.RecordWebCall(context.WithoutCancel(r.Context()), c); err != nil {
		s.logger.ErrorContext(r.Context(), "recording a web call",
			slog.String("event", "modelgateway.web_usage_failed"), slog.String("error", err.Error()))
	}
}

// webFailed answers a web call that failed, in OpenAI's error shape.
func (s *Server) webFailed(w http.ResponseWriter, r *http.Request, err error) {
	var resting *websearch.RestingError

	switch {
	case r.Context().Err() != nil:
		// The client left; nobody reads the answer.
	case errors.As(err, &resting):
		wait := max(1, int(resting.Until.Sub(s.svc.clock.Now()).Round(time.Second).Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeError(w, http.StatusTooManyRequests, typeRateLimit, "search_rate_limited", err.Error())
	case errors.Is(err, websearch.ErrInvalid):
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())
	case errors.Is(err, websearch.ErrRefused):
		writeError(w, http.StatusForbidden, typePermission, "address_refused", err.Error())
	case errors.Is(err, websearch.ErrFailed), errors.Is(err, websearch.ErrUnreadable):
		writeError(w, http.StatusBadGateway, typeServer, "upstream_failed", err.Error())
	default:
		s.internal(w, r, "modelgateway.web_failed", err)
	}
}
