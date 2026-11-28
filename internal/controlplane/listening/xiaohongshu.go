package listening

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Xiaohongshu read as the owner's account (issue #1362), as xpzouying/xiaohongshu-mcp reads it (Apache-2.0; adapted
// parts noticed in third_party/xiaohongshu-mcp):
//
//   - its search page, search_result?keyword=…&source=web_explore_feed, in the account's tab (xiaohongshu/search.go:
//     106-126 and 316-325), waited for until the page's state is this keyword's and its feeds are there, or empty
//     beside a search answer that says there is no more, or a failure (search_ready.go:107-215, searchReadinessJS);
//   - a block read from where the page landed, its path only: /website-login/captcha is a verification, so the account
//     is refused, any other /website-login a sign-in asked (page_check.go:13-49), read at every look and again before
//     the feeds are read (search.go:128-133); a page whose state says no one is signed in asks for a sign-in too
//     (last30days-skill-cn crawler_bridge.py:524-534, the web_session a visitor has too, :73-76);
//   - the window as the 发布时间 filter, 一天内, 一周内 or 半年内 when one holds it (search.go:24-55), chosen in the
//     page's filter panel by hovering its button and clicking the option found by its group's and its own text
//     (search.go:135-176, findFilterOption :258-314), then waiting for the feeds to change (waitFeedsChanged :222-239).
//     Departure: no humanized pointer path or random delay (humanize/): pacing "must not imitate random human behavior
//     or bypass bot detection" (trend-opportunity-radar collection-pacing-contract.md:6);
//   - the feeds read from the page's state, notes only (search.go:178-204, types.go:31-50), each at its explore address
//     with its xsec_token (last30days-skill-cn xiaohongshu.py:122-129); a search card has no date, so a note is
//     undated and the filter bounds it;
//   - the search answer's own statuses and codes: HTTP 461 and 471 its captcha, code 300012 an IP risk and 300011 an
//     account security block, each a refusal (MediaCrawler media_platform/xhs/client.py:78-80, 158-164 and 179-183, read only),
//     429 a rate limit.
//
// Departure from the issue's table: xiaohongshu-mcp serves its search to other programs as POST /api/v1/feeds/search
// (routes.go:47-57), which last30days-skill-cn calls (xiaohongshu.py:132-171). That route is the server's own; here
// the browser flow behind it (search.go) is rewritten in Go and run in the account's browser context, so no
// xiaohongshu-mcp server runs beside the control plane.

const (
	xiaohongshuURL = "https://www.xiaohongshu.com"
	// xiaohongshuLoad is how long the search page is waited for, its page's own bound (search.go:101, 60 s), looked at
	// every pagePoll; xiaohongshuFilterWait how long the feeds are waited for to change after a filter
	// (search.go:175, 15 s).
	xiaohongshuLoad       = 60 * time.Second
	xiaohongshuFilterWait = 15 * time.Second
	// xiaohongshuBodyLooks is how many looks, pagePoll apart, a search's body not loaded yet is waited for before a
	// sign-in landing is trusted. Inferred: no reference bounds it.
	xiaohongshuBodyLooks = 5
)

// xiaohongshuRefused are the search answer's captcha statuses (MediaCrawler xhs/client.py:158-164, read only).
var xiaohongshuRefused = []int{461, 471}

// xiaohongshuNoteURL is a note's address, with its xsec_token when the card gave one (xiaohongshu.py:122-129).
func xiaohongshuNoteURL(id, xsec string) string {
	base := xiaohongshuURL + "/explore/" + id
	if xsec == "" {
		return base
	}

	return base + "?" + url.Values{"xsec_token": {xsec}, "xsec_source": {"pc_search"}}.Encode()
}

// xiaohongshuSearchURL is the search page (search.go:316-325).
func xiaohongshuSearchURL(keyword string) string {
	return xiaohongshuURL + "/search_result?" + url.Values{"keyword": {keyword}, "source": {"web_explore_feed"}}.Encode()
}

// xiaohongshuBlocked classifies a landing path (page_check.go:22-37): the captcha's path refuses the account, any other
// /website-login asks for a sign-in; nil when it is neither.
func xiaohongshuBlocked(path string) *SourceError {
	const captcha, login = "/website-login/captcha", "/website-login"

	switch {
	case path == captcha, strings.HasPrefix(path, captcha+"/"):
		return Fail(CodeRefused, "Xiaohongshu sent the account to its security check (%s)", captcha)
	case path == login, strings.HasPrefix(path, login+"/"):
		return Fail(CodeNeedLogin, "Xiaohongshu sent the account to its sign-in (%s)", login)
	}

	return nil
}

// xiaohongshuReadiness reads the page at once: where it stands, whether its state is this keyword's search, ready,
// empty or failed (searchReadinessJS, search_ready.go:142-160), whether its state says someone is signed in
// (below), and the feeds' ids (feedIDsJS, search.go:207-212).
//
// Signed in is any of the references' signs: the state's loggedIn flag, or its userInfo naming a user who is no guest
// (last30days-skill-cn crawler_bridge.py:524-533), or the signed-in user's link in the page's side bar, the element
// xiaohongshu-mcp looks for (login.go:20-36). A flag of false says no one is signed in. Inferred, under #781's rule:
// neither reference combines the two, and neither says which wins when they disagree; any positive sign is taken,
// and a verification keeps the cookies only beside one.
const xiaohongshuReadiness = `((keyword) => {
  const path = location.pathname, host = location.hostname;
  const unwrap = v => v?.value !== undefined ? v.value : (v?._value !== undefined ? v._value : v);
  const s = window.__INITIAL_STATE__;
  const flag = unwrap(s?.user?.loggedIn), info = unwrap(s?.user?.userInfo);
  const signedIn = flag === true || !!(info && (info.user_id || info.userId) && !info.guest) ||
    !!document.querySelector('.main-container .user .link-wrapper .channel');
  const out = { path, host, logged_in: signedIn ? 1 : (flag === false ? 0 : -1) };
  const search = s?.search;
  const current = unwrap(search?.searchContext)?.keyword;
  const urlKeyword = new URL(location.href).searchParams.get('keyword');
  if (current !== undefined ? current !== keyword : urlKeyword !== keyword) return out;
  const feeds = unwrap(search?.feeds), state = unwrap(search?.state);
  out.ours = true;
  out.ready = Array.isArray(feeds) && feeds.length > 0 && state !== 'loading' && state !== 'error';
  out.empty = Array.isArray(feeds) && feeds.length === 0 && state !== 'loading';
  out.failed = state === 'error';
  out.ids = Array.isArray(feeds) ? feeds.map(x => x.id).join(',') : '';
  return out;
})`

type xiaohongshuState struct {
	Path     string `json:"path"`
	Host     string `json:"host"`
	LoggedIn int    `json:"logged_in"`
	Ours     bool   `json:"ours"`
	Ready    bool   `json:"ready"`
	Empty    bool   `json:"empty"`
	Failed   bool   `json:"failed"`
	IDs      string `json:"ids"`
}

// xiaohongshuFeedsScript reads the feeds from the page's state (search.go:178-189).
const xiaohongshuFeedsScript = `(() => {
  const f = window.__INITIAL_STATE__?.search?.feeds;
  const v = f ? (f.value !== undefined ? f.value : f._value) : null;
  return v ? JSON.stringify(v) : '';
})()`

// xiaohongshuFeed is a search card (types.go:23-29, 52-79).
type xiaohongshuFeed struct {
	ID        string `json:"id"`
	XsecToken string `json:"xsecToken"`
	ModelType string `json:"modelType"`
	NoteCard  struct {
		Type         string `json:"type"`
		DisplayTitle string `json:"displayTitle"`
		User         struct {
			Nickname string `json:"nickname"`
			NickName string `json:"nickName"`
		} `json:"user"`
		InteractInfo struct {
			LikedCount     jsonCount `json:"likedCount"`
			CollectedCount jsonCount `json:"collectedCount"`
			CommentCount   jsonCount `json:"commentCount"`
			SharedCount    jsonCount `json:"sharedCount"`
		} `json:"interactInfo"`
	} `json:"noteCard"`
}

// xiaohongshuPublishTime is the window as the 发布时间 filter's option; empty when none holds it, 不限 being the page's
// own (search.go:49-50).
func xiaohongshuPublishTime(from, to time.Time) string {
	if from.IsZero() {
		return ""
	}

	switch days := to.Sub(from).Hours() / 24; {
	case days <= 1:
		return "一天内"
	case days <= 7:
		return "一周内"
	case days <= 183:
		return "半年内"
	default:
		return ""
	}
}

func xiaohongshuPageBackend() Backend {
	return sessionBackend(Backend{
		Name: BackendXiaohongshuPage, Host: "xiaohongshu.com",
		Search: signedPageSearch(xiaohongshuPageRead),
	})
}

func xiaohongshuSearchXHR(u *url.URL) bool {
	return xiaohongshuSite.Owns(u.Hostname()) &&
		(u.Path == "/api/sns/web/v1/search/notes" || u.Path == "/api/sns/web/v2/search/notes")
}

// xiaohongshuAnswer is the search answer's parts read (search_ready.go:107-135 parseSearchResponse).
type xiaohongshuAnswer struct {
	Code    *int   `json:"code"`
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Data    *struct {
		Items   []json.RawMessage `json:"items"`
		HasMore *bool             `json:"has_more"`
	} `json:"data"`
}

// xiaohongshuSearched is what the page's search answers said: empty when one confirmed no result, failed with what one
// met.
type xiaohongshuSearched struct {
	empty  bool
	failed *SourceError
	// pending: a search was answered 200 and its body has not loaded yet.
	pending bool
}

// read reads the search answers the page received since its last look (search_ready.go:57-135): a captcha status,
// a rate limit, a refusing code or a failed answer is kept, and an answer with no item and has_more false confirms an
// empty search.
func (s *xiaohongshuSearched) read(ctx context.Context, page Page, seen map[string]bool, now time.Time) {
	got, pending := pageXHRsPending(ctx, page, xiaohongshuSearchXHR, seen)
	s.pending = pending > 0

	for _, x := range got {
		if x.Status != http.StatusOK {
			s.empty = false

			switch {
			case x.Status == xiaohongshuRefused[0] || x.Status == xiaohongshuRefused[1]:
				s.failed = Fail(CodeRefused, "Xiaohongshu answered HTTP %d to its search, its captcha", x.Status)
			default:
				s.failed = HTTPFailure(x.Status, nil, now)
				s.failed.Detail = "Xiaohongshu's search answered " + s.failed.Detail
			}

			continue
		}

		var a xiaohongshuAnswer
		if err := json.Unmarshal(x.Body, &a); err != nil {
			s.failed = Fail(CodeBroken, "Xiaohongshu's search answered what is not its JSON: %s", err.Error())

			continue
		}

		switch {
		case a.Code != nil && *a.Code == 300012:
			s.failed = Fail(CodeRefused, "Xiaohongshu answered code 300012: it sees an IP risk")
		case a.Code != nil && *a.Code == 300011:
			s.failed = Fail(CodeRefused, "Xiaohongshu answered code 300011: the account's security is restricted")
		case a.Code == nil || !a.Success || *a.Code != 0:
			code := "none"
			if a.Code != nil {
				code = fmt.Sprint(*a.Code)
			}

			s.failed = Fail(CodeBroken, "Xiaohongshu's search answered code %s: %s", code, oneLine(a.Msg))
		case a.Data == nil:
			s.failed = Fail(CodeBroken, "Xiaohongshu's search answered no data")
		default:
			// A new answer replaces what an earlier one said.
			s.failed = nil
			s.empty = len(a.Data.Items) == 0 && a.Data.HasMore != nil && !*a.Data.HasMore
		}
	}
}

// xiaohongshuPageRead searches in the account's tab, filters it to the window, and reads its notes. The search and
// the filter's search are each a read in a turn of their own (pageStep; radar collection-pacing-contract.md:7-10, the
// filter's click sends Xiaohongshu a search; review round 1 of PR #1364).
func xiaohongshuPageRead(ctx context.Context, r Request, page Page) (Result, error) {
	var (
		searched xiaohongshuSearched
		seen     = map[string]bool{}
		st       xiaohongshuState
		res      Result
		filter   = xiaohongshuPublishTime(r.From, r.To)
	)

	// look reads the page once. What the page's own requests answered is read before where it landed: a search answered
	// 300012 or 429 and then sent to a sign-in path is that refusal or that rate limit, not a sign-in asked
	// (review round 1 of PR #1364; Agent-Reach #1011, 300012 beside the redirect).
	look := func() (xiaohongshuState, *SourceError) {
		var got xiaohongshuState

		if page.Evaluate(ctx, xiaohongshuReadiness+"("+jsArgs(r.Query)+")", &got) != nil {
			// A navigation makes the page's context go for a moment (search_ready.go:166-185): looked at again.
			return xiaohongshuState{}, nil //nolint:nilerr // Not a failure of the read: the next look reads it.
		}

		for tries := 1; ; tries++ {
			searched.read(ctx, page, seen, r.now())

			if f := siteThrottled(ctx, page, xiaohongshuSite, r.now(), xiaohongshuRefused...); f != nil {
				return got, f
			}

			if f := searched.failed; f != nil && f.Code != CodeBroken {
				return got, f
			}

			f := xiaohongshuLanding(got)
			if f == nil {
				return got, nil
			}

			// A sign-in landing beside a search answered 200 whose body has not loaded is not trusted yet: the body
			// may hold 300012 or 300011 (review round 2 of PR #1364), so it is waited for, as xiaohongshu-mcp reads an
			// answer once it has loaded (search_ready.go:61-84). How long, xiaohongshuBodyLooks, is inferred.
			if f.Code != CodeNeedLogin || !searched.pending || tries >= xiaohongshuBodyLooks {
				return got, f
			}

			if err := r.wait(ctx, pagePoll); err != nil {
				return got, AsFailure(err)
			}
		}
	}

	// finish looks once more before the feeds are read, a block may land in between (search.go:128-133), and reads
	// them: a clean zero when the page is empty beside an answer that says there is no more.
	finish := func() *SourceError {
		got, f := look()
		if f != nil {
			return f
		}

		if got.Empty && searched.empty {
			res.Detail = "Xiaohongshu's search has no note"
			if filter != "" {
				res.Detail += " published " + filter
			}

			return nil
		}

		items, f := xiaohongshuFeeds(ctx, page)
		if f != nil {
			return f
		}

		res.Items = items[:min(len(items), r.Limit)]

		res.Detail = fmt.Sprintf("Xiaohongshu's search showed %d notes", len(res.Items))
		if filter != "" {
			res.Detail += ", published " + filter
		}

		return nil
	}

	f := r.pageStep(ctx, "xiaohongshu.com", xiaohongshuCadence, func() *SourceError {
		if err := page.Navigate(ctx, xiaohongshuSearchURL(r.Query)); err != nil {
			return Fail(CodeBroken, "opening Xiaohongshu's search: %s", err.Error())
		}

		var err error

		st, err = xiaohongshuWait(ctx, r, look, func(st xiaohongshuState) bool {
			return st.Ready || st.Empty && searched.empty || st.Failed && searched.failed != nil
		}, xiaohongshuLoad)
		if err != nil {
			return AsFailure(err)
		}

		// A verification keeps the cookies only when the page says an account is signed in, as xiaohongshu-mcp checks
		// for the signed-in user before it reads (login.go:20-36): a visitor has a web_session too.
		if r.Session.Verifying() && st.LoggedIn != 1 {
			return Fail(CodeNeedLogin, "Xiaohongshu's page does not say the account is signed in")
		}

		switch {
		case st.Failed && searched.failed != nil:
			return searched.failed
		case !st.Ready && !(st.Empty && searched.empty):
			return Fail(CodeBroken, "Xiaohongshu's search page did not show this search's notes in time")
		case !st.Ready:
			res.Detail = "Xiaohongshu's search has no note"

			return nil
		case filter == "":
			return finish()
		}

		return nil
	})
	if f != nil || !st.Ready || filter == "" {
		if f != nil {
			return Result{}, f
		}

		return res, nil
	}

	f = r.pageStep(ctx, "xiaohongshu.com", xiaohongshuCadence, func() *SourceError {
		// What the tab logged during the wait for this turn, read before the filter's search is sent.
		if _, f := look(); f != nil {
			return f
		}

		before := st.IDs
		searched.empty = false

		if err := xiaohongshuFilter(ctx, r, page, filter, look, func(st xiaohongshuState) bool {
			return st.IDs != "" && st.IDs != before || st.Empty && searched.empty
		}); err != nil {
			return AsFailure(err)
		}

		return finish()
	})
	if f != nil {
		return Result{}, f
	}

	return res, nil
}

// xiaohongshuFeeds reads the notes from the page's state (search.go:178-204).
func xiaohongshuFeeds(ctx context.Context, page Page) ([]Item, *SourceError) {
	var raw string
	if err := page.Evaluate(ctx, xiaohongshuFeedsScript, &raw); err != nil {
		return nil, Fail(CodeBroken, "reading Xiaohongshu's feeds: %s", err.Error())
	}

	if raw == "" {
		return nil, Fail(CodeBroken, "Xiaohongshu's page has no feeds in its state")
	}

	var feeds []xiaohongshuFeed
	if err := json.Unmarshal([]byte(raw), &feeds); err != nil {
		return nil, Fail(CodeBroken, "Xiaohongshu's feeds do not decode: %s", err.Error())
	}

	var items []Item

	for _, f := range feeds {
		if it, ok := f.item(); ok && !containsItem(items, it) {
			items = append(items, it)
		}
	}

	if len(items) == 0 {
		return nil, Fail(CodeBroken, "Xiaohongshu's feeds held %d entries and no note", len(feeds))
	}

	return items, nil
}

// xiaohongshuLanding is a block where the page landed: a /website-login path, a host that is not Xiaohongshu's (a
// visitor from zm is sent to rednote.com, research note §8), or a state that says no one is signed in.
func xiaohongshuLanding(st xiaohongshuState) *SourceError {
	if f := xiaohongshuBlocked(st.Path); f != nil {
		return f
	}

	switch {
	case st.Host != "" && !xiaohongshuSite.Owns(st.Host) && st.Host != "about:blank":
		return Fail(CodeNeedLogin, "Xiaohongshu sent the search to %s, as it sends a visitor", st.Host)
	case st.LoggedIn == 0:
		return Fail(CodeNeedLogin, "Xiaohongshu's page says no account is signed in: its web_session is a visitor's")
	}

	return nil
}

// xiaohongshuWait looks at the page every pagePoll until done says so, a look fails, or limit ends, counted in looks.
func xiaohongshuWait(ctx context.Context, r Request, look func() (xiaohongshuState, *SourceError),
	done func(xiaohongshuState) bool, limit time.Duration,
) (xiaohongshuState, error) {
	var st xiaohongshuState

	for n := 1; n <= int(limit/pagePoll); n++ {
		var f *SourceError
		if st, f = look(); f != nil {
			return st, f
		}

		if done(st) {
			return st, nil
		}

		if err := r.wait(ctx, pagePoll); err != nil {
			return st, AsFailure(err)
		}
	}

	return st, nil
}

// xiaohongshuRect finds an element's center: the filter button, the panel, or an option of a group of the panel by
// its group's label and its own text, the visible one (findFilterOption, search.go:258-314).
const xiaohongshuRect = `((what, group, option) => {
  const center = el => { const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 ? { x: r.left + r.width / 2, y: r.top + r.height / 2, found: true } : null; };
  if (what === 'button') {
    const b = document.querySelector('div.filter'); return (b && center(b)) || { found: false };
  }
  if (what === 'panel') return { found: !!document.querySelector('div.filter-panel') };
  for (const g of document.querySelectorAll('div.filter-panel div.filters')) {
    const label = g.querySelector(':scope > span');
    if (!label || label.textContent.trim() !== group) continue;
    for (const o of g.querySelectorAll('div.tags')) {
      if (o.textContent.trim() !== option) continue;
      const c = center(o); if (c) return c;
    }
    return { found: false, why: 'no visible option' };
  }
  return { found: false, why: 'no group' };
})`

type xiaohongshuPoint struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Found bool    `json:"found"`
	Why   string  `json:"why"`
}

// xiaohongshuFilter chooses the 发布时间 option in the filter panel and waits for the feeds to change; a feed that does
// not change in time is read as it is, as waitFeedsChanged leaves it (search.go:222-239); done says they changed, or
// the filtered search was answered empty.
func xiaohongshuFilter(ctx context.Context, r Request, page Page, option string,
	look func() (xiaohongshuState, *SourceError), done func(xiaohongshuState) bool,
) error {
	find := func(what string) (xiaohongshuPoint, error) {
		var p xiaohongshuPoint

		if err := page.Evaluate(ctx, xiaohongshuRect+"("+jsArgs(what, "发布时间", option)+")", &p); err != nil {
			return p, Fail(CodeBroken, "reading Xiaohongshu's filter: %s", err.Error())
		}

		return p, nil
	}

	button, err := find("button")
	if err != nil {
		return err
	}

	if !button.Found {
		return Fail(CodeBroken, "Xiaohongshu's search page has no filter button: the page may have changed")
	}

	if err := page.Hover(ctx, button.X, button.Y); err != nil {
		return Fail(CodeBroken, "hovering Xiaohongshu's filter: %s", err.Error())
	}

	var opt xiaohongshuPoint

	for n := 1; ; n++ {
		if opt, err = find("option"); err != nil {
			return err
		}

		if opt.Found || n >= int(pageLoad/pagePoll) {
			break
		}

		if err := r.wait(ctx, pagePoll); err != nil {
			return AsFailure(err)
		}
	}

	if !opt.Found {
		return Fail(CodeBroken, "Xiaohongshu's filter panel has no 发布时间 option %s (%s): the page may have changed",
			option, opt.Why)
	}

	if err := page.Click(ctx, opt.X, opt.Y); err != nil {
		return Fail(CodeBroken, "choosing Xiaohongshu's filter: %s", err.Error())
	}

	_, err = xiaohongshuWait(ctx, r, look, done, xiaohongshuFilterWait)

	return err
}

// item is a note as an Item (xiaohongshu.py:192-230 parse_mcp_feed): notes only (types.go:41-50).
func (f xiaohongshuFeed) item() (Item, bool) {
	if f.ModelType != "note" || f.ID == "" || !xiaohongshuPostURL.MatchString("/explore/"+f.ID) {
		return Item{}, false
	}

	n := f.NoteCard

	return Item{
		NativeID: f.ID, URL: xiaohongshuNoteURL(f.ID, f.XsecToken), Title: strings.TrimSpace(n.DisplayTitle),
		Author: cmpOr(n.User.Nickname, n.User.NickName), Direct: true, Engagement: map[string]float64{
			"likes": float64(n.InteractInfo.LikedCount), "collects": float64(n.InteractInfo.CollectedCount),
			"comments": float64(n.InteractInfo.CommentCount), "shares": float64(n.InteractInfo.SharedCount),
		},
	}, true
}
