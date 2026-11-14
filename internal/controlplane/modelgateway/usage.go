package modelgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// Outcome is how a request to the gateway ended.
type Outcome string

// Outcomes.
const (
	// OutcomeOK: the provider answered and the answer reached the client.
	OutcomeOK Outcome = "ok"
	// OutcomeUpstreamError: the provider answered with an error.
	OutcomeUpstreamError Outcome = "upstream_error"
	// OutcomeTransportError: no provider gave an answer.
	OutcomeTransportError Outcome = "transport_error"
	// OutcomeTruncated: a stream ended without its end mark.
	OutcomeTruncated Outcome = "truncated"
	// OutcomeClientGone: the client left before the answer ended.
	OutcomeClientGone Outcome = "client_gone"
)

// Tokens are the counts a provider reports for one request.
type Tokens struct {
	Prompt     int64
	Completion int64
	// Cached and Reasoning are parts of Prompt and Completion, when the
	// provider says; CacheWrite is the part of Prompt written to the cache,
	// and CacheWrite1h the part of CacheWrite written for an hour (issue
	// #448).
	Cached       int64
	CacheWrite   int64
	CacheWrite1h int64
	Reasoning    int64
	// Estimated says the provider reported none and the counts are bytes
	// divided by four, as 9router-go estimates
	// (internal/handlers/chat/usage.go): a rough size, not a bill.
	Estimated bool
}

// Usage is the record of one request the gateway served.
type Usage struct {
	ID      string
	KeyID   string
	KeyName string
	// Model is the public name asked for; Provider and UpstreamModel are
	// where the last attempt went.
	Model         string
	Provider      string
	UpstreamModel string
	Stream        bool
	Attempts      int
	Outcome       Outcome
	// Status is the provider's HTTP status, 0 when it gave none.
	Status int
	// AccountID is the subscription account of the last attempt; empty for an
	// API provider.
	AccountID string
	// Error is the provider's message for a request that failed.
	Error string
	// CostMicros is what the tokens cost at the vendor's API, in
	// micro-dollars, and PricedModel the model priced; nil and empty when the
	// price book has none (issue #304).
	CostMicros  *int64
	PricedModel string
	Tokens      Tokens
	Duration    time.Duration
	CreatedAt   time.Time
}

func flag(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

// recordUsage stores one request's record.
func (s *Service) recordUsage(ctx context.Context, u Usage) error {
	if u.Error == "" {
		u.Error = defaultError(u.Outcome)
	}

	s.price(ctx, &u)

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertGatewayUsage(ctx, store.InsertGatewayUsageParams{
			ID: s.newID("gwu"), KeyID: u.KeyID, Model: u.Model, Provider: u.Provider, UpstreamModel: u.UpstreamModel,
			Stream: flag(u.Stream), Attempts: int64(u.Attempts), Outcome: string(u.Outcome), Status: int64(u.Status),
			PromptTokens: u.Tokens.Prompt, CompletionTokens: u.Tokens.Completion, CachedTokens: u.Tokens.Cached,
			ReasoningTokens: u.Tokens.Reasoning, Estimated: flag(u.Tokens.Estimated),
			DurationMs: u.Duration.Milliseconds(), AccountID: u.AccountID, Error: clip(u.Error, maxErrorText),
			CacheWriteTokens: u.Tokens.CacheWrite, CacheWrite1hTokens: u.Tokens.CacheWrite1h,
			CostMicros: nullable(u.CostMicros), PricedModel: u.PricedModel, Now: now,
		}); err != nil {
			return fmt.Errorf("recording usage: %w", err)
		}

		return nil
	})
}

// ListUsage returns the latest requests the gateway served, newest first.
func (s *Service) ListUsage(ctx context.Context, limit int) ([]Usage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := store.New(s.db).ListGatewayUsage(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("listing usage: %w", err)
	}

	out := make([]Usage, 0, len(rows))
	for _, r := range rows {
		out = append(out, usageOf(r))
	}

	return out, nil
}

// usageOf is a stored row as a record.
func usageOf(r store.ListGatewayUsageRow) Usage {
	return Usage{
		ID: r.ID, KeyID: r.KeyID, KeyName: r.KeyName, Model: r.Model, Provider: r.Provider,
		UpstreamModel: r.UpstreamModel, Stream: r.Stream == 1, Attempts: int(r.Attempts),
		Outcome: Outcome(r.Outcome), Status: int(r.Status), AccountID: r.AccountID, Error: r.Error,
		CostMicros: fromNullable(r.CostMicros), PricedModel: r.PricedModel,
		Tokens: Tokens{
			Prompt: r.PromptTokens, Completion: r.CompletionTokens, Cached: r.CachedTokens,
			CacheWrite: r.CacheWriteTokens, CacheWrite1h: r.CacheWrite1hTokens, Reasoning: r.ReasoningTokens,
			Estimated: r.Estimated == 1,
		},
		Duration:  time.Duration(r.DurationMs) * time.Millisecond,
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

// openAIUsage is the usage object of an OpenAI chat completion or chunk.
type openAIUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
		// The one-hour part of the cache write, where the chat route's
		// Claude usage and LiteLLM carry it (issue #448).
		CacheCreationTokenDetails struct {
			Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation_token_details"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// merge keeps the largest count seen. Some OpenAI-compatible upstreams report
// usage on every chunk as it grows (vLLM's continuous usage), so the last
// frame is not always the total (Bifrost, core/providers/openai/openai.go).
func (t *Tokens) merge(raw json.RawMessage) {
	var u openAIUsage
	if len(raw) == 0 || json.Unmarshal(raw, &u) != nil {
		return
	}

	t.Prompt = max(t.Prompt, u.PromptTokens)
	t.Completion = max(t.Completion, u.CompletionTokens)
	t.Cached = max(t.Cached, u.PromptTokensDetails.CachedTokens)
	t.Reasoning = max(t.Reasoning, u.CompletionTokensDetails.ReasoningTokens)
	t.CacheWrite = max(t.CacheWrite, u.CacheCreationInputTokens)
	t.CacheWrite1h = max(t.CacheWrite1h, u.PromptTokensDetails.CacheCreationTokenDetails.Ephemeral1hInputTokens)
}

// estimate fills counts the provider did not report from the sizes of what
// was sent and received: bytes divided by four, at least one.
func (t *Tokens) estimate(requestBytes, responseBytes int) {
	rough := func(n int) int64 {
		if n == 0 {
			return 0
		}

		return max(int64(n/4), 1)
	}

	if t.Prompt == 0 && rough(requestBytes) > 0 {
		t.Prompt, t.Estimated = rough(requestBytes), true
	}

	if t.Completion == 0 && rough(responseBytes) > 0 {
		t.Completion, t.Estimated = rough(responseBytes), true
	}
}
