package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// How much of each provider is used (issue #393), none of it invented, as
// search-rotation's dashboard tells them apart (docs/provider-access.md):
//
//   - the provider's own balance where it has an endpoint: Tavily's GET
//     /usage (mcp-nexus tavily/credits.ts; OmniRoute tavilyQuotaFetcher.ts:
//     the key's limit when it has one, else the plan's with its
//     pay-as-you-go) and Firecrawl's GET /v2/team/credit-usage (OmniRoute
//     firecrawlQuotaFetcher.ts, search-rotation engines/firecrawl.ts),
//     read at most every 5 minutes a key (search-rotation's REMOTE_TTL_MS),
//     failures too; a key whose balance is read as spent is skipped
//     (search-rotation's exhausted band, mcp-nexus's rest for no credits);
//   - Exa's spend this month, the sum of each answer's costDollars (owner,
//     2026-11-14): Exa has no balance to read;
//   - otherwise this control plane's own calls, answered and failed, today
//     and this month (UTC), for each provider and key, which the caller's
//     Usage keeps;
//   - and nothing where nothing is known: the page says the allowance is
//     unknown. No cap is assumed.

const (
	balanceTTL     = 5 * time.Minute
	balanceTimeout = 5 * time.Second // mcp-nexus's CREDITS_REFRESH_TIMEOUT_MS
)

// Use is one call to a provider.
type Use struct {
	Provider string
	// Key is the key's name; "" for a call without one.
	Key string
	// Answered says it answered with results.
	Answered bool
	// CostDollars is what the provider says the call cost; 0 when it does not.
	CostDollars float64
	At          time.Time
}

// Tally counts calls.
type Tally struct {
	Answered int
	Failed   int
}

// Count is what was used of one provider's key, or of the provider without a key.
type Count struct {
	Provider string
	Key      string
	Today    Tally
	Month    Tally
	// MonthDollars is what the month's calls cost, as the provider said.
	MonthDollars float64
}

// Usage keeps the calls; the model gateway's tables hold them.
type Usage interface {
	RecordSearch(ctx context.Context, u Use) error
	// SearchCounts are the counts of the UTC day and month that now is in.
	SearchCounts(ctx context.Context, now time.Time) ([]Count, error)
}

// Balance is a key's balance as its provider tells it, in its credits.
type Balance struct {
	Used      float64
	Limit     float64
	Remaining float64
	CheckedAt time.Time
}

type balanceRead struct {
	balance Balance
	ok      bool
	at      time.Time
}

// record keeps one call.
func (s *Service) record(ctx context.Context, r route, answered bool, cost float64) {
	if s.cfg.Usage == nil {
		return
	}

	err := s.cfg.Usage.RecordSearch(context.WithoutCancel(ctx), Use{
		Provider: r.provider, Key: r.key.Name, Answered: answered, CostDollars: cost, At: s.now(),
	})
	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "recording a web search call",
			slog.String("event", "websearch.usage_failed"), slog.String("error", err.Error()))
	}
}

// hasBalance says the provider's balance can be read with a key.
func hasBalance(provider string) bool {
	return provider == ProviderTavily || provider == ProviderFirecrawl
}

// spent says a key's balance, read now or within balanceTTL, is spent.
func (s *Service) spent(ctx context.Context, r route) bool {
	b, ok := s.balance(ctx, r.key)

	return ok && b.Remaining <= 0
}

// balance reads a key's balance, from memory within balanceTTL; ok is false
// when its provider has none or it could not be read.
func (s *Service) balance(ctx context.Context, k Key) (Balance, bool) {
	if !hasBalance(k.Provider) {
		return Balance{}, false
	}

	s.mu.Lock()
	read, kept := s.balances[k.Name]
	s.mu.Unlock()

	if kept && s.now().Sub(read.at) < balanceTTL {
		return read.balance, read.ok
	}

	v, _, _ := s.flight.Do("balance:"+k.Name, func() (any, error) {
		run, cancel := context.WithTimeout(context.WithoutCancel(ctx), balanceTimeout)
		defer cancel()

		b, err := s.readBalance(run, k)
		if err != nil {
			s.cfg.Logger.WarnContext(ctx, "reading a web search key's balance",
				slog.String("event", "websearch.balance_failed"), slog.String("key", k.Name),
				slog.String("error", err.Error()))
		}

		read := balanceRead{balance: b, ok: err == nil, at: s.now()}

		s.mu.Lock()
		s.balances[k.Name] = read
		s.mu.Unlock()

		return read, nil
	})

	read, _ = v.(balanceRead)

	return read.balance, read.ok
}

func (s *Service) readBalance(ctx context.Context, k Key) (Balance, error) {
	endpoint := s.cfg.TavilyURL + "/usage"
	if k.Provider == ProviderFirecrawl {
		endpoint = s.cfg.FirecrawlURL + "/team/credit-usage"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Balance{}, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+k.Value)

	raw, err := s.send(req, maxMCPAnswer)
	if err != nil {
		return Balance{}, err
	}

	var b Balance

	if k.Provider == ProviderFirecrawl {
		b, err = firecrawlBalance(raw)
	} else {
		b, err = tavilyBalance(raw)
	}

	b.CheckedAt = s.now()

	return b, err
}

// tavilyBalance is OmniRoute's reading of Tavily's /usage: the key's limit
// and usage when the key has a limit, else the plan's, its pay-as-you-go
// allowance added.
func tavilyBalance(raw []byte) (Balance, error) {
	var u struct {
		Key struct {
			Usage *float64 `json:"usage"`
			Limit *float64 `json:"limit"`
		} `json:"key"`
		Account struct {
			PlanUsage  *float64 `json:"plan_usage"`
			PlanLimit  *float64 `json:"plan_limit"`
			PaygoUsage *float64 `json:"paygo_usage"`
			PaygoLimit *float64 `json:"paygo_limit"`
		} `json:"account"`
	}

	if err := json.Unmarshal(raw, &u); err != nil {
		return Balance{}, fmt.Errorf("reading Tavily's usage: %w", err)
	}

	num := func(v *float64) float64 {
		if v == nil {
			return 0
		}

		return *v
	}

	var b Balance

	switch {
	case u.Key.Limit != nil && *u.Key.Limit > 0:
		b.Limit, b.Used = *u.Key.Limit, num(u.Key.Usage)
	case u.Account.PlanLimit != nil && *u.Account.PlanLimit >= 0:
		b.Limit = *u.Account.PlanLimit + max(num(u.Account.PaygoLimit), num(u.Account.PaygoUsage), 0)
		b.Used = num(u.Account.PlanUsage) + num(u.Account.PaygoUsage)
	default:
		return Balance{}, fmt.Errorf("tavily's usage names no limit")
	}

	b.Remaining = max(b.Limit-b.Used, 0)

	return b, nil
}

// firecrawlBalance is OmniRoute's reading of Firecrawl's credit usage.
func firecrawlBalance(raw []byte) (Balance, error) {
	var u struct {
		Data struct {
			Remaining *float64 `json:"remainingCredits"`
			Plan      *float64 `json:"planCredits"`
		} `json:"data"`
	}

	if err := json.Unmarshal(raw, &u); err != nil {
		return Balance{}, fmt.Errorf("reading Firecrawl's credit usage: %w", err)
	}

	if u.Data.Remaining == nil || u.Data.Plan == nil {
		return Balance{}, fmt.Errorf("firecrawl's credit usage names no credits")
	}

	remaining, plan := max(*u.Data.Remaining, 0), max(*u.Data.Plan, 0)

	return Balance{Limit: plan, Remaining: remaining, Used: max(plan-remaining, 0)}, nil
}
