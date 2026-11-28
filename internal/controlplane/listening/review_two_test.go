package listening_test

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 2 of PR #1321: each finding's probe, failing before its fix.

// P1-1: a first-page spelling Apple answers 400 passes to the next, as a 404 does, and rests nothing (obsei
// sources/appstore.py:119-140: _get returns None for 400 and 404, _page tries the next spelling).
func TestReviewTwoAppStore400PassesToTheNextSpelling(t *testing.T) {
	t.Parallel()

	_, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			jsonAnswer(w, map[string]any{"results": []map[string]any{{"trackId": 42, "trackName": "VETC"}}})
		case strings.Contains(r.URL.Path, "/page=1/"):
			w.WriteHeader(http.StatusBadRequest)
		case strings.Contains(r.URL.Path, "/id=42/sortby=mostrecent/"):
			jsonAnswer(w, map[string]any{"feed": map[string]any{"entry": []any{
				appStoreEntry("r1", "minh", "Lỗi", "VETC trừ tiền sai", "1", "2026-11-26T10:00:00-07:00"),
			}}})
		default:
			jsonAnswer(w, map[string]any{"feed": map[string]any{}})
		}
	})

	a := one(t, keyless(newClock(), listening.Endpoints{ITunesURL: base}), listening.SourceAppStore, "VETC")

	assert.Equal(t, listening.StateOK, coverageOf(t, a, listening.SourceAppStore).State)
	assert.Len(t, a.Items, 1)
}

// P1-2: a voucher on the WBI search, then a legacy search whose only video is outside the window, is still the
// refusal: nothing usable was read (last30days-skill-cn bilibili.py:204-213, 236-237).
func TestReviewTwoBilibiliVoucherThenOnlyOldVideosIsRefused(t *testing.T) {
	t.Parallel()

	_, base := startBilibili(t, voucher, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"code": 0, "data": map[string]any{"result": []map[string]any{
			{"bvid": "BV1old", "title": "VETC 2020", "author": "x", "pubdate": start.AddDate(-3, 0, 0).Unix()},
		}}})
	})

	a := one(t, keyless(newClock(), listening.Endpoints{BilibiliURL: base}), listening.SourceBilibili, "VETC")

	cov := coverageOf(t, a, listening.SourceBilibili)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code)
}

// P1-3: a forum whose robots.txt answers 429 with Retry-After rests: its threads are not read, the read is partial,
// and the next search sends nothing to the forum.
func TestReviewTwoARobotsRateLimitRestsTheForum(t *testing.T) {
	t.Parallel()

	forum, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Retry-After", "7200")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<meta property="og:title" content="thread">`)
	})

	web := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{
			{Title: "VETC", URL: base + "/threads/vetc.1971431/", Snippet: "VETC trừ tiền"},
		}}, nil
	}

	s := keyless(newClock(), listening.Endpoints{ForumURLs: map[string]string{listening.SourceOtofun: base}})
	p := plan([]string{listening.SourceOtofun}, "VETC")

	for range 2 {
		a, err := s.Search(t.Context(), p, web)
		require.NoError(t, err)

		cov := coverageOf(t, a, listening.SourceOtofun)
		assert.Equal(t, listening.StatePartial, cov.State)
		assert.Equal(t, listening.CodeRateLimited, cov.Code)
	}

	assert.Equal(t, 1, requestsTo(forum, "/robots.txt"))
	assert.Zero(t, requestsTo(forum, "/threads/"))
}

// P1-3: a Retry-After given as an HTTP date is kept to that date (RFC 9110 §10.2.3), not read as an hour.
func TestReviewTwoRetryAfterAsADateIsKept(t *testing.T) {
	t.Parallel()

	c := newClock()
	forum, base, web := otofunWith(t, "User-agent: *\nAllow: /\n", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", start.Add(2*time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	})

	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{ForumURLs: map[string]string{listening.SourceOtofun: base}},
			http.DefaultClient),
		Now: c.Now, Sleep: noSleep,
	})
	p := plan([]string{listening.SourceOtofun}, "VETC")

	a, err := s.Search(t.Context(), p, web)
	require.NoError(t, err)

	tried := coverageOf(t, a, listening.SourceOtofun).Streams[0].Tried[0]
	assert.Equal(t, start.Add(2*time.Hour).UTC().Format(time.RFC3339), tried.Until)

	c.Advance(90 * time.Minute)

	_, err = s.Search(t.Context(), p, web)
	require.NoError(t, err)
	assert.Equal(t, 1, requestsTo(forum, "/threads/"), "still resting 90 minutes on")
}

// P1-4: YouTube's recent-uploads search rate limited stops the search's other reads and rests the backend: no
// captions or comments are asked, and the next search runs no yt-dlp (trend-opportunity-radar
// references/collection-pacing-contract.md:12; last30days youtube_yt.py:967-972).
func TestReviewTwoYouTubeStopsAfterARateLimit(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		runs [][]string
	)

	run := func(_ context.Context, args ...string) ([]byte, string, error) {
		mu.Lock()
		runs = append(runs, args)
		mu.Unlock()

		switch {
		case slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "https://www.youtube.com/results") }):
			return nil, "ERROR: HTTP Error 429: Too Many Requests", &exec.ExitError{}
		case slices.Contains(args, "--flat-playlist"):
			return []byte(`{"id": "new1", "ie_key": "Youtube"}`), "", nil
		case slices.Contains(args, "--no-download"):
			return []byte(video("new1", "VETC tháng này", 2, 500, 10, 2)), "", nil
		default:
			return []byte(`{"comments": []}`), "", nil
		}
	}

	s := keyless(newClock(), listening.Endpoints{YtDlp: run})

	a := one(t, s, listening.SourceYouTube, "VETC")
	assert.Equal(t, listening.CodeRateLimited, coverageOf(t, a, listening.SourceYouTube).Code)

	mu.Lock()
	first := len(runs)
	enrich := slices.ContainsFunc(runs, func(r []string) bool {
		return slices.Contains(r, "--write-comments") || slices.Contains(r, "--write-subs")
	})
	mu.Unlock()

	assert.False(t, enrich, "no caption or comment read after the rate limit")

	one(t, s, listening.SourceYouTube, "VETC")

	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, runs, first, "the backend rests: the next search runs no yt-dlp")
}

// P2: a source that is off (Toutiao, unpinned) sends nothing, and its health is left as it was: no read, no
// verdict (last30days doctor.py:143-161: run evidence only).
func TestReviewTwoAnOffSourceRecordsNoHealth(t *testing.T) {
	t.Parallel()

	_, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) { jsonAnswer(w, map[string]any{}) })
	s := keyless(newClock(), listening.Endpoints{ToutiaoURL: base, ToutiaoHotURL: base})

	one(t, s, listening.SourceToutiao, "VETC")

	for _, h := range s.Health() {
		if h.Source == listening.SourceToutiao {
			assert.Equal(t, listening.HealthUnverified, h.State)
			assert.Nil(t, h.LastFailure)
		}
	}
}
