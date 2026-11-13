// Package websearch searches the web and reads a page for every agent (issue
// #354): the model gateway serves it on /v1/search and /v1/web/fetch, a
// worker gets it as its web_search and web_fetch tools through the agent API,
// and a Bot through `hypomnema web search` and `hypomnema web fetch`. It holds
// no key of its own and records nothing: the caller's wiring reads Exa's key
// from the vault and records each call on a gateway key.
//
// What it does is learned from the gateways and agents that have it:
//
//   - the providers are Exa (its REST API with a key, its public MCP without
//     one), Parallel's public MCP and a SearXNG the owner runs: omp
//     (packages/coding-agent/src/web/search/providers/exa.ts, parallel.ts),
//     opencode (packages/core/src/plugin/websearch/exa.ts, parallel.ts, mcp.ts)
//     and OmniRoute (open-sse/handlers/search.ts, config/searchRegistry.ts);
//   - a call that names no provider tries them keyed first, then SearXNG when
//     it is set, then the keyless ones, then the subscriptions the owner
//     logged in (subscriptions.go); a call that names one gets that one only:
//     OmniRoute's autoSearchCandidates;
//   - a provider that fails is noted and the next one is asked, and the answer
//     says which one answered after which failed: omp web/search/index.ts
//     (fallbackNotes);
//   - a provider that answered 429 rests for its Retry-After, else 2 s doubling
//     up to 5 minutes, and is skipped meanwhile: 9router
//     open-sse/config/errorConfig.js (BACKOFF_CONFIG) and
//     services/accountFallback.js, the numbers the gateway's accounts cool
//     down with;
//   - the same search within 5 minutes is answered from memory, and the same
//     search asked twice at once goes out once: OmniRoute
//     open-sse/services/searchCache.ts;
//   - a page is read as markdown when its server offers it, else its readable
//     part is taken from its HTML, at most 20,000 characters: OpenClaw
//     src/agents/tools/web-fetch.ts and extensions/web-readability; Jina
//     Reader reads it when that fails: omp src/tools/fetch.ts;
//   - a page is read only from public addresses, the shared browser's rules
//     (internal/controlplane/browser/egress), checked at each connection, with
//     redirects followed by hand: OmniRoute
//     open-sse/handlers/search/redirectGuard.ts;
//   - what reaches an agent as text is marked as untrusted outside content:
//     OpenClaw src/security/external-content.ts.
package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
)

// The providers a call may name.
const (
	ProviderExa      = "exa"
	ProviderSearXNG  = "searxng"
	ProviderParallel = "parallel"
)

// Providers are the names a call may give, in the order they are listed.
func Providers() []string {
	return append([]string{ProviderExa, ProviderSearXNG, ProviderParallel}, SubscriptionProviders()...)
}

const (
	// DefaultResults is how many results a call that names none gets
	// (OmniRoute's defaultMaxResults, 9router's skill: 5); MaxResults is the
	// most a call gets, OmniRoute's smallest maxMaxResults (Parallel's 20).
	DefaultResults = 5
	MaxResults     = 20

	// providerTimeout bounds one provider's answer (OmniRoute's timeoutMs).
	providerTimeout = 10 * time.Second
	// cacheTTL and maxCacheEntries are OmniRoute's (cacheTTLMs, MAX_CACHE_ENTRIES).
	cacheTTL        = 5 * time.Minute
	maxCacheEntries = 500
	// searchBudget bounds a search that every provider is tried for, so a
	// coalesced search ends even when the caller that started it left:
	// OmniRoute's longest (MAX_SEARCH_TIMEOUT_MS), since Codex's search alone
	// may take its minute.
	searchBudget = 2 * time.Minute
)

// Errors callers match with errors.Is.
var (
	// ErrInvalid: the query, the provider or the address is not one that can be asked.
	ErrInvalid = errors.New("invalid request")
	// ErrFailed: every provider tried failed; the error says why each did.
	ErrFailed = errors.New("no search provider answered")
	// ErrRefused: the page is not on the public internet.
	ErrRefused = errors.New("the address is not on the public internet")
	// ErrUnreadable: the page could not be read.
	ErrUnreadable = errors.New("the page could not be read")
)

// RestingError says every provider the call could use rests after a 429,
// and when the first is free again.
type RestingError struct{ Until time.Time }

func (e *RestingError) Error() string {
	return "every search provider is rate limited; try again after " + e.Until.UTC().Format(time.RFC3339)
}

// Result is one page a search found.
type Result struct {
	Title   string
	URL     string
	Snippet string
	// PublishedAt is the date the provider gives, as it gives it; empty when none.
	PublishedAt string
}

// Failure is why a provider did not answer a search.
type Failure struct {
	Provider string
	Reason   string
}

// Answer is a search's results and where they came from.
type Answer struct {
	// Provider is the provider that answered.
	Provider string
	Query    string
	Results  []Result
	// Cached says the answer was not asked of the provider for this call.
	Cached bool
	// Errors are the providers tried before Provider answered, and why each did not.
	Errors []Failure
}

// Page is a page read as markdown.
type Page struct {
	// URL is where the page was read, after its redirects.
	URL     string
	Title   string
	Content string
	// Truncated says Content was cut at MaxContent characters, or the page at
	// its byte limit.
	Truncated bool
}

// Config is what the Service needs; the zero value searches with the keyless
// providers only and reads public pages.
type Config struct {
	// ExaKey gives Exa's API key, read at each search so that a key the owner
	// adds is used at once; it answers "" when there is none. nil: no key.
	ExaKey func(ctx context.Context) (string, error)
	// SearXNGURL is the owner's SearXNG; empty: SearXNG is not asked.
	SearXNGURL string
	// Off are the subscription providers the owner turned off: they are
	// neither tried nor may be named.
	Off []string
	// Rules decide which addresses a page may be read from: the shared
	// browser's, so that what an agent reads through this package is what it
	// may open in the browser.
	Rules egress.Rules
	// Endpoints of the providers and of Jina Reader; empty: theirs. Tests
	// name their own.
	ExaURL         string
	ExaMCPURL      string
	ParallelMCPURL string
	JinaURL        string
	// Now is the clock rests and the cache read; nil: the wall clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// The providers' and Jina Reader's own endpoints.
const (
	exaURL         = "https://api.exa.ai/search"
	exaMCPURL      = "https://mcp.exa.ai/mcp"
	parallelMCPURL = "https://search.parallel.ai/mcp"
	jinaURL        = "https://r.jina.ai"
)

// Service searches and reads pages.
type Service struct {
	cfg Config
	// client asks the providers and Jina Reader: fixed hosts and the owner's
	// own SearXNG, which may be on the local network (OmniRoute keeps
	// loopback and LAN for SearXNG).
	client *http.Client
	// pages reads the pages agents name, through the address check.
	pages *http.Client
	now   func() time.Time

	// subs searches with the logged-in subscriptions; nil: none.
	subs Subscriptions

	mu      sync.Mutex
	resting map[string]rest
	cache   map[string]cached
	flight  singleflight.Group
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.ExaURL == "" {
		cfg.ExaURL = exaURL
	}

	if cfg.ExaMCPURL == "" {
		cfg.ExaMCPURL = exaMCPURL
	}

	if cfg.ParallelMCPURL == "" {
		cfg.ParallelMCPURL = parallelMCPURL
	}

	if cfg.JinaURL == "" {
		cfg.JinaURL = jinaURL
	}

	cfg.SearXNGURL = strings.TrimRight(cfg.SearXNGURL, "/")

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	//nolint:forcetypeassert // the standard library's own type
	transport := http.DefaultTransport.(*http.Transport).Clone()

	return &Service{
		cfg: cfg, client: &http.Client{Transport: transport}, pages: pageClient(cfg.Rules), now: now,
		resting: map[string]rest{}, cache: map[string]cached{},
	}
}

// Search asks the web for query: limit results at most (0: DefaultResults),
// from the provider named, or from the first that answers when none is.
func (s *Service) Search(ctx context.Context, query string, limit int, provider string) (Answer, error) {
	query = strings.TrimSpace(query)
	provider = strings.ToLower(strings.TrimSpace(provider))

	switch {
	case query == "":
		return Answer{}, fmt.Errorf("%w: a search needs a query", ErrInvalid)
	case provider != "" && !validProvider(provider):
		return Answer{}, fmt.Errorf("%w: the provider is one of %s", ErrInvalid, strings.Join(Providers(), ", "))
	case provider == ProviderSearXNG && s.cfg.SearXNGURL == "":
		return Answer{}, fmt.Errorf("%w: no SearXNG is set on this control plane", ErrInvalid)
	case slices.Contains(SubscriptionProviders(), provider) && !s.subscriptionOn(provider):
		return Answer{}, fmt.Errorf("%w: %s's search is turned off on this control plane", ErrInvalid, provider)
	}

	if limit <= 0 {
		limit = DefaultResults
	}

	limit = min(limit, MaxResults)
	key := cacheKey(query, provider, limit)

	if a, ok := s.cached(key); ok {
		a.Cached = true

		return a, nil
	}

	// The same search asked while one is out waits for it, as OmniRoute's
	// inflight map does. The search runs on past a caller that leaves, within
	// its own budget, so the others still get its answer.
	led := false
	answers := s.flight.DoChan(key, func() (any, error) {
		led = true

		run, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchBudget)
		defer cancel()

		a, err := s.search(run, query, limit, provider)
		if err == nil {
			s.keep(key, a)
		}

		return a, err
	})

	select {
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	case r := <-answers:
		if r.Err != nil {
			return Answer{}, r.Err
		}

		a, _ := r.Val.(Answer)
		// A caller that joined a search under way was answered by another's call (OmniRoute: cached).
		a.Cached = !led

		return a, nil
	}
}

func validProvider(name string) bool { return slices.Contains(Providers(), name) }

// search asks each provider the call may use in turn until one answers.
func (s *Service) search(ctx context.Context, query string, limit int, provider string) (Answer, error) {
	routes, failures := s.routes(ctx, provider)

	var (
		tried     bool
		firstFree time.Time
	)

	for _, r := range routes {
		if until, ok := s.restingUntil(r.id); ok {
			failures = append(failures, Failure{
				Provider: r.provider, Reason: r.label + "rate limited until " + until.UTC().Format(time.RFC3339),
			})

			if firstFree.IsZero() || until.Before(firstFree) {
				firstFree = until
			}

			continue
		}

		results, err := s.ask(ctx, r, query, limit)
		if errors.Is(err, ErrNoAccount) {
			// A subscription with no account is not in the chain; named, it cannot be asked.
			if provider != "" {
				return Answer{}, fmt.Errorf("%w: no %s account is logged in on this control plane", ErrInvalid, provider)
			}

			continue
		}

		tried = true

		if err == nil && len(results) == 0 {
			// A provider with nothing to show is not an answer: the next is asked (omp: no renderable content).
			err = errors.New("no results")
		}

		if err != nil {
			if ctx.Err() != nil {
				return Answer{}, ctx.Err()
			}

			failures = append(failures, Failure{Provider: r.provider, Reason: r.label + reasonOf(err)})
			s.failed(r.id, err)
			s.cfg.Logger.WarnContext(ctx, "a web search provider failed",
				slog.String("event", "websearch.provider_failed"), slog.String("provider", r.id),
				slog.String("error", err.Error()))

			continue
		}

		s.served(r.id)

		return Answer{
			Provider: r.provider, Query: query, Results: results[:min(len(results), limit)], Errors: failures,
		}, nil
	}

	if !tried && !firstFree.IsZero() {
		return Answer{}, &RestingError{Until: firstFree}
	}

	return Answer{}, fmt.Errorf("%w: %s", ErrFailed, failureText(failures))
}

func failureText(failures []Failure) string {
	if len(failures) == 0 {
		return "none could be asked"
	}

	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, f.Provider+" ("+f.Reason+")")
	}

	return strings.Join(parts, "; ")
}

// route is one way of asking a provider: Exa has two, with its key and
// without one, which rest apart since their limits are apart.
type route struct {
	// id names the route for its rest.
	id       string
	provider string
	// label says which way, in a failure's reason.
	label string
	key   string
	// timeout is how long the provider has; zero: providerTimeout.
	timeout time.Duration
	ask     func(s *Service, ctx context.Context, r route, query string, limit int) ([]Result, error)
}

// routes are the ways a call may ask, in the order they are tried, and the
// failures of those that could not even be set up.
func (s *Service) routes(ctx context.Context, provider string) ([]route, []Failure) {
	var failures []Failure

	key := ""

	if s.cfg.ExaKey != nil && (provider == "" || provider == ProviderExa) {
		k, err := s.cfg.ExaKey(ctx)
		if err != nil {
			failures = append(failures, Failure{Provider: ProviderExa, Reason: "its key could not be read"})
			s.cfg.Logger.ErrorContext(ctx, "reading Exa's key",
				slog.String("event", "websearch.exa_key_failed"), slog.String("error", err.Error()))
		}

		key = k
	}

	keyed := route{id: "exa", provider: ProviderExa, label: "with its key: ", key: key, ask: (*Service).askExa}
	keyless := route{id: "exa-mcp", provider: ProviderExa, label: "public MCP: ", ask: (*Service).askExaMCP}
	searxng := route{id: "searxng", provider: ProviderSearXNG, ask: (*Service).askSearXNG}
	parallel := route{id: "parallel-mcp", provider: ProviderParallel, label: "public MCP: ", ask: (*Service).askParallel}

	switch provider {
	case ProviderExa:
		// Exa named: its key when there is one, else its public MCP, as omp's searchExa chooses.
		if key != "" {
			return []route{keyed}, failures
		}

		return []route{keyless}, failures
	case ProviderSearXNG:
		return []route{searxng}, failures
	case ProviderParallel:
		return []route{parallel}, failures
	case ProviderCodex, ProviderClaude, ProviderAntigravity:
		return []route{subscriptionRoute(provider)}, failures
	}

	var out []route
	if key != "" {
		out = append(out, keyed)
	}

	if s.cfg.SearXNGURL != "" {
		out = append(out, searxng)
	}

	out = append(out, keyless, parallel)

	for _, p := range SubscriptionProviders() {
		if s.subscriptionOn(p) {
			out = append(out, subscriptionRoute(p))
		}
	}

	return out, failures
}

func (s *Service) ask(ctx context.Context, r route, query string, limit int) ([]Result, error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = providerTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return r.ask(s, ctx, r, query, limit)
}
