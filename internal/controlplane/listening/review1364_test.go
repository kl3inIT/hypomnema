package listening_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// Review round 1 of PR #1364 (issue #1362): the reviewer's probes, written from its findings.

// weiboTab is m.weibo.cn's page: each fetch its page asks is answered by answer and its time kept.
type weiboTab struct {
	mu     sync.Mutex
	clock  *clock
	answer func(path string) (int, string)
	asked  []string
	at     []time.Time
}

func (w *weiboTab) page() *pageFake {
	return &pageFake{eval: func(expression, _ string) any {
		if !strings.Contains(expression, "fetch(") {
			return map[string]any{"href": "https://m.weibo.cn/", "ready": "complete", "text": ""}
		}

		path := expression[strings.Index(expression, `fetch("`)+len(`fetch("`):]
		path = path[:strings.Index(path, `"`)]

		w.mu.Lock()
		defer w.mu.Unlock()

		w.asked = append(w.asked, path)
		w.at = append(w.at, w.clock.Now())

		status, body := w.answer(path)

		return map[string]any{"status": status, "url": "https://m.weibo.cn" + path, "body": body}
	}}
}

func (w *weiboTab) answers(t *testing.T) func(path string) (int, string) {
	t.Helper()

	return func(path string) (int, string) {
		switch {
		case strings.HasPrefix(path, "/api/config"):
			return 200, `{"ok":1,"data":{"login":true}}`
		case strings.Contains(path, "page=2"):
			return 200, `{"ok":0,"msg":"这里还没有内容","data":{"cards":[]}}`
		default:
			return 200, weiboCards(t)
		}
	}
}

// P1-1: each request m.weibo.cn's page is made to ask, /api/config, the search and its second page, is a read of its
// own, at least the gap after the one before it.
func TestReview1364WeiboPageRequestsArePacedOneByOne(t *testing.T) {
	rig := newCNRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceWeibo))
	rig.weibo.set(weiboAnswers(true, func() (int, string) { return 200, weiboCards(t) }))
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceWeibo, "SUB=account-sub", ""))

	// The API breaks: m.weibo.cn's page serves.
	rig.weibo.set(weiboAnswers(true, func() (int, string) { return http.StatusInternalServerError, "" }))

	tab := &weiboTab{clock: rig.clock}
	tab.answer = tab.answers(t)
	rig.page("listening:weibo", tab.page())

	a := rig.search(t, listening.SourceWeibo)
	assert.Equal(t, listening.BackendWeiboPage, coverageOf(t, a, listening.SourceWeibo).Streams[0].ServedBy)

	tab.mu.Lock()
	defer tab.mu.Unlock()

	require.Len(t, tab.asked, 3, "/api/config, the search and its second page")

	for i := 1; i < len(tab.at); i++ {
		assert.GreaterOrEqual(t, tab.at[i].Sub(tab.at[i-1]), 10*time.Second, "%s waits its own turn", tab.asked[i])
	}
}

// P1-1: the filter's click sends Xiaohongshu a search of its own: it waits Xiaohongshu's gap as its own read.
func TestReview1364XiaohongshuFilterIsAReadOfItsOwn(t *testing.T) {
	rig := newCNRig(t)
	signInXiaohongshu(t, rig)

	tab := &xhsTab{answer: `{"code":0,"success":true,"data":{"items":[{}],"has_more":true}}`, feeds: []any{
		map[string]any{"id": "0123456789abcdef01234567", "modelType": "note",
			"noteCard": map[string]any{"displayTitle": "越南"}},
	}}
	rig.page("listening:xiaohongshu", tab.page(t))

	*rig.waits = nil
	_ = rig.search(t, listening.SourceXiaohongshu)

	fifteen := 0

	for _, w := range *rig.waits {
		if w == 15*time.Second {
			fifteen++
		}
	}

	assert.Equal(t, 2, fifteen, "the search, then the filter's search, each 15 seconds after the read before it")
}

// P1-1 and P1-2: each Douyin scroll is a read of its own, and the first 429 a scroll meets stops the reading: no
// scroll after it, the site stopped for a day, the videos read before it kept.
func TestReview1364DouyinStopsScrollingAtTheFirst429(t *testing.T) {
	rig := newCNRig(t)
	signInDouyin(t, rig)

	first := asJSON(t, map[string]any{"status_code": 0, "has_more": 1, "data": []any{
		map[string]any{"aweme_info": map[string]any{
			"aweme_id": "7300000000000000009", "desc": "越南", "create_time": start.Add(-time.Hour).Unix(),
		}},
	}})
	page := &pageFake{
		respond: func(_ string, scrolls int) []browser.Response {
			out := []browser.Response{{URL: "https://www.douyin.com/aweme/v1/web/search/item/?a=1", Status: 200,
				RequestID: "d1"}}
			if scrolls >= 1 {
				out = append(out, browser.Response{URL: "https://www.douyin.com/aweme/v1/web/search/item/?a=2",
					Status: http.StatusTooManyRequests, RequestID: "d2"})
			}

			return out
		},
		bodies: map[string]string{"d1": first},
		eval: func(string, string) any {
			return map[string]any{"href": "https://www.douyin.com/search/x", "ready": "complete", "text": ""}
		},
	}
	rig.page("listening:douyin", page)

	*rig.waits = nil
	a := rig.search(t, listening.SourceDouyin)

	page.mu.Lock()
	scrolls := page.scrolls
	page.mu.Unlock()

	assert.Equal(t, 1, scrolls, "no scroll after the one that met a 429")
	assert.Len(t, a.Items, 1, "the video read before it is kept")
	assert.Contains(t, *rig.waits, 10*time.Second, "the scroll waited its own turn")
	assert.False(t, stateOf(t, rig.svc, listening.SourceDouyin).Signed.StoppedUntil.IsZero(),
		"the 429 stops the site")
}

// P1-3: what Xiaohongshu's search answered holds when the page is also sent to a sign-in path: 300012 refuses the
// account, and a 429 stops the site for a day.
func TestReview1364XiaohongshuKeepsTheAnswersBlockWhenThePageRedirects(t *testing.T) {
	cases := []struct {
		name string
		tab  xhsTab
		code listening.Code
	}{
		{"ip risk", xhsTab{path: "/website-login/error", answer: `{"code":300012,"success":false}`},
			listening.CodeRefused},
		{"rate limit", xhsTab{path: "/website-login", status: http.StatusTooManyRequests}, listening.CodeRateLimited},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newCNRig(t)
			signInXiaohongshu(t, rig)

			tab := c.tab
			rig.page("listening:xiaohongshu", tab.page(t))

			a := rig.search(t, listening.SourceXiaohongshu)
			assert.Equal(t, string(c.code), coverageOf(t, a, listening.SourceXiaohongshu).Streams[0].Tried[0].Result)

			if c.code == listening.CodeRateLimited {
				assert.False(t, stateOf(t, rig.svc, listening.SourceXiaohongshu).Signed.StoppedUntil.IsZero())
			}
		})
	}
}

// P1-4: Zhihu's page read posts, then its next search_v3 answered 403: the session's end is kept with the posts read.
func TestReview1364ZhihuKeepsA403MetAfterPosts(t *testing.T) {
	rig := newCNRig(t)
	signInZhihu(t, rig)
	rig.zhihu.set(func(*http.Request) (int, string) { return 200, `{"error":{"code":10003,"message":"x"}}` })

	page := &pageFake{
		respond: func(_ string, scrolls int) []browser.Response {
			out := []browser.Response{{URL: "https://www.zhihu.com/api/v4/search_v3?q=1", Status: 200, RequestID: "s1"}}
			if scrolls >= 1 {
				out = append(out, browser.Response{URL: "https://www.zhihu.com/api/v4/search_v3?q=2",
					Status: http.StatusForbidden, RequestID: "s2"})
			}

			return out
		},
		bodies: map[string]string{"s1": zhihuResults(t, start.Add(-time.Hour))},
		eval: func(string, string) any {
			return map[string]any{"href": "https://www.zhihu.com/search", "ready": "complete", "text": ""}
		},
	}
	rig.page("listening:zhihu", page)

	a := rig.search(t, listening.SourceZhihu)

	assert.Len(t, a.Items, 3, "the posts read before the 403 are kept")
	assert.Equal(t, listening.AccountNeedLogin, stateOf(t, rig.svc, listening.SourceZhihu).Signed.Account.State)
	assert.Equal(t, 1, rig.told.Count(), "the owner is told")
}

// P1-5: when the API search fails without a sign that the account must stop, a new sign-in is verified through the
// source's own browser route before it is refused.
func TestReview1364ANewSignInIsVerifiedThroughTheBrowserWhenTheAPIFails(t *testing.T) {
	t.Run("zhihu", func(t *testing.T) {
		rig := newCNRig(t)
		require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceZhihu))
		rig.zhihu.set(func(*http.Request) (int, string) {
			return 200, `{"error":{"code":10003,"message":"请求参数异常，请升级客户端后重试"}}`
		})

		check := &pageFake{
			respond: func(string, int) []browser.Response {
				return []browser.Response{{URL: "https://www.zhihu.com/api/v4/search_v3?q=x", Status: 200, RequestID: "s1"}}
			},
			bodies: map[string]string{"s1": zhihuResults(t, start.Add(-time.Hour))},
			eval: func(string, string) any {
				return map[string]any{"href": "https://www.zhihu.com/search", "ready": "complete", "text": ""}
			},
		}
		rig.page("listening-check:zhihu", check)

		require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceZhihu, "z_c0=zhihu-token", ""))
		assert.Equal(t, listening.AccountSignedIn, stateOf(t, rig.svc, listening.SourceZhihu).Signed.Account.State)

		check.mu.Lock()
		defer check.mu.Unlock()

		assert.Len(t, check.visits, 1)
	})

	t.Run("weibo", func(t *testing.T) {
		rig := newCNRig(t)
		require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceWeibo))
		rig.weibo.set(weiboAnswers(true, func() (int, string) { return http.StatusInternalServerError, "" }))

		tab := &weiboTab{clock: rig.clock}
		tab.answer = tab.answers(t)
		rig.page("listening-check:weibo", tab.page())

		require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceWeibo, "SUB=account-sub", ""))
		assert.Equal(t, listening.AccountSignedIn, stateOf(t, rig.svc, listening.SourceWeibo).Signed.Account.State)

		tab.mu.Lock()
		defer tab.mu.Unlock()

		assert.Contains(t, tab.asked, "/api/config")
	})
}

// P2: a filter whose search Xiaohongshu answers empty, has_more false, is a clean zero, not a broken read.
func TestReview1364XiaohongshuEmptyAfterTheFilterIsAZero(t *testing.T) {
	rig := newCNRig(t)
	signInXiaohongshu(t, rig)

	tab := &xhsTab{
		answer: `{"code":0,"success":true,"data":{"items":[{}],"has_more":true}}`,
		feeds: []any{map[string]any{"id": "0123456789abcdef01234567", "modelType": "note",
			"noteCard": map[string]any{"displayTitle": "旧的"}}},
		filtered: []any{}, filteredAnswer: `{"code":0,"success":true,"data":{"has_more":false}}`,
	}
	rig.page("listening:xiaohongshu", tab.page(t))

	a := rig.search(t, listening.SourceXiaohongshu)

	cov := coverageOf(t, a, listening.SourceXiaohongshu)
	assert.Equal(t, string(listening.CodeNoResults), cov.Streams[0].Tried[0].Result)
}

// P2: a verification keeps Xiaohongshu's cookies only when its page says an account is signed in, not when it says
// nothing (xiaohongshu-mcp login.go:20-36 looks for the signed-in user).
func TestReview1364XiaohongshuVerificationNeedsTheSignedInSign(t *testing.T) {
	rig := newCNRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceXiaohongshu))

	unknown := &xhsTab{loggedIn: "unknown", answer: `{"code":0,"success":true,"data":{"has_more":false}}`}
	rig.page("listening-check:xiaohongshu", unknown.page(t))

	err := rig.svc.PasteCookies(t.Context(), listening.SourceXiaohongshu, "web_session=maybe", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.Nil(t, stateOf(t, rig.svc, listening.SourceXiaohongshu).Signed.Account)
}

// P2: a site whose login cookie a visitor does not have (LinkedIn's li_at) is verified again when only its other
// cookies change, as before PR #1364: only Xiaohongshu's and Weibo's memo keys on the login cookie.
func TestReview1364LinkedInIsVerifiedAgainWhenItsOtherCookiesChange(t *testing.T) {
	rig := newSignedRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceLinkedIn))

	check := &pageFake{snapshot: func(string, int) map[string]any {
		return map[string]any{"href": "https://www.linkedin.com/login", "status": 200, "ready": "complete"}
	}}
	rig.browser.mu.Lock()
	rig.browser.pages["listening-check:linkedin"] = check
	rig.browser.mu.Unlock()

	for i := range 2 {
		err := rig.svc.PasteCookies(t.Context(), listening.SourceLinkedIn, fmt.Sprintf("li_at=same; JSESSIONID=j%d", i),
			"")
		require.ErrorIs(t, err, listening.ErrInvalid)
	}

	check.mu.Lock()
	defer check.mu.Unlock()

	assert.Len(t, check.visits, 2)
}
