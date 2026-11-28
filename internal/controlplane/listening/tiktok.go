package listening

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// TikTok (issue #1294): read first as the owner's signed-in account through TikTok's own web API, the way
// davidteather/TikTok-Api reads it (MIT; adapted parts noticed in third_party/tiktok-api), then, while the account
// cannot be read, by the videos the web search finds read with yt-dlp, then by those videos' search cards alone
// (research note §2: "davidteather/TikTok-Api …, then yt-dlp, then `site:tiktok.com` web search"; no paid route,
// owner 2026-11-27).
//
// Where its Python and browser run (the issue's third row): TikTok-Api is a Python library that drives a Playwright
// browser (TikTokApi/tiktok.py:520-548). The control plane provides no Python, and it already installs and runs a
// browser of its own, pinned Chrome for Testing, with a browser context for each signed-in account
// (internal/controlplane/browser, install.go; listening/accounts.go). TikTok-Api's mechanism is a browser page on
// tiktok.com whose own JavaScript signs the requests made from it, so that mechanism is run in that browser, in the
// account's own context, and TikTok-Api's Python is not: nothing is installed on a node, and nothing relies on one
// having Python.
//
//   - The account's tab is a page on https://www.tiktok.com (tiktok.py:314 and 368), kept for the account's reads as
//     TikTok-Api keeps one page per session (tiktok.py:396-404, run_fetch_script :716-742), and opened again when it
//     is not on TikTok or TikTok's web client has not loaded; the client is loaded once window.byted_acrawler is
//     defined, waited for up to 20 seconds, TikTok-Api's longest wait (tiktok.py:752-760).
//   - A request is a fetch from that page, its answer read as text (generate_js_fetch, tiktok.py:658-670), with the
//     session's parameters TikTok-Api sends (__set_session_params, tiktok.py:138-181) and none of msToken, X-Bogus or
//     X-Gnarly: TikTok's web client now signs a request made from its page itself, and a second signature makes it
//     answer an empty body (TikTok-Api issue #1311, 2026-07-18: X-Gnarly superseded X-Bogus and "those calls return
//     200 with an empty body"; its fix, pull request #1310, open since 2026-07-17, drops those parameters and "lets
//     TikTok's native system attach fresh parameters"). Departure: TikTok-Api's released make_request still signs the
//     address with frontierSign (tiktok.py:871-872); its open fix is followed, for the failure its issues report.
//   - Search is /api/search/item/full/ with the keyword, cursor 0, from_page "search" and web_search_code
//     (TikTokApi/api/search.py:75-86), its videos item_list (:106-108); item search needs a signed-in session
//     (search.py:52, "other endpoints require auth"; trend-opportunity-radar opencli-orchestration.md:7: "a
//     user-authorized, already logged-in browser session as the required environment for TikTok"). One result page a
//     search, never the next (radar collection-pacing-contract.md:10, "start with one result screen").
//   - A video is its id, createTime, author and stats, statsV2 first (TikTokApi/api/video.py:228-247), its address
//     https://www.tiktok.com/@<author>/video/<id> (last30days shortform_oss.py:186-195); its counts are views, likes,
//     comments and shares (last30days tiktok.py:181-192).
//   - Comments are /api/comment/list/ with aweme_id, count 20 and cursor 0 (video.py:258-303), each with cid, text,
//     user and digg_count (TikTokApi/api/comment.py:43-52); top-level only, the list's, never replies (comment.py:54-70
//     reads those apart); five at most of each of the three most watched videos (radar browser-collection.md:41, "at
//     most five visible comment bodies"; last30days' three videos, as YouTube's here), the most liked first and the
//     video's own author left out (last30days _normalize_comments on the same endpoint, tiktok.py:695-780, its
//     _PUBLIC_COMMENTS_URL; it sorts by digg_count, :746-751). A comment's date is its create_time (tiktok.py:761-767).
//   - An empty body is TikTok refusing the session as a bot (TikTok-Api README.md:110, "this means TikTok is blocking
//     the request and detects you're a bot"; tiktok.py:885-889, raised without a retry; MediaCrawler, read only,
//     treats Douyin's empty answer as "account blocked"): the account is refused until the owner signs in again
//     (radar collection-pacing-contract.md:12, "stop immediately on captcha, rate limit, login expiry … Waiting longer
//     is not permission to retry a safety stop"). A CAPTCHA is never solved: TikTok-Api has no solver either
//     (exceptions.py:14-15 names one and raises it nowhere). An HTTP status is read as every source reads one
//     (HTTPFailure: 401 a sign-in, 403 a refusal, 429 a rate limit until its Retry-After), an answer that is not JSON
//     or whose status_code is not 0 is broken (tiktok.py:891-899), and the page landing on TikTok's sign-in is the
//     session's end (radar :12, "login expiry").
//   - One request at a time, 12 seconds apart and 30 seconds more after every five, each comment list a request of its
//     own (radar collection-pacing-contract.md:5-9: "TikTok pilot search 12 seconds"; "count … a separately requested
//     comment read as new requests").

// The TikTok source's and its backend's names.
const (
	SourceTikTok  = "tiktok"
	BackendWebAPI = "web_api"
)

const (
	tiktokURL = "https://www.tiktok.com"
	// tiktokHost is where the account's requests and yt-dlp's go, one at a time (pace.go).
	tiktokHost = "tiktok.com"
	// tiktokYtDlpHost is the yt-dlp route's host as the search stops one: TikTok refusing the account, or asking it to
	// sign in, is the account's, and the route without one is still asked, the owner's "next free backend meanwhile"
	// of issue #1291 (accounts.go); its calls are paced at tiktokHost with the account's.
	tiktokYtDlpHost = "yt-dlp:tiktok.com"
	// tiktokLoad is how long TikTok's web client is waited for (tiktok.py:757, the longest of its waits); tiktokPoll
	// how often the page is looked at meanwhile (inferred, as linkedInPoll).
	tiktokLoad = 20 * time.Second
	tiktokPoll = time.Second
	// tiktokCommented videos, the most watched, get tiktokComments comments each (radar browser-collection.md:41; last30days
	// enrich_with_comments' three videos); tiktokCommentPage is the list's page (video.py:280).
	tiktokCommented   = 3
	tiktokComments    = 5
	tiktokCommentPage = 20
	// tiktokURLs is how many videos the web search found are read with yt-dlp (last30days shortform_oss._DEPTH_URLS,
	// its default depth), one yt-dlp run each (_ENRICH_BATCH, "so a stalled post loses only itself"), a run bounded by
	// _ENRICH_TIMEOUT and its socket by 25 seconds (shortform_oss.py:41-46 and 560-571).
	tiktokURLs         = 12
	tiktokYtDlpTimeout = 180 * time.Second
	// tiktokWebSearchCode is the search's web_search_code (search.py:79).
	tiktokWebSearchCode = `{"tiktok":{"client_params_x":{"search_engine":{"ies_mt_user_live_video_card_use_libra":1,` +
		`"mt_search_general_user_live_card":1}},"search_server":{}}}`
	// tiktokTitle bounds the line of a video's caption its comments name as where they were said (inferred).
	tiktokTitle = 100
)

// tiktokCadence is TikTok's pace as an account (radar collection-pacing-contract.md:7-8).
var tiktokCadence = Cadence{Gap: 12 * time.Second, Every: 5, Cooldown: 30 * time.Second}

// tiktokSite is TikTok as the shared sign-in knows it: its sign-in page and the session cookies only a signed-in
// account has, sessionid, sid_tt or uid_tt, as OpenCLI checks a TikTok session (jackwener/OpenCLI, Apache-2.0,
// clis/tiktok/auth.js:4-8 and 60), and the msToken cookie TikTok-Api needs (README.md:129, "get your own ms_token
// from your cookies on tiktok.com"; tiktok.py:329-332).
var tiktokSite = signin.Site{
	Name: "TikTok", URL: tiktokURL + "/login", Domains: []string{"tiktok.com"},
	Login: [][]string{{"sessionid", "sid_tt", "uid_tt"}, {"msToken"}},
}

// tiktokPostPath is a video's path (last30days shortform_oss._TT_RE, shortform_oss.py:33-35).
var tiktokPostPath = regexp.MustCompile(`^/@([\w.\-]+)/video/(\d+)`)

// tiktokProbe reads where the account's tab is and whether TikTok's web client has loaded there (tiktok.py:759).
const tiktokProbe = `(() => ({ href: location.href, ready: document.readyState,
  signer: typeof window.byted_acrawler !== 'undefined' }))()`

// tiktokFetch is a fetch from the account's tab of the address in its first line, with the session's parameters of
// __set_session_params (tiktok.py:138-181) where the address has none of its own, as make_request merges them
// (tiktok.py:848-849); the device id, history length and screen are drawn once a page, as TikTok-Api draws them once
// a session. Its answer is the status, Retry-After and body as text (generate_js_fetch, tiktok.py:658-670).
const tiktokFetch = `(async () => {
  const address = %s;
  const n = navigator;
  const digits = k => Array.from({ length: k }, () => Math.floor(Math.random() * 10)).join('');
  const s = window.__hypomnemaTikTok = window.__hypomnemaTikTok || {
    device: String(1 + Math.floor(Math.random() * 9)) + digits(18),
    history: String(1 + Math.floor(Math.random() * 10)),
    height: String(600 + Math.floor(Math.random() * 481)),
    width: String(800 + Math.floor(Math.random() * 1121)),
  };
  const lang = n.language || n.userLanguage || '';
  const params = {
    aid: '1988', app_language: lang, app_name: 'tiktok_web', browser_language: lang, browser_name: 'Mozilla',
    browser_online: 'true', browser_platform: n.platform, browser_version: n.userAgent, channel: 'tiktok_web',
    cookie_enabled: 'true', device_id: s.device, device_platform: 'web_pc', focus_state: 'true', from_page: 'user',
    history_len: s.history, is_fullscreen: 'false', is_page_visible: 'true', language: lang, os: n.platform,
    priority_region: '', referer: '', region: 'US', screen_height: s.height, screen_width: s.width,
    tz_name: Intl.DateTimeFormat().resolvedOptions().timeZone, webcast_language: lang,
  };
  const u = new URL(address);
  for (const [k, v] of Object.entries(params)) { if (!u.searchParams.has(k)) u.searchParams.set(k, v); }
  try {
    const r = await fetch(u.toString(), { method: 'GET', credentials: 'include' });
    return { status: r.status, retry_after: r.headers.get('retry-after') || '', body: await r.text() };
  } catch (e) {
    return { status: 0, error: String((e && e.message) || e) };
  }
})()`

type tiktokState struct {
	Href   string `json:"href"`
	Ready  string `json:"ready"`
	Signer bool   `json:"signer"`
}

type tiktokAnswer struct {
	Status     int    `json:"status"`
	RetryAfter string `json:"retry_after"`
	Body       string `json:"body"`
	Error      string `json:"error"`
}

// tiktokVideo is a video as TikTok's web API gives it (video.py:228-247).
type tiktokVideo struct {
	ID         string          `json:"id"`
	Desc       string          `json:"desc"`
	CreateTime json.RawMessage `json:"createTime"`
	Author     json.RawMessage `json:"author"`
	Stats      tiktokStats     `json:"stats"`
	StatsV2    *tiktokStats    `json:"statsV2"`
}

type tiktokStats struct {
	Plays    json.RawMessage `json:"playCount"`
	Likes    json.RawMessage `json:"diggCount"`
	Comments json.RawMessage `json:"commentCount"`
	Shares   json.RawMessage `json:"shareCount"`
}

// tiktokComment is a comment as the comment list gives it (comment.py:43-52).
type tiktokComment struct {
	CID        string          `json:"cid"`
	Text       string          `json:"text"`
	Likes      json.RawMessage `json:"digg_count"`
	CreateTime json.RawMessage `json:"create_time"`
	User       struct {
		UniqueID string `json:"unique_id"`
		Nickname string `json:"nickname"`
	} `json:"user"`
}

// tiktokBackend is TikTok's web API, asked from the account's tab.
func tiktokBackend() Backend {
	return sessionBackend(Backend{Name: BackendWebAPI, Host: tiktokHost, Search: tiktokSearch})
}

// tiktokSearch reads one search, then the comments of the most watched videos in the window; the comments are not
// read while a sign-in is verified, whose search alone shows that the session reads (Agent-Reach sessions.md:29,
// "redo the original action to verify").
func tiktokSearch(ctx context.Context, r Request) (Result, error) {
	page, err := r.Session.Page(ctx)
	if err != nil {
		return Result{}, err
	}

	body, f := tiktokCall(ctx, r, page, tiktokSearchURL(r.Query))
	if f != nil {
		return Result{}, f
	}

	videos, f := tiktokVideos(body, r.now())
	if f != nil {
		return Result{}, f
	}

	if len(videos) == 0 {
		return Result{Detail: "TikTok's search found no video"}, nil
	}

	var inWindow []Item

	for _, v := range videos {
		if inDays(v.PublishedAt, r.From, r.To) {
			inWindow = append(inWindow, v)
		}
	}

	// The most watched first, as last30days sorts TikTok's (tiktok.py:354-355) and picks the ones it enriches.
	slices.SortStableFunc(inWindow, func(a, b Item) int { return cmp.Compare(watched(b), watched(a)) })

	var (
		items  []Item
		notes  []string
		failed *SourceError
	)

	for i, v := range inWindow {
		items = append(items, v)

		if i >= tiktokCommented || failed != nil || r.Session.verifying() {
			continue
		}

		comments, f := tiktokCommentsOf(ctx, r, page, v)
		if f == nil {
			items = append(items, comments...)

			continue
		}

		notes = append(notes, "comments of "+v.NativeID+" not read: "+f.Error())

		// A refusal, a rate limit or a sign-in asked stops this search's other reads: the videos read stay as a part
		// read, and the backend rests (radar collection-pacing-contract.md:12; review round 2 of PR #1359: a throttle
		// after posts still stops).
		if stopsHost(f.Code) {
			failed = &SourceError{Code: f.Code, Detail: "a comment list: " + f.Detail, Until: f.Until,
				transient: f.transient, restBackend: true}
		}
	}

	detail := fmt.Sprintf("TikTok's search gave %d videos, %d in the window", len(videos), len(inWindow))
	if len(notes) > 0 {
		detail += "; " + strings.Join(notes, "; ")
	}

	return Result{Items: items, Detail: detail, Failed: failed}, nil
}

// tiktokSearchURL is the item search's address (search.py:75-86).
func tiktokSearchURL(query string) string {
	q := url.Values{
		"keyword": {strings.Join(strings.Fields(query), " ")}, "cursor": {"0"}, "from_page": {"search"},
		"web_search_code": {tiktokWebSearchCode},
	}

	return tiktokURL + "/api/search/item/full/?" + q.Encode()
}

// tiktokCall makes one request in TikTok's turn. The account's state is read again once the turn came, right before
// the request, so a read queued behind one that met a refusal sends nothing, as linkedInSearch does
// (linkedin.go:119-153).
func tiktokCall(ctx context.Context, r Request, page Page, address string) ([]byte, *SourceError) {
	if r.Read != nil {
		done, err := r.Read(ctx, tiktokHost, tiktokCadence)
		if err != nil {
			return nil, AsFailure(err)
		}

		defer done()
	}

	if f := r.Session.Ready(ctx); f != nil {
		// Not asked: skipped.
		return nil, &SourceError{Code: f.Code, Detail: f.Detail, Until: f.Until, resting: true, transient: f.transient}
	}

	if f := tiktokOpen(ctx, r, page); f != nil {
		return nil, f
	}

	raw, err := json.Marshal(address)
	if err != nil {
		return nil, Fail(CodeBroken, "encoding TikTok's address: %s", err.Error())
	}

	var got tiktokAnswer
	if err := page.Evaluate(ctx, fmt.Sprintf(tiktokFetch, raw), &got); err != nil {
		return nil, Fail(CodeBroken, "asking TikTok from the account's tab: %s", oneLine(err.Error()))
	}

	body, f := tiktokBody(got, address, r.now())

	// A rate limit is kept before TikTok's turn ends, a sign-in's check's too, so a read or a check queued behind it
	// sends nothing until TikTok's wait ends (accounts.go stop; linkedInSearch does the same, linkedin.go:144-150).
	r.Session.stop(ctx, f)

	return body, f
}

// tiktokBody is a request's answer, or what it says of the session (the package comment's rules).
func tiktokBody(got tiktokAnswer, address string, now time.Time) ([]byte, *SourceError) {
	path := address
	if u, err := url.Parse(address); err == nil {
		path = u.Path
	}

	if got.Error != "" {
		return nil, Fail(CodeBroken, "TikTok's %s could not be fetched: %s", path, oneLine(got.Error))
	}

	if got.Status != http.StatusOK {
		h := http.Header{}
		h.Set("Retry-After", got.RetryAfter)

		f := HTTPFailure(got.Status, h, now)
		f.Detail = "TikTok answered " + f.Detail + " to " + path
		f.transient = f.Code == CodeRateLimited

		// A 404 here is the web API's endpoint gone, not the question's page: TikTok's web API changed, and the
		// source's next route is still asked (review round 1 of PR #1363; registry.go stops a source's chain at a
		// not_found, which is about the question, Agent-Reach FINAL_CODES). Inferred: no reference reads a 404 of
		// TikTok's web API; TikTok-Api raises it as an invalid response (video.py:104-107 for a page).
		if f.Code == CodeNotFound {
			f.Code, f.Detail = CodeBroken, f.Detail+": TikTok's web API may have changed"
		}

		return nil, f
	}

	body := bytes.TrimSpace([]byte(got.Body))
	if len(body) == 0 {
		return nil, Fail(CodeRefused, "TikTok answered %s with an empty body, its answer to a session it takes for a "+
			"bot (TikTok-Api's EmptyResponseException)", path)
	}

	var head struct {
		Status *int   `json:"status_code"`
		Msg    string `json:"status_msg"`
	}

	if err := json.Unmarshal(body, &head); err != nil {
		return nil, Fail(CodeBroken, "TikTok's answer to %s is not JSON", path)
	}

	if head.Status != nil && *head.Status != 0 {
		return nil, Fail(CodeBroken, "TikTok answered %s with status_code %d (%s)", path, *head.Status,
			cut(oneLine(head.Msg), 200))
	}

	return body, nil
}

// tiktokOpen makes sure the account's tab is on TikTok with its web client loaded, opening TikTok when it is not,
// and reads where the page landed: TikTok's sign-in asks for one, another site is broken.
func tiktokOpen(ctx context.Context, r Request, page Page) *SourceError {
	st, err := tiktokLook(ctx, page)
	if err == nil && tiktokOn(st) && st.Signer {
		return nil
	}

	if err := page.Navigate(ctx, tiktokURL+"/"); err != nil {
		return Fail(CodeBroken, "opening TikTok: %s", oneLine(err.Error()))
	}

	for look := 1; ; look++ {
		st, err := tiktokLook(ctx, page)
		if err == nil {
			if f := tiktokBarrier(st); f != nil {
				return f
			}

			if tiktokOn(st) && st.Signer && st.Ready == "complete" {
				return nil
			}
		}

		if look >= int(tiktokLoad/tiktokPoll) {
			return Fail(CodeBroken, "TikTok's page did not load its web client in %s", tiktokLoad)
		}

		if err := r.wait(ctx, tiktokPoll); err != nil {
			return AsFailure(err)
		}
	}
}

func tiktokLook(ctx context.Context, page Page) (tiktokState, error) {
	var st tiktokState

	err := page.Evaluate(ctx, tiktokProbe, &st)

	return st, err
}

func tiktokOn(st tiktokState) bool {
	u, err := url.Parse(st.Href)

	return err == nil && tiktokSite.Owns(u.Hostname())
}

// tiktokBarrier is where the page landed: TikTok's sign-in, or a site that is not TikTok; nil when neither.
func tiktokBarrier(st tiktokState) *SourceError {
	if st.Href == "" {
		return nil
	}

	u, err := url.Parse(st.Href)
	if err != nil {
		return Fail(CodeBroken, "TikTok's page has an address that does not parse")
	}

	if u.Scheme == "about" {
		return nil
	}

	if !tiktokSite.Owns(u.Hostname()) {
		return Fail(CodeBroken, "TikTok's page landed on %s, not TikTok", u.Hostname())
	}

	if p := strings.TrimSuffix(u.Path, "/"); p == "/login" || strings.HasPrefix(p, "/login/") {
		return Fail(CodeNeedLogin, "TikTok sent the account to its sign-in")
	}

	return nil
}

// tiktokVideos are a search answer's videos. An answer with neither item_list nor has_more was not read, never a clean
// zero (radar facebook-topic-adapter.md:9: no zero without the site's own empty answer). Inferred, no reference
// says it: one with has_more and no item_list is TikTok's own empty answer; TikTok-Api reads any missing list as
// empty (search.py:107 and 111), which this narrows to an answer that carries has_more.
func tiktokVideos(body []byte, now time.Time) ([]Item, *SourceError) {
	var payload struct {
		Items   *[]tiktokVideo  `json:"item_list"`
		HasMore json.RawMessage `json:"has_more"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, Fail(CodeBroken, "TikTok's search answer does not decode: %s", oneLine(err.Error()))
	}

	if payload.Items == nil {
		if len(payload.HasMore) > 0 {
			return nil, nil
		}

		return nil, Fail(CodeBroken, "TikTok's search answered without its item_list: the API may have changed")
	}

	var out []Item

	seen := map[string]bool{}

	for _, v := range *payload.Items {
		it, ok := v.item(now)
		if !ok || seen[it.NativeID] {
			continue
		}

		seen[it.NativeID] = true
		out = append(out, it)
	}

	return out, nil
}

func (v tiktokVideo) item(now time.Time) (Item, bool) {
	if v.ID == "" || strings.Trim(v.ID, "0123456789") != "" {
		return Item{}, false
	}

	var author, name string

	var a struct {
		UniqueID string `json:"uniqueId"`
		Nickname string `json:"nickname"`
	}

	// The author is an object, or its handle alone (video.py:242-246).
	if json.Unmarshal(v.Author, &a) == nil {
		author, name = a.UniqueID, a.Nickname
	} else {
		_ = json.Unmarshal(v.Author, &author)
	}

	if !tiktokHandle.MatchString(author) {
		return Item{}, false
	}

	it := Item{
		NativeID: v.ID, URL: tiktokURL + "/@" + author + "/video/" + v.ID, Author: author, Text: strings.TrimSpace(v.Desc),
		Direct: true, Engagement: map[string]float64{},
	}

	if name != "" && name != author {
		it.Container = name
	}

	if at, ok := unixOf(v.CreateTime); ok {
		it.PublishedAt = &at
	} else if at, ok := tiktokIDTime(v.ID, now); ok {
		it.PublishedAt = &at
	}

	stats := v.Stats
	if v.StatsV2 != nil {
		stats = *v.StatsV2
	}

	for name, raw := range map[string]json.RawMessage{
		"views": stats.Plays, "likes": stats.Likes, "comments": stats.Comments, "shares": stats.Shares,
	} {
		if n, ok := countOf(raw); ok {
			it.Engagement[name] = n
		}
	}

	return it, true
}

// tiktokHandle is a TikTok handle (shortform_oss.py:33-35).
var tiktokHandle = regexp.MustCompile(`^[\w.\-]+$`)

// tiktokCommentsOf reads a video's first comments, in a request of its own.
func tiktokCommentsOf(ctx context.Context, r Request, page Page, v Item) ([]Item, *SourceError) {
	q := url.Values{"aweme_id": {v.NativeID}, "count": {strconv.Itoa(tiktokCommentPage)}, "cursor": {"0"}}

	body, f := tiktokCall(ctx, r, page, tiktokURL+"/api/comment/list/?"+q.Encode())
	if f != nil {
		return nil, f
	}

	var payload struct {
		Comments []tiktokComment `json:"comments"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, Fail(CodeBroken, "TikTok's comments do not decode: %s", oneLine(err.Error()))
	}

	var items []Item

	// The most liked first (last30days _normalize_comments, tiktok.py:746-751).
	slices.SortStableFunc(payload.Comments, func(a, b tiktokComment) int {
		x, _ := countOf(a.Likes)
		y, _ := countOf(b.Likes)

		return cmp.Compare(y, x)
	})

	for _, c := range payload.Comments {
		text := strings.TrimSpace(c.Text)
		if c.CID == "" || strings.Trim(c.CID, "0123456789") != "" || text == "" ||
			strings.EqualFold(c.User.UniqueID, v.Author) {
			continue
		}

		// Inferred, TikTok gives a comment no address of its own: the video's, with the comment's id as a parameter,
		// so that each comment keeps its own address through the dedupe (NormalizeURL keeps the query, text.go).
		// Nothing shows that TikTok's page opens that comment: it opens the video.
		it := Item{
			NativeID: c.CID, URL: v.URL + "?comment_id=" + c.CID, Author: c.User.UniqueID, Text: text,
			Parent: v.NativeID, Container: tiktokCaptionLine(v.Text),
		}

		if n, ok := countOf(c.Likes); ok {
			it.Engagement = map[string]float64{"likes": n}
		}

		if at, ok := unixOf(c.CreateTime); ok {
			it.PublishedAt = &at
		}

		items = append(items, it)

		if len(items) == tiktokComments {
			break
		}
	}

	return items, nil
}

func tiktokCaptionLine(caption string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(caption), "\n")

	return cut(line, tiktokTitle)
}

// tiktokIDTime is when a TikTok video was posted, from its id: its high 32 bits are the upload time in seconds; a time
// before 2016 or more than two days ahead is no date (last30days post_timestamp_from_url, shortform_oss.py:129-157).
func tiktokIDTime(id string, now time.Time) (time.Time, bool) {
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	at := time.Unix(int64(v>>32), 0).UTC()
	if at.Year() < 2016 || at.After(now.Add(48*time.Hour)) {
		return time.Time{}, false
	}

	return at, true
}

// unixOf is a time in seconds, given as a number or a string.
func unixOf(raw json.RawMessage) (time.Time, bool) {
	n, ok := countOf(raw)
	if !ok || n <= 0 {
		return time.Time{}, false
	}

	return time.Unix(int64(n), 0).UTC(), true
}

// countOf is a count given as a number or as a string of digits (statsV2 gives strings, video.py:240).
func countOf(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}

	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n, !math.IsNaN(n) && n >= 0
	}

	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0, false
	}

	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)

	return n, err == nil && n >= 0
}

// tiktokPost is a web result that is a TikTok video: its id, canonical address, author and the date its id holds
// (shortform_oss.py:129-195).
func tiktokPost(u *url.URL, now time.Time) (Item, bool) {
	if !tiktokSite.Owns(u.Hostname()) {
		return Item{}, false
	}

	path, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return Item{}, false
	}

	m := tiktokPostPath.FindStringSubmatch(path)
	if m == nil {
		return Item{}, false
	}

	it := Item{NativeID: m[2], URL: tiktokURL + "/@" + m[1] + "/video/" + m[2], Author: m[1]}
	if at, ok := tiktokIDTime(m[2], now); ok {
		it.PublishedAt = &at
	}

	return it, true
}

// tiktokYtDlpBackend reads the TikTok videos the web search finds, `site:tiktok.com <query>`, each with yt-dlp, as
// last30days' fallback without a paid key does (shortform_oss.search, discover then enrich_urls,
// shortform_oss.py:640-779): a video whose id dates it outside the window by more than a day is not read
// (_outside_window, :168-179), each read is one yt-dlp run (:43-46, 560-571) in TikTok's turn, and a video's text,
// author, date and counts are yt-dlp's (_item_from_ytdlp, :497-548). Departure: the web search is asked with
// `site:tiktok.com`, where last30days asks "<topic> tiktok video" (discovery_query, :202-207) because Serper's free
// accounts reject site:, which the keyless providers here do not; the same words are the next backend's, whose web
// answer is then the same one.
func tiktokYtDlpBackend(run Command) Backend {
	return Backend{
		Name: BackendYtDlp, Host: tiktokYtDlpHost, Probe: needsWeb,
		Search: func(ctx context.Context, r Request) (Result, error) {
			if !r.From.IsZero() {
				ctx = websearch.WithWindow(ctx, r.From, r.To)
			}

			a, err := r.Web(ctx, "site:tiktok.com "+r.Query, min(r.Limit, websearch.MaxResults))
			if errors.Is(err, websearch.ErrNoResults) {
				return Result{Detail: "no provider found anything"}, nil
			}

			if err != nil {
				return Result{}, webFailure(err)
			}

			var (
				res  Result
				urls []string
			)

			for _, w := range a.Results {
				u, err := url.Parse(w.URL)
				if err != nil {
					continue
				}

				it, ok := tiktokPost(u, r.now())

				switch {
				case !ok:
					res.dropped.OffTopic++
				case it.PublishedAt != nil && !nearWindow(*it.PublishedAt, r):
					res.dropped.OutsideWindow++
				case !slices.Contains(urls, it.URL) && len(urls) < tiktokURLs:
					urls = append(urls, it.URL)
				}
			}

			res.provider, res.windowBasis = a.Provider, a.WindowBasis

			if len(urls) == 0 {
				res.Detail = answeredBy(a) + "; it named no TikTok video in the window"

				return res, nil
			}

			var (
				notes []string
				// first is the first video's failure, a value: kept as the part not read, never returned as an error.
				first  SourceError
				missed int
			)

			for _, address := range urls {
				it, f := tiktokYtDlp(ctx, run, r, address)
				if f == nil {
					res.Items = append(res.Items, it)

					continue
				}

				// yt-dlp missing, or TikTok refusing this network: nothing else is read (last30days enrich_urls
				// skips the whole read without yt-dlp, shortform_oss.py:603-605).
				if f.Code == CodeBroken && strings.Contains(f.Detail, "not installed") {
					return Result{}, f
				}

				if stopsHost(f.Code) {
					f.restBackend = true
					if len(res.Items) == 0 {
						return Result{}, f
					}

					res.Failed = f

					break
				}

				notes = append(notes, address+": "+f.Error())
				if missed++; missed == 1 {
					first = *f
				}
			}

			// Nothing read and every read failed: the failure, never a clean zero (last30days "yt-dlp extracted 0
			// posts", shortform_oss.py:747-749). It is the videos', each one's own (a video gone answers "Video
			// unavailable", ytFailureAt's not_found), not the question's, so it is kept as the part not read and the
			// source's next route is still asked, where an error with not_found would end the source's chain
			// (registry.go; review round 1 of PR #1363); last30days likewise drops a post yt-dlp cannot read and
			// goes on (enrich_urls, --ignore-errors, shortform_oss.py:560-571 and 617-637).
			if len(res.Items) == 0 && missed > 0 {
				res.Failed = &SourceError{Code: first.Code, Detail: "yt-dlp read none of the " + strconv.Itoa(len(urls)) +
					" TikTok videos the web search named: " + strings.Join(notes, "; "), Until: first.Until,
					transient: first.transient}
				res.Detail = answeredBy(a)

				return res, nil
			}

			res.Detail = fmt.Sprintf("%s; yt-dlp read %d of %d TikTok videos", answeredBy(a), len(res.Items), len(urls))
			if len(notes) > 0 {
				res.Detail += "; " + strings.Join(notes, "; ")
			}

			return res, nil
		},
	}
}

// nearWindow says a date is no more than a day outside the window (shortform_oss.py:168-179, "one day of slack on
// each side"); the window then holds the date yt-dlp gives.
func nearWindow(at time.Time, r Request) bool {
	if r.From.IsZero() {
		return true
	}

	day := 24 * time.Hour

	return !at.Before(r.From.Add(-day)) && !at.After(r.To.Add(day))
}

// tiktokYtDlp reads one video with yt-dlp in TikTok's turn: one yt-dlp run at a time at TikTok, the account's reads
// queued in the same turn, with no gap between two runs. The gap is the one YouTube's yt-dlp reads take here (ytRun);
// last30days runs four such reads at once (shortform_oss.py:43-46, _ENRICH_WORKERS), and radar's cadence, 12 seconds
// for TikTok, is for a browser-backed read (collection-pacing-contract.md:3), which a CLI run without an account is
// not.
func tiktokYtDlp(ctx context.Context, run Command, r Request, address string) (Item, *SourceError) {
	if r.Pace != nil {
		done, err := r.Pace(ctx, tiktokHost, 0)
		if err != nil {
			return Item{}, AsFailure(err)
		}

		defer done()
	}

	ctx, cancel := context.WithTimeout(ctx, tiktokYtDlpTimeout)
	defer cancel()

	stdout, stderr, err := run(ctx, append(slices.Clone(ytBase), "--skip-download", "--dump-json",
		"--socket-timeout", "25", "--", address)...)
	if err != nil {
		return Item{}, ytFailureAt(ctx, "TikTok", stderr, err)
	}

	var v struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Title       string   `json:"title"`
		Uploader    string   `json:"uploader"`
		Channel     string   `json:"channel"`
		Creator     string   `json:"creator"`
		Timestamp   int64    `json:"timestamp"`
		WebpageURL  string   `json:"webpage_url"`
		Views       *float64 `json:"view_count"`
		Likes       *float64 `json:"like_count"`
		Comments    *float64 `json:"comment_count"`
		Reposts     *float64 `json:"repost_count"`
	}

	if json.Unmarshal(bytes.TrimSpace(stdout), &v) != nil || v.ID == "" {
		return Item{}, Fail(CodeBroken, "yt-dlp's answer for a TikTok video is not its JSON")
	}

	it := Item{
		NativeID: v.ID, URL: address, Author: cmp.Or(v.Uploader, v.Channel, v.Creator),
		Text: strings.TrimSpace(cmp.Or(v.Description, v.Title)), Direct: true, Engagement: map[string]float64{},
	}

	if u, err := url.Parse(v.WebpageURL); err == nil {
		if p, ok := tiktokPost(u, r.now()); ok && p.NativeID == v.ID {
			it.URL, it.Author = p.URL, p.Author
		}
	}

	if v.Timestamp > 0 {
		at := time.Unix(v.Timestamp, 0).UTC()
		it.PublishedAt = &at
	}

	for name, n := range map[string]*float64{"views": v.Views, "likes": v.Likes, "comments": v.Comments,
		"shares": v.Reposts} {
		if n != nil {
			it.Engagement[name] = *n
		}
	}

	return it, nil
}
