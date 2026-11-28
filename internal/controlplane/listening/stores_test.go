package listening_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// The app stores of issue #1289: Google Play's reviews through google-play-scraper's call, and the App Store's
// public reviews feed (obsei sources/appstore.py), each for the app the query names.

// playReview is a review as Play's batchexecute answer lays it out (google-play-scraper constants/element.py:177-189).
func playReview(id, name, text string, score int, at time.Time, thumbs int) []any {
	return []any{id, []any{name, nil}, score, nil, text, []any{at.Unix(), 0}, thumbs, nil, nil, nil, "7.3.0"}
}

// playAnswer is Play's batchexecute answer: its guard line, then the RPC's reviews as a JSON string, the next page's
// token second to last as Play sends it (google-play-scraper features/reviews.py:60, `[-2][-1]`).
func playAnswer(reviews [][]any, token string) string {
	var tail any
	if token != "" {
		tail = []any{nil, token}
	}

	inner := mustJSON([]any{reviews, tail, []any{[]any{nil}}})
	outer := mustJSON([]any{[]any{"wrb.fr", "oCPfdb", string(inner), nil, nil, nil, "generic"}})

	return ")]}'\n\n" + string(outer)
}

func play(t *testing.T, pages func(token string) (int, string)) (*site, string) {
	t.Helper()

	return startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/store/search":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<a href="/store/apps/details?id=vn.com.vetc.app">VETC</a>`+
				`<a href="/store/apps/details?id=vn.com.vetc.enterprise">VETC Doanh nghiệp</a>`)
		case "/_/PlayStoreUi/data/batchexecute":
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))

			token := ""
			if i := strings.Index(form.Get("f.req"), `\"tok`); i >= 0 {
				token = form.Get("f.req")[i+2 : i+6]
			}

			status, answer := pages(token)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, answer)
		default:
			http.NotFound(w, r)
		}
	})
}

// Design row "Google Play": the app the query names found on Play's search, its reviews read newest first in
// Vietnamese for Vietnam (hl=vi, gl=vn), page after page until the window ends; a review's `at`, which
// google-play-scraper makes naive local time, is the epoch Play sends, in UTC.
func TestGooglePlayReadsTheNewestVietnameseReviews(t *testing.T) {
	t.Parallel()

	p, base := play(t, func(token string) (int, string) {
		if token == "" {
			return http.StatusOK, playAnswer([][]any{
				playReview("r1", "Rain HTL", "app lỏ trừ tiền hai lần", 1, start.Add(-3*time.Hour), 4),
				playReview("r2", "Hiếu", "VETC lỗi đăng nhập", 2, start.AddDate(0, 0, -5), 0),
			}, "tok1")
		}

		return http.StatusOK, playAnswer([][]any{
			playReview("r3", "Lan", "rất tốt", 5, start.AddDate(0, 0, -40), 1),
		}, "tok2")
	})

	a := one(t, keyless(newClock(), listening.Endpoints{PlayURL: base}), listening.SourceGooglePlay, "VETC")

	reqs := p.Requests()
	require.Len(t, reqs, 3, "search, then two pages: the second reached past the window")
	assert.Equal(t, "VETC", reqs[0].URL.Query().Get("q"))
	assert.Equal(t, "vi", reqs[1].URL.Query().Get("hl"))
	assert.Equal(t, "vn", reqs[1].URL.Query().Get("gl"))
	assert.Equal(t, http.MethodPost, reqs[1].Method)

	items := byNative(a.Items)
	require.Len(t, items, 2)

	r1 := items["r1"]
	require.NotNil(t, r1.PublishedAt)
	assert.Equal(t, start.Add(-3*time.Hour), *r1.PublishedAt)
	assert.Equal(t, time.UTC, r1.PublishedAt.Location())
	assert.Equal(t, "app lỏ trừ tiền hai lần", r1.Text)
	assert.Equal(t, "Rain HTL", r1.Author)
	assert.Equal(t, "vn.com.vetc.app", r1.Container)
	assert.Equal(t, map[string]float64{"rating": 1, "thumbs_up": 4}, r1.Engagement)
	assert.Equal(t, "https://play.google.com/store/apps/details?gl=vn&hl=vi&id=vn.com.vetc.app&reviewId=r1", r1.URL)
}

// Design row "Google Play": an app id in the query is read without a search; an error on a later page fails the
// stream, never the end of the reviews (google-play-scraper features/reviews.py:113-125 swallows it); Play's gateway
// error is a rate limit, rested, not retried in place (utils/request.py:213-217 sleeps and retries).
func TestGooglePlayFailuresAreFailures(t *testing.T) {
	t.Parallel()

	p, base := play(t, func(token string) (int, string) {
		if token == "" {
			return http.StatusOK, playAnswer([][]any{
				playReview("r1", "A", "VETC chậm", 2, start.Add(-time.Hour), 0),
			}, "tok1")
		}

		return http.StatusInternalServerError, "oops"
	})

	a := one(t, keyless(newClock(), listening.Endpoints{PlayURL: base}), listening.SourceGooglePlay,
		"vn.com.vetc.app")

	for _, r := range p.Requests() {
		assert.NotEqual(t, "/store/search", r.URL.Path, "an app id needs no search")
	}

	cov := coverageOf(t, a, listening.SourceGooglePlay)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeBroken, cov.Code)
	assert.Empty(t, a.Items)

	_, gateway := play(t, func(string) (int, string) {
		return http.StatusOK, `)]}'` + "\n\n" +
			`[["er",null,null,null,null,"com.google.play.gateway.proto.PlayGatewayError"]]`
	})

	a = one(t, keyless(newClock(), listening.Endpoints{PlayURL: gateway}), listening.SourceGooglePlay,
		"vn.com.vetc.app")
	assert.Equal(t, "rate_limited", coverageOf(t, a, listening.SourceGooglePlay).Streams[0].Tried[0].Result)
}

func appStoreEntry(id, author, title, body, rating, updated string) map[string]any {
	label := func(s string) map[string]any { return map[string]any{"label": s} }

	return map[string]any{
		"id": label(id), "author": map[string]any{"name": label(author)}, "title": label(title),
		"content": label(body), "im:rating": label(rating), "updated": label(updated), "im:version": label("7.3"),
		"link": map[string]any{"attributes": map[string]any{"href": "https://apps.apple.com/vn/review?id=" + id}},
	}
}

// Design row "App Store": the app found by Apple's search for Vietnam (marketingskills teardown.mjs), then its public
// reviews feed newest first, page after page (obsei sources/appstore.py; at most 10 pages of 50), until the window
// ends; a review is its title and text, rated.
func TestAppStoreReadsThePublicReviewsFeed(t *testing.T) {
	t.Parallel()

	as, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			// Apple answers its search's JSON as text/javascript.
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"trackId": 1179408386, "trackName": "VETC"},
			}})
		case strings.Contains(r.URL.Path, "/page=1/"):
			jsonAnswer(w, map[string]any{"feed": map[string]any{"entry": []any{
				appStoreEntry("a1", "minh", "Lỗi", "VETC trừ tiền sai", "1", "2026-11-26T19:42:23-07:00"),
				appStoreEntry("a2", "lan", "Ổn", "Dùng tốt", "5", "2026-09-01T10:00:00-07:00"),
			}}})
		default:
			jsonAnswer(w, map[string]any{"feed": map[string]any{}})
		}
	})

	a := one(t, keyless(newClock(), listening.Endpoints{ITunesURL: base}), listening.SourceAppStore, "VETC")

	reqs := as.Requests()
	require.Len(t, reqs, 2, "the second review is past the window: no later page")
	assert.Equal(t, "VETC", reqs[0].URL.Query().Get("term"))
	assert.Equal(t, "vn", reqs[0].URL.Query().Get("country"))
	assert.Equal(t, "software", reqs[0].URL.Query().Get("entity"))
	assert.Equal(t, "/vn/rss/customerreviews/page=1/id=1179408386/sortby=mostrecent/json", reqs[1].URL.Path)

	require.Len(t, a.Items, 1)
	it := a.Items[0]
	assert.Equal(t, "Lỗi", it.Title)
	assert.Equal(t, "VETC trừ tiền sai", it.Text)
	assert.Equal(t, "minh", it.Author)
	assert.Equal(t, map[string]float64{"rating": 1}, it.Engagement)
	require.NotNil(t, it.PublishedAt)
	assert.Equal(t, time.Date(2026, 11, 27, 2, 42, 23, 0, time.UTC), *it.PublishedAt)

	_, none := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"results": []any{}})
	})

	a = one(t, keyless(newClock(), listening.Endpoints{ITunesURL: none}), listening.SourceAppStore, "Nothing")
	assert.Equal(t, listening.CodeNotFound, coverageOf(t, a, listening.SourceAppStore).Code)
	assert.Contains(t, fmt.Sprint(coverageOf(t, a, listening.SourceAppStore).Streams[0].Tried[0].Detail), "Nothing")
}
