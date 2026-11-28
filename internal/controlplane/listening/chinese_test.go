package listening_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// The Chinese sources of issue #1289's addition (owner, 2026-11-28): Toutiao's search page and Bilibili's
// visitor search, both keyless.

// cst is Beijing time, which Bilibili's window is counted in (last30days-skill-cn dates.CST).
var cst = time.FixedZone("CST", 8*3600)

func toutiaoCard(id, title, source string, at time.Time, reads, comments, likes int) string {
	return `<script data-druid-card-data-id="` + id + `" type="application/json">` + string(mustJSON(map[string]any{
		"data": map[string]any{
			"title": "<em>" + title + "</em>", "source": source, "abstract": "Tóm tắt " + id,
			"publish_time": strconv.FormatInt(at.Unix(), 10), "datetime": at.In(cst).Format(time.DateTime),
			"group_id": id, "read_count": reads, "comment_count": comments, "digg_count": likes,
		},
	})) + `</script>`
}

// Design row "Toutiao": so.toutiao.com's information channel, its page embedding each result's JSON card (title,
// source, publish time, counts) with no signature, asked as a browser from Toutiao's own page; a page shell without
// cards asked again after a growing wait; then the hot board's entries about the topic (last30days-skill-cn
// toutiao.py:1-12, 112-134, 180-208, 240-258).
func TestToutiaoReadsItsSearchCardsThenTheHotBoard(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		shown int
	)

	site, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			mu.Lock()
			shown++
			n := shown
			mu.Unlock()

			w.Header().Set("Content-Type", "text/html; charset=utf-8")

			if n == 1 {
				_, _ = io.WriteString(w, `<html><body><div id="root"></div></body></html>`)

				return
			}

			_, _ = io.WriteString(w, "<html><body>"+
				toutiaoCard("7712276526502363657", "VETC thu phí", "金融界", start.AddDate(0, 0, -2), 583, 3, 3)+
				toutiaoCard("7700000000000000001", "VETC 2025", "人民网", start.AddDate(0, 0, -90), 9, 0, 0)+
				"</body></html>")
		case "/hot-event/hot-board/":
			jsonAnswer(w, map[string]any{"data": []map[string]any{
				{"Title": "VETC 收费争议", "Url": "https://www.toutiao.com/trending/771/?log_pb=x", "HotValue": "10426",
					"ClusterIdStr": "771"},
				{"Title": "央行新规", "Url": "https://www.toutiao.com/trending/772/", "HotValue": "99", "ClusterIdStr": "772"},
			}})
		default:
			http.NotFound(w, r)
		}
	})

	var (
		slept []time.Duration
		smu   sync.Mutex
	)

	c := newClock()
	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{ToutiaoURL: base, ToutiaoHotURL: base}, http.DefaultClient),
		Now:       c.Now,
		Sleep: func(_ context.Context, d time.Duration) error {
			smu.Lock()
			defer smu.Unlock()

			slept = append(slept, d)

			return nil
		},
	})

	a := one(t, s, listening.SourceToutiao, "VETC")

	var searches []*http.Request

	for _, r := range site.Requests() {
		if r.URL.Path == "/search" {
			searches = append(searches, r)
		}
	}

	require.Len(t, searches, 2, "the shell is asked again; a short page is the last")
	q := searches[0].URL.Query()
	assert.Equal(t, "VETC", q.Get("keyword"))
	assert.Equal(t, "information", q.Get("pd"))
	assert.Equal(t, "pc", q.Get("dvpf"))
	assert.Equal(t, "https://so.toutiao.com/", searches[0].Header.Get("Referer"))
	assert.Contains(t, searches[0].UserAgent(), "Chrome/")
	assert.Contains(t, searches[0].Header.Get("Accept-Language"), "zh-CN")
	assert.Contains(t, slept, 800*time.Millisecond, "the shell's retry waits")

	items := byNative(a.Items)
	require.Len(t, items, 2, "the 90-day-old card is outside the window; one hot entry names the topic")

	card := items["7712276526502363657"]
	assert.Equal(t, "VETC thu phí", card.Title)
	assert.Equal(t, "https://www.toutiao.com/group/7712276526502363657/", card.URL)
	assert.Equal(t, "金融界", card.Author)
	assert.Equal(t, map[string]float64{"reads": 583, "comments": 3, "likes": 3}, card.Engagement)
	require.NotNil(t, card.PublishedAt)
	assert.Equal(t, start.AddDate(0, 0, -2), *card.PublishedAt)

	hot := items["771"]
	assert.Equal(t, "VETC 收费争议", hot.Title)
	assert.Equal(t, "https://www.toutiao.com/trending/771/", hot.URL)
	assert.Equal(t, map[string]float64{"hot_value": 10426}, hot.Engagement)
	assert.Nil(t, hot.PublishedAt)
}

// Design row "Toutiao": its verify page is a refusal, never an empty search (last30days-skill-cn toutiao.py:127).
func TestToutiaoVerifyPageIsRefused(t *testing.T) {
	t.Parallel()

	_, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			http.Redirect(w, r, "/verify?from=search", http.StatusFound)

			return
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>captcha</html>")
	})

	a := one(t, keyless(newClock(), listening.Endpoints{ToutiaoURL: base, ToutiaoHotURL: base}),
		listening.SourceToutiao, "VETC")
	cov := coverageOf(t, a, listening.SourceToutiao)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code)
}

// Bilibili's WBI signature, checked against the value the reference's sign_wbi gives for the same input
// (last30days-skill-cn bilibili.py:52-66; the first is bilibili-API-collect's documented example).
func TestBilibiliWBISignature(t *testing.T) {
	t.Parallel()

	img, sub := "7cd084941338484aae1ad9425b84077c", "4932caff0ff746eab6f01bf08b70ac45"

	signed := listening.SignWBI(map[string]string{"foo": "114", "bar": "514", "zab": "1919810"}, img, sub, 1702204169)
	assert.Equal(t, "8f6f2b5b3d485fe1886cec6a0be8c5d4", signed.Get("w_rid"))
	assert.Equal(t, "1702204169", signed.Get("wts"))

	signed = listening.SignWBI(map[string]string{"keyword": "越南 (test)!", "page": "1"}, img, sub, 1702204169)
	assert.Equal(t, "ee281fc97db815ee1897940bf91c3582", signed.Get("w_rid"))
	assert.Equal(t, "越南 test", signed.Get("keyword"))
}

// bilibili is a test Bilibili: visitor cookies, WBI keys, and the two searches, each answering what the test sets.
type bilibili struct {
	*site

	wbi, legacy func(w http.ResponseWriter, r *http.Request)
	spis        int
	mu          sync.Mutex
}

func startBilibili(t *testing.T, wbi, legacy func(w http.ResponseWriter, r *http.Request)) (*bilibili, string) {
	t.Helper()

	b := &bilibili{wbi: wbi, legacy: legacy}
	s, base := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/frontend/finger/spi":
			b.mu.Lock()
			b.spis++
			n := b.spis
			b.mu.Unlock()

			jsonAnswer(w, map[string]any{"code": 0, "data": map[string]any{
				"b_3": fmt.Sprintf("B3-%d", n), "b_4": fmt.Sprintf("B4-%d", n),
			}})
		case "/x/web-interface/nav":
			jsonAnswer(w, map[string]any{"code": -101, "data": map[string]any{"wbi_img": map[string]any{
				"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
				"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
			}}})
		case "/x/web-interface/wbi/search/type":
			b.wbi(w, r)
		case "/x/web-interface/search/type":
			b.legacy(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	b.site = s

	return b, base
}

func (b *bilibili) Spis() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.spis
}

func biliResults(w http.ResponseWriter, _ *http.Request) {
	jsonAnswer(w, map[string]any{"code": 0, "data": map[string]any{"result": []map[string]any{
		{"bvid": "BV1hX4w6fEhw", "title": `<em class="keyword">VETC</em> trạm thu phí`, "author": "小代",
			"pubdate": start.AddDate(0, 0, -3).Unix(), "play": 43663, "video_review": 11, "review": 33,
			"favorites": 168, "like": 1288, "description": "-"},
	}}})
}

func voucher(w http.ResponseWriter, _ *http.Request) {
	jsonAnswer(w, map[string]any{"code": 0, "message": "0", "ttl": 1, "data": map[string]any{
		"v_voucher": "voucher_6d1a1b2c-test",
	}})
}

// Design row "Bilibili": visitor cookies from finger/spi, a full browser UA and the search page as Referer and
// Origin, the WBI-signed search with the window as pubtime_begin_s and pubtime_end_s in Beijing days
// (last30days-skill-cn bilibili.py:1-17, 73-116, 130-162).
func TestBilibiliSearchesAsAVisitorWithItsWindow(t *testing.T) {
	t.Parallel()

	bl, base := startBilibili(t, biliResults, biliResults)

	a := one(t, keyless(newClock(), listening.Endpoints{BilibiliURL: base}), listening.SourceBilibili, "VETC")

	var searches []*http.Request

	for _, r := range bl.Requests() {
		if strings.HasSuffix(r.URL.Path, "/search/type") {
			searches = append(searches, r)
		}
	}

	require.NotEmpty(t, searches)
	r := searches[0]
	assert.Equal(t, "/x/web-interface/wbi/search/type", r.URL.Path)
	assert.Contains(t, r.Header.Get("Cookie"), "buvid3=B3-1")
	assert.Contains(t, r.Header.Get("Cookie"), "buvid4=B4-1")
	assert.Equal(t, "https://search.bilibili.com/", r.Header.Get("Referer"))
	assert.Equal(t, "https://search.bilibili.com", r.Header.Get("Origin"))
	assert.Contains(t, r.UserAgent(), "Chrome/")

	q := r.URL.Query()
	assert.Equal(t, "video", q.Get("search_type"))
	assert.Equal(t, "VETC", q.Get("keyword"))
	assert.Equal(t, strconv.FormatInt(time.Date(2026, 10, 28, 0, 0, 0, 0, cst).Unix(), 10), q.Get("pubtime_begin_s"))
	assert.Equal(t, strconv.FormatInt(time.Date(2026, 11, 27, 23, 59, 59, 0, cst).Unix(), 10), q.Get("pubtime_end_s"))
	assert.Len(t, q.Get("w_rid"), 32)

	require.Len(t, a.Items, 1)
	it := a.Items[0]
	assert.Equal(t, "BV1hX4w6fEhw", it.NativeID)
	assert.Equal(t, "https://www.bilibili.com/video/BV1hX4w6fEhw", it.URL)
	assert.Equal(t, "VETC trạm thu phí", it.Title)
	assert.Equal(t, "小代", it.Author)
	assert.Empty(t, it.Text, `"-" is no description`)
	assert.Equal(t, map[string]float64{"views": 43663, "danmaku": 11, "comments": 33, "favorites": 168, "likes": 1288},
		it.Engagement)
}

// Design row "Bilibili": a risk answer (HTTP 412) gets fresh visitor cookies and the legacy search
// (last30days-skill-cn bilibili.py:33-34, 200-213).
func TestBilibiliRiskAnswerFallsToTheLegacySearch(t *testing.T) {
	t.Parallel()

	bl, base := startBilibili(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}, biliResults)

	a := one(t, keyless(newClock(), listening.Endpoints{BilibiliURL: base}), listening.SourceBilibili, "VETC")

	cov := coverageOf(t, a, listening.SourceBilibili)
	assert.Equal(t, listening.StateOK, cov.State)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "legacy")
	assert.GreaterOrEqual(t, bl.Spis(), 2, "the visitor cookies are refreshed")

	for _, r := range bl.Requests() {
		if r.URL.Path == "/x/web-interface/search/type" {
			assert.Contains(t, r.Header.Get("Cookie"), "buvid3=B3-2")
		}
	}
}

// Design row "Bilibili voucher answer": HTTP 200 with code 0 whose body holds only v_voucher is a risk block, so
// refused, never no_results (the coverage rule of #1287; last30days-skill-cn bilibili.py:119-127 reads it as empty).
func TestBilibiliVoucherIsRefusedNotAZero(t *testing.T) {
	t.Parallel()

	_, base := startBilibili(t, voucher, voucher)

	a := one(t, keyless(newClock(), listening.Endpoints{BilibiliURL: base}), listening.SourceBilibili, "VETC")

	cov := coverageOf(t, a, listening.SourceBilibili)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeRefused, cov.Code)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "v_voucher")
}
