// Package websearch searches the web and reads a page for every agent (issues
// #354 and #393): the model gateway serves it on /v1/search and /v1/web/fetch,
// a worker gets it as its web_search and web_fetch tools through the agent
// API, and a Bot through `hypomnema web search` and `hypomnema web fetch`. It
// holds no key and stores nothing itself: the caller's wiring gives it the
// providers' keys from the vault and keeps the counts of its calls.
//
// What it does is learned from the gateways and agents that have it:
//
//   - the providers are those free without a card (owner, 2026-11-14): Exa,
//     Firecrawl, Parallel, Tavily and TinyFish with the owner's keys, Exa,
//     Parallel, Firecrawl, Tavily and You.com without one (pool.go,
//     adapters.go, providers.go), then the subscriptions the owner logged in
//     (subscriptions.go);
//   - a call that names no provider tries the keyed providers first, in the
//     owner's order (Hermes tools/web_tools.py, _autodetect_backend), each
//     provider's keys least recently used first (mcp-nexus
//     tavily/keyPool.ts); then the keyless ones, taken in turn from a moving
//     start to spread their per-address limits (Hermes
//     plugins/web/keyless_mcp.py, _ring_order); then the subscriptions,
//     last (OmniRoute's fallbackOnly); a call that names one gets that one
//     only: OmniRoute's autoSearchCandidates;
//   - a provider that fails is noted and the next one is asked, and the answer
//     says which one answered after which failed: omp web/search/index.ts
//     (fallbackNotes);
//   - a key or a keyless provider that was rate limited, refused or out of
//     credits rests on its own while the others go on (rest.go); a key whose
//     balance is read as spent is skipped (usage.go);
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

// The providers a call may name, besides the subscriptions.
const (
	ProviderExa       = "exa"
	ProviderFirecrawl = "firecrawl"
	ProviderParallel  = "parallel"
	ProviderTavily    = "tavily"
	ProviderTinyFish  = "tinyfish"
	ProviderYou       = "you"
)

// KeyedProviders are the providers that take the owner's keys, in the order
// a search tries them until the owner reorders them: by the independent
// benchmarks (owner, 2026-11-14, issue #393), TinyFish last.
func KeyedProviders() []string {
	return []string{ProviderExa, ProviderFirecrawl, ProviderParallel, ProviderTavily, ProviderTinyFish}
}

// keylessRing are the providers asked without a key, in the ring's order
// (issue #393; Hermes keyless_mcp.py _KEYLESS_RING, with Tavily's and
// You.com's keyless search from search-rotation).
func keylessRing() []string {
	return []string{ProviderExa, ProviderParallel, ProviderFirecrawl, ProviderTavily, ProviderYou}
}

// Providers are the names a call may give, in the order they are listed.
func Providers() []string {
	return append(append(KeyedProviders(), ProviderYou), SubscriptionProviders()...)
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

// RestingError says every provider the call could use rests, and when the
// first is free again.
type RestingError struct{ Until time.Time }

func (e *RestingError) Error() string {
	return "every search provider is resting; try again after " + e.Until.UTC().Format(time.RFC3339)
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

// Key is one of the owner's API keys for a provider, as the vault holds it.
type Key struct {
	Provider string
	// Name is the vault secret's name (KeyName); it names the key everywhere.
	Name  string
	Value string
}

// Config is what the Service needs; the zero value searches with the keyless
// providers only and reads public pages.
type Config struct {
	// Keys gives the owner's keys of every keyed provider, read at each
	// search so that a key the owner adds is used at once. nil: no key.
	Keys func(ctx context.Context) ([]Key, error)
	// Settings gives what the owner set on the Web search page: the providers
	// switched off and the keyed providers' order. It is read at each search,
	// as Keys is. nil: every provider is on, in the default order.
	Settings func(ctx context.Context) (Settings, error)
	// Usage keeps the count of each provider's and key's calls; nil: none is kept.
	Usage Usage
	// Rules decide which addresses a page may be read from: the shared
	// browser's, so that what an agent reads through this package is what it
	// may open in the browser.
	Rules egress.Rules
	// Endpoints of the providers and of Jina Reader; empty: theirs. Tests
	// name their own.
	ExaURL         string
	ExaMCPURL      string
	ParallelURL    string
	ParallelMCPURL string
	TavilyURL      string
	FirecrawlURL   string
	TinyFishURL    string
	YouMCPURL      string
	JinaURL        string
	// Now is the clock rests, counts and the cache read; nil: the wall clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// The providers' and Jina Reader's own endpoints.
const (
	exaURL         = "https://api.exa.ai/search"
	exaMCPURL      = "https://mcp.exa.ai/mcp"
	parallelURL    = "https://api.parallel.ai/v1/search"
	parallelMCPURL = "https://search.parallel.ai/mcp"
	tavilyURL      = "https://api.tavily.com"
	firecrawlURL   = "https://api.firecrawl.dev/v2"
	tinyFishURL    = "https://api.search.tinyfish.ai/"
	youMCPURL      = "https://api.you.com/mcp?profile=free"
	jinaURL        = "https://r.jina.ai"
)

// Service searches and reads pages.
type Service struct {
	cfg Config
	// client asks the providers and Jina Reader, whose hosts are fixed.
	client *http.Client
	// pages reads the pages agents name, through the address check.
	pages *http.Client
	now   func() time.Time

	// subs searches with the logged-in subscriptions; nil: none.
	subs Subscriptions

	mu      sync.Mutex
	resting map[string]rest
	// lastUsed is when each key was last asked, for least recently used first.
	lastUsed map[string]time.Time
	// ring is where the next search starts in the keyless ring.
	ring     int
	balances map[string]balanceRead
	cache    map[string]cached
	flight   singleflight.Group
}

// New returns a Service.
func New(cfg Config) *Service {
	defaults := []struct {
		field *string
		value string
	}{
		{&cfg.ExaURL, exaURL}, {&cfg.ExaMCPURL, exaMCPURL}, {&cfg.ParallelURL, parallelURL},
		{&cfg.ParallelMCPURL, parallelMCPURL}, {&cfg.TavilyURL, tavilyURL}, {&cfg.FirecrawlURL, firecrawlURL},
		{&cfg.TinyFishURL, tinyFishURL}, {&cfg.YouMCPURL, youMCPURL}, {&cfg.JinaURL, jinaURL},
	}
	for _, d := range defaults {
		if *d.field == "" {
			*d.field = d.value
		}
	}

	cfg.TavilyURL = strings.TrimRight(cfg.TavilyURL, "/")
	cfg.FirecrawlURL = strings.TrimRight(cfg.FirecrawlURL, "/")

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
		resting: map[string]rest{}, lastUsed: map[string]time.Time{}, balances: map[string]balanceRead{},
		cache: map[string]cached{},
		// Each control plane starts the ring somewhere else, as Hermes seeds its cursor with its session.
		ring: int(now().UnixNano() % int64(len(keylessRing()))),
	}
}

// Search asks the web for query: limit results at most (0: DefaultResults),
// from the provider named, or from the first that answers when none is.
func (s *Service) Search(ctx context.Context, query string, limit int, provider string) (Answer, error) {
	query = strings.TrimSpace(query)
	provider = strings.ToLower(strings.TrimSpace(provider))

	set, err := s.settings(ctx)
	if err != nil {
		return Answer{}, err
	}

	switch {
	case query == "":
		return Answer{}, fmt.Errorf("%w: a search needs a query", ErrInvalid)
	case provider != "" && !validProvider(provider):
		return Answer{}, fmt.Errorf("%w: the provider is one of %s", ErrInvalid, strings.Join(Providers(), ", "))
	case slices.Contains(set.Off, provider):
		return Answer{}, fmt.Errorf("%w: %s's search is turned off on this control plane", ErrInvalid, provider)
	case slices.Contains(SubscriptionProviders(), provider) && s.subs == nil:
		return Answer{}, fmt.Errorf("%w: this control plane searches with no subscription", ErrInvalid)
	}

	if limit <= 0 {
		limit = DefaultResults
	}

	limit = min(limit, MaxResults)
	// A setting the owner changes makes another search: an answer kept from before is not given for it.
	key := cacheKey(query, provider, limit, set)

	if a, ok := s.cached(key); ok {
		a.Cached = true

		return a, nil
	}

	// The same search asked while one is out waits for it, as OmniRoute's
	// inflight map does. The search runs on past a caller that leaves, within
	// its own budget, so the others still get its answer.
	led := false
	answers := s.flight.DoChan("search:"+key, func() (any, error) {
		led = true

		run, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchBudget)
		defer cancel()

		a, err := s.search(run, query, limit, provider, set)
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

// settings reads what the owner set.
func (s *Service) settings(ctx context.Context) (Settings, error) {
	if s.cfg.Settings == nil {
		return Settings{}, nil
	}

	set, err := s.cfg.Settings(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("reading the web search settings: %w", err)
	}

	return set, nil
}

// search asks each way the call may use in turn until one answers.
func (s *Service) search(ctx context.Context, query string, limit int, provider string, set Settings,
) (Answer, error) {
	routes, failures := s.routes(ctx, provider, set, true)

	var (
		tried     bool
		firstFree time.Time
		// skipKeys is a provider whose failure was not its key's: its other keys are not asked.
		skipKeys = map[string]bool{}
	)

	for _, r := range routes {
		if r.source == SourceKey && skipKeys[r.provider] {
			continue
		}

		if until, why, ok := s.restingUntil(r.id); ok {
			failures = append(failures, Failure{
				Provider: r.provider, Reason: r.label + why + " until " + until.UTC().Format(time.RFC3339),
			})

			if firstFree.IsZero() || until.Before(firstFree) {
				firstFree = until
			}

			continue
		}

		if r.source == SourceKey && s.spent(ctx, r) {
			failures = append(failures, Failure{Provider: r.provider, Reason: r.label + "its balance is spent"})

			continue
		}

		s.use(r.id)

		rep, err := s.ask(ctx, r, query, limit)
		if errors.Is(err, ErrNoAccount) {
			// A subscription with no account is not in the chain; named, it cannot be asked.
			if provider != "" {
				return Answer{}, fmt.Errorf("%w: no %s account is logged in on this control plane", ErrInvalid, provider)
			}

			continue
		}

		tried = true

		if err == nil && len(rep.results) == 0 {
			// A provider with nothing to show is not an answer: the next is asked (omp: no renderable content).
			err = errors.New("no results")
		}

		s.record(ctx, r, err == nil, rep.costDollars)

		if err != nil {
			if ctx.Err() != nil {
				return Answer{}, ctx.Err()
			}

			failures = append(failures, Failure{Provider: r.provider, Reason: r.label + reasonOf(err)})

			if !s.failed(r, err) && r.source == SourceKey {
				skipKeys[r.provider] = true
			}

			s.cfg.Logger.WarnContext(ctx, "a web search provider failed",
				slog.String("event", "websearch.provider_failed"), slog.String("provider", r.provider),
				slog.String("source", r.source), slog.String("key", r.key.Name), slog.String("error", err.Error()))

			continue
		}

		s.served(r.id)

		return Answer{
			Provider: r.provider, Query: query, Results: rep.results[:min(len(rep.results), limit)], Errors: failures,
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

// reply is what a provider answered: its results, and what the answer cost
// when the provider says so (Exa's costDollars).
type reply struct {
	results     []Result
	costDollars float64
}

// route is one way of asking a provider: with one of its keys, without a
// key, or through a subscription. Each rests apart, since their limits are
// apart.
type route struct {
	// id names the route for its rest and its last use.
	id       string
	provider string
	// source says where its search comes from.
	source string
	// label says which way, in a failure's reason.
	label string
	key   Key
	// daily: the provider's limit is a day's, so a 429 rests it until 00:00 UTC (You.com's 100 a day).
	daily bool
	// timeout is how long the provider has; zero: providerTimeout.
	timeout time.Duration
	ask     func(s *Service, ctx context.Context, r route, query string, limit int) (reply, error)
}

// routes are the ways a call may ask, in the order they are tried, and the
// failures of those that could not even be set up. turn moves the keyless
// ring's start: a search does, the page's listing does not.
func (s *Service) routes(ctx context.Context, provider string, set Settings, turn bool) ([]route, []Failure) {
	var failures []Failure

	keys, err := s.keys(ctx)
	if err != nil {
		failures = append(failures, Failure{Provider: "keys", Reason: "the providers' keys could not be read"})
	}

	off := func(p string) bool { return slices.Contains(set.Off, p) }

	var out []route

	keyed := map[string]bool{}

	for _, p := range set.order() {
		if off(p) || (provider != "" && provider != p) {
			continue
		}

		for _, k := range s.leastRecentlyUsed(keys[p]) {
			out = append(out, keyRoute(k))
			keyed[p] = true
		}
	}

	ring := keylessRing()
	start := 0

	if provider == "" && turn {
		s.mu.Lock()
		start = s.ring
		s.ring = (s.ring + 1) % len(ring)
		s.mu.Unlock()
	}

	for _, p := range append(ring[start:], ring[:start]...) {
		// A provider named is asked with its keys when it has some, else without (omp's searchExa).
		if off(p) || (provider != "" && (provider != p || keyed[p])) {
			continue
		}

		out = append(out, keylessRoute(p))
	}

	if s.subs != nil {
		for _, p := range SubscriptionProviders() {
			if !off(p) && (provider == "" || provider == p) {
				out = append(out, subscriptionRoute(p))
			}
		}
	}

	return out, failures
}

func (s *Service) ask(ctx context.Context, r route, query string, limit int) (reply, error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = providerTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return r.ask(s, ctx, r, query, limit)
}
