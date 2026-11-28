package listening_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 1 of PR #1322 (issue #1290): the reviewer's probes, failing on the reviewed code.

// keylessWeb is the web search chain over one fake server for every keyless provider, and the listening service's
// web source on it.
type keylessWeb struct {
	calls  atomic.Int32
	status atomic.Int32
	svc    *websearch.Service
}

func newKeylessWeb(t *testing.T, only ...string) *keylessWeb {
	t.Helper()

	k := &keylessWeb{}
	k.status.Store(http.StatusOK)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.calls.Add(1)

		if s := int(k.status.Load()); s != http.StatusOK {
			w.WriteHeader(s)

			return
		}

		// Exa's MCP is asked its advanced search, which answers JSON, when the search names a date window (#1319).
		text := `Title: Python 3.15 released\nURL: https://python.example/3.15\nPublished: 2026-11-20\n` +
			`Author: N/A\nHighlights:\nPython 3.15 is out.`
		if r.URL.Query().Get("tools") == "web_search_advanced_exa" {
			text = `{\"results\":[{\"title\":\"Python 3.15 released\",\"url\":\"https://python.example/3.15\",` +
				`\"publishedDate\":\"2026-11-20\",\"text\":\"Python 3.15 is out.\"}]}`
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: message` + "\n" + `data: {"jsonrpc":"2.0","id":1,"result":{"content":[` +
			`{"type":"text","text":"` + text + `"}]}}` + "\n\n"))
	}))
	t.Cleanup(srv.Close)

	var off []string

	if len(only) > 0 {
		for _, p := range []string{"exa", "parallel", "firecrawl", "tavily", "you"} {
			if p != only[0] {
				off = append(off, p)
			}
		}
	}

	k.svc = websearch.New(websearch.Config{
		ExaMCPURL: srv.URL, ParallelMCPURL: srv.URL, TavilyURL: srv.URL, FirecrawlURL: srv.URL, YouMCPURL: srv.URL,
		Settings: func(context.Context) (websearch.Settings, error) { return websearch.Settings{Off: off}, nil },
	})

	return k
}

func (k *keylessWeb) search(ctx context.Context, query string, limit int) (websearch.Answer, error) {
	return k.svc.Search(ctx, query, limit, "")
}

func webOnly() listening.Platform {
	return listening.Sources(listening.Endpoints{}, http.DefaultClient)[0]
}

// P1: every keyless provider answering 504 is a timeout, which comes back by itself: the web's code stays timeout
// through the chain, and the owner is not told (fix.go tells).
func TestReviewTimeoutNotification(t *testing.T) {
	t.Parallel()

	web := newKeylessWeb(t)
	web.status.Store(http.StatusGatewayTimeout)

	tell := &told{}
	s := kept(t, newClock(), tell, webOnly())

	a, err := s.Search(t.Context(), plan([]string{"web"}, "VETC"), web.search)
	require.NoError(t, err)

	cov := coverageOf(t, a, "web")
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeTimeout, cov.Code, "five 504s are a timeout, not a broken source")
	assert.Equal(t, 0, tell.Count(), "a timeout is not told")
}

// P1: Reddit's JSON refused (as from zm) while its fallback is throttled comes back by itself once the fallback is
// free: the owner is not told.
func TestReviewRedditFallbackThrottleNotifies(t *testing.T) {
	t.Parallel()

	json := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	arctic := &backend{err: listening.Fail(listening.CodeRateLimited, "HTTP 429")}
	tell := &told{}
	s := kept(t, newClock(), tell, listening.Platform{Name: "reddit", Backends: []listening.Backend{
		json.spec("reddit_json", "reddit.example"), arctic.spec("web_arctic", "arctic.example"),
	}})

	search(t, s, "reddit")
	assert.Equal(t, 0, tell.Count(), "its fallback rests and comes back by itself")
}

// P1: "Test again" and the daily test read the source fresh, never an answer the web search keeps for five minutes
// (last30days doctor.py:2882-2902, an explicit doctor runs live; :2954-2958, the live probe).
func TestReviewBrokenCardRetestReportsStaleWorking(t *testing.T) {
	t.Parallel()

	web := newKeylessWeb(t, "exa")
	s := kept(t, newClock(), nil, webOnly())

	require.NoError(t, s.Test(t.Context(), "web", web.search, true))
	require.Equal(t, listening.GroupWorking, stateOf(t, s, "web").Group)

	before := web.calls.Load()
	web.status.Store(http.StatusInternalServerError)

	require.NoError(t, s.Test(t.Context(), "web", web.search, true))
	assert.Greater(t, web.calls.Load(), before, "the test asked the provider again")
	assert.Equal(t, listening.GroupBroken, stateOf(t, s, "web").Group, "the upstream still fails")
}

// P1: once the owner saved Off, a search in progress starts no new read of the source.
func TestReviewDefaultWebContinuesAfterOwnerSwitchesOff(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})

	var calls atomic.Int32

	held := listening.Backend{
		Name: "web_search", Host: "web",
		Search: func(context.Context, listening.Request) (listening.Result, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}

			return listening.Result{Items: []listening.Item{post("p1", "VETC", 0)}}, nil
		},
	}
	s := kept(t, newClock(), nil, listening.Platform{Name: "web", Backends: []listening.Backend{held}})

	var (
		wg     sync.WaitGroup
		answer listening.Answer
		err    error
	)

	wg.Go(func() {
		answer, err = s.Search(t.Context(), plan([]string{"web"}, "VETC", "VETC trừ tiền"), nil)
	})

	<-started
	require.NoError(t, s.SetSource(t.Context(), "web", false))
	close(release)
	wg.Wait()

	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "no read starts after Off")
	assert.Equal(t, listening.StateOff, coverageOf(t, answer, "web").State)
	assert.Empty(t, answer.Items, "nothing of a source switched off is answered")
	assert.Equal(t, listening.GroupCouldBeOn, stateOf(t, s, "web").Group)
}

// P2: a clean zero between two failures does not tell the owner again: only a reading with items clears the flag,
// as only a refresh that returns an access token clears postiz's refreshNeeded (refresh.integration.service.ts:20-54,
// 148-166).
func TestReviewEmptyReadRearmsBrokenNotification(t *testing.T) {
	t.Parallel()

	c := newClock()
	b := &backend{}
	tell := &told{}
	s := kept(t, c, tell, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("api", "f.example")}})

	for range 3 {
		b.set(nil, listening.Fail(listening.CodeBroken, "a page, not JSON"))
		search(t, s, "forum")
		c.Advance(time.Hour)

		b.set(nil, nil)
		search(t, s, "forum")
		c.Advance(time.Hour)
	}

	assert.Equal(t, 1, tell.Count(), "told once: no reading with items came in between")
}

// P2: "Regressed" is Agent-Reach's: the reading right before the latest passed and the latest fails
// (runtime/manage.py:189-216, previous and latest check), whatever the days between them.
func TestReviewRegressedSkipsMissingYesterday(t *testing.T) {
	t.Parallel()

	c := newClock()
	b := &backend{items: []listening.Item{post("p1", "VETC", 0)}}
	s := kept(t, c, nil, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("api", "f.example")}})

	search(t, s, "forum")
	c.Advance(time.Hour)
	b.set(nil, listening.Fail(listening.CodeBroken, "down"))
	search(t, s, "forum")
	assert.True(t, stateOf(t, s, "forum").Regressed, "it passed in the reading right before, the same day")

	c.Advance(time.Hour)
	search(t, s, "forum")
	assert.False(t, stateOf(t, s, "forum").Regressed, "the reading right before failed too")

	c.Advance(time.Hour)
	b.set([]listening.Item{post("p2", "VETC", 0)}, nil)
	search(t, s, "forum")
	require.Equal(t, listening.GroupWorking, stateOf(t, s, "forum").Group)
	c.Advance(48 * time.Hour)
	b.set(nil, listening.Fail(listening.CodeBroken, "down"))
	search(t, s, "forum")
	assert.True(t, stateOf(t, s, "forum").Regressed, "two days apart, the reading right before passed")
}
