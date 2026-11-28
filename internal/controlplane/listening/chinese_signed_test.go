package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The Chinese platforms behind a signed-in account (issue #1362), with fakes only: Zhihu's and Weibo's APIs served by
// test servers, the pages by fake tabs whose scripts, responses and bodies are set, the vault and the profile by maps.
// No real account, cookie or call.

// siteFake is a site's API: what it answers each request, and what it was asked.
type siteFake struct {
	mu     sync.Mutex
	answer func(r *http.Request) (int, string)
	asked  []*http.Request
}

func (f *siteFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r)
		answer := f.answer
		f.mu.Unlock()

		status, body := http.StatusNotFound, ""
		if answer != nil {
			status, body = answer(r)
		}

		if status >= 300 && status < 400 {
			w.Header().Set("Location", body)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}

		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	})
}

func (f *siteFake) set(answer func(r *http.Request) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.answer = answer
}

func (f *siteFake) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []string{}
	for _, r := range f.asked {
		out = append(out, r.URL.Path)
	}

	return out
}

func (f *siteFake) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.asked[len(f.asked)-1]
}

type cnRig struct {
	svc     *listening.Service
	zhihu   *siteFake
	weibo   *siteFake
	vault   *vaultFake
	browser *browserFake
	told    *told
	clock   *clock
	web     listening.WebSearch
	waits   *[]time.Duration
}

func newCNRig(t *testing.T) *cnRig {
	t.Helper()

	rig := &cnRig{
		zhihu: &siteFake{}, weibo: &siteFake{}, vault: newVault(), browser: newBrowser(), told: &told{},
		clock: newClock(),
	}

	zs := httptest.NewServer(rig.zhihu.handler())
	ws := httptest.NewServer(rig.weibo.handler())
	t.Cleanup(zs.Close)
	t.Cleanup(ws.Close)

	// Each site's web result, as the web search finds its posts.
	rig.web = func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		links := map[string]string{
			"site:zhihu.com":       "https://www.zhihu.com/question/123/answer/456",
			"site:weibo.com":       "https://weibo.com/1001/Abcdef123",
			"site:douyin.com":      "https://www.douyin.com/video/7300000000000000001",
			"site:xiaohongshu.com": "https://www.xiaohongshu.com/explore/0123456789abcdef01234567",
		}

		for site, link := range links {
			if strings.HasPrefix(query, site+" ") {
				return websearch.Answer{Provider: "fake", Results: []websearch.Result{{URL: link, Title: "a post"}}}, nil
			}
		}

		return websearch.Answer{}, websearch.ErrNoResults
	}

	waits := []time.Duration{}
	rig.waits = &waits

	var mu sync.Mutex

	rig.svc = listening.New(listening.Config{
		Platforms: listening.SignedChineseSources(listening.Endpoints{},
			hostsTo(map[string]string{"www.zhihu.com": zs.URL, "m.weibo.cn": ws.URL})),
		Now: rig.clock.Now, DB: migrated(t), Tell: rig.told.tell, Browser: rig.browser, Secrets: rig.vault,
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()

			*rig.waits = append(*rig.waits, d)
			rig.clock.Advance(d)

			return nil
		},
	})

	return rig
}

func (r *cnRig) search(t *testing.T, source string) listening.Answer {
	t.Helper()

	p := plan([]string{source}, "越南")
	p.WindowDays = 30

	a, err := r.svc.Search(t.Context(), p, listening.Caller{Web: r.web})
	require.NoError(t, err)

	return a
}

func (r *cnRig) page(key string, p *pageFake) {
	r.browser.mu.Lock()
	defer r.browser.mu.Unlock()

	r.browser.pages[key] = p
}

func asJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	require.NoError(t, err)

	return string(raw)
}

func TestChineseSignedSourcesAreOffUntilTheirOwnConsentCards(t *testing.T) {
	rig := newCNRig(t)

	on, err := rig.svc.On(t.Context())
	require.NoError(t, err)
	assert.Empty(t, on, "each is off until the owner agrees to its own card")

	for _, source := range []string{
		listening.SourceZhihu, listening.SourceWeibo, listening.SourceDouyin, listening.SourceXiaohongshu,
	} {
		st := stateOf(t, rig.svc, source)
		require.NotNil(t, st.Signed, source)
		assert.Contains(t, st.Signed.Consent, "secondary account", source)
		assert.Contains(t, st.Signed.Consent, "never answers", source, "a captcha is never solved")

		require.ErrorIs(t, rig.svc.SignInStart(t.Context(), source), listening.ErrInvalid, "no sign-in before consent")
	}

	xhs := stateOf(t, rig.svc, listening.SourceXiaohongshu).Signed.Consent
	assert.Contains(t, xhs, "300011", "Xiaohongshu's card names its ban reports")
	assert.Contains(t, xhs, "another web browser", "and its one web session an account")
	assert.Contains(t, xhs, "15 seconds apart and 45 seconds more after every five")

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceXiaohongshu))
	on, err = rig.svc.On(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{listening.SourceXiaohongshu}, on, "consent is per source")

	require.NoError(t, rig.svc.SignInStart(t.Context(), listening.SourceXiaohongshu))
	assert.Equal(t, []string{"https://www.xiaohongshu.com/explore"}, rig.browser.opened)
}

// zhihuResults is a search_v3 answer with an answer, an article and a question.
func zhihuResults(t *testing.T, created time.Time) string {
	t.Helper()

	return asJSON(t, map[string]any{
		"data": []any{
			map[string]any{"type": "search_result", "object": map[string]any{
				"type": "answer", "id": 456, "excerpt": "<em>越南</em> ETC 收费", "created_time": created.Unix(),
				"question": map[string]any{"id": "123", "name": "越南的 ETC 好用吗"},
				"author":   map[string]any{"name": "某人"}, "voteup_count": 12, "comment_count": 3,
			}},
			map[string]any{"type": "search_result", "object": map[string]any{
				"type": "article", "id": "789", "title": "越南高速", "created_time": created.Unix(),
			}},
			map[string]any{"type": "search_result", "object": map[string]any{
				"type": "question", "id": 321, "title": "越南怎么样",
			}},
			map[string]any{"type": "search_result", "object": map[string]any{"type": "answer", "id": 1}},
		},
		"paging": map[string]any{"is_end": false},
	})
}

func signInZhihu(t *testing.T, rig *cnRig) {
	t.Helper()

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceZhihu))
	rig.zhihu.set(func(*http.Request) (int, string) { return 200, `{"data":[],"paging":{"is_end":true}}` })
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceZhihu, "z_c0=zhihu-token; d_c0=device", ""))
}

func TestZhihuSearchesSearchV3AsTheAccount(t *testing.T) {
	rig := newCNRig(t)
	signInZhihu(t, rig)

	created := start.Add(-72 * time.Hour)
	rig.zhihu.set(func(*http.Request) (int, string) { return 200, zhihuResults(t, created) })

	a := rig.search(t, listening.SourceZhihu)

	cov := coverageOf(t, a, listening.SourceZhihu)
	assert.Equal(t, listening.BackendZhihuAPI, cov.Streams[0].ServedBy)
	require.Len(t, a.Items, 3, "an answer naming no question is no result")

	urls := []string{}
	for _, it := range a.Items {
		urls = append(urls, it.URL)
	}

	assert.ElementsMatch(t, []string{
		"https://www.zhihu.com/question/123/answer/456", "https://zhuanlan.zhihu.com/p/789",
		"https://www.zhihu.com/question/321",
	}, urls, "an id sent as a number keeps its digits")

	for _, it := range a.Items {
		if strings.Contains(it.URL, "/answer/") {
			assert.Equal(t, "越南的 ETC 好用吗", it.Title, "an answer is titled by its question")
			assert.Equal(t, created.Truncate(time.Second), it.PublishedAt.UTC())
			assert.InDelta(t, 12, it.Engagement["voteups"], 0)
			assert.NotContains(t, it.Text, "<em>")
		}
	}

	req := rig.zhihu.last()
	assert.Equal(t, "/api/v4/search_v3", req.URL.Path)
	assert.Equal(t, "越南", req.URL.Query().Get("q"))
	assert.Equal(t, "z_c0=zhihu-token; d_c0=device", req.Header.Get("Cookie"))
	assert.Equal(t, "https://www.zhihu.com/search", req.Header.Get("Referer"))
}

func TestZhihuForbiddenEndsTheSessionAndTheWebServesMeanwhile(t *testing.T) {
	rig := newCNRig(t)
	signInZhihu(t, rig)
	rig.zhihu.set(func(*http.Request) (int, string) {
		return http.StatusForbidden, `{"error":{"code":40352,"message":"need login"}}`
	})

	a := rig.search(t, listening.SourceZhihu)

	cov := coverageOf(t, a, listening.SourceZhihu)
	tried := cov.Streams[0].Tried
	require.Len(t, tried, 3)
	assert.Equal(t, string(listening.CodeNeedLogin), tried[0].Result)
	assert.True(t, tried[1].Skipped, "the page is Zhihu's too: not asked after its sign-in wall")
	assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].ServedBy, "the web search on its site serves")
	require.Len(t, a.Items, 1)
	assert.Equal(t, "https://www.zhihu.com/question/123/answer/456", a.Items[0].URL)

	acct := stateOf(t, rig.svc, listening.SourceZhihu).Signed.Account
	assert.Equal(t, listening.AccountNeedLogin, acct.State)
	require.Equal(t, 1, rig.told.Count())
	assert.Contains(t, rig.told.all[0].Fix, "sign it in again")

	asked := len(rig.zhihu.paths())
	_ = rig.search(t, listening.SourceZhihu)
	assert.Len(t, rig.zhihu.paths(), asked, "an expired account is not asked again")
}

func TestZhihuPageReadsItsOwnSearchXHRWhenTheAPIAnswersSomethingElse(t *testing.T) {
	rig := newCNRig(t)
	signInZhihu(t, rig)
	rig.zhihu.set(func(*http.Request) (int, string) {
		return 200, `{"error":{"code":10003,"message":"请求参数异常，请升级客户端后重试"}}`
	})

	created := start.Add(-24 * time.Hour)
	page := &pageFake{
		respond: func(address string, _ int) []browser.Response {
			return []browser.Response{
				{URL: "https://static.zhihu.com/app.js", Status: 200, RequestID: "js"},
				{URL: "https://www.zhihu.com/api/v4/search_v3?q=x", Status: 200, RequestID: "s1"},
			}
		},
		bodies: map[string]string{"s1": zhihuResults(t, created)},
		eval: func(string, string) any {
			return map[string]any{"href": "https://www.zhihu.com/search", "ready": "complete", "text": ""}
		},
	}
	rig.page("listening:zhihu", page)

	a := rig.search(t, listening.SourceZhihu)

	cov := coverageOf(t, a, listening.SourceZhihu)
	assert.Equal(t, string(listening.CodeBroken), cov.Streams[0].Tried[0].Result)
	assert.Equal(t, listening.BackendZhihuPage, cov.Streams[0].ServedBy, "the page's own search XHR serves")
	assert.Len(t, a.Items, 3)

	page.mu.Lock()
	defer page.mu.Unlock()

	require.NotEmpty(t, page.visits)
	assert.Equal(t, "https://www.zhihu.com/search?q=%E8%B6%8A%E5%8D%97&type=content", page.visits[0])
	assert.Equal(t, "zhihu-token", rig.browser.made["listening:zhihu"][0].Value)
}

// weiboCards is a getIndex answer with two posts.
func weiboCards(t *testing.T) string {
	t.Helper()

	return asJSON(t, map[string]any{"ok": 1, "data": map[string]any{"cards": []any{
		map[string]any{"card_type": 9, "mblog": map[string]any{
			"mid": "5000000000000001", "bid": "AbCdEf123", "text": "越南 <a href='x'>#ETC#</a> 收费",
			"created_at": "Tue Nov 24 10:00:00 +0800 2026", "reposts_count": 4, "comments_count": "1万",
			"attitudes_count": 9, "user": map[string]any{"id": 1001, "screen_name": "某用户"},
		}},
		map[string]any{"card_type": 11, "card_group": []any{map[string]any{"mblog": map[string]any{
			"mid": "5000000000000002", "text": "越南路况", "created_at": "3小时前",
			"user": map[string]any{"id": "1002", "screen_name": "另一位"},
		}}}},
	}}})
}

// weiboAnswers answers /api/config with login and getIndex with page, the next pages empty.
func weiboAnswers(login bool, page func() (int, string)) func(*http.Request) (int, string) {
	return func(r *http.Request) (int, string) {
		switch {
		case r.URL.Path == "/api/config":
			return 200, fmt.Sprintf(`{"ok":1,"data":{"login":%t}}`, login)
		case r.URL.Query().Get("page") != "":
			return 200, `{"ok":0,"msg":"这里还没有内容","data":{"cards":[]}}`
		default:
			return page()
		}
	}
}

func TestWeiboKeepsASignInOnlyOnceWeiboSaysTheAccountIsSignedIn(t *testing.T) {
	rig := newCNRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceWeibo))

	rig.weibo.set(weiboAnswers(false, func() (int, string) { return 200, weiboCards(t) }))
	err := rig.svc.PasteCookies(t.Context(), listening.SourceWeibo, "SUB=visitor-sub; SUBP=v", "")
	require.ErrorIs(t, err, listening.ErrInvalid, "a visitor's SUB is no sign-in, though the search may answer")
	assert.Contains(t, err.Error(), "does not take the session")
	assert.NotContains(t, err.Error(), "visitor-sub")
	assert.Nil(t, stateOf(t, rig.svc, listening.SourceWeibo).Signed.Account)
	assert.Equal(t, []string{"/api/config"}, rig.weibo.paths(), "no search before Weibo says it is signed in")

	rig.weibo.set(weiboAnswers(true, func() (int, string) { return 200, weiboCards(t) }))
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceWeibo, "SUB=account-sub; SUBP=v", ""))
	assert.Equal(t, listening.AccountSignedIn, stateOf(t, rig.svc, listening.SourceWeibo).Signed.Account.State)
}

func TestWeiboReadsTheMobileSearchAsTheAccount(t *testing.T) {
	rig := newCNRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceWeibo))
	rig.weibo.set(weiboAnswers(true, func() (int, string) { return 200, weiboCards(t) }))
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceWeibo, "SUB=account-sub", ""))

	a := rig.search(t, listening.SourceWeibo)

	cov := coverageOf(t, a, listening.SourceWeibo)
	assert.Equal(t, listening.BackendWeiboAPI, cov.Streams[0].ServedBy)
	require.Len(t, a.Items, 2)

	byID := map[string]listening.Item{}
	for _, it := range a.Items {
		byID[it.URL] = it
	}

	first := byID["https://weibo.com/1001/AbCdEf123"]
	assert.Equal(t, "越南 #ETC# 收费", first.Text)
	assert.Equal(t, time.Date(2026, 11, 24, 2, 0, 0, 0, time.UTC), first.PublishedAt.UTC(), "Beijing time")
	assert.InDelta(t, 10000, first.Engagement["comments"], 0, "1万 is ten thousand")

	second := byID["https://weibo.com/1002/5000000000000002"]
	require.NotNil(t, second.PublishedAt)
	assert.WithinDuration(t, start.Add(-3*time.Hour), second.PublishedAt.UTC(), time.Minute,
		"3小时前 is three hours before the read")

	req := rig.weibo.last()
	assert.Equal(t, "/api/container/getIndex", req.URL.Path)
	assert.Equal(t, "100103type=1&q=越南", req.URL.Query().Get("containerid"))
	assert.Equal(t, "2", req.URL.Query().Get("page"), "a second page is asked, empty")
	assert.Equal(t, "XMLHttpRequest", req.Header.Get("X-Requested-With"))
	assert.Equal(t, "SUB=account-sub", req.Header.Get("Cookie"))
}

func TestWeiboOkMinus100AndThePassportAskForASignIn(t *testing.T) {
	cases := map[string]func() (int, string){
		"ok -100":  func() (int, string) { return 200, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}` },
		"passport": func() (int, string) { return http.StatusFound, "https://passport.weibo.com/visitor/visitor?a=1" },
	}

	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newCNRig(t)
			require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceWeibo))
			rig.weibo.set(weiboAnswers(true, func() (int, string) { return 200, weiboCards(t) }))
			require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceWeibo, "SUB=account-sub", ""))

			rig.weibo.set(weiboAnswers(true, answer))

			a := rig.search(t, listening.SourceWeibo)

			cov := coverageOf(t, a, listening.SourceWeibo)
			assert.Equal(t, string(listening.CodeNeedLogin), cov.Streams[0].Tried[0].Result)
			assert.True(t, cov.Streams[0].Tried[1].Skipped, "Weibo's page is not asked after its sign-in wall")
			assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].ServedBy)
			assert.Equal(t, listening.AccountNeedLogin,
				stateOf(t, rig.svc, listening.SourceWeibo).Signed.Account.State)
		})
	}
}

// douyinPage is Douyin's search page whose search XHR answers body.
func douyinPage(body string) *pageFake {
	return &pageFake{
		respond: func(string, int) []browser.Response {
			return []browser.Response{
				{URL: "https://www.douyin.com/aweme/v1/web/search/item/?keyword=x&a_bogus=y", Status: 200,
					RequestID: "d1"},
			}
		},
		bodies: map[string]string{"d1": body},
		eval: func(string, string) any {
			return map[string]any{"href": "https://www.douyin.com/search/x", "ready": "complete", "text": ""}
		},
	}
}

func signInDouyin(t *testing.T, rig *cnRig) {
	t.Helper()

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceDouyin))
	rig.page("listening-check:douyin", douyinPage(`{"status_code":0,"data":[],"has_more":0}`))
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceDouyin, "sessionid_ss=dy-session", ""))
}

func TestDouyinReadsThePagesOwnSearchXHRWithTheWindowAsPublishTime(t *testing.T) {
	rig := newCNRig(t)
	signInDouyin(t, rig)

	made := start.Add(-48 * time.Hour)
	page := douyinPage(asJSON(t, map[string]any{"status_code": 0, "has_more": 0, "data": []any{
		map[string]any{"type": 1, "aweme_info": map[string]any{
			"aweme_id": "7300000000000000009", "desc": "越南 ETC", "create_time": made.Unix(),
			"author":     map[string]any{"nickname": "抖音用户"},
			"statistics": map[string]any{"play_count": 1000, "digg_count": 50, "comment_count": 7, "share_count": 2},
		}},
	}}))
	rig.page("listening:douyin", page)

	a := rig.search(t, listening.SourceDouyin)

	cov := coverageOf(t, a, listening.SourceDouyin)
	assert.Equal(t, listening.BackendDouyinPage, cov.Streams[0].ServedBy)
	require.Len(t, a.Items, 1)
	assert.Equal(t, "https://www.douyin.com/video/7300000000000000009", a.Items[0].URL)
	assert.Equal(t, made.Truncate(time.Second), a.Items[0].PublishedAt.UTC())
	assert.InDelta(t, 1000, a.Items[0].Engagement["views"], 0)

	page.mu.Lock()
	defer page.mu.Unlock()

	require.NotEmpty(t, page.visits)
	assert.Equal(t, "https://www.douyin.com/search/%E8%B6%8A%E5%8D%97?publish_time=180&sort_type=0&type=video",
		page.visits[0], "a 30-day window asks half a year, the shortest filter that holds it")
}

func TestDouyinVerificationRefusesTheAccountAndAnEmptyAnswerIsAZeroOnlyWhenItSaysSo(t *testing.T) {
	rig := newCNRig(t)
	signInDouyin(t, rig)

	rig.page("listening:douyin", douyinPage(`{"status_code":0,"data":[],"has_more":1}`))

	a := rig.search(t, listening.SourceDouyin)
	assert.Equal(t, string(listening.CodeBroken), coverageOf(t, a, listening.SourceDouyin).Streams[0].Tried[0].Result,
		"an empty answer that says there is more is no clean zero")

	// A new rig: the broken read above rests the backend.
	rig = newCNRig(t)
	signInDouyin(t, rig)
	rig.page("listening:douyin", &pageFake{eval: func(string, string) any {
		return map[string]any{"href": "https://www.douyin.com/search/x", "ready": "complete", "text": "请完成下列验证后继续"}
	}})

	a = rig.search(t, listening.SourceDouyin)
	assert.Equal(t, string(listening.CodeRefused), coverageOf(t, a, listening.SourceDouyin).Streams[0].Tried[0].Result)
	assert.Equal(t, listening.AccountRefused, stateOf(t, rig.svc, listening.SourceDouyin).Signed.Account.State,
		"a captcha refuses the account until a new sign-in; it is never answered")
}

// xhsTab is Xiaohongshu's search page: its state by the script asked, its filter panel, and its search answers.
type xhsTab struct {
	path     string
	loggedIn any
	feeds    []any
	status   int
	answer   string
	// filtered and filteredAnswer are the feeds and the search's answer after the filter's click, when set.
	filtered       []any
	filteredAnswer string
}

func (x *xhsTab) page(t *testing.T) *pageFake {
	t.Helper()

	p := &pageFake{bodies: map[string]string{}}
	p.respond = func(string, int) []browser.Response {
		status := x.status
		if status == 0 {
			status = 200
		}

		p.bodies["n1"] = x.answer
		out := []browser.Response{{URL: "https://edith.xiaohongshu.com/api/sns/web/v1/search/notes", Status: status,
			RequestID: "n1"}}

		if len(p.clicks) > 0 && x.filteredAnswer != "" {
			p.bodies["n2"] = x.filteredAnswer
			out = append(out, browser.Response{URL: "https://edith.xiaohongshu.com/api/sns/web/v1/search/notes",
				Status: 200, RequestID: "n2"})
		}

		return out
	}
	feeds := func() []any {
		if len(p.clicks) > 0 && x.filtered != nil {
			return x.filtered
		}

		return x.feeds
	}
	p.eval = func(expression, _ string) any {
		switch {
		case strings.Contains(expression, "searchContext"):
			ids := "a,b"
			if len(p.clicks) > 0 {
				ids = "c,d"
			}

			if len(feeds()) == 0 {
				ids = ""
			}

			path := cmpOr(x.path, "/search_result")
			logged := 1

			switch x.loggedIn {
			case false:
				logged = 0
			case "unknown":
				logged = -1
			}

			return map[string]any{"path": path, "host": "www.xiaohongshu.com", "logged_in": logged, "ours": true,
				"ready": len(feeds()) > 0, "empty": len(feeds()) == 0, "ids": ids}
		case strings.Contains(expression, "div.filter-panel div.filters"):
			assert.Contains(t, expression, `"发布时间","半年内"`, "a 30-day window chooses 半年内")

			if strings.Contains(expression, `"button"`) {
				return map[string]any{"x": 10, "y": 20, "found": true}
			}

			return map[string]any{"x": 30, "y": 40, "found": true}
		case strings.Contains(expression, "JSON.stringify(v)"):
			return asJSON(t, feeds())
		default:
			return map[string]any{"href": "https://www.xiaohongshu.com" + cmpOr(x.path, "/search_result"),
				"ready": "complete"}
		}
	}

	return p
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

func signInXiaohongshu(t *testing.T, rig *cnRig) {
	t.Helper()

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceXiaohongshu))
	empty := &xhsTab{answer: `{"code":0,"success":true,"data":{"has_more":false}}`}
	rig.page("listening-check:xiaohongshu", empty.page(t))
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceXiaohongshu,
		"web_session=xhs-session; a1=device", ""))
}

func TestXiaohongshuFiltersTheSearchToTheWindowAndReadsItsNotes(t *testing.T) {
	rig := newCNRig(t)
	signInXiaohongshu(t, rig)

	tab := &xhsTab{answer: `{"code":0,"success":true,"data":{"items":[{}],"has_more":true}}`, feeds: []any{
		map[string]any{"id": "0123456789abcdef01234567", "xsecToken": "tok=", "modelType": "note",
			"noteCard": map[string]any{"displayTitle": "越南旅行", "user": map[string]any{"nickname": "小红"},
				"interactInfo": map[string]any{"likedCount": "1.2万", "collectedCount": "30", "commentCount": "10+"}}},
		map[string]any{"id": "live1", "modelType": "live_v2"},
	}}
	page := tab.page(t)
	rig.page("listening:xiaohongshu", page)

	a := rig.search(t, listening.SourceXiaohongshu)

	cov := coverageOf(t, a, listening.SourceXiaohongshu)
	assert.Equal(t, listening.BackendXiaohongshuPage, cov.Streams[0].ServedBy)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "半年内")
	require.Len(t, a.Items, 1, "a live card is no note")

	it := a.Items[0]
	assert.Equal(t, "https://www.xiaohongshu.com/explore/0123456789abcdef01234567?xsec_source=pc_search&xsec_token=tok%3D",
		it.URL)
	assert.Equal(t, "越南旅行", it.Title)
	assert.InDelta(t, 12000, it.Engagement["likes"], 0)
	assert.InDelta(t, 10, it.Engagement["comments"], 0)
	assert.Nil(t, it.PublishedAt, "a search card has no date: the filter bounds it")

	page.mu.Lock()
	defer page.mu.Unlock()

	assert.Equal(t, "https://www.xiaohongshu.com/search_result?keyword=%E8%B6%8A%E5%8D%97&source=web_explore_feed",
		page.visits[0])
	assert.Equal(t, [][2]float64{{10, 20}}, page.hovers, "the filter button is hovered")
	assert.Equal(t, [][2]float64{{30, 40}}, page.clicks, "and the option clicked")
}

func TestXiaohongshuBlocksStopTheAccount(t *testing.T) {
	cases := []struct {
		name string
		tab  xhsTab
		code listening.Code
		acct string
	}{
		{"security check", xhsTab{path: "/website-login/captcha"}, listening.CodeRefused, listening.AccountRefused},
		{"sign-in", xhsTab{path: "/website-login"}, listening.CodeNeedLogin, listening.AccountNeedLogin},
		{"visitor", xhsTab{loggedIn: false}, listening.CodeNeedLogin, listening.AccountNeedLogin},
		{"captcha status", xhsTab{status: 461}, listening.CodeRefused, listening.AccountRefused},
		{"account security", xhsTab{answer: `{"code":300011,"success":false}`}, listening.CodeRefused,
			listening.AccountRefused},
		{"ip risk", xhsTab{answer: `{"code":300012,"success":false}`}, listening.CodeRefused, listening.AccountRefused},
		{"rate limit", xhsTab{status: 429}, listening.CodeRateLimited, listening.AccountSignedIn},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newCNRig(t)
			signInXiaohongshu(t, rig)

			tab := c.tab
			page := tab.page(t)
			rig.page("listening:xiaohongshu", page)

			a := rig.search(t, listening.SourceXiaohongshu)

			cov := coverageOf(t, a, listening.SourceXiaohongshu)
			assert.Equal(t, string(c.code), cov.Streams[0].Tried[0].Result)
			assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].ServedBy, "the web serves meanwhile")

			st := stateOf(t, rig.svc, listening.SourceXiaohongshu)
			assert.Equal(t, c.acct, st.Signed.Account.State)

			page.mu.Lock()
			visits := len(page.visits)
			page.mu.Unlock()

			if c.code == listening.CodeRateLimited {
				assert.Equal(t, rig.clock.Now().Add(24*time.Hour).Truncate(time.Minute),
					st.Signed.StoppedUntil.Truncate(time.Minute), "a rate limit stops the site for a day")
			}

			rig.clock.Advance(time.Hour)
			_ = rig.search(t, listening.SourceXiaohongshu)

			page.mu.Lock()
			defer page.mu.Unlock()

			assert.Len(t, page.visits, visits, "a stopped account is not read again")
		})
	}
}

func TestXiaohongshuSignInCheckSearchesOnceForAVisitorsWebSession(t *testing.T) {
	rig := newCNRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceXiaohongshu))

	visitor := &xhsTab{path: "/website-login"}
	check := visitor.page(t)
	rig.page("listening-check:xiaohongshu", check)

	profile := func(session, waf string) {
		rig.browser.mu.Lock()
		defer rig.browser.mu.Unlock()

		rig.browser.profile = []browser.Cookie{
			{Name: "web_session", Value: session, Domain: ".xiaohongshu.com"},
			{Name: "acw_tc", Value: waf, Domain: ".xiaohongshu.com"},
		}
	}

	for i := range 3 {
		// A visitor has a web_session too, and the site's WAF cookie changes on each page.
		profile("visitor", fmt.Sprint("waf-", i))

		got, err := rig.svc.SignInCheck(t.Context(), listening.SourceXiaohongshu, "")
		require.NoError(t, err)
		assert.False(t, got.Done)
		assert.Contains(t, got.Waiting, "does not take the session yet")
	}

	check.mu.Lock()
	assert.Len(t, check.visits, 1, "searched once for the visitor's web_session, whatever its other cookies")
	check.mu.Unlock()

	signedIn := &xhsTab{answer: `{"code":0,"success":true,"data":{"has_more":false}}`}
	rig.page("listening-check:xiaohongshu", signedIn.page(t))
	profile("account", "waf-9")

	got, err := rig.svc.SignInCheck(t.Context(), listening.SourceXiaohongshu, "secondary")
	require.NoError(t, err)
	assert.True(t, got.Done, "a new web_session is the owner's sign-in, verified by a real search")
	assert.Empty(t, rig.browser.profileNames(), "the site's cookies leave the shared profile")
}

func TestEachSiteIsReadAtItsOwnPace(t *testing.T) {
	rig := newCNRig(t)
	signInZhihu(t, rig)
	signInXiaohongshu(t, rig)

	rig.zhihu.set(func(*http.Request) (int, string) { return 200, zhihuResults(t, start.Add(-time.Hour)) })

	tab := &xhsTab{answer: `{"code":0,"success":true,"data":{"items":[{}]}}`, feeds: []any{
		map[string]any{"id": "0123456789abcdef01234567", "modelType": "note",
			"noteCard": map[string]any{"displayTitle": "越南"}},
	}}
	rig.page("listening:xiaohongshu", tab.page(t))

	// Both sign-ins read at the same moment; Xiaohongshu's search comes right after its own.
	*rig.waits = nil
	_ = rig.search(t, listening.SourceXiaohongshu)
	assert.Equal(t, []time.Duration{15 * time.Second}, *rig.waits, "Xiaohongshu's next read waits 15 seconds")

	// Zhihu's last read was its sign-in's, 15 seconds ago: no wait; then 10 seconds after that search.
	_ = rig.search(t, listening.SourceZhihu)
	*rig.waits = nil
	_ = rig.search(t, listening.SourceZhihu)
	assert.Equal(t, []time.Duration{10 * time.Second}, *rig.waits, "Zhihu's next read waits 10 seconds")
}

func TestASignedChineseSourcesFixSaysToSignInAgain(t *testing.T) {
	for _, source := range []string{listening.SourceZhihu, listening.SourceXiaohongshu} {
		assert.Contains(t, listening.Fix(source, listening.CodeRefused, time.Time{}), "sign in again", source)
		assert.Contains(t, listening.Fix(source, listening.CodeNeedLogin, time.Time{}), "sign it in again", source)
	}
}
