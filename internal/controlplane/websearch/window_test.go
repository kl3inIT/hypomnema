package websearch_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

func TestWindowUsesEachProvidersDocumentedArguments(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, provider, bodyPart string
		stand                    func(*world) *provider
		basis                    websearch.WindowBasis
		key                      bool
	}{
		{"exa REST", "exa", `"startPublishedDate":"2026-11-01T00:00:00.000Z"`,
			func(w *world) *provider { return w.exa }, websearch.WindowServerBounds, true},
		{"exa MCP", "exa", `"endPublishedDate":"2026-11-13T23:59:59.999Z"`,
			func(w *world) *provider { return w.exaMCP }, websearch.WindowServerBounds, false},
		{"keyed Parallel", "parallel", `"source_policy":{"after_date":"2026-11-01"}`,
			func(w *world) *provider { return w.parallel }, websearch.WindowServerBounds, true},
		{"Tavily", "tavily", `"filter_by_published_date":true`,
			func(w *world) *provider { return w.tavily }, websearch.WindowServerBounds, false},
		{"You.com", "you", `"freshness":"2026-11-01to2026-11-13"`,
			func(w *world) *provider { return w.you }, websearch.WindowNone, false},
		{"TinyFish", "tinyfish", "", func(w *world) *provider { return w.tinyFish }, websearch.WindowServerBounds, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keys := map[string][]string{}
			if tc.key {
				keys[tc.provider] = []string{"test-key"}
			}

			w := newWorld(t, options{keys: keys})
			w.exaMCP.set(mcpText(exaAnswer))
			to := w.clock.Now().UTC()
			from := time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, time.UTC)
			ctx := websearch.WithWindow(t.Context(), from, to)
			a, err := w.svc.Search(ctx, "go", 3, tc.provider)
			require.NoError(t, err)
			assert.Equal(t, tc.basis, a.WindowBasis)

			req, body := tc.stand(w).request()
			if tc.bodyPart != "" {
				assert.Contains(t, body, tc.bodyPart)
			}

			switch tc.name {
			case "exa MCP":
				assert.Equal(t, "web_search_advanced_exa", req.URL.Query().Get("tools"))
				require.NotEmpty(t, a.Results)
				assert.Equal(t, "2026-03-04", a.Results[0].PublishedAt)
			case "Tavily":
				assert.Contains(t, body, `"start_date":"2026-11-01"`)
				assert.Contains(t, body, `"end_date":"2026-11-13"`)
			case "TinyFish":
				assert.Equal(t, "2026-11-01", req.URL.Query().Get("after_date"))
				assert.Equal(t, "2026-11-13", req.URL.Query().Get("before_date"))
			}
		})
	}
}

func TestSearchCacheKeepsDifferentWindowsApart(t *testing.T) {
	t.Parallel()

	w := newWorld(t, options{})
	now := w.clock.Now()
	start := now.AddDate(0, 0, -90)
	query := "Go releases"

	for _, ctx := range []struct {
		from, to time.Time
		cached   bool
	}{
		{from: start, to: now},
		{from: start, to: now.AddDate(0, 0, -1)},
		{from: start.AddDate(0, 0, 1), to: now},
		{from: start, to: now, cached: true},
	} {
		a, err := w.svc.Search(websearch.WithWindow(t.Context(), ctx.from, ctx.to), query, 2, "firecrawl")
		require.NoError(t, err)
		assert.Equal(t, ctx.cached, a.Cached)
	}

	a, err := w.svc.Search(t.Context(), query, 2, "firecrawl")
	require.NoError(t, err)
	assert.False(t, a.Cached)
	assert.Equal(t, websearch.WindowNone, a.WindowBasis)
	assert.Equal(t, int32(4), w.firecrawl.calls.Load())
}
