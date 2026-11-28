package listening_test

import (
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

// Review round 2 of PR #1364 (issue #1362): the reviewer's probes, written from its findings.

// douyinVideos is a Douyin search answer with one video, more to come.
func douyinVideos(t *testing.T, id, desc string) string {
	t.Helper()

	return asJSON(t, map[string]any{"status_code": 0, "has_more": 1, "data": []any{
		map[string]any{"aweme_info": map[string]any{
			"aweme_id": id, "desc": desc, "create_time": start.Add(-time.Hour).Unix(),
		}},
	}})
}

// P1-1: a 429 the tab logged while the read waited for its next turn stops it before another scroll.
func TestReview1364Round2DouyinRechecksTheTabBeforeEachScroll(t *testing.T) {
	rig := newCNRig(t)
	signInDouyin(t, rig)

	var limited time.Time

	page := &pageFake{bodies: map[string]string{"d1": douyinVideos(t, "7300000000000000009", "越南")}}
	page.respond = func(_ string, scrolls int) []browser.Response {
		out := []browser.Response{{URL: "https://www.douyin.com/aweme/v1/web/search/item/?a=1", Status: 200,
			RequestID: "d1"}}

		// The 429 comes a moment after the first scroll's answer was read: during the wait for the next turn.
		switch {
		case scrolls >= 1 && limited.IsZero():
			limited = rig.clock.Now().Add(2 * time.Second)
		case scrolls >= 1 && !rig.clock.Now().Before(limited):
			out = append(out, browser.Response{URL: "https://www.douyin.com/aweme/v1/web/search/item/?a=2",
				Status: http.StatusTooManyRequests, RequestID: "d2"})
		}

		return out
	}
	page.eval = func(string, string) any {
		return map[string]any{"href": "https://www.douyin.com/search/x", "ready": "complete", "text": ""}
	}
	rig.page("listening:douyin", page)

	a := rig.search(t, listening.SourceDouyin)

	page.mu.Lock()
	scrolls := page.scrolls
	page.mu.Unlock()

	assert.Equal(t, 1, scrolls, "the 429 logged during the wait stops the read before a second scroll")
	assert.Len(t, a.Items, 1)
	assert.False(t, stateOf(t, rig.svc, listening.SourceDouyin).Signed.StoppedUntil.IsZero(), "the site is stopped")
}

// P1-2: two Douyin searches at once: the second waits for the first to end and never navigates the tab under it, so
// neither search's videos are the other's.
func TestReview1364Round2TwoDouyinSearchesDoNotShareTheTab(t *testing.T) {
	rig := newCNRig(t)
	signInDouyin(t, rig)

	page := &pageFake{bodies: map[string]string{
		"a1": douyinVideos(t, "7300000000000000001", "甲"), "b1": douyinVideos(t, "7300000000000000002", "乙"),
	}}
	page.respond = func(address string, _ int) []browser.Response {
		id := "a1"
		if strings.Contains(address, "%E4%B9%99") { // 乙
			id = "b1"
		}

		return []browser.Response{{URL: "https://www.douyin.com/aweme/v1/web/search/item/?q=" + id, Status: 200,
			RequestID: id}}
	}
	page.eval = func(string, string) any {
		return map[string]any{"href": "https://www.douyin.com/search/x", "ready": "complete", "text": ""}
	}
	rig.page("listening:douyin", page)

	var (
		wg      sync.WaitGroup
		answers = map[string]listening.Answer{}
		mu      sync.Mutex
	)

	for _, q := range []string{"甲", "乙"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			p := plan([]string{listening.SourceDouyin}, q)
			p.WindowDays = 30

			a, err := rig.svc.Search(t.Context(), p, listening.Caller{Web: rig.web})
			assert.NoError(t, err)

			mu.Lock()
			answers[q] = a
			mu.Unlock()
		}()
	}

	wg.Wait()

	ids := func(a listening.Answer) []string {
		out := []string{}
		for _, it := range a.Items {
			out = append(out, it.URL[strings.LastIndex(it.URL, "/")+1:])
		}

		return out
	}

	assert.Equal(t, []string{"7300000000000000001"}, ids(answers["甲"]), "甲's search reads 甲's videos only")
	assert.Equal(t, []string{"7300000000000000002"}, ids(answers["乙"]), "乙's search reads 乙's videos only")
}

// P1-3: a verification that read posts and then met Zhihu's 403 does not keep the new session.
func TestReview1364Round2AVerificationThatMetA403DoesNotKeepTheSession(t *testing.T) {
	rig := newCNRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceZhihu))
	rig.zhihu.set(func(*http.Request) (int, string) { return 200, `{"error":{"code":10003,"message":"x"}}` })

	check := &pageFake{
		respond: func(_ string, scrolls int) []browser.Response {
			out := []browser.Response{{URL: "https://www.zhihu.com/api/v4/search_v3?q=1", Status: 200, RequestID: "s1"}}
			if scrolls >= 1 {
				out = append(out, browser.Response{URL: "https://www.zhihu.com/api/v4/search_v3?q=2",
					Status: http.StatusForbidden, RequestID: "s2"})
			}

			return out
		},
		// One post, so the check, which reads two, scrolls for more (the reviewer's probe).
		bodies: map[string]string{"s1": asJSON(t, map[string]any{"data": []any{
			map[string]any{"type": "search_result", "object": map[string]any{"type": "question", "id": 321, "title": "越南"}},
		}})},
		eval: func(string, string) any {
			return map[string]any{"href": "https://www.zhihu.com/search", "ready": "complete", "text": ""}
		},
	}
	rig.page("listening-check:zhihu", check)

	err := rig.svc.PasteCookies(t.Context(), listening.SourceZhihu, "z_c0=zhihu-token", "")
	require.ErrorIs(t, err, listening.ErrInvalid, "a 403 in the check refuses the session, whatever was read before")
	assert.Nil(t, stateOf(t, rig.svc, listening.SourceZhihu).Signed.Account)
}

// P2: the search answered 200 and the page landed on the sign-in, its body not loaded yet: the body is waited for, and
// its 300012 is the read's answer, not a sign-in asked.
func TestReview1364Round2XiaohongshuWaitsForTheBodyBeforeTrustingASignInLanding(t *testing.T) {
	rig := newCNRig(t)
	signInXiaohongshu(t, rig)

	tab := &xhsTab{path: "/website-login", answer: `{"code":300012,"success":false}`}
	page := tab.page(t)
	page.bodyFails = map[string]int{"n1": 1}
	rig.page("listening:xiaohongshu", page)

	a := rig.search(t, listening.SourceXiaohongshu)
	assert.Equal(t, string(listening.CodeRefused),
		coverageOf(t, a, listening.SourceXiaohongshu).Streams[0].Tried[0].Result)
}
