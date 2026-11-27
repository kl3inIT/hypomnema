package listening_test

import (
	"context"
	"encoding/json"
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

// site is a test server that counts its requests and answers what the test sets.
type site struct {
	mu       sync.Mutex
	requests []*http.Request
	answer   func(w http.ResponseWriter, r *http.Request)
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r)
	answer := s.answer
	s.mu.Unlock()

	answer(w, r)
}

func (s *site) Requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]*http.Request(nil), s.requests...)
}

func startSite(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) (*site, string) {
	t.Helper()

	s := &site{answer: answer}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	return s, srv.URL
}

// blockedPage is what Reddit answers this network: 403 and an HTML page ("blocked by network security").
func blockedPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("<body class=theme-beta>blocked by network security</body>"))
}

// redditWeb is the web search's answer to `site:reddit.com …`: two threads, a profile and another site.
func redditWeb(_ context.Context, query string, limit int) (websearch.Answer, error) {
	if !strings.HasPrefix(query, "site:reddit.com ") || limit != 20 {
		return websearch.Answer{}, websearch.ErrInvalid
	}

	return websearch.Answer{Provider: "exa", Query: query, Results: []websearch.Result{
		{Title: "r/VietNam on Reddit: VETC trừ tiền hai lần", URL: "https://www.reddit.com/r/VietNam/comments/abc123/vetc_tru_tien/"},
		{Title: "u/someone", URL: "https://www.reddit.com/user/someone/"},
		{Title: "App VETC lỗi đăng nhập : r/vozforums", URL: "https://old.reddit.com/r/vozforums/comments/def456/app_vetc/"},
		{Title: "VETC", URL: "https://vetc.com.vn/"},
	}}, nil
}

func archive(t *testing.T) (*site, string) {
	t.Helper()

	return startSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "abc123", "title": "VETC trừ tiền hai lần", "selftext": "Qua trạm Long Thành bị trừ 2 lần",
				"author": "minh", "subreddit": "VietNam", "permalink": "/r/VietNam/comments/abc123/vetc_tru_tien/",
				"created_utc": start.AddDate(0, 0, -3).Unix(), "score": 120, "num_comments": 30},
			{"id": "def456", "title": "App VETC lỗi đăng nhập", "selftext": "", "author": "[deleted]",
				"subreddit": "vozforums", "permalink": "/r/vozforums/comments/def456/app_vetc/",
				"created_utc": start.Add(-10 * time.Hour).Unix(), "score": 1, "num_comments": 4},
		}})
	})
}

// Design rows "Reddit backend" and "Failure codes": Reddit's keyless JSON answering 403 is `refused`, never zero
// results; it rests 6 hours, asked by no search until then, while the web search's Reddit threads, filled in from
// Arctic Shift, serve Reddit.
func TestARedditRefusalRestsSixHoursAndTheArchiveServes(t *testing.T) {
	t.Parallel()

	c := newClock()
	reddit, redditURL := startSite(t, blockedPage)
	archived, arcticURL := archive(t)
	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: redditURL, ArcticURL: arcticURL}, http.DefaultClient),
		Now:       c.Now, Sleep: noSleep,
	})
	p := plan([]string{listening.SourceReddit}, "VETC")

	a, err := s.Search(t.Context(), p, redditWeb)
	require.NoError(t, err)
	require.Len(t, reddit.Requests(), 1)

	asked := reddit.Requests()[0]
	assert.Equal(t, "/search.json", asked.URL.Path)
	assert.Equal(t, "VETC", asked.URL.Query().Get("q"))
	assert.Equal(t, "month", asked.URL.Query().Get("t"))
	assert.Equal(t, "linux:hypomnema:v1 (+https://github.com/kl3inIT/hypomnema)", asked.UserAgent())

	cov := coverageOf(t, a, listening.SourceReddit)
	assert.Equal(t, listening.StateOK, cov.State)
	require.Len(t, cov.Streams[0].Tried, 2)
	assert.Equal(t, listening.Attempt{
		Backend: "reddit_json", Result: "refused", Detail: "HTTP 403", Until: start.Add(6 * time.Hour).Format(time.RFC3339),
	}, cov.Streams[0].Tried[0])
	assert.Equal(t, "web_arctic", cov.Streams[0].ServedBy)
	assert.Equal(t, "web search answered by exa; arctic-shift filled 2 of 2", cov.Streams[0].Tried[1].Detail)

	require.Len(t, archived.Requests(), 1)
	assert.Equal(t, "/api/posts/ids", archived.Requests()[0].URL.Path)
	assert.Equal(t, "abc123,def456", archived.Requests()[0].URL.Query().Get("ids"))

	byID := map[string]listening.Item{}
	for _, it := range a.Items {
		byID[it.NativeID] = it
	}

	thread := byID["abc123"]
	assert.Equal(t, "https://www.reddit.com/r/VietNam/comments/abc123/vetc_tru_tien/", thread.URL)
	assert.Equal(t, "u/minh", thread.Author)
	assert.Equal(t, "r/VietNam", thread.Container)
	assert.Equal(t, map[string]float64{"score": 120, "num_comments": 30}, thread.Engagement)
	assert.Equal(t, "high", thread.DateConfidence)
	assert.Equal(t, map[string]float64{"num_comments": 4}, byID["def456"].Engagement,
		"a post under 36 hours old has no settled score in the archive")
	assert.Empty(t, byID["def456"].Author)

	c.Advance(time.Hour)

	a, err = s.Search(t.Context(), p, redditWeb)
	require.NoError(t, err)
	assert.Len(t, reddit.Requests(), 1, "a refused path is not asked again within 6 hours")
	tried := coverageOf(t, a, listening.SourceReddit).Streams[0].Tried
	assert.Equal(t, "reddit_json", tried[0].Backend)
	assert.Equal(t, "refused", tried[0].Result)
	assert.True(t, tried[0].Skipped)
	assert.Equal(t, start.Add(6*time.Hour).Format(time.RFC3339), tried[0].Until)

	c.Advance(5*time.Hour + time.Minute)

	_, err = s.Search(t.Context(), p, redditWeb)
	require.NoError(t, err)
	assert.Len(t, reddit.Requests(), 2, "after 6 hours it is asked again")
}

// With every Reddit backend failing, Reddit is not read, with the refusal as its cause, never a zero; and a 429
// rests as long as Reddit's x-ratelimit-reset says.
func TestRedditFailingEverywhereIsNotRead(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, blockedPage)
	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: redditURL, ArcticURL: "http://127.0.0.1:1"},
			http.DefaultClient),
		Now: c.Now, Sleep: noSleep,
	})

	noWeb := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{}, &websearch.RestingError{Until: start.Add(10 * time.Minute)}
	}

	a, err := s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), noWeb)
	require.NoError(t, err)

	cov := coverageOf(t, a, listening.SourceReddit)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code)
	assert.Equal(t, listening.CodeRateLimited, listening.Code(cov.Streams[0].Tried[1].Result))

	limited, limitedURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Reset", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	s = listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: limitedURL}, http.DefaultClient),
		Now:       c.Now, Sleep: noSleep,
	})

	a, err = s.Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), nil)
	require.NoError(t, err)
	assert.Len(t, limited.Requests(), 1)
	first := coverageOf(t, a, listening.SourceReddit).Streams[0].Tried[0]
	assert.Equal(t, "rate_limited", first.Result)
	assert.Equal(t, start.Add(30*time.Second).Format(time.RFC3339), first.Until)
}

// Reddit's JSON, when it answers, serves Reddit with its posts' counts.
func TestRedditJSONServesWhenItAnswers(t *testing.T) {
	t.Parallel()

	c := newClock()
	_, redditURL := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"children": []map[string]any{
			{"data": map[string]any{
				"id": "zz9", "title": "Cốc Cốc có còn ai dùng không", "selftext": "Mình vẫn dùng", "author": "lan",
				"subreddit": "VietNam", "permalink": "/r/VietNam/comments/zz9/coc_coc/",
				"created_utc": start.AddDate(0, 0, -2).Unix(), "score": 42, "num_comments": 17, "upvote_ratio": 0.9,
			}},
		}}})
	})

	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{RedditURL: redditURL}, http.DefaultClient),
		Now:       c.Now, Sleep: noSleep,
	})

	p := plan([]string{listening.SourceReddit}, "Cốc Cốc")
	p.WindowDays = 7
	a, err := s.Search(t.Context(), p, nil)
	require.NoError(t, err)

	assert.Equal(t, "reddit_json", coverageOf(t, a, listening.SourceReddit).Streams[0].ServedBy)
	require.Len(t, a.Items, 1)
	assert.Equal(t, "zz9", a.Items[0].NativeID)
	assert.Equal(t, map[string]float64{"score": 42, "num_comments": 17, "upvote_ratio": 0.9}, a.Items[0].Engagement)
	require.NotNil(t, a.Items[0].EngagementScore)
}
