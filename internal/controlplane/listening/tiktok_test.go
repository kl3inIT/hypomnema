package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
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

// TikTok (issue #1294), with fakes only: the account's tab is a fake page whose fetches TikTok's web API answers as
// the test says, yt-dlp a fake command and the web search a function. No real account, cookie or call.

// tiktokTab is the account's tab on TikTok: navigating loads TikTok's web client, and each fetch from it is answered
// by answer, given the address the backend asked.
type tiktokTab struct {
	mu      sync.Mutex
	at      string
	visits  []string
	fetches []string
	// land is where navigating lands, when it is not the address.
	land   string
	answer func(address string) map[string]any
}

var tiktokAddress = regexp.MustCompile(`const address = ("(?:[^"\\]|\\.)*");`)

func (p *tiktokTab) Navigate(_ context.Context, address string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.at = address
	if p.land != "" {
		p.at = p.land
	}

	p.visits = append(p.visits, address)

	return nil
}

func (p *tiktokTab) Responses(context.Context) ([]browser.Response, error) { return nil, nil }

// Body, Hover and Click are the tab's (listening.Page, issue #1362); TikTok's read uses none.
func (p *tiktokTab) Body(context.Context, browser.Response) ([]byte, error) { return nil, nil }

func (p *tiktokTab) Hover(context.Context, float64, float64) error { return nil }

func (p *tiktokTab) Click(context.Context, float64, float64) error { return nil }

func (p *tiktokTab) Evaluate(_ context.Context, expression string, result any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var answer any

	switch m := tiktokAddress.FindStringSubmatch(expression); {
	case m != nil:
		var address string
		if err := json.Unmarshal([]byte(m[1]), &address); err != nil {
			return err
		}

		p.fetches = append(p.fetches, address)
		answer = map[string]any{"status": 200, "body": `{"status_code":0,"item_list":[],"has_more":0}`}

		if p.answer != nil {
			answer = p.answer(address)
		}
	case strings.Contains(expression, "byted_acrawler"):
		answer = map[string]any{"href": p.at, "ready": "complete", "signer": p.at != ""}
	default:
		return fmt.Errorf("an expression the fake does not know: %.60s", expression)
	}

	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}

	return json.Unmarshal(raw, result)
}

func (p *tiktokTab) fetched() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.fetches...)
}

// tiktokID is a TikTok id made at t: its high 32 bits are the time (shortform_oss.py:132).
func tiktokID(t time.Time, low uint64) string {
	return strconv.FormatUint(uint64(t.Unix())<<32|low, 10) //nolint:gosec // The test's times are after 1970.
}

func tiktokBody(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return map[string]any{"status": 200, "body": string(raw)}
}

func tiktokVideo(id, author, desc string, at time.Time, plays string) map[string]any {
	return map[string]any{
		"id": id, "desc": desc, "createTime": at.Unix(),
		"author": map[string]any{"uniqueId": author, "nickname": "Name of " + author},
		"stats":  map[string]any{"playCount": 1, "diggCount": 1, "commentCount": 1, "shareCount": 1},
		"statsV2": map[string]any{
			"playCount": plays, "diggCount": "5000", "commentCount": "300", "shareCount": "20",
		},
	}
}

// ytDlpFake answers each address it is given with its JSON, or with failure for that address.
type ytDlpFake struct {
	mu      sync.Mutex
	asked   []string
	videos  map[string]map[string]any
	failure func(address string) (string, error)
}

func (y *ytDlpFake) run(_ context.Context, args ...string) ([]byte, string, error) {
	y.mu.Lock()
	defer y.mu.Unlock()

	address := args[len(args)-1]
	y.asked = append(y.asked, address)

	if y.failure != nil {
		if stderr, err := y.failure(address); err != nil {
			return nil, stderr, err
		}
	}

	v, ok := y.videos[address]
	if !ok {
		return nil, "ERROR: [TikTok] Unable to extract", fmt.Errorf("exit status 1")
	}

	raw, err := json.Marshal(v)

	return raw, "", err
}

func (y *ytDlpFake) Asked() []string {
	y.mu.Lock()
	defer y.mu.Unlock()

	return append([]string(nil), y.asked...)
}

type tiktokRig struct {
	svc     *listening.Service
	vault   *vaultFake
	browser *browserFake
	told    *told
	yt      *ytDlpFake
	web     listening.WebSearch
	webAsks []string
	tab     *tiktokTab
	check   *tiktokTab
	clock   *clock
	// sleep is the pacer's wait, onPage what an account tab's opening tells; nil: none waits, none is told.
	sleep  func(ctx context.Context, d time.Duration) error
	onPage func(key string)
}

func newTikTokRig(t *testing.T) *tiktokRig {
	t.Helper()

	rig := &tiktokRig{
		vault: newVault(), told: &told{}, yt: &ytDlpFake{videos: map[string]map[string]any{}},
		tab: &tiktokTab{}, check: &tiktokTab{}, clock: newClock(),
	}

	rig.browser = &browserFake{made: map[string][]browser.Cookie{}, pages: map[string]*pageFake{}}
	tabs := &tiktokBrowser{browserFake: rig.browser, rig: rig, tabs: map[string]listening.Page{
		"listening:tiktok": rig.tab, "listening-check:tiktok": rig.check,
	}}

	var mu sync.Mutex

	rig.web = func(_ context.Context, query string, _ int) (websearch.Answer, error) {
		mu.Lock()
		rig.webAsks = append(rig.webAsks, query)
		mu.Unlock()

		return websearch.Answer{Provider: "fake", Results: []websearch.Result{
			{URL: "https://www.tiktok.com/@viewer.one/video/" + tiktokID(start.Add(-72*time.Hour), 7), Title: "a video",
				Snippet: "VETC trừ tiền hai lần"},
			{URL: "https://www.tiktok.com/@old.one/video/" + tiktokID(start.AddDate(0, -6, 0), 8), Title: "old",
				Snippet: "VETC năm ngoái"},
			{URL: "https://www.tiktok.com/discover/vetc", Title: "not a video", Snippet: "VETC"},
		}}, nil
	}

	rig.svc = listening.New(listening.Config{
		Platforms: listening.SignedSources(listening.Endpoints{YtDlp: rig.yt.run}, http.DefaultClient),
		Now:       rig.clock.Now, Sleep: func(ctx context.Context, d time.Duration) error {
			if rig.sleep != nil {
				return rig.sleep(ctx, d)
			}

			return nil
		}, DB: migrated(t), Tell: rig.told.tell, Browser: tabs,
		Secrets: rig.vault,
	})

	return rig
}

// tiktokBrowser is the shared browser whose account tabs are TikTok's.
type tiktokBrowser struct {
	*browserFake
	rig  *tiktokRig
	tabs map[string]listening.Page
}

func (b *tiktokBrowser) Page(ctx context.Context, key string, cookies []browser.Cookie) (listening.Page, error) {
	if _, err := b.browserFake.Page(ctx, key, cookies); err != nil {
		return nil, err
	}

	if b.rig.onPage != nil {
		b.rig.onPage(key)
	}

	if p, ok := b.tabs[key]; ok {
		return p, nil
	}

	return &tiktokTab{}, nil
}

func (r *tiktokRig) signIn(t *testing.T) {
	t.Helper()

	require.NoError(t, r.svc.Consent(t.Context(), listening.SourceTikTok))
	require.NoError(t, r.svc.PasteCookies(t.Context(), listening.SourceTikTok,
		"tt_csrf_token=c1; sessionid=session-1; msToken=token-1", "secondary"))
}

func (r *tiktokRig) search(t *testing.T) listening.Answer {
	t.Helper()

	p := plan([]string{listening.SourceTikTok}, "VETC")
	p.WindowDays = 30

	a, err := r.svc.Search(t.Context(), p, listening.Caller{Web: r.web})
	require.NoError(t, err)

	return a
}

func TestTikTokIsOffUntilItsConsentAndNeedsItsSessionAndMsToken(t *testing.T) {
	rig := newTikTokRig(t)

	st := stateOf(t, rig.svc, listening.SourceTikTok)
	require.NotNil(t, st.Signed)
	assert.Equal(t, "TikTok", st.Label)
	assert.Contains(t, st.Signed.Consent, "CAPTCHA")
	assert.Contains(t, st.Signed.Consent, "12 seconds")

	err := rig.svc.PasteCookies(t.Context(), listening.SourceTikTok, "sessionid=s1; msToken=m1", "")
	require.ErrorIs(t, err, listening.ErrInvalid, "no sign-in before the consent")

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceTikTok))

	for header, missing := range map[string]string{
		"msToken=only-a-visitors-token": "sessionid or sid_tt or uid_tt",
		"sessionid=very-secret":         "msToken",
	} {
		err := rig.svc.PasteCookies(t.Context(), listening.SourceTikTok, header, "")
		require.ErrorIs(t, err, listening.ErrInvalid)
		assert.Contains(t, err.Error(), missing)
		assert.NotContains(t, err.Error(), "very-secret")
	}

	assert.False(t, rig.vault.has("listening-tiktok"))
	assert.Empty(t, rig.check.fetched(), "cookies that lack the session are not searched with")

	require.NoError(t, rig.svc.SignInStart(t.Context(), listening.SourceTikTok))
	assert.Equal(t, []string{"https://www.tiktok.com/login"}, rig.browser.opened)
}

func TestTikTokReadsItsWebAPIFromTheAccountsTab(t *testing.T) {
	rig := newTikTokRig(t)

	recent := start.Add(-24 * time.Hour)
	top := tiktokID(recent, 1)
	second := tiktokID(recent.Add(-time.Hour), 2)
	old := tiktokID(start.AddDate(0, -3, 0), 3)

	rig.tab.answer = func(address string) map[string]any {
		u, _ := url.Parse(address)

		switch u.Path {
		case "/api/search/item/full/":
			return tiktokBody(map[string]any{"status_code": 0, "has_more": 1, "item_list": []any{
				tiktokVideo(second, "second.one", "VETC lại lỗi", recent.Add(-time.Hour), "900"),
				tiktokVideo(top, "top.one", "VETC thu phí sai\nlần hai", recent, "120000"),
				tiktokVideo(old, "old.one", "VETC cũ", start.AddDate(0, -3, 0), "999999"),
				map[string]any{"id": "not-an-id", "author": "x"},
			}})
		case "/api/comment/list/":
			video := u.Query().Get("aweme_id")
			author := map[string]string{top: "top.one", second: "second.one"}[video]
			comments := []any{map[string]any{
				"cid": video + "0", "text": "VETC my own reply", "digg_count": 9, "create_time": recent.Unix(),
				"user": map[string]any{"unique_id": author},
			}}

			said := []string{
				"VETC trừ tiền hai lần ở trạm", "VETC hỗ trợ chậm quá", "VETC app hay lỗi đăng nhập",
				"VETC hoàn tiền sau một tuần", "VETC thẻ dán bị bong", "VETC tổng đài không nghe máy",
				"VETC nạp tiền mất phí",
			}
			for i, text := range said {
				comments = append(comments, map[string]any{
					"cid": video + strconv.Itoa(i+1), "text": text + " (" + author + ")", "digg_count": 3 + 50*(i/6),
					"create_time": recent.Add(time.Minute).Unix(),
					"user":        map[string]any{"unique_id": fmt.Sprintf("%s-u%d", author, i)},
				})
			}

			return tiktokBody(map[string]any{"status_code": 0, "comments": comments})
		}

		return map[string]any{"status": 404, "body": ""}
	}

	rig.signIn(t)
	assert.Len(t, rig.check.fetched(), 1, "a sign-in is verified by one search, without the comments")

	a := rig.search(t)

	cov := coverageOf(t, a, listening.SourceTikTok)
	assert.Equal(t, listening.BackendWebAPI, cov.Streams[0].ServedBy)

	var videos, comments []listening.Item

	for _, it := range a.Items {
		if it.Parent == "" {
			videos = append(videos, it)
		} else {
			comments = append(comments, it)
		}
	}

	require.Len(t, videos, 2, "the video outside the window and the one without an id are dropped")

	byID := map[string]listening.Item{}
	for _, v := range videos {
		byID[v.NativeID] = v
	}

	v := byID[top]
	assert.Equal(t, "https://www.tiktok.com/@top.one/video/"+top, v.URL)
	assert.Equal(t, recent, v.PublishedAt.UTC())
	assert.InDelta(t, 120000, v.Engagement["views"], 0, "statsV2's counts are read first")
	assert.InDelta(t, 20, v.Engagement["shares"], 0)
	assert.True(t, v.Direct)

	require.Len(t, comments, 2*5, "five comments of each video read, the author's own left out")

	assert.True(t, slices.ContainsFunc(comments, func(c listening.Item) bool { return c.Author == "top.one-u6" }),
		"the most liked comment, last in TikTok's list, is kept: the most liked come first")

	for _, c := range comments {
		assert.NotEqual(t, "top.one", c.Author)
		assert.NotEqual(t, "second.one", c.Author)
		assert.Contains(t, c.URL, "?comment_id=", "each comment keeps its own address")
	}

	fetches := rig.tab.fetched()
	require.Len(t, fetches, 3, "one search page, then one comment list for each video in the window")
	assert.Contains(t, fetches[0], "/api/search/item/full/?")
	assert.Contains(t, fetches[0], "keyword=VETC")
	assert.Contains(t, fetches[0], "from_page=search")
	assert.Contains(t, fetches[1], "aweme_id="+top, "the most watched video's comments first")

	for _, f := range fetches {
		assert.NotContains(t, f, "msToken", "TikTok's own client signs the request")
		assert.NotContains(t, f, "X-Bogus")
	}

	rig.tab.mu.Lock()
	assert.Equal(t, []string{"https://www.tiktok.com/"}, rig.tab.visits, "the tab opens TikTok once and is kept")
	rig.tab.mu.Unlock()

	rig.browser.mu.Lock()
	assert.Equal(t, "session-1", cookieValue(rig.browser.made["listening:tiktok"], "sessionid"),
		"the account's tab is made in its own context with its cookies")
	rig.browser.mu.Unlock()
}

func cookieValue(jar []browser.Cookie, name string) string {
	for _, c := range jar {
		if c.Name == name {
			return c.Value
		}
	}

	return ""
}

func TestTikTokEmptyBodyRefusesTheAccountTellsOnceAndYtDlpServesMeanwhile(t *testing.T) {
	rig := newTikTokRig(t)
	rig.signIn(t)

	rig.tab.answer = func(string) map[string]any { return map[string]any{"status": 200, "body": "  "} }

	found := "https://www.tiktok.com/@viewer.one/video/" + tiktokID(start.Add(-72*time.Hour), 7)
	rig.yt.videos[found] = map[string]any{
		"id": tiktokID(start.Add(-72*time.Hour), 7), "description": "VETC trừ tiền hai lần", "uploader": "viewer.one",
		"timestamp": start.Add(-72 * time.Hour).Unix(), "webpage_url": found, "view_count": 4000, "like_count": 80,
		"comment_count": 12, "repost_count": 3,
	}

	a := rig.search(t)

	cov := coverageOf(t, a, listening.SourceTikTok)
	assert.Equal(t, listening.BackendYtDlp, cov.Streams[0].ServedBy, "the next free backend serves meanwhile")
	assert.Equal(t, string(listening.CodeRefused), cov.Streams[0].Tried[0].Result)
	require.Len(t, a.Items, 1)
	assert.Equal(t, found, a.Items[0].URL)
	assert.InDelta(t, 4000, a.Items[0].Engagement["views"], 0)
	assert.Equal(t, []string{found}, rig.yt.Asked(), "a video the web search dates outside the window is not read")

	st := stateOf(t, rig.svc, listening.SourceTikTok)
	assert.Equal(t, listening.AccountRefused, st.Signed.Account.State)
	require.Equal(t, 1, rig.told.Count())

	asked := len(rig.tab.fetched())
	_ = rig.search(t)
	assert.Len(t, rig.tab.fetched(), asked, "a refused account is not asked again until a new sign-in")
	assert.Equal(t, 1, rig.told.Count(), "the owner is told once")
}

func TestTikTokRateLimitOnCommentsKeepsTheVideosAsAPartRead(t *testing.T) {
	rig := newTikTokRig(t)
	rig.signIn(t)

	recent := start.Add(-24 * time.Hour)
	id := tiktokID(recent, 1)

	rig.tab.answer = func(address string) map[string]any {
		if strings.Contains(address, "/api/comment/list/") {
			return map[string]any{"status": 429, "retry_after": "120", "body": ""}
		}

		return tiktokBody(map[string]any{"status_code": 0, "item_list": []any{
			tiktokVideo(id, "top.one", "VETC thu phí sai", recent, "100"),
			tiktokVideo(tiktokID(recent, 2), "two.one", "VETC lần nữa", recent, "50"),
		}})
	}

	a := rig.search(t)

	cov := coverageOf(t, a, listening.SourceTikTok)
	assert.Equal(t, listening.BackendWebAPI, cov.Streams[0].ServedBy)
	assert.Equal(t, listening.CodeRateLimited, cov.Code, "a rate limit after the videos makes the read partial")
	assert.Len(t, a.Items, 2)
	assert.Len(t, rig.tab.fetched(), 2, "no other comment list is asked after the rate limit")
	assert.Equal(t, listening.AccountSignedIn, stateOf(t, rig.svc, listening.SourceTikTok).Signed.Account.State,
		"a rate limit is no refusal of the account")
}

func TestTikTokBarriersAndBrokenAnswersAreNotZeros(t *testing.T) {
	cases := []struct {
		name   string
		answer map[string]any
		land   string
		code   listening.Code
	}{
		{name: "sign-in page", land: "https://www.tiktok.com/login?redirect_url=x", code: listening.CodeNeedLogin},
		{name: "another site", land: "https://example.com/", code: listening.CodeBroken},
		{name: "401", answer: map[string]any{"status": 401, "body": ""}, code: listening.CodeNeedLogin},
		{name: "403", answer: map[string]any{"status": 403, "body": "denied"}, code: listening.CodeRefused},
		{name: "not JSON", answer: map[string]any{"status": 200, "body": "<html>"}, code: listening.CodeBroken},
		{name: "status code", answer: tiktokBody(map[string]any{"status_code": 10201, "status_msg": "x"}),
			code: listening.CodeBroken},
		{name: "no list", answer: tiktokBody(map[string]any{"status_code": 0}), code: listening.CodeBroken},
		{name: "more without a list", answer: tiktokBody(map[string]any{"status_code": 0, "has_more": 1}),
			code: listening.CodeBroken},
		{name: "fetch failed", answer: map[string]any{"status": 0, "error": "Failed to fetch"}, code: listening.CodeBroken},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newTikTokRig(t)
			rig.signIn(t)

			if c.land != "" {
				rig.tab.land = c.land
			} else {
				answer := c.answer
				rig.tab.answer = func(string) map[string]any { return answer }
			}

			a := rig.search(t)
			cov := coverageOf(t, a, listening.SourceTikTok)
			assert.Equal(t, string(c.code), cov.Streams[0].Tried[0].Result)
		})
	}
}

func TestTikTokWithoutYtDlpTheWebSearchCardsServe(t *testing.T) {
	rig := newTikTokRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceTikTok))
	rig.yt.failure = func(string) (string, error) { return "", exec.ErrNotFound }

	a := rig.search(t)

	cov := coverageOf(t, a, listening.SourceTikTok)
	require.Len(t, cov.Streams[0].Tried, 3)
	assert.Equal(t, string(listening.CodeNeedLogin), cov.Streams[0].Tried[0].Result, "no account is signed in")
	assert.Equal(t, string(listening.CodeBroken), cov.Streams[0].Tried[1].Result)
	assert.Contains(t, cov.Streams[0].Tried[1].Detail, "yt-dlp is not installed")
	assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].ServedBy)
	assert.Equal(t, []string{"site:tiktok.com VETC", "site:tiktok.com VETC"}, rig.webAsks)

	require.Len(t, a.Items, 1, "only a video's address is kept, and the old one is outside the window")
	assert.Equal(t, "viewer.one", a.Items[0].Author)
	assert.Equal(t, start.Add(-72*time.Hour), a.Items[0].PublishedAt.UTC(), "a card is dated by its video's id")
}
