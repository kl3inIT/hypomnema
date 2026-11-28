package listening

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
)

// Weibo read as the owner's account (issue #1362), as last30days-skill-cn reads it (weibo.py:1-17): m.weibo.cn's
// mobile search, container 100103type=1&q=<query>, two pages at its default depth (weibo.py:65, 181-199), with the
// account's cookie and a phone's headers as XMLHttpRequest (weibo.py:151-159); else the same search asked by
// m.weibo.cn's own page in the account's tab (crawler_bridge.py:612-650). An answer of ok -100, or one that names the
// passport or sends there, is the session's end (weibo.py:193-194; the issue: "302 to the visitor passport"). Weibo
// gives a visitor a SUB cookie too, so a sign-in's verification first asks /api/config whether the account is signed
// in, as MediaCrawler's pong does (media_platform/weibo/client.py:118-129, read only) and crawler_bridge.py:621-629.
// The open API's token and the hot search of weibo.py:70-113 are not read.

const (
	weiboMobileURL = "https://m.weibo.cn"
	// weiboPages is how many pages of the search are read (weibo.py:65, default 2).
	weiboPages = 2
)

// weiboAnswer is getIndex's answer, the parts read (weibo.py:202-211 parse_mobile_cards).
type weiboAnswer struct {
	OK   *int   `json:"ok"`
	URL  string `json:"url"`
	Msg  string `json:"msg"`
	Data struct {
		Cards []struct {
			CardType  int          `json:"card_type"`
			Mblog     *weiboMblog  `json:"mblog"`
			CardGroup []weiboGroup `json:"card_group"`
		} `json:"cards"`
	} `json:"data"`
}

type weiboGroup struct {
	Mblog *weiboMblog `json:"mblog"`
}

// weiboMblog is one post (weibo.py:307-325 _parse_mblog).
type weiboMblog struct {
	ID        jsonID    `json:"id"`
	Mid       jsonID    `json:"mid"`
	Bid       string    `json:"bid"`
	Text      string    `json:"text"`
	CreatedAt string    `json:"created_at"`
	Reposts   jsonCount `json:"reposts_count"`
	Comments  jsonCount `json:"comments_count"`
	Likes     jsonCount `json:"attitudes_count"`
	User      *struct {
		ID         jsonID `json:"id"`
		ScreenName string `json:"screen_name"`
	} `json:"user"`
}

// weiboParams are a page's query of the search (weibo.py:184-189).
func weiboParams(query string, page int) url.Values {
	v := url.Values{"containerid": {"100103type=1&q=" + query}, "page_type": {"searchall"}}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}

	return v
}

// weiboHeaders are a phone's, asking JSON as XMLHttpRequest, with the account's cookie (weibo.py:151-156).
func weiboHeaders(jar []browser.Cookie) http.Header {
	h := browserHeaders(weiboMobileURL+"/", true)
	h.Set("User-Agent", mobileUA)
	h.Set("Sec-Ch-Ua-Mobile", "?1")
	h.Set("Sec-Ch-Ua-Platform", `"iOS"`)
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("Cookie", weiboSite.Header(jar, "m.weibo.cn"))

	return h
}

// weiboAPIBackend is the mobile search with the account's cookie, a page a turn.
func weiboAPIBackend(client *http.Client) Backend {
	page := func(ctx context.Context, r Request, jar []browser.Cookie, address string) (answer, error) {
		a, err := call{address: address, header: weiboHeaders(jar), json: true, redirect: true,
			userAgent: mobileUA}.do(ctx, client, r)
		if err != nil {
			return a, err
		}

		if a.location != "" {
			return a, weiboRedirect(a.location)
		}

		return a, nil
	}

	// read is one request in a turn of its own, what it met kept on the account before the turn ends: each request
	// counts as a read (trend-opportunity-radar collection-pacing-contract.md:9).
	read := func(ctx context.Context, r Request, jar []browser.Cookie, address string, parse func([]byte) error) error {
		end, err := r.turn(ctx, "weibo.com", chineseCadence)
		if err != nil {
			return err
		}

		a, err := page(ctx, r, jar, address)
		if err == nil {
			err = parse(a.body)
		}

		end(failureOf(err))

		return err
	}

	search := func(ctx context.Context, r Request) (Result, error) {
		jar, err := r.Session.Jar(ctx)
		if err != nil {
			return Result{}, err
		}

		if missing := weiboSite.Missing(jar); missing != "" {
			return Result{}, Fail(CodeNeedLogin, "the kept Weibo session lacks %s", missing)
		}

		if r.Session.Verifying() {
			// The visitor's SUB is no sign-in: Weibo must say the account is signed in first.
			err := read(ctx, r, jar, weiboMobileURL+"/api/config", func(body []byte) error {
				if f := weiboSignedIn(body); f != nil {
					return f
				}

				return nil
			})
			if err != nil {
				return Result{}, err
			}
		}

		var res Result

		// The pages, until one is empty (weibo.py:186-198); a failure after the first keeps what was read.
		for n := 1; n <= weiboPages && len(res.Items) < r.Limit; n++ {
			var items []Item

			err := read(ctx, r, jar, weiboMobileURL+"/api/container/getIndex?"+weiboParams(r.Query, n).Encode(),
				func(body []byte) error {
					var err error

					items, err = weiboRead(body, r.now())

					return err
				})
			if err != nil && n == 1 {
				return Result{}, err
			}

			if err != nil {
				res.Failed = AsFailure(err)

				break
			}

			if len(items) == 0 {
				break
			}

			res.Items = appendNew(res.Items, items)
		}

		if len(res.Items) == 0 && res.Failed == nil {
			return Result{Detail: "Weibo's mobile search has no result"}, nil
		}

		res.Detail = fmt.Sprintf("Weibo's mobile search read %d posts as the account", len(res.Items))

		return res, nil
	}

	return sessionBackend(Backend{Name: BackendWeiboAPI, Host: "weibo.com", Search: search})
}

func appendNew(items, more []Item) []Item {
	for _, it := range more {
		if !containsItem(items, it) {
			items = append(items, it)
		}
	}

	return items
}

// weiboRedirect is where Weibo sent a search: the passport is a sign-in asked.
func weiboRedirect(location string) *SourceError {
	if u, err := url.Parse(location); err == nil && strings.Contains(u.Hostname(), "passport.") {
		return Fail(CodeNeedLogin, "Weibo sent the search to %s, its sign-in", u.Hostname())
	}

	return Fail(CodeBroken, "Weibo redirected the search to %s", hostOf(location))
}

// weiboSignedIn reads /api/config: nil when it says the account is signed in (MediaCrawler weibo/client.py:122-124,
// read only; crawler_bridge.py:621-624).
func weiboSignedIn(body []byte) *SourceError {
	var c struct {
		Data struct {
			Login bool `json:"login"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &c); err != nil {
		return Fail(CodeBroken, "Weibo's /api/config answered what is not its JSON: %s", err.Error())
	}

	if !c.Data.Login {
		return Fail(CodeNeedLogin, "Weibo says the account is not signed in: its SUB cookie is a visitor's")
	}

	return nil
}

// weiboRead reads one getIndex answer: its posts; ok -100 or the passport named is the session's end
// (weibo.py:193-194).
func weiboRead(body []byte, now time.Time) ([]Item, error) {
	var a weiboAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, Fail(CodeBroken, "Weibo's search answered what is not its JSON: %s", err.Error())
	}

	if a.OK != nil && *a.OK == -100 || strings.Contains(a.URL, "passport.weibo.com") {
		return nil, Fail(CodeNeedLogin, "Weibo answered ok -100: the account's session ended")
	}

	switch {
	case a.OK == nil:
		return nil, Fail(CodeBroken, "Weibo's search answered no ok: %s", oneLine(a.Msg))
	case *a.OK == 0 && len(a.Data.Cards) == 0:
		// Inferred, no reference reads it: ok 0 with no card and its message is Weibo's answer to a search with no
		// result; last30days-skill-cn reads any answer but -100 for its cards (weibo.py:191-197).
		return nil, nil
	case *a.OK != 1:
		return nil, Fail(CodeBroken, "Weibo's search answered ok %d: %s", *a.OK, oneLine(a.Msg))
	}

	var items []Item

	add := func(m *weiboMblog) {
		if it, ok := m.item(now); ok && !containsItem(items, it) {
			items = append(items, it)
		}
	}

	for _, c := range a.Data.Cards {
		if c.CardType == 9 && c.Mblog != nil {
			add(c.Mblog)
		}

		for _, g := range c.CardGroup {
			if g.Mblog != nil {
				add(g.Mblog)
			}
		}
	}

	return items, nil
}

// item is a post as an Item, at weibo.com/<user>/<bid> (weibo.py:307-325).
func (m *weiboMblog) item(now time.Time) (Item, bool) {
	text := stripHTML(m.Text)
	mid := cmpOr(string(m.Mid), string(m.ID))

	if text == "" || mid == "" || m.User == nil || m.User.ID == "" {
		return Item{}, false
	}

	it := Item{
		NativeID: mid, URL: "https://weibo.com/" + string(m.User.ID) + "/" + cmpOr(m.Bid, mid), Text: text,
		Author: m.User.ScreenName, Direct: true, Engagement: map[string]float64{
			"reposts": float64(m.Reposts), "comments": float64(m.Comments), "likes": float64(m.Likes),
		},
	}

	if at, ok := weiboTime(m.CreatedAt, now); ok {
		it.PublishedAt = &at
	}

	return it, true
}

var (
	weiboMinutes = regexp.MustCompile(`(\d+)\s*分钟前`)
	weiboHours   = regexp.MustCompile(`(\d+)\s*小时前`)
	weiboYMD     = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})`)
	weiboMD      = regexp.MustCompile(`^(\d{1,2})-(\d{1,2})`)
)

// weiboTime is a post's created_at in any of the forms Weibo sends, read in Beijing time (weibo.py:347-379): its full
// date, 刚刚, minutes or hours ago, 昨天 and 前天, a date, or a month and day of this year or, past today, of the last.
func weiboTime(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}

	if at, err := time.Parse("Mon Jan 02 15:04:05 -0700 2006", s); err == nil {
		return at.UTC(), true
	}

	local := now.In(beijing)
	day := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, beijing).UTC()
	}

	switch {
	case strings.Contains(s, "刚刚"):
		return now.UTC(), true
	case weiboMinutes.MatchString(s):
		n, _ := strconv.Atoi(weiboMinutes.FindStringSubmatch(s)[1])

		return now.Add(-time.Duration(n) * time.Minute).UTC(), true
	case weiboHours.MatchString(s):
		n, _ := strconv.Atoi(weiboHours.FindStringSubmatch(s)[1])

		return now.Add(-time.Duration(n) * time.Hour).UTC(), true
	case strings.Contains(s, "前天"):
		return day(local.AddDate(0, 0, -2)), true
	case strings.Contains(s, "昨天"):
		return day(local.AddDate(0, 0, -1)), true
	}

	if m := weiboYMD.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])

		return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, beijing).UTC(), true
	}

	if m := weiboMD.FindStringSubmatch(s); m != nil {
		mo, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])

		y := local.Year()
		if mo > int(local.Month()) || mo == int(local.Month()) && d > local.Day() {
			y--
		}

		return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, beijing).UTC(), true
	}

	return time.Time{}, false
}

// weiboPageBackend is the same search asked by m.weibo.cn's own page in the account's tab: the page loaded, its
// /api/config asked whether the account is signed in, then getIndex fetched from the page, as crawler_bridge.py:612-650
// does.
func weiboPageBackend() Backend {
	return sessionBackend(Backend{
		Name: BackendWeiboPage, Host: "weibo.com", Search: signedPageSearch(weiboPageRead),
	})
}

// weiboFetch fetches one of m.weibo.cn's own addresses from its page, as XMLHttpRequest (crawler_bridge.py:621-640).
func weiboFetch(path string) string {
	return `(async () => { const r = await fetch(` + jsArgs(path) + `, {credentials: 'include', headers: ` +
		`{'X-Requested-With': 'XMLHttpRequest'}}); return { status: r.status, url: r.url, body: await r.text() }; })()`
}

type fetched struct {
	Status int    `json:"status"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// weiboPageRead loads m.weibo.cn and asks /api/config, then each page of the search, each request a read in a turn of
// its own (pageStep; radar collection-pacing-contract.md:9, "count … a separately requested … read as new requests";
// review round 1 of PR #1364), a throttle on any of the page's requests read after each.
func weiboPageRead(ctx context.Context, r Request, page Page) (Result, error) {
	ask := func(path string) (fetched, *SourceError) {
		var f fetched
		if err := page.Evaluate(ctx, weiboFetch(path), &f); err != nil {
			return f, Fail(CodeBroken, "m.weibo.cn's page could not ask %s: %s", path, err.Error())
		}

		if strings.Contains(f.URL, "passport.") {
			return f, Fail(CodeNeedLogin, "Weibo sent %s to its sign-in", path)
		}

		if f.Status != http.StatusOK {
			return f, HTTPFailure(f.Status, nil, r.now())
		}

		return f, nil
	}

	throttled := func() *SourceError { return siteThrottled(ctx, page, weiboSite, r.now()) }

	// The page, then /api/config: whether the account is signed in.
	f := r.pageStep(ctx, "weibo.com", chineseCadence, func() *SourceError {
		if err := page.Navigate(ctx, weiboMobileURL+"/"); err != nil {
			return Fail(CodeBroken, "opening m.weibo.cn: %s", err.Error())
		}

		for look := 1; ; look++ {
			l, err := landingOf(ctx, page)
			if err == nil && l.Ready == "complete" {
				if u, ok := landedAt(l.Href); ok && !weiboSite.Owns(u.Hostname()) {
					if f := pageBarrier(weiboSite, l); f != nil {
						return f
					}
				}

				break
			}

			if look >= int(pageLoad/pagePoll) {
				return Fail(CodeBroken, "m.weibo.cn did not load in time")
			}

			if err := r.wait(ctx, pagePoll); err != nil {
				return AsFailure(err)
			}
		}

		if f := throttled(); f != nil {
			return f
		}

		cfg, f := ask("/api/config")
		if f != nil {
			return f
		}

		return weiboSignedIn([]byte(cfg.Body))
	})
	if f != nil {
		return Result{}, f
	}

	var res Result

	for n := 1; n <= weiboPages && len(res.Items) < r.Limit; n++ {
		var items []Item

		f := r.pageStep(ctx, "weibo.com", chineseCadence, func() *SourceError {
			got, f := ask("/api/container/getIndex?" + weiboParams(r.Query, n).Encode())
			if f == nil {
				var err error
				if items, err = weiboRead([]byte(got.Body), r.now()); err != nil {
					f = AsFailure(err)
				}
			}

			if t := throttled(); t != nil {
				return t
			}

			return f
		})
		if f != nil {
			return partial(res, f)
		}

		if len(items) == 0 {
			break
		}

		res.Items = appendNew(res.Items, items)
	}

	if len(res.Items) == 0 {
		return Result{Detail: "m.weibo.cn's search has no result"}, nil
	}

	res.Detail = fmt.Sprintf("m.weibo.cn's page read %d posts as the account", len(res.Items))

	return res, nil
}
