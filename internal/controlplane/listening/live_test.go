package listening_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// TestLiveRedditAndWeb reads the real Reddit, Arctic Shift and the web search's keyless providers, and logs what each
// backend answered. It never runs in CI: set HYPOMNEMA_LISTENING_LIVE=1 (issue #1287, the pull request's
// Verification).
func TestLiveRedditAndWeb(t *testing.T) {
	if os.Getenv("HYPOMNEMA_LISTENING_LIVE") == "" {
		t.Skip("live check: set HYPOMNEMA_LISTENING_LIVE=1 to read the real sources")
	}

	web := websearch.New(websearch.Config{})
	s := listening.New(listening.Config{Platforms: listening.Sources(listening.Endpoints{}, &http.Client{
		Timeout: 20 * time.Second,
	})})

	plan := listening.Plan{
		Topic:   "VETC",
		Sources: []string{listening.SourceWeb, listening.SourceReddit},
		Subqueries: []listening.Subquery{
			{Label: "vi", Query: "VETC thu phí không dừng trừ tiền"},
			{Label: "en", Query: "VETC toll Vietnam"},
		},
		WindowDays: 365,
	}

	a, err := s.Search(t.Context(), plan, func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, query, limit, "")
	})
	require.NoError(t, err)

	for _, c := range a.Coverage {
		t.Logf("coverage %s: %s %s, %d items", c.Source, c.State, c.Code, c.Items)

		for _, st := range c.Streams {
			for _, at := range st.Tried {
				t.Logf("  %s %s: %s %s %s skipped=%v", st.Subquery, at.Backend, at.Result, at.Detail, at.Until, at.Skipped)
			}
		}
	}

	for _, it := range a.Items {
		date := ""
		if it.PublishedAt != nil {
			date = it.PublishedAt.Format(time.DateOnly)
		}

		t.Logf("item %s %s %s %s %v %q", it.Source, it.Backend, date, it.URL, it.Engagement, it.Title)
	}

	for _, c := range a.Clusters {
		t.Logf("cluster %s %s %.1f %v %d items %q", c.ID, c.Uncertainty, c.Score, c.Sources, len(c.Items), c.Title)
	}
}

// TestLiveSourcesTest runs the live test of the real web and Reddit, as the Social listening page's daily test and
// its "Test again" do, and logs each source's state as the page shows it (issue #1290, the pull request's
// Verification). It never runs in CI: set HYPOMNEMA_LISTENING_LIVE=1.
func TestLiveSourcesTest(t *testing.T) {
	if os.Getenv("HYPOMNEMA_LISTENING_LIVE") == "" {
		t.Skip("live check: set HYPOMNEMA_LISTENING_LIVE=1 to read the real sources")
	}

	web := websearch.New(websearch.Config{})
	s := listening.New(listening.Config{DB: migrated(t), Platforms: listening.Sources(listening.Endpoints{},
		&http.Client{Timeout: 20 * time.Second})})
	search := func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, query, limit, "")
	}

	s.TestStale(t.Context(), search)

	states, err := s.States(t.Context())
	require.NoError(t, err)

	for _, st := range states {
		t.Logf("source %s: %s code=%q regressed=%v checked=%s", st.Name, st.Group, st.Code, st.Regressed,
			st.CheckedAt.Format(time.RFC3339))

		if st.LastError != nil {
			t.Logf("  last error %s %s: %s; fix: %s", st.LastError.Backend, st.LastError.Code, st.LastError.Detail,
				st.LastError.Fix)
		}

		for _, b := range st.Backends {
			rest := ""
			if b.Resting != nil {
				rest = fmt.Sprintf(" resting until %s (%s)", b.Resting.Until.Format(time.RFC3339), b.Resting.Code)
			}

			last := ""
			if b.LastError != nil {
				last = fmt.Sprintf(" last error %s: %s", b.LastError.Code, b.LastError.Detail)
			}

			t.Logf("  backend %s ok=%s%s%s", b.Name, b.LastOK.Format(time.RFC3339), rest, last)
		}
	}
}
