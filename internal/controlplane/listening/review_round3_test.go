package listening_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 3 of PR #1322 (issue #1290): the reviewer's probe, failing on the reviewed code.

// P1: one keyless provider's 504, 429 or 402 among four 500s is a failure that may come back by itself, and the web
// backend then rests; the next search a second later is skipped on that rest, and must not tell the owner either:
// the rest keeps the transient signal it was made from (ARCHITECTURE.md, "A reading is not told when any backend it
// tried, or any web search provider behind one, failed with a rate limit, a spent quota or a timeout").
func TestReviewerMixedFailureMustNotAlertWhileResting(t *testing.T) {
	t.Parallel()

	for _, transient := range []int{http.StatusGatewayTimeout, http.StatusTooManyRequests, http.StatusPaymentRequired} {
		t.Run(http.StatusText(transient), func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)

				if strings.HasPrefix(r.URL.Path, "/exa") {
					w.WriteHeader(transient)

					return
				}

				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)

			web := websearch.New(websearch.Config{
				ExaMCPURL: srv.URL + "/exa", ParallelMCPURL: srv.URL + "/parallel", TavilyURL: srv.URL + "/tavily",
				FirecrawlURL: srv.URL + "/firecrawl", YouMCPURL: srv.URL + "/you",
			})
			ask := func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
				return web.Search(ctx, query, limit, "")
			}

			c := newClock()
			tell := &told{}
			s := kept(t, c, tell, webOnly())

			_, err := s.Search(t.Context(), plan([]string{"web"}, "VETC"), listening.Caller{Web: ask})
			require.NoError(t, err)
			require.Equal(t, 0, tell.Count(), "the first search is not told")

			asked := calls.Load()

			c.Advance(time.Second)

			a, err := s.Search(t.Context(), plan([]string{"web"}, "VETC trừ tiền"), listening.Caller{Web: ask})
			require.NoError(t, err)
			require.True(t, coverageOf(t, a, "web").Streams[0].Tried[0].Skipped, "the web backend rests")
			assert.Equal(t, asked, calls.Load(), "no provider was asked again")
			assert.Equal(t, 0, tell.Count(), "a search skipped on that rest is not told either")
		})
	}
}

// The control: a refusal (403) behind the web is no transient failure, and is told once.
func TestReviewerRefusedWebIsToldOnce(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	web := websearch.New(websearch.Config{
		ExaMCPURL: srv.URL, ParallelMCPURL: srv.URL, TavilyURL: srv.URL, FirecrawlURL: srv.URL, YouMCPURL: srv.URL,
	})
	ask := func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, query, limit, "")
	}

	c := newClock()
	tell := &told{}
	s := kept(t, c, tell, webOnly())

	for range 2 {
		_, err := s.Search(t.Context(), plan([]string{"web"}, "VETC"), listening.Caller{Web: ask})
		require.NoError(t, err)
		c.Advance(time.Second)
	}

	assert.Equal(t, 1, tell.Count())
	assert.Equal(t, listening.GroupBroken, stateOf(t, s, "web").Group)
}
