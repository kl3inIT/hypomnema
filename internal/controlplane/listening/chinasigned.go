package listening

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
)

// The Chinese platforms behind a signed-in account (issue #1362, the owner's addition of 2026-11-28 on #1291): from
// zm, without a cookie, Zhihu's search answers 403 need_login, Weibo's sends to the visitor passport, Douyin's answers
// empty and Xiaohongshu's sends to rednote.com (research note §8). Each is read as one of the owner's accounts through
// the signed-in mechanism of X and LinkedIn (accounts.go), off until he agrees to its own consent card, and, while the
// account cannot be read, by the web search's results on its site, which need none (last30days-skill-cn's last route
// for each, zhihu.py:11, weibo.py:13, douyin.py:9, xiaohongshu.py:12-13):
//
//   - Zhihu: search_v3 with the account's cookie, else the page's own search_v3 XHR in the account's tab; 401 or 403
//     is the session's end (zhihu.go);
//   - Weibo: the mobile search with the account's cookie, else the same search asked by m.weibo.cn's own page in the
//     account's tab; ok -100 or the passport is the session's end (weibo.go);
//   - Douyin: only the page, whose search XHR carries the a_bogus signature the page computes, read as it arrives
//     (douyin.go);
//   - Xiaohongshu: its search page, as xiaohongshu-mcp reads it (Apache-2.0; notice in third_party/xiaohongshu-mcp),
//     the window as its 发布时间 filter (xiaohongshu.go).
//
// Each reads one request at a time in its account's turn at the site (Request.turn), and a captcha, a rate limit or a
// login expiry stops it, never answered (trend-opportunity-radar collection-pacing-contract.md:5-12): a captcha or a
// security check refuses the account until the owner signs in again, an expiry asks for a sign-in, and a rate limit
// stops the site for chineseStop.

// The Chinese signed-in sources' and their backends' names.
const (
	SourceZhihu       = "zhihu"
	SourceWeibo       = "weibo"
	SourceDouyin      = "douyin"
	SourceXiaohongshu = "xiaohongshu"

	BackendZhihuAPI        = "zhihu_api"
	BackendZhihuPage       = "zhihu_page"
	BackendWeiboAPI        = "weibo_api"
	BackendWeiboPage       = "weibo_page"
	BackendDouyinPage      = "douyin_page"
	BackendXiaohongshuPage = "xiaohongshu_page"
)

const (
	// chineseStop is how long a rate limit stops one of these sites: linkedin-mcp-server's 24 hours after a
	// rate-limit page (AGENTS.md:209-223), the stop LinkedIn's account keeps. Inferred: no reference of these sites
	// names a stop's length; radar says only to stop and never to retry a safety stop by waiting
	// (collection-pacing-contract.md:12).
	chineseStop = 24 * time.Hour
	// pageLoad is how long a page is waited for to show its search's answer, looked at every pagePoll, and pageScroll
	// the wait after a scroll: last30days-skill-cn waits 8 s for Zhihu's first XHR and scrolls Douyin eight times
	// 1.2 s apart (crawler_bridge.py:969-976, 1096-1101); 20 s is linkedInLoad's, inferred.
	pageLoad   = 20 * time.Second
	pagePoll   = time.Second
	pageScroll = 1200 * time.Millisecond
	// pageScrolls is the most scrolls of a search page (crawler_bridge.py:969, range(8)).
	pageScrolls = 8
	// mobileUA is the iPhone Safari last30days-skill-cn sends to m.weibo.cn (crawler_bridge.py:63-66).
	mobileUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) " +
		"Version/18.5 Mobile/15E148 Safari/604.1"
)

// The paces of the sites (trend-opportunity-radar collection-pacing-contract.md:7-8): Xiaohongshu's search 15 seconds
// apart and 45 more after every five reads; Zhihu, Weibo and Douyin are platforms it did not calibrate, 10 seconds
// apart and 30 more after five.
var (
	chineseCadence     = Cadence{Gap: 10 * time.Second, Every: 5, Cooldown: 30 * time.Second}
	xiaohongshuCadence = Cadence{Gap: 15 * time.Second, Every: 5, Cooldown: 45 * time.Second}
)

// The sites as the shared sign-in knows them: where the owner signs in and the cookies only a session has
// (last30days-skill-cn crawler_bridge.py:77-112 LOGIN_SPECS). Weibo's SUB and Xiaohongshu's web_session are given to
// a visitor too (anonymous_cookie_ambiguous, :73-76): a sign-in is kept only once a real call reads as the account
// (accounts.go verify; weibo.go's /api/config check).
var (
	zhihuSite = signin.Site{
		Name: "Zhihu", URL: "https://www.zhihu.com/signin", Domains: []string{"zhihu.com"}, Login: [][]string{{"z_c0"}},
	}
	weiboSite = signin.Site{
		Name: "Weibo", URL: "https://passport.weibo.com/sso/signin?entry=wapsso&source=wapssowb&url=" +
			url.QueryEscape("https://m.weibo.cn/"),
		Domains: []string{"weibo.cn", "weibo.com"}, Login: [][]string{{"SUB"}},
	}
	douyinSite = signin.Site{
		Name: "Douyin", URL: "https://www.douyin.com/", Domains: []string{"douyin.com"},
		Login: [][]string{{"sessionid", "sessionid_ss"}},
	}
	xiaohongshuSite = signin.Site{
		Name: "Xiaohongshu", URL: "https://www.xiaohongshu.com/explore", Domains: []string{"xiaohongshu.com"},
		Login: [][]string{{"web_session"}},
	}
)

// The consent cards' fixed wording, each its own source's, in the form of X's and LinkedIn's (last30days
// references/reddit-session.md:13): what is done as the account, the risk to it, which cookie is kept and where it
// goes, and when reading stops. Xiaohongshu's names its ban reports (xiaohongshu-mcp issues #930, 2026-11-27, a
// security page after three read-only calls; #877, code 300011; Agent-Reach #1011, code 300012) and its one web
// session an account (xiaohongshu-mcp README.md:301).
const (
	zhihuConsent = "Reading Zhihu as your account may break Zhihu's terms: automated use of a signed-in account can " +
		"bring a verification, a restriction or a ban of the account. Sign in a secondary account made for this, " +
		"never your own. Hypomnema then searches Zhihu as that account, one search at a time, 10 seconds apart and 30 " +
		"seconds more after every five: through Zhihu's search API with the account's cookies, else through Zhihu's " +
		"own search page in a browser of its own. Only Zhihu's cookies are kept, in the vault: they are never shown " +
		"or logged and go only to zhihu.com. If Zhihu asks the account to sign in again or shows a check, Hypomnema " +
		"stops reading it until you sign in again, and never answers a check; after a rate limit, it stops for 24 hours."
	weiboConsent = "Reading Weibo as your account may break Weibo's terms: automated use of a signed-in account can " +
		"bring a verification, a restriction or a ban of the account. Sign in a secondary account made for this, " +
		"never your own. Hypomnema then searches Weibo's mobile site as that account, one search page at a time, 10 " +
		"seconds apart and 30 seconds more after every five, with the account's cookies, else through m.weibo.cn in " +
		"a browser of its own. Weibo gives visitors a SUB cookie too, so a sign-in is kept only once Weibo says the " +
		"account is signed in and a real search reads. Only Weibo's cookies are kept, in the vault: they are never " +
		"shown or logged and go only to weibo.cn and weibo.com. If Weibo asks the account to sign in again or shows a " +
		"check, Hypomnema stops reading it until you sign in again, and never answers a check; after a rate limit, it " +
		"stops for 24 hours."
	douyinConsent = "Reading Douyin as your account may break Douyin's terms: automated use of a signed-in account can " +
		"bring a captcha, a restriction or a ban of the account. Sign in a secondary account made for this, never " +
		"your own. Hypomnema then opens Douyin's search page as that account in a browser of its own and reads the " +
		"results the page itself asks for, one search at a time, 10 seconds apart and 30 seconds more after every " +
		"five. Only Douyin's cookies are kept, in the vault: they are never shown or logged and go only to douyin.com. " +
		"If Douyin shows a captcha or asks the account to sign in again, Hypomnema stops reading it until you sign in " +
		"again, and never answers a captcha; after a rate limit, it stops for 24 hours."
	xiaohongshuConsent = "Xiaohongshu restricts accounts more than any other site here, some after reading only: users " +
		"of xiaohongshu-mcp, which reads it the same way, report a security page after three read-only searches " +
		"(2026-11-27), account-security blocks (code 300011) and IP-risk blocks (code 300012). Turn it on yourself " +
		"only with a secondary account made for this, never your own, and do not sign that account in on another web " +
		"browser meanwhile: Xiaohongshu keeps one web session an account, so a sign-in elsewhere signs Hypomnema out. " +
		"Hypomnema then opens Xiaohongshu's search page as that account in a browser of its own, one search at a " +
		"time, 15 seconds apart and 45 seconds more after every five. Only Xiaohongshu's cookies are kept, in the " +
		"vault: they are never shown or logged and go only to xiaohongshu.com. If Xiaohongshu shows its captcha or " +
		"security check, Hypomnema stops reading the account until you sign in again, and never answers it; after a " +
		"rate limit, it stops for 24 hours."
)

// SignedChineseSources are Zhihu, Weibo, Douyin and Xiaohongshu; client reads Zhihu's and Weibo's searches with the
// account's cookie. Each one's live query, which a sign-in's verification searches too, is Toutiao's and Bilibili's,
// 越南 (sources.go).
func SignedChineseSources(_ Endpoints, client *http.Client) []Platform {
	signed := func(site signin.Site, consent, backend string) *Signed {
		return &Signed{Site: site, Consent: consent, Backend: backend, Throttle: chineseStop}
	}

	return []Platform{
		{
			Name: SourceZhihu, LiveQuery: "越南",
			Backends: []Backend{
				zhihuAPIBackend(client), zhihuPageBackend(), siteSearchBackend("zhihu.com", zhihuPost),
			},
			Signed: signed(zhihuSite, zhihuConsent, BackendZhihuAPI),
			Adds: "Zhihu's questions, answers and articles with their votes and comments, searched as your account; " +
				"without one, Zhihu's pages the web search finds.",
		},
		{
			Name: SourceWeibo, LiveQuery: "越南",
			Backends: []Backend{
				weiboAPIBackend(client), weiboPageBackend(), siteSearchBackend("weibo.com", weiboPost),
			},
			Signed: signed(weiboSite, weiboConsent, BackendWeiboAPI),
			Adds: "Weibo posts with their author, date, reposts, comments and likes, searched as your account; without " +
				"one, Weibo posts the web search finds.",
		},
		{
			Name: SourceDouyin, LiveQuery: "越南",
			Backends: []Backend{douyinPageBackend(), siteSearchBackend("douyin.com", douyinPost)},
			Signed:   signed(douyinSite, douyinConsent, BackendDouyinPage),
			Adds: "Douyin videos with their author, date, views, likes, comments and shares, from its search page read " +
				"as your account; without one, Douyin videos the web search finds.",
		},
		{
			Name: SourceXiaohongshu, LiveQuery: "越南",
			Backends: []Backend{
				xiaohongshuPageBackend(), siteSearchBackend("xiaohongshu.com", xiaohongshuPost),
			},
			Signed: signed(xiaohongshuSite, xiaohongshuConsent, BackendXiaohongshuPage),
			Adds: "Xiaohongshu notes with their author, likes, collects and comments, from its search page read as " +
				"your account; without one, notes the web search finds.",
		},
	}
}

// signedSearch is a backend's read of a signed-in account in its turn at host: the account's jar checked for the
// site's login cookies, the turn taken (Request.turn), and what the read met kept before the turn ends.
func signedSearch(host string, cadence Cadence, read func(ctx context.Context, r Request, jar []browser.Cookie,
) (Result, error),
) func(ctx context.Context, r Request) (Result, error) {
	return func(ctx context.Context, r Request) (Result, error) {
		site := r.Session.Site()

		jar, err := r.Session.Jar(ctx)
		if err != nil {
			return Result{}, err
		}

		if missing := site.Missing(jar); missing != "" {
			return Result{}, Fail(CodeNeedLogin, "the kept %s session lacks %s", site.Name, missing)
		}

		end, err := r.turn(ctx, host, cadence)
		if err != nil {
			return Result{}, err
		}

		res, err := read(ctx, r, jar)
		if err != nil {
			end(AsFailure(err))
		} else {
			end(res.Failed)
		}

		return res, err
	}
}

// signedPageSearch is signedSearch for a read in the account's tab, opened before the turn as linkedInSearch opens it.
func signedPageSearch(host string, cadence Cadence, read func(ctx context.Context, r Request, page Page) (Result,
	error),
) func(ctx context.Context, r Request) (Result, error) {
	return func(ctx context.Context, r Request) (Result, error) {
		page, err := r.Session.Page(ctx)
		if err != nil {
			return Result{}, err
		}

		end, err := r.turn(ctx, host, cadence)
		if err != nil {
			return Result{}, err
		}

		res, err := read(ctx, r, page)
		if err != nil {
			end(AsFailure(err))
		} else {
			end(res.Failed)
		}

		return res, err
	}
}

// landing is where a tab stands: its address, whether it loaded, and the start of its text.
type landing struct {
	Href  string `json:"href"`
	Ready string `json:"ready"`
	Text  string `json:"text"`
}

// landingScript reads a landing at once.
const landingScript = `(() => ({ href: location.href, ready: document.readyState,
  text: document.body ? (document.body.innerText || '').slice(0, 4000) : '' }))()`

func landingOf(ctx context.Context, page Page) (landing, error) {
	var l landing

	err := page.Evaluate(ctx, landingScript, &l)

	return l, err
}

// loginWallMarkers are the words of these sites' sign-in walls (last30days-skill-cn crawler_bridge.py:113).
var loginWallMarkers = []string{"扫码登录", "登录后查看", "请登录", "登录后即可", "手机号登录"}

// pageBarrier is what a page's landing says when it showed no result: a captcha or a verification refuses the
// account, a sign-in route or wall asks for one, another site than the account's is broken (last30days-skill-cn
// crawler_bridge.py:508-516 _looks_like_login_wall, 992 a captcha in the address); nil when none.
func pageBarrier(site signin.Site, l landing) *SourceError {
	u, ok := landedAt(l.Href)
	if !ok {
		return nil
	}

	switch {
	case strings.Contains(strings.ToLower(u.Path+" "+u.Host), "captcha"):
		return Fail(CodeRefused, "%s showed a captcha or a security check", site.Name)
	case strings.Contains(u.Host, "passport.") || strings.Contains(u.Path, "/signin") ||
		strings.Contains(u.Path, "/login"):
		return Fail(CodeNeedLogin, "%s sent the account to its sign-in (%s)", site.Name, u.Host+u.Path)
	case !site.Owns(u.Hostname()):
		return Fail(CodeBroken, "%s's search landed on %s, not %s", site.Name, u.Hostname(), site.Domains[0])
	}

	for _, m := range loginWallMarkers {
		if strings.Contains(l.Text, m) {
			return Fail(CodeNeedLogin, "%s shows its sign-in wall (%s)", site.Name, m)
		}
	}

	return nil
}

// landedAt is a landing's address, false when it has none to read: none yet, a blank page, or one that does not parse.
func landedAt(href string) (*url.URL, bool) {
	u, err := url.Parse(href)

	return u, err == nil && href != "" && u.Scheme != "about"
}

// siteThrottled is a site refusing a request its page sent, on its own hosts only, as linkedInThrottled reads
// LinkedIn's (linkedin-mcp-server core/throttle.py:43 and 190-210): a 429 is a rate limit until its Retry-After, and
// refused are the site's own statuses of a captcha (Xiaohongshu's 461 and 471, MediaCrawler media_platform/xhs/
// client.py:158-164, read only); nil when none was.
func siteThrottled(ctx context.Context, page Page, site signin.Site, now time.Time, refused ...int) *SourceError {
	// A record that cannot be read says nothing of a throttle.
	responses, err := page.Responses(ctx)
	if err != nil {
		responses = nil
	}

	for i := len(responses) - 1; i >= 0; i-- {
		got := responses[i]

		u, err := url.Parse(got.URL)
		if err != nil || !site.Owns(u.Hostname()) {
			continue
		}

		for _, status := range refused {
			if got.Status == status {
				return Fail(CodeRefused, "%s answered HTTP %d to %s, its captcha", site.Name, status, u.Path)
			}
		}

		if got.Status != http.StatusTooManyRequests {
			continue
		}

		f := &SourceError{Code: CodeRateLimited, transient: true, Detail: site.Name + " answered HTTP 429 to " + u.Path +
			": it is throttling this account"}
		if secs, err := strconv.Atoi(strings.TrimSpace(got.RetryAfter)); err == nil && secs > 0 {
			f.Until = now.Add(time.Duration(secs) * time.Second)
		}

		return f
	}

	return nil
}

// xhr is one answer to the page's own search request.
type xhr struct {
	URL    string
	Status int
	Body   []byte
}

// pageXHRs are the answers, since the page's navigation, to the requests whose address match says is its search,
// each once (seen keeps those read), as last30days-skill-cn's response listeners keep a search's XHR
// (crawler_bridge.py:944-962, 1083-1095). A body not loaded yet is read at the next look.
func pageXHRs(ctx context.Context, page Page, match func(*url.URL) bool, seen map[string]bool) []xhr {
	responses, err := page.Responses(ctx)
	if err != nil {
		return nil
	}

	var out []xhr

	for _, got := range responses {
		u, err := url.Parse(got.URL)
		key := got.RequestID + " " + got.URL

		if err != nil || seen[key] || !match(u) {
			continue
		}

		if got.Status != http.StatusOK {
			seen[key] = true
			out = append(out, xhr{URL: got.URL, Status: got.Status})

			continue
		}

		body, err := page.Body(ctx, got)
		if err != nil {
			continue
		}

		seen[key] = true
		out = append(out, xhr{URL: got.URL, Status: got.Status, Body: body})
	}

	return out
}

// jsArgs are strings as a script's arguments, each a JSON string, which is a JavaScript one.
func jsArgs(args ...string) string {
	out := make([]string, 0, len(args))

	for _, a := range args {
		raw, err := json.Marshal(a)
		if err != nil {
			raw = []byte(`""`)
		}

		out = append(out, string(raw))
	}

	return strings.Join(out, ",")
}

// jsonID is an id these sites send as a number or a string; a number is kept whole.
type jsonID string

func (id *jsonID) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)

	var s string
	if json.Unmarshal(raw, &s) == nil {
		*id = jsonID(strings.TrimSpace(s))

		return nil
	}

	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		*id = ""

		return nil //nolint:nilerr // An id of another shape is no id.
	}

	*id = jsonID(n.String())

	return nil
}

// jsonCount is a count these sites send as a number or as words: 1200, "1.2万", "3亿", "10+", "1,024"
// (last30days-skill-cn xiaohongshu.parse_count, xiaohongshu.py:257-276; weibo._to_int, weibo.py:328-337).
type jsonCount float64

func (c *jsonCount) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil //nolint:nilerr // A count of another shape is none.
	}

	*c = jsonCount(chineseCount(v))

	return nil
}

func chineseCount(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		text := strings.TrimSuffix(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(n)), ",", ""), "+")
		scale := 1.0

		switch {
		case strings.HasSuffix(text, "万"), strings.HasSuffix(text, "w"):
			text, scale = strings.TrimSuffix(strings.TrimSuffix(text, "万"), "w"), 10000
		case strings.HasSuffix(text, "亿"):
			text, scale = strings.TrimSuffix(text, "亿"), 100000000
		case strings.HasSuffix(text, "k"):
			text, scale = strings.TrimSuffix(text, "k"), 1000
		}

		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0
		}

		return f * scale
	default:
		return 0
	}
}

// unixTime is a time these sites send in seconds or milliseconds (last30days-skill-cn xiaohongshu.py:207-212: over
// 1e10 is milliseconds); none before 2001 (zhihu.py:276, over 1e9).
func unixTime(v float64) *time.Time {
	switch {
	case v > 10_000_000_000:
		v /= 1000
	case v <= 1_000_000_000:
		return nil
	}

	at := time.Unix(int64(v), 0).UTC()

	return &at
}

// The addresses of each site's posts the web search may show, as last30days-skill-cn accepts them in its site-search
// fallbacks (zhihu.py:26, weibo.py:29, douyin.py:27, xiaohongshu.py:26).
var (
	zhihuPostURL       = regexp.MustCompile(`^/(question/\d+(?:/answer/\d+)?|p/\d+)`)
	weiboPostURL       = regexp.MustCompile(`^/(\d+/[A-Za-z0-9]{6,}|(?:status|detail)/\w+)`)
	douyinPostURL      = regexp.MustCompile(`^/(?:video|note)/(\d+)`)
	xiaohongshuPostURL = regexp.MustCompile(`^/(?:explore|discovery/item)/([0-9a-f]{24})`)
)

func hostIs(u *url.URL, hosts ...string) bool {
	host := strings.ToLower(u.Hostname())

	for _, h := range hosts {
		if host == h {
			return true
		}
	}

	return false
}

// zhihuPost is a web result that is a Zhihu question, answer or column article.
func zhihuPost(u *url.URL, _ time.Time) (Item, bool) {
	m := zhihuPostURL.FindStringSubmatch(u.Path)

	switch {
	case m == nil:
		return Item{}, false
	case strings.HasPrefix(m[1], "p/") && hostIs(u, "zhuanlan.zhihu.com"):
		return Item{NativeID: "article:" + strings.TrimPrefix(m[1], "p/"), URL: "https://zhuanlan.zhihu.com/" + m[1]}, true
	case strings.HasPrefix(m[1], "question/") && hostIs(u, "zhihu.com", "www.zhihu.com"):
		return Item{NativeID: zhihuWebID(m[1]), URL: zhihuURL + "/" + m[1]}, true
	default:
		return Item{}, false
	}
}

// zhihuWebID is the id a question's or an answer's address gives, the one its search entry gives it too.
func zhihuWebID(path string) string {
	if _, answer, ok := strings.Cut(path, "/answer/"); ok {
		return "answer:" + answer
	}

	return "question:" + strings.TrimPrefix(path, "question/")
}

// weiboPost is a web result that is a Weibo post.
func weiboPost(u *url.URL, _ time.Time) (Item, bool) {
	m := weiboPostURL.FindStringSubmatch(u.Path)
	if m == nil || !hostIs(u, "weibo.com", "www.weibo.com", "m.weibo.cn") {
		return Item{}, false
	}

	if hostIs(u, "m.weibo.cn") != (strings.HasPrefix(m[1], "status/") || strings.HasPrefix(m[1], "detail/")) {
		return Item{}, false
	}

	id := m[1][strings.LastIndex(m[1], "/")+1:]

	return Item{NativeID: id, URL: "https://" + strings.ToLower(u.Hostname()) + "/" + m[1]}, true
}

// douyinPost is a web result that is a Douyin video or note.
func douyinPost(u *url.URL, _ time.Time) (Item, bool) {
	m := douyinPostURL.FindStringSubmatch(u.Path)
	if m == nil || !hostIs(u, "douyin.com", "www.douyin.com") {
		return Item{}, false
	}

	return Item{NativeID: m[1], URL: douyinURL + u.Path}, true
}

// xiaohongshuPost is a web result that is a Xiaohongshu note.
func xiaohongshuPost(u *url.URL, _ time.Time) (Item, bool) {
	m := xiaohongshuPostURL.FindStringSubmatch(u.Path)
	if m == nil || !hostIs(u, "xiaohongshu.com", "www.xiaohongshu.com") {
		return Item{}, false
	}

	return Item{NativeID: m[1], URL: xiaohongshuNoteURL(m[1], "")}, true
}
