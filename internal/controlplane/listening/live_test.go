package listening_test

import (
	"context"
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
		Topic:   "VETC, VMAP, Cốc Cốc",
		Sources: []string{listening.SourceWeb, listening.SourceReddit},
		Subqueries: []listening.Subquery{
			{Label: "vetc", Query: "VETC thu phí không dừng trừ tiền"},
			{Label: "vmap", Query: "VMAP bản đồ số Việt Nam ra mắt"},
			{Label: "coc-coc", Query: "Cốc Cốc trải nghiệm người dùng Việt Nam"},
		},
		WindowDays: 90,
	}

	a, err := s.Search(t.Context(), plan, func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, query, limit, "")
	})
	require.NoError(t, err)

	t.Logf("window %s to %s, %d returned items, warnings %v", a.From, a.To, len(a.Items), a.Warnings)

	for _, c := range a.Coverage {
		t.Logf("coverage %s: %s %s, %d items, missing_date=%d outside_window=%d off_topic=%d",
			c.Source, c.State, c.Code, c.Items, c.Dropped.MissingDate, c.Dropped.OutsideWindow, c.Dropped.OffTopic)

		for _, st := range c.Streams {
			for _, at := range st.Tried {
				t.Logf("  %s %s: %s %s %s skipped=%v provider=%s window_basis=%s",
					st.Subquery, at.Backend, at.Result, at.Detail, at.Until, at.Skipped, at.Provider, at.WindowBasis)
			}
		}
	}

	for _, it := range a.Items {
		date := ""
		if it.PublishedAt != nil {
			date = it.PublishedAt.Format(time.DateOnly)
		}

		t.Logf("item %s %s %s %s %s %v %q", it.Source, it.Backend, date, it.DateConfidence, it.URL, it.Engagement, it.Title)
	}

	for _, c := range a.Clusters {
		t.Logf("cluster %s %s %.1f %v %d items %q", c.ID, c.Uncertainty, c.Score, c.Sources, len(c.Items), c.Title)
	}
}

// TestLiveEverySource is every source's live test (issue #1289: "it has a daily live test"; Agent-Reach `check` runs
// each action's live_test): each source reads its LiveQuery from the real site, and one not read fails. It never runs
// in CI: set HYPOMNEMA_LISTENING_LIVE=1; YouTube needs yt-dlp on PATH. One source at a time, as a daily check would.
func TestLiveEverySource(t *testing.T) {
	if os.Getenv("HYPOMNEMA_LISTENING_LIVE") == "" {
		t.Skip("live check: set HYPOMNEMA_LISTENING_LIVE=1 to read the real sources")
	}

	web := websearch.New(websearch.Config{})
	platforms := listening.Sources(listening.Endpoints{}, &http.Client{Timeout: 60 * time.Second})
	s := listening.New(listening.Config{Platforms: platforms})
	search := func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, query, limit, "")
	}

	for _, p := range platforms {
		t.Run(p.Name, func(t *testing.T) {
			plan := listening.Plan{
				Topic: p.LiveQuery, Sources: []string{p.Name}, WindowDays: 365, MaxResults: 10,
				Subqueries: []listening.Subquery{{Query: p.LiveQuery}},
			}

			// An opt-in source (Toutiao, off until the owner decides on its terms) is pinned to be read.
			for _, b := range p.Backends {
				if b.OptIn {
					plan.Backends = map[string]string{p.Name: b.Name}
				}
			}

			a, err := s.Search(t.Context(), plan, search)
			require.NoError(t, err)

			c := a.Coverage[0]
			t.Logf("%s: %s %s, %d items", c.Source, c.State, c.Code, c.Items)

			for _, at := range c.Streams[0].Tried {
				t.Logf("  %s: %s %s %s", at.Backend, at.Result, at.Detail, at.Until)
			}

			for i, it := range a.Items {
				if i == 5 {
					break
				}

				date := ""
				if it.PublishedAt != nil {
					date = it.PublishedAt.Format(time.DateOnly)
				}

				t.Logf("  item %s %s %v %v %q %q", date, it.URL, it.Engagement, it.Signals, it.Title, clipText(it.Text))
			}

			if c.State == listening.StateNotRead {
				t.Errorf("%s was not read: %s", p.Name, c.Code)
			}
		})
	}
}

func clipText(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}

	return s
}
