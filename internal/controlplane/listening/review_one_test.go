package listening_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 1 of PR #1321: each finding's repro, failing before its fix.

// P1-1: the App Store feed gives every review the app's link; reviews are kept apart by their id (obsei
// sources/appstore.py:149-165), and a first-page spelling that answers 404 passes to the next (appstore.py:119-140).
func TestReviewOneAppStoreReviewsKeepTheirOwnIdentity(t *testing.T) {
	t.Parallel()

	_, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			jsonAnswer(w, map[string]any{"results": []map[string]any{{"trackId": 42, "trackName": "VETC"}}})
		case strings.Contains(r.URL.Path, "/page=1/"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/id=42/sortby=mostrecent/"):
			entry := func(id, body string) map[string]any {
				e := appStoreEntry(id, "user-"+id, "Tiêu đề "+id, body, "2", "2026-11-26T10:00:00-07:00")
				e["link"] = map[string]any{"attributes": map[string]any{"href": "https://apps.apple.com/vn/app/vetc/id42"}}

				return e
			}

			jsonAnswer(w, map[string]any{"feed": map[string]any{"entry": []any{
				entry("r1", "VETC trừ tiền hai lần ở trạm Long Thành"),
				entry("r2", "Ứng dụng không nạp được tiền từ thẻ ngân hàng"),
			}}})
		default:
			jsonAnswer(w, map[string]any{"feed": map[string]any{}})
		}
	})

	a := one(t, keyless(newClock(), listening.Endpoints{ITunesURL: base}), listening.SourceAppStore, "VETC")

	require.Len(t, a.Items, 2, "two reviews, two items")

	urls := map[string]bool{}
	for _, it := range a.Items {
		urls[it.URL] = true
		assert.Contains(t, it.URL, it.NativeID, "each review's own address")
	}

	assert.Len(t, urls, 2)
}

// P1-2: a voucher on the WBI search, then a legacy search with no result, is still a refusal
// (last30days-skill-cn bilibili.py:204-213, 236-237: no items and errors raise).
func TestReviewOneBilibiliVoucherThenEmptyLegacyIsRefused(t *testing.T) {
	t.Parallel()

	_, base := startBilibili(t, voucher, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"code": 0, "data": map[string]any{"result": []any{}}})
	})

	a := one(t, keyless(newClock(), listening.Endpoints{BilibiliURL: base}), listening.SourceBilibili, "VETC")

	cov := coverageOf(t, a, listening.SourceBilibili)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code)
}

// P1-3: Toutiao's search sent to its verify page still leaves the hot board read, its refusal kept beside what the
// board gave (last30days-skill-cn toutiao.py:54-80).
func TestReviewOneToutiaoVerifyStillReadsTheHotBoard(t *testing.T) {
	t.Parallel()

	_, search := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			http.Redirect(w, r, "/verify/page?from=search", http.StatusFound)

			return
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>verify</html>")
	})

	hot, board := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"data": []map[string]any{
			{"Title": "VETC 收费争议", "Url": "https://www.toutiao.com/trending/771/", "HotValue": "10", "ClusterIdStr": "771"},
		}})
	})

	a := pinned(t, keyless(newClock(), listening.Endpoints{ToutiaoURL: search, ToutiaoHotURL: board}),
		listening.SourceToutiao, listening.BackendToutiao, "VETC")

	assert.Len(t, hot.Requests(), 1, "the hot board is read")
	require.Len(t, a.Items, 1)

	cov := coverageOf(t, a, listening.SourceToutiao)
	assert.Equal(t, listening.StatePartial, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code)
}

// P2: a keyword holding "verify" is not Toutiao's verify page.
func TestReviewOneToutiaoVerifyInTheKeywordIsNotARefusal(t *testing.T) {
	t.Parallel()

	_, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, toutiaoCard("1", "verify tài khoản VETC", "VnE", start.AddDate(0, 0, -1), 1, 1, 1))

			return
		}

		jsonAnswer(w, map[string]any{"data": []any{}})
	})

	a := pinned(t, keyless(newClock(), listening.Endpoints{ToutiaoURL: base, ToutiaoHotURL: base}),
		listening.SourceToutiao, listening.BackendToutiao, "verify tài khoản VETC")

	assert.Equal(t, listening.StateOK, coverageOf(t, a, listening.SourceToutiao).State)
	assert.Len(t, a.Items, 1)
}

// Toutiao is off by default until the owner decides on its terms: unpinned, it is not asked, and says why.
func TestReviewOneToutiaoIsOffUntilPinned(t *testing.T) {
	t.Parallel()

	site, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) { jsonAnswer(w, map[string]any{}) })

	a := one(t, keyless(newClock(), listening.Endpoints{ToutiaoURL: base, ToutiaoHotURL: base}),
		listening.SourceToutiao, "VETC")

	assert.Empty(t, site.Requests())

	cov := coverageOf(t, a, listening.SourceToutiao)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "off by default")
}

// otofunWith is the Otofun source on a test forum whose one thread answers as the test sets.
func otofunWith(t *testing.T, robots string, thread func(w http.ResponseWriter, r *http.Request)) (*site, string,
	listening.WebSearch,
) {
	t.Helper()

	forum, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, robots)

			return
		}

		thread(w, r)
	})

	web := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{
			{Title: "VETC trừ tiền", URL: base + "/threads/vetc-tru-tien.1971431/", Snippet: "VETC trừ tiền 2 lần"},
		}}, nil
	}

	return forum, base, web
}

func requestsTo(s *site, path string) int {
	n := 0

	for _, r := range s.Requests() {
		if strings.HasPrefix(r.URL.Path, path) {
			n++
		}
	}

	return n
}

// P1-4: a forum thread that answers 429 with Retry-After rests, and the read is partial, not ok: coverage and Health
// show it (last30days doctor.py:128-161; ARCHITECTURE: partial is "some streams not read").
func TestReviewOneAThreadsRateLimitRestsAndShowsPartial(t *testing.T) {
	t.Parallel()

	forum, base, web := otofunWith(t, "User-agent: *\nAllow: /\n", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	s := keyless(newClock(), listening.Endpoints{ForumURLs: map[string]string{listening.SourceOtofun: base}})
	p := plan([]string{listening.SourceOtofun}, "VETC")

	for range 2 {
		a, err := s.Search(t.Context(), p, web)
		require.NoError(t, err)

		cov := coverageOf(t, a, listening.SourceOtofun)
		assert.Equal(t, listening.StatePartial, cov.State)
		assert.Equal(t, listening.CodeRateLimited, cov.Code)
	}

	assert.Equal(t, 1, requestsTo(forum, "/threads/"), "the thread rests after its 429")

	for _, h := range s.Health() {
		if h.Source == listening.SourceOtofun {
			assert.Equal(t, listening.HealthUnverified, h.State)
			assert.Equal(t, listening.CodeRateLimited, h.LastCode)
		}
	}
}

// P2: robots.txt is checked again at every redirect: a thread moved to a disallowed path is not read.
func TestReviewOneRobotsAreCheckedAtEveryRedirect(t *testing.T) {
	t.Parallel()

	forum, base, web := otofunWith(t, "User-agent: *\nDisallow: /attachments/\nAllow: /\n",
		func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/threads/") {
				http.Redirect(w, r, "/attachments/secret", http.StatusFound)

				return
			}

			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<meta property="og:title" content="secret">`)
		})

	s := keyless(newClock(), listening.Endpoints{ForumURLs: map[string]string{listening.SourceOtofun: base}})
	a, err := s.Search(t.Context(), plan([]string{listening.SourceOtofun}, "VETC"), web)
	require.NoError(t, err)

	assert.Zero(t, requestsTo(forum, "/attachments/"), "the disallowed target is never asked")
	require.Len(t, a.Items, 1)
	assert.Equal(t, "VETC trừ tiền 2 lần", a.Items[0].Text, "the search's excerpt stays")
}

// P1-4: a careers role page's 429 rests its site and the read is partial; the next search sends nothing there.
func TestReviewOneCareersRestsAfterARateLimit(t *testing.T) {
	t.Parallel()

	site, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/careers":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<a href="/jobs/a">A</a><a href="/jobs/b">B</a>`)
		case "/jobs/a":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, jobPage("Backend Engineer", "Hà Nội", "2026-11-22", "Go"))
		default:
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})

	web := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{{URL: base + "/careers"}}}, nil
	}

	s := keyless(newClock(), listening.Endpoints{})
	p := plan([]string{listening.SourceCareers}, "VETC")

	a, err := s.Search(t.Context(), p, web)
	require.NoError(t, err)

	cov := coverageOf(t, a, listening.SourceCareers)
	assert.Equal(t, listening.StatePartial, cov.State)
	assert.Equal(t, listening.CodeRateLimited, cov.Code)

	before := len(site.Requests())
	_, err = s.Search(t.Context(), p, web)
	require.NoError(t, err)
	assert.Len(t, site.Requests(), before, "the site rests: nothing is sent")
}

// P1-4: Bilibili's rank order read, its click order rate limited: partial, not ok.
func TestReviewOneBilibiliSecondOrderFailingIsPartial(t *testing.T) {
	t.Parallel()

	_, base := startBilibili(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("order") == "click" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		biliResults(w, r)
	}, biliResults)

	s := keyless(newClock(), listening.Endpoints{BilibiliURL: base})
	a := one(t, s, listening.SourceBilibili, "VETC")

	cov := coverageOf(t, a, listening.SourceBilibili)
	assert.Equal(t, listening.StatePartial, cov.State)
	assert.Equal(t, listening.CodeRateLimited, cov.Code)

	for _, h := range s.Health() {
		if h.Source == listening.SourceBilibili {
			assert.Equal(t, listening.HealthUnverified, h.State)
		}
	}
}

// P1-5: YouTube's recent-uploads search failing when every video found was old is that failure, not a clean zero
// (last30days youtube_yt.py:935-973, 1092-1105).
func TestReviewOneYouTubeKeepsTheRecentSearchsFailure(t *testing.T) {
	t.Parallel()

	run := func(_ context.Context, args ...string) ([]byte, string, error) {
		switch {
		case slices.Contains(args, "--flat-playlist") && slices.ContainsFunc(args, func(a string) bool {
			return strings.HasPrefix(a, "https://www.youtube.com/results")
		}):
			return nil, "ERROR: unable to download webpage: HTTP Error 429: Too Many Requests", &exec.ExitError{}
		case slices.Contains(args, "--flat-playlist"):
			return []byte(`{"id": "old1", "ie_key": "Youtube"}`), "", nil
		case slices.Contains(args, "--no-download"):
			return []byte(video("old1", "VETC 2020", 900, 9e6, 1e5, 1e4)), "", nil
		default:
			return nil, "", errors.New("unexpected run")
		}
	}

	a := one(t, keyless(newClock(), listening.Endpoints{YtDlp: run}), listening.SourceYouTube, "VETC")

	cov := coverageOf(t, a, listening.SourceYouTube)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRateLimited, cov.Code)
}

// P2: "Hanoi" and "Ha Noi" are one place, as on Cốc Cốc's board: no role there is in a new place (last30days hiring_signals.py:383-394
// counts a place once however the board spells it).
func TestReviewOneCareersCountsOnePlaceOnce(t *testing.T) {
	t.Parallel()

	jobs := map[string]string{
		"/jobs/a": jobPage("Strategic Planner", "Hanoi", "2026-11-20", "Lập kế hoạch chiến lược cho khối agency"),
		"/jobs/b": jobPage("Telesales", "Ha Noi", "2026-11-21", "Gọi điện tư vấn khách hàng doanh nghiệp"),
		"/jobs/c": jobPage("UX Researcher", "Ha Noi", "2026-11-22", "Phỏng vấn người dùng trình duyệt"),
		"/jobs/d": jobPage("Business Analyst", "Ha Noi", "2026-11-23", "Phân tích yêu cầu sản phẩm quảng cáo"),
	}

	_, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")

		if page, ok := jobs[r.URL.Path]; ok {
			_, _ = io.WriteString(w, page)

			return
		}

		_, _ = io.WriteString(w, `<a href="/jobs/a">a</a><a href="/jobs/b">b</a><a href="/jobs/c">c</a><a href="/jobs/d">d</a>`)
	})

	web := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{Provider: "duckduckgo", Results: []websearch.Result{{URL: base + "/careers"}}}, nil
	}

	a, err := keyless(newClock(), listening.Endpoints{}).Search(t.Context(),
		plan([]string{listening.SourceCareers}, "Cốc Cốc"), web)
	require.NoError(t, err)
	require.Len(t, a.Items, 4)

	for _, it := range a.Items {
		assert.NotContains(t, it.Signals, listening.SignalNewGeo, it.Title)
	}
}
