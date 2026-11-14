package websearch

import (
	"context"
	"errors"
	"time"
)

// A subscription the owner logged in on the control plane searches too, with
// its own web search and no key of its own (issue #354, owner 2026-11-13): a
// Codex account through the Responses API's web_search tool (OmniRoute
// open-sse/handlers/search/codexSearch.ts, which borrows the Codex chat
// credential: config/searchRegistry.ts SEARCH_CREDENTIAL_FALLBACKS), a Claude
// account through the Messages API's web_search_20250305 server tool (omp
// packages/coding-agent/src/web/search/providers/anthropic.ts) and an
// Antigravity account through Google Search grounding (9router
// open-sse/handlers/search/chatSearch.js, 9router-go
// internal/handlers/media/antigravity_search.go). The model gateway makes
// those calls, under its accounts; this package only orders them:
//
//   - they come last, after the free providers, as OmniRoute keeps Codex's
//     search for the last resort (its fallbackOnly), in the order Codex,
//     Claude, Antigravity;
//   - one is in the chain only while an account of it is logged in;
//   - a 429 rests that provider's search as any provider's (rest.go), and
//     nothing of the account's chat: OmniRoute's borrowedKeyLock.ts locks
//     only the search;
//   - each has the time its reference gives it, which a vendor's model
//     searching needs: OmniRoute's 60 s for Codex (codex-search timeoutMs),
//     omp's 60 s (DEFAULT_WEB_SEARCH_TIMEOUT_SECONDS) for Claude, 9router's
//     15 s (chatSearch.js REQUEST_TIMEOUT_MS) for Antigravity.

// The subscription providers a call may name.
const (
	ProviderCodex       = "codex"
	ProviderClaude      = "claude"
	ProviderAntigravity = "antigravity"
)

// SubscriptionProviders are the providers a logged-in subscription answers,
// in the order they are tried.
func SubscriptionProviders() []string {
	return []string{ProviderCodex, ProviderClaude, ProviderAntigravity}
}

// subscriptionTimeouts are how long each subscription provider has.
var subscriptionTimeouts = map[string]time.Duration{
	ProviderCodex:       time.Minute,
	ProviderClaude:      time.Minute,
	ProviderAntigravity: 15 * time.Second,
}

// ErrNoAccount says no account of a subscription provider can be used: none
// is logged in, or each needs a new login. The provider is then not in the
// chain.
var ErrNoAccount = errors.New("no account of this provider is logged in")

// Subscriptions searches with the web search of a subscription account; the
// control plane gives the model gateway's.
type Subscriptions interface {
	// Search asks one of SubscriptionProviders for query, at most limit
	// results. It answers an error wrapping ErrNoAccount when the provider has
	// no account to use, and Refused for the vendor's refusal.
	Search(ctx context.Context, provider, query string, limit int) ([]Result, error)
}

// WithSubscriptions makes the logged-in subscriptions search too. It is
// called once, before the Service is used: the model gateway that makes the
// calls is made after the Service it serves.
func (s *Service) WithSubscriptions(subs Subscriptions) *Service {
	s.subs = subs

	return s
}

// Refused is a provider's refusal for its status and the wait it asked for,
// so that a provider asked elsewhere rests after a 429 as the others do.
func Refused(status int, retryAfter time.Duration, message string) error {
	return &providerError{status: status, retryAfter: min(retryAfter, longestRetryAfter), message: clip(message, 200)}
}

// subscriptionRoute asks a subscription provider through the gateway.
func subscriptionRoute(provider string) route {
	return route{
		id: provider + "-search", provider: provider, source: SourceSubscription, label: "its subscription: ",
		timeout: subscriptionTimeouts[provider], ask: (*Service).askSubscription,
	}
}

func (s *Service) askSubscription(ctx context.Context, r route, query string, limit int) (reply, error) {
	results, err := s.subs.Search(ctx, r.provider, query, limit)

	return reply{results: results}, err
}
