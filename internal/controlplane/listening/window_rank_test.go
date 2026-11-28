package listening

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRankDownweightsAnOutOfWindowSurvivor(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	atStart := to.AddDate(0, 0, -30)
	before := atStart.AddDate(0, 0, -1)
	score := func(published time.Time) float64 {
		items := rank(Plan{WindowDays: 30}, []stream{{
			source: "web", subquery: Subquery{Label: "q1", Query: "VETC", Weight: 1},
			items: []Item{{Source: "web", NativeID: "a", URL: "https://example.org/a",
				Title: "VETC", PublishedAt: &published}},
		}}, to)
		require.Len(t, items, 1)

		return items[0].Score
	}
	assert.InDelta(t, math.Round(score(atStart)*0.35*10)/10, score(before), 0.1)
}
