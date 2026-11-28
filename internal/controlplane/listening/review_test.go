package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Tests for the findings of review round 1 of PR #1303, each named as the reviewer named it.

// threadsWeb is a web search that finds the given Reddit threads, by id and title.
func threadsWeb(threads ...[2]string) listening.WebSearch {
	return func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		var results []websearch.Result
		for _, th := range threads {
			results = append(results, websearch.Result{
				Title: th[1], URL: "https://www.reddit.com/r/VietNam/comments/" + th[0] + "/x/",
			})
		}

		return websearch.Answer{Provider: "exa", Query: query, Results: results}, nil
	}
}

// archiveOf answers /api/posts/ids with the posts given, by id, each with its title and age in days.
func archiveOf(t *testing.T, posts map[string]struct {
	title string
	days  int
},
) (*site, string) {
	t.Helper()

	return startSite(t, func(w http.ResponseWriter, r *http.Request) {
		var rows []map[string]any

		for id := range strings.SplitSeq(r.URL.Query().Get("ids"), ",") {
			if p, ok := posts[id]; ok {
				rows = append(rows, map[string]any{
					"id": id, "title": p.title, "subreddit": "VietNam", "author": "a",
					"permalink":   "/r/VietNam/comments/" + id + "/x/",
					"created_utc": start.AddDate(0, 0, -p.days).Unix(), "score": 10, "num_comments": 2,
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
	})
}

func redditOnly(t *testing.T, c *clock, redditURL, arcticURL string) *listening.Service {
	t.Helper()

	return listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: redditURL, ArcticURL: arcticURL},
			http.DefaultClient),
		Now: c.Now, Sleep: noSleep,
	})
}

// Finding 1: a Reddit thread the web search found whose title does not name the topic is not evidence: last30days
// puts web-found threads through its topic bar (reddit_keyless.py:500-508). With only off-topic threads, Reddit is a
// clean zero, never "ok".
func TestReviewOffTopicRedditThreadsAreDropped(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)
	_, arcticURL := archiveOf(t, map[string]struct {
		title string
		days  int
	}{
		"aaa1": {"VETC trừ tiền hai lần ở trạm Long Thành", 2},
		"bbb2": {"Honda D16 VTEC intake manifold", 3},
		"ccc3": {"Trường Giang lên tiếng", 4},
	})
	s := redditOnly(t, c, redditURL, arcticURL)

	p := plan([]string{listening.SourceReddit}, "VETC")
	a, err := s.Search(t.Context(), p, threadsWeb([2]string{"aaa1", "x"}, [2]string{"bbb2", "y"}, [2]string{"ccc3", "z"}))
	require.NoError(t, err)

	require.Len(t, a.Items, 1)
	assert.Equal(t, "aaa1", a.Items[0].NativeID)

	cov := coverageOf(t, a, listening.SourceReddit)
	assert.Equal(t, 1, cov.Items)
	assert.Contains(t, cov.Streams[0].Tried[1].Detail, "2 of 3 threads dropped for not naming the topic")

	a, err = s.Search(t.Context(), p, threadsWeb([2]string{"bbb2", "y"}, [2]string{"ccc3", "z"}))
	require.NoError(t, err)
	assert.Empty(t, a.Items)
	assert.Equal(t, listening.StateNoResults, coverageOf(t, a, listening.SourceReddit).State)
}

// Finding 2: every thread found is read from the archive before the window and the cap apply (last30days
// reddit_hydrate.hydrate: the archive, then _in_window), so an in-window thread found eleventh is kept; and a stream
// keeps 25 by default, last30days' default depth (reddit_public.py:39-44).
func TestReviewWindowBeforeStreamCap(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)

	posts := map[string]struct {
		title string
		days  int
	}{}

	var found [][2]string

	for i := range 11 {
		id := fmt.Sprintf("t%02d", i)
		days := 200
		if i == 10 {
			days = 3
		}

		posts[id] = struct {
			title string
			days  int
		}{"VETC chuyện số " + id, days}
		found = append(found, [2]string{id, "VETC"})
	}

	archived, arcticURL := archiveOf(t, posts)
	s := redditOnly(t, c, redditURL, arcticURL)

	a, err := s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), threadsWeb(found...))
	require.NoError(t, err)

	require.Len(t, archived.Requests(), 1)
	assert.Len(t, strings.Split(archived.Requests()[0].URL.Query().Get("ids"), ","), 11, "every thread is looked up")

	cov := coverageOf(t, a, listening.SourceReddit)
	assert.Equal(t, listening.StateOK, cov.State)
	require.Len(t, a.Items, 1)
	assert.Equal(t, "t10", a.Items[0].NativeID)
	assert.Equal(t, 25, listening.DefaultResults)
}

// Finding 3: the web searches a plan makes go through pacing where the chain knows the route: a plan reading the
// web and Reddit asks one keyless provider for "VETC" and "site:reddit.com VETC", and the provider never has two of
// them at once, also across plans running together.
func TestReviewWebCallsDoNotOverlapOnARoute(t *testing.T) {
	t.Parallel()

	var (
		inFlight, most atomic.Int32
		calls          atomic.Int32
	)

	_, exaURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)

		for {
			seen := most.Load()
			if now <= seen || most.CompareAndSwap(seen, now) {
				break
			}
		}

		calls.Add(1)
		time.Sleep(30 * time.Millisecond)

		text, _ := json.Marshal("Title: VETC trừ tiền\nURL: https://www.reddit.com/r/VietNam/comments/abc123/x/\n" +
			"Published: 2026-11-25\nText: VETC trừ tiền hai lần")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n",
			`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":`+string(text)+`}]}}`)
	})
	web := websearch.New(websearch.Config{
		ExaMCPURL: exaURL,
		Settings: func(context.Context) (websearch.Settings, error) {
			return websearch.Settings{Off: []string{"parallel", "firecrawl", "tavily", "you"}}, nil
		},
	})
	_, redditURL := startSite(t, blockedPage)
	_, arcticURL := archiveOf(t, nil)
	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: redditURL, ArcticURL: arcticURL},
			http.DefaultClient),
		Sleep: noSleep,
	})
	search := func(ctx context.Context, q string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, q, limit, "")
	}

	var wg sync.WaitGroup

	errs := make([]error, 3)

	for i := range 3 {
		wg.Go(func() {
			p := plan([]string{listening.SourceWeb, listening.SourceReddit}, fmt.Sprintf("VETC %d", i))
			_, errs[i] = s.Search(t.Context(), p, search)
		})
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(6), calls.Load(), "three plans, a web and a site:reddit.com search each")
	assert.Equal(t, int32(1), most.Load(), "one call at a time to the provider")
}

// Finding 4, Reddit: a search queued behind one that Reddit refused does not send its own request: the path's rest
// is checked again once the call has its turn at the host.
func TestReviewQueued403(t *testing.T) {
	t.Parallel()

	c := newClock()
	reddit, redditURL := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		blockedPage(w, r)
	})
	s := redditOnly(t, c, redditURL, "http://127.0.0.1:1")

	var wg sync.WaitGroup

	answers := make([]listening.Answer, 2)
	errs := make([]error, 2)

	for i := range 2 {
		wg.Go(func() {
			answers[i], errs[i] = s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), nil)
		})
	}

	wg.Wait()

	for i := range 2 {
		require.NoError(t, errs[i])
		assert.Equal(t, listening.CodeRefused, coverageOf(t, answers[i], listening.SourceReddit).Code)
	}
	assert.Len(t, reddit.Requests(), 1, "the second search found the path resting after the first's 403")
}

// Finding 4, Arctic Shift: a 429 from the archive rests the archive's path for its Retry-After, even though the
// threads are still served as the search showed them; the next subquery does not ask the archive.
func TestReviewArchiveRateLimit(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)
	archived, arcticURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	s := redditOnly(t, c, redditURL, arcticURL)

	a, err := s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC một", "VETC hai"),
		threadsWeb([2]string{"abc123", "VETC trừ tiền"}))
	require.NoError(t, err)

	assert.Len(t, archived.Requests(), 1, "the archive rests 120 s after its 429")

	streams := coverageOf(t, a, listening.SourceReddit).Streams
	require.Len(t, streams, 2)
	until := start.Add(120 * time.Second).UTC().Format(time.RFC3339)
	assert.Contains(t, streams[0].Tried[1].Detail, "arctic-shift not read: rate_limited: HTTP 429 until "+until)
	assert.Contains(t, streams[1].Tried[1].Detail, "arctic-shift not read: rate_limited")
	assert.Contains(t, streams[1].Tried[1].Detail, until)
	assert.Equal(t, "web_arctic", streams[1].ServedBy, "the threads still serve, as the search showed them")
}

// Finding 5: when every web search provider was rate limited, the web is not read with rate_limited and rests until
// the first provider is free, not broken; a provider that failed without resting makes it broken.
func TestReviewWebRateLimitCode(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := listening.New(listening.Config{Platforms: listening.Sources(listening.Endpoints{}, http.DefaultClient),
		Now: c.Now, Sleep: noSleep})

	limited := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{}, &websearch.FailedError{Asked: 5, Rests: []websearch.RouteRest{
			{Provider: "exa", Why: websearch.RestRateLimited, Until: start.Add(90 * time.Second)},
			{Provider: "tavily", Why: websearch.RestRateLimited, Until: start.Add(30 * time.Second)},
			{Provider: "you", Why: websearch.RestDailyLimit, Until: start.Add(10 * time.Hour)},
			{Provider: "parallel", Why: websearch.RestRateLimited, Until: start.Add(40 * time.Second)},
			{Provider: "firecrawl", Why: websearch.RestRateLimited, Until: start.Add(50 * time.Second)},
		}}
	}

	a, err := s.Search(t.Context(), plan([]string{listening.SourceWeb}, "VETC"), limited)
	require.NoError(t, err)

	cov := coverageOf(t, a, listening.SourceWeb)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeLimitReached, cov.Code, "You.com's daily limit is the most actionable")
	assert.Equal(t, start.Add(30*time.Second).Format(time.RFC3339), cov.Streams[0].Tried[0].Until,
		"it rests until the first provider is free")

	c.Advance(time.Minute)

	broken := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{}, &websearch.FailedError{Asked: 2, Others: 1, Rests: []websearch.RouteRest{
			{Provider: "exa", Why: websearch.RestRateLimited, Until: start.Add(2 * time.Minute)},
		}}
	}

	a, err = s.Search(t.Context(), plan([]string{listening.SourceWeb}, "VETC"), broken)
	require.NoError(t, err)
	assert.Equal(t, listening.CodeBroken, coverageOf(t, a, listening.SourceWeb).Code)
}

// Finding 6: an address's encoded query keeps its boundaries: "?q=a%26z%3Dc" and "?q=a&z=c" are two searches.
func TestReviewNormalizeKeepsQueryValueBoundary(t *testing.T) {
	t.Parallel()

	one := listening.NormalizeURL("https://example.com/search?q=a%26z%3Dc")
	two := listening.NormalizeURL("https://example.com/search?q=a&z=c")
	assert.NotEqual(t, one, two)
	assert.Equal(t, "https://example.com/search?q=a%26z%3Dc", one)
	assert.Equal(t, "https://example.com/search?q=a&z=c", two)
	assert.Equal(t, "https://voz.vn/t/th%E1%BB%9Bt", listening.NormalizeURL("https://voz.vn/t/thớt/"),
		"a path is kept encoded, the same whichever way it came")
	assert.Equal(t, listening.NormalizeURL("https://voz.vn/t/th%E1%BB%9Bt"),
		listening.NormalizeURL("https://voz.vn/t/thớt"))
}

// Review round 2, finding 2: Arctic Shift's Retry-After counts from when its 429 arrived, not from when the plan
// began (last30days retry_delay_from_headers, lib/http.py:77-119): an answer 8 s late asking for 5 s rests until 13 s
// after the start, and the archive is asked again after that.
func TestRoundTwoArcticRetryAfterFromResponse(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)

	var limited atomic.Bool

	limited.Store(true)

	archived, arcticURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		if limited.Load() {
			c.Advance(8 * time.Second)
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	s := redditOnly(t, c, redditURL, arcticURL)
	web := threadsWeb([2]string{"abc123", "VETC trừ tiền"})

	a, err := s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), web)
	require.NoError(t, err)

	until := start.Add(13 * time.Second).UTC().Format(time.RFC3339)
	assert.Contains(t, coverageOf(t, a, listening.SourceReddit).Streams[0].Tried[1].Detail, "until "+until)

	limited.Store(false)
	c.Advance(6 * time.Second)

	_, err = s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), web)
	require.NoError(t, err)
	assert.Len(t, archived.Requests(), 2, "after its Retry-After the archive is asked again")
}

// Review round 2, finding 3: when the archive could not be read and the topic bar leaves nothing of what the web
// search showed (a title such as "Reddit"), Reddit was not read: its code stays, never a clean no_results. With the
// archive read, the same thread is found by its real title.
func TestRoundTwoNoCleanZeroWhenTopicCannotBeHydrated(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)

	var limited atomic.Bool

	limited.Store(true)

	_, arcticURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		if limited.Load() {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{
			"id": "abc123", "title": "VETC trừ tiền hai lần", "subreddit": "VietNam",
			"permalink": "/r/VietNam/comments/abc123/x/", "created_utc": start.AddDate(0, 0, -2).Unix(), "score": 5,
		}}})
	})
	s := redditOnly(t, c, redditURL, arcticURL)
	web := threadsWeb([2]string{"abc123", "Reddit"})

	a, err := s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), web)
	require.NoError(t, err)

	cov := coverageOf(t, a, listening.SourceReddit)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code, "Reddit's own refusal is the most actionable")
	assert.Equal(t, "rate_limited", cov.Streams[0].Tried[1].Result, "the archive's 429 is web_arctic's result")
	assert.Empty(t, a.Items)

	limited.Store(false)
	c.Advance(time.Minute)

	a, err = s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), web)
	require.NoError(t, err)
	require.Len(t, a.Items, 1)
	assert.Equal(t, "VETC trừ tiền hai lần", a.Items[0].Title)
}

// Review round 2, P2 (owner, 2026-11-27: no paid services): listening asks only the web search's keyless providers,
// even when the owner has a key for one; its Reddit searches too.
func TestRoundTwoListeningUsesKeylessRoutesOnly(t *testing.T) {
	t.Parallel()

	var keyed, keyless atomic.Int32

	_, exaKeyed := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		keyed.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"VETC","url":"https://example.com/a"}],"costDollars":{"total":0.007}}`))
	})
	_, exaKeyless := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		keyless.Add(1)

		text, _ := json.Marshal("Title: VETC trừ tiền\nURL: https://example.com/b\nText: VETC trừ tiền hai lần")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n",
			`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":`+string(text)+`}]}}`)
	})

	web := websearch.New(websearch.Config{
		ExaURL: exaKeyed, ExaMCPURL: exaKeyless,
		Keys: func(context.Context) ([]websearch.Key, error) {
			return []websearch.Key{{Provider: "exa", Name: "web-search-exa", Value: "test-key"}}, nil
		},
		Settings: func(context.Context) (websearch.Settings, error) {
			return websearch.Settings{Off: []string{"firecrawl", "parallel", "tavily", "tinyfish", "you"}}, nil
		},
	})
	_, redditURL := startSite(t, blockedPage)
	_, arcticURL := archiveOf(t, nil)
	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: redditURL, ArcticURL: arcticURL},
			http.DefaultClient),
		Sleep: noSleep,
	})

	_, err := s.Search(t.Context(), plan([]string{listening.SourceWeb, listening.SourceReddit}, "VETC"),
		func(ctx context.Context, q string, limit int) (websearch.Answer, error) {
			return web.Search(ctx, q, limit, "")
		})
	require.NoError(t, err)
	assert.Zero(t, keyed.Load(), "the owner's Exa key is not used by listening")
	assert.Equal(t, int32(2), keyless.Load(), "the web and the site:reddit.com search, keyless")
}

// Review round 3, finding 1: an Arctic Shift 429 rests the archive's path only. The first subquery, whose web result
// is titled only "Reddit", is not read with the archive's code; the second still runs its web discovery and keeps
// its thread as the search showed it, the archive resting (last30days reddit_hydrate.py:467-492: a skipped archive
// leaves the threads their search-result data).
func TestRoundThreeArcticRestLeavesWebDiscoveryAvailable(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)
	archived, arcticURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	s := redditOnly(t, c, redditURL, arcticURL)

	var webCalls atomic.Int32

	web := func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		webCalls.Add(1)

		title := "Reddit"
		if strings.Contains(query, "hai") {
			title = "VETC trừ tiền hai lần"
		}

		return websearch.Answer{Provider: "exa", Query: query, Results: []websearch.Result{{
			Title: title, URL: "https://www.reddit.com/r/VietNam/comments/abc123/x/",
		}}}, nil
	}

	a, err := s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC một", "VETC hai"), web)
	require.NoError(t, err)

	assert.Equal(t, int32(2), webCalls.Load(), "the second subquery still searches the web")
	assert.Len(t, archived.Requests(), 1, "the archive rests after its 429")
	require.Len(t, a.Items, 1)
	assert.Equal(t, "VETC trừ tiền hai lần", a.Items[0].Title)

	streams := coverageOf(t, a, listening.SourceReddit).Streams
	require.Len(t, streams, 2)
	assert.Equal(t, listening.CodeRefused, streams[0].Code, "the first subquery was not read; Reddit's 403 ranks first")
	assert.Equal(t, "rate_limited", streams[0].Tried[1].Result, "web_arctic reports the archive's code")
	assert.False(t, streams[0].Tried[1].Skipped)
	assert.Equal(t, "web_arctic", streams[1].ServedBy)
	assert.Equal(t, listening.StatePartial, coverageOf(t, a, listening.SourceReddit).State)
}
