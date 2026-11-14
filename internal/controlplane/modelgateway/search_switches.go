package modelgateway

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// What the owner sets on Settings → Gateway → Web search (issues #354, #393):
// a switch for each search provider and the order of the keyed ones. A
// provider that is off is neither tried nor may be named; each is on until
// the owner turns it off (owner, 2026-11-13), TinyFish too (owner,
// 2026-11-14). The count of each provider's calls is kept here as well.
// They are kept here because web search is the gateway's: it serves
// /v1/search, and the gateway_* tables are its own.

// SearchSettings reads what the owner set, for each search.
func (s *Service) SearchSettings(ctx context.Context) (websearch.Settings, error) {
	rows, err := store.New(s.db).ListGatewaySearchProviders(ctx)
	if err != nil {
		return websearch.Settings{}, fmt.Errorf("reading the search providers' settings: %w", err)
	}

	var (
		out    websearch.Settings
		placed []store.GatewaySearchProvider
	)

	for _, r := range rows {
		if r.Off != 0 {
			out.Off = append(out.Off, r.Provider)
		}

		if r.Position.Valid {
			placed = append(placed, r)
		}
	}

	slices.SortFunc(placed, func(a, b store.GatewaySearchProvider) int {
		return cmp.Or(cmp.Compare(a.Position.Int64, b.Position.Int64), cmp.Compare(a.Provider, b.Provider))
	})

	for _, r := range placed {
		out.Order = append(out.Order, r.Provider)
	}

	return out, nil
}

// SetSearchProvider switches a search provider on or off.
func (s *Service) SetSearchProvider(ctx context.Context, provider string, on bool) error {
	if !slices.Contains(websearch.Providers(), provider) {
		return fmt.Errorf("%w: no search provider %q", ErrNotFound, provider)
	}

	off := int64(1)
	if on {
		off = 0
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.SetGatewaySearchOff(ctx, store.SetGatewaySearchOffParams{
			Provider: provider, Off: off, Now: now,
		}); err != nil {
			return fmt.Errorf("switching search provider %s: %w", provider, err)
		}

		return nil
	})
}

// SetSearchOrder keeps the order of the keyed providers: each of them once.
func (s *Service) SetSearchOrder(ctx context.Context, providers []string) error {
	keyed := websearch.KeyedProviders()
	if len(providers) != len(keyed) || !sameSet(providers, keyed) {
		return fmt.Errorf("%w: the order names each keyed provider once: %v", ErrInvalid, keyed)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		for i, p := range providers {
			if err := q.SetGatewaySearchPosition(ctx, store.SetGatewaySearchPositionParams{
				Provider: p, Position: sql.NullInt64{Int64: int64(i + 1), Valid: true}, Now: now,
			}); err != nil {
				return fmt.Errorf("placing search provider %s: %w", p, err)
			}
		}

		return nil
	})
}

func sameSet(a, b []string) bool {
	x, y := slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))

	return slices.Equal(slices.Compact(x), y)
}

// usageDay is a UTC day as the usage rows name it.
func usageDay(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// monthStart is the first day of the UTC month t is in.
func monthStart(t time.Time) time.Time {
	t = t.UTC()

	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// RecordSearch counts one web search call on its UTC day, and drops the
// days before last month: the page shows today and this month.
func (s *Service) RecordSearch(ctx context.Context, u websearch.Use) error {
	answered, failed := int64(0), int64(1)
	if u.Answered {
		answered, failed = 1, 0
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if err := q.CountGatewaySearch(ctx, store.CountGatewaySearchParams{
			Provider: u.Provider, Key: u.Key, Day: usageDay(u.At), Answered: answered, Failed: failed,
			CostMicros: int64(math.Round(u.CostDollars * 1e6)),
		}); err != nil {
			return fmt.Errorf("counting a web search call: %w", err)
		}

		if err := q.PruneGatewaySearchUsage(ctx, usageDay(monthStart(u.At).AddDate(0, -1, 0))); err != nil {
			return fmt.Errorf("dropping old web search counts: %w", err)
		}

		return nil
	})
}

// SearchCounts are the counts of the UTC day and month now is in, for each
// provider and key.
func (s *Service) SearchCounts(ctx context.Context, now time.Time) ([]websearch.Count, error) {
	rows, err := store.New(s.db).ListGatewaySearchUsage(ctx, usageDay(monthStart(now)))
	if err != nil {
		return nil, fmt.Errorf("reading the web search counts: %w", err)
	}

	today := usageDay(now)
	byKey := map[[2]string]*websearch.Count{}

	var order [][2]string

	for _, r := range rows {
		id := [2]string{r.Provider, r.Key}

		c, ok := byKey[id]
		if !ok {
			c = &websearch.Count{Provider: r.Provider, Key: r.Key}
			byKey[id] = c
			order = append(order, id)
		}

		c.Month.Answered += int(r.Answered)
		c.Month.Failed += int(r.Failed)
		c.MonthDollars += float64(r.CostMicros) / 1e6

		if r.Day == today {
			c.Today.Answered += int(r.Answered)
			c.Today.Failed += int(r.Failed)
		}
	}

	out := make([]websearch.Count, 0, len(order))
	for _, id := range order {
		out = append(out, *byKey[id])
	}

	return out, nil
}
