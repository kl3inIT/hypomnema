package listening_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 2 of PR #1322 (issue #1290): the reviewer's probes, failing on the reviewed code.

// P1: one keyless provider that times out or is rate limited among four that answer 500 is a failure that may come
// back by itself: the owner is not told (ARCHITECTURE.md, "A reading is not told when any backend it tried failed
// with a rate limit, a spent quota or a timeout"), whatever code the web's failure takes.
func TestReviewerMixedTimeoutStillTellsOwner(t *testing.T) {
	t.Parallel()

	for _, transient := range []int{http.StatusGatewayTimeout, http.StatusTooManyRequests} {
		t.Run(http.StatusText(transient), func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			tell := &told{}
			s := kept(t, newClock(), tell, webOnly())

			_, err := s.Search(t.Context(), plan([]string{"web"}, "VETC"),
				listening.Caller{Web: func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
					return web.Search(ctx, query, limit, "")
				}})
			require.NoError(t, err)
			assert.Equal(t, 0, tell.Count(), "one provider's %d comes back by itself", transient)
		})
	}
}

// P1: the owner switches the web off while its one and last request is out: the answer has no item of it, its
// coverage is off, and nothing of the read is kept.
func TestReviewerOffDuringSingleWebRequest(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	held := listening.Backend{
		Name: "web_search", Host: "web",
		Search: func(context.Context, listening.Request) (listening.Result, error) {
			close(started)
			<-release

			return listening.Result{Items: []listening.Item{post("p1", "VETC", 0)}}, nil
		},
	}
	s := kept(t, newClock(), nil, listening.Platform{Name: "web", Backends: []listening.Backend{held}})

	var (
		wg     sync.WaitGroup
		answer listening.Answer
		err    error
	)

	wg.Go(func() { answer, err = s.Search(t.Context(), plan([]string{"web"}, "VETC"), listening.Caller{}) })

	<-started
	require.NoError(t, s.SetSource(t.Context(), "web", false))
	close(release)
	wg.Wait()

	require.NoError(t, err)
	assert.Equal(t, listening.StateOff, coverageOf(t, answer, "web").State)
	assert.Empty(t, answer.Items)

	st := stateOf(t, s, "web")
	assert.Zero(t, st.ItemsThisWeek, "nothing of the read is kept")
	assert.True(t, st.CheckedAt.IsZero(), "no reading is kept")
}

// P2: a read in part that read items is a recovery: the next failure tells the owner again, as a passed read with
// items does (postiz clears refreshNeeded on a refresh that returns a token).
func TestReviewerItemsFromPartialReadRearmAlert(t *testing.T) {
	t.Parallel()

	c := newClock()
	failing := true
	half := listening.Backend{
		Name: "api", Host: "f.example",
		Search: func(_ context.Context, r listening.Request) (listening.Result, error) {
			if failing || r.Query == "ETC" {
				return listening.Result{}, listening.Fail(listening.CodeBroken, "a page, not JSON")
			}

			return listening.Result{Items: []listening.Item{post("p1", "VETC", 0)}}, nil
		},
	}
	tell := &told{}
	s := kept(t, c, tell, listening.Platform{Name: "forum", Backends: []listening.Backend{half}})

	search(t, s, "forum")
	require.Equal(t, 1, tell.Count())

	c.Advance(time.Hour)
	failing = false
	_, err := s.Search(t.Context(), plan([]string{"forum"}, "VETC", "ETC"), listening.Caller{})
	require.NoError(t, err)
	require.Equal(t, listening.GroupUnverified, stateOf(t, s, "forum").Group, "read in part, with an item")

	c.Advance(time.Hour)
	failing = true
	search(t, s, "forum")
	assert.Equal(t, 2, tell.Count(), "the partial read with items cleared the flag")
}
