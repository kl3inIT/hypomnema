package modelgateway_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Scenario: the owner drags Tavily first and switches You.com off on the Web
// search page (issue #393). The search reads both back; an order that names
// a provider twice, or leaves one out, is refused and the stored one kept.
func TestTheOwnersSearchOrderAndSwitchesAreKept(t *testing.T) {
	t.Parallel()

	g := newGateway(t)
	ctx := t.Context()

	set, err := g.SearchSettings(ctx)
	require.NoError(t, err)
	assert.Empty(t, set.Order, "nothing stored: the default order")

	order := []string{"tavily", "exa", "firecrawl", "parallel", "tinyfish"}
	require.NoError(t, g.SetSearchOrder(ctx, order))
	require.NoError(t, g.SetSearchProvider(ctx, "you", false))

	for _, bad := range [][]string{
		{"exa", "exa", "firecrawl", "parallel", "tinyfish"}, {"exa", "firecrawl"},
		{"exa", "firecrawl", "parallel", "tinyfish", "you"},
	} {
		require.ErrorIs(t, g.SetSearchOrder(ctx, bad), modelgateway.ErrInvalid, "%v", bad)
	}

	require.ErrorIs(t, g.SetSearchProvider(ctx, "searxng", false), modelgateway.ErrNotFound, "SearXNG is gone")

	set, err = g.SearchSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, websearch.Settings{Off: []string{"you"}, Order: order}, set)
}

// Scenario: searches are counted by UTC day, for each provider and key:
// what answered, what failed, and what Exa said each cost. The counts of
// today and of the month are read back; a day of last month counts in
// neither, and the days before it are dropped.
func TestWebSearchCallsAreCountedByDayAndMonth(t *testing.T) {
	t.Parallel()

	g := newGateway(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 14, 23, 30, 0, 0, time.UTC)

	for _, u := range []websearch.Use{
		{Provider: "tavily", Key: "web-search-tavily", Answered: true, At: now.AddDate(0, -3, 0)},
		{Provider: "exa", Key: "web-search-exa", Answered: true, CostDollars: 0.007, At: now},
		{Provider: "exa", Key: "web-search-exa", Answered: false, At: now.Add(-time.Hour)},
		{Provider: "exa", Key: "web-search-exa", Answered: true, CostDollars: 0.005, At: now.AddDate(0, 0, -3)},
		{Provider: "exa", Key: "web-search-exa", Answered: true, CostDollars: 1, At: now.AddDate(0, -1, 0)},
		{Provider: "you", Answered: true, At: now},
	} {
		require.NoError(t, g.RecordSearch(ctx, u))
	}

	counts, err := g.SearchCounts(ctx, now)
	require.NoError(t, err)
	require.Len(t, counts, 2, "last month's and older calls are not this month's")

	exa := counts[0]
	assert.Equal(t, "exa", exa.Provider)
	assert.Equal(t, "web-search-exa", exa.Key)
	assert.Equal(t, websearch.Tally{Answered: 1, Failed: 1}, exa.Today)
	assert.Equal(t, websearch.Tally{Answered: 2, Failed: 1}, exa.Month)
	assert.InDelta(t, 0.012, exa.MonthDollars, 1e-9)

	assert.Equal(t, websearch.Count{
		Provider: "you", Today: websearch.Tally{Answered: 1}, Month: websearch.Tally{Answered: 1},
	}, counts[1])

	// Tavily's call three months back was dropped by the later ones: a read from its month finds none of it.
	old, err := g.SearchCounts(ctx, now.AddDate(0, -3, 0))
	require.NoError(t, err)

	for _, c := range old {
		assert.NotEqual(t, "tavily", c.Provider)
	}
}
