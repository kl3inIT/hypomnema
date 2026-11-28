package listening

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
)

// LinkedIn read as the owner's account (issue #1291), as stickerdaniel/linkedin-mcp-server reads it (Apache-2.0;
// adapted parts noticed in third_party/linkedin-mcp-server):
//
//   - only the page LinkedIn renders, never its private API: the account's tab opens the Posts tab of LinkedIn's
//     search, /search/results/content/?keywords=…&origin=FACETED_SEARCH, with the window as its datePosted facet when
//     one fits (docs/decisions/2026-09-16-rendered-page.md; tools/post.py:38-115, linkedin/search_urls.py:62-69 and
//     235-284), and reads its posts from what the page rendered;
//   - each post is paired with its own URN by the boundary that holds it, an article or an element whose data-urn or
//     data-id is the post's, a boundary naming no single post giving no item (linkedin/feed.py:35-77,
//     FEED_POST_BOUNDARIES_JS, made for the feed and run here on the search page's cards); the post's address is its
//     /feed/update/<urn>/ (linkedin/feed_payload.py:80-84), its text the boundary's, the media player's lines left
//     out (linkedin/text.py:172-195);
//   - throttling is read from the statuses LinkedIn answered, 429 or its 999 "Request denied", never from the words
//     on the page (docs/decisions/2026-09-23-throttling-is-a-status.md, core/throttle.py:43): the status of every
//     request the tab sent since it opened the search, the page's own fetches of its results among them, recorded as
//     linkedin-mcp-server's page listener records them (core/throttle.py:67-78 and 171-210; the tab's record is
//     browser.Page.Responses), and the document's own from its navigation entry; a read that found posts is left as
//     it is, one that found none is rate_limited (review round 1 of PR #1359);
//   - a barrier is read from where the page landed, its path only: the account-restriction route
//     (login/login-restriction) and a checkpoint or challenge refuse the account, a sign-in route asks for one
//     (core/auth.py:24-36 and 348-387, docs/linkedin-auth-routes.md);
//   - zero posts is a clean zero only beside LinkedIn's own empty state; without it the page was not read (radar
//     facebook-topic-adapter.md:9, "zero parsed cards without an explicit … empty-state message is a collection
//     failure, not a zero-result conclusion");
//   - one page at a time, 10 seconds apart and 30 more after every five (radar collection-pacing-contract.md:5-9, its
//     interval for a platform it did not calibrate), and after a challenge, a CAPTCHA or a rate-limit page no read
//     for 24 hours (AGENTS.md:209-223), the stop kept across restarts (accounts.go).

const (
	linkedInURL = "https://www.linkedin.com"
	// linkedInScrolls is the most scrolls of a search: linkedin-mcp-server's default of three result "pages" of five
	// scrolls each (tools/post.py:42, linkedin/posts.py:16-19 and 79), a stream stopping sooner once it has its
	// limit or a scroll shows no new post. Inferred, no reference sets them: linkedInLoad, how long a page is waited
	// for to render its posts, its empty state or a barrier, looked at every linkedInPoll, and linkedInScroll, the
	// wait after one scroll.
	linkedInLoad    = 20 * time.Second
	linkedInPoll    = time.Second
	linkedInScroll  = 2 * time.Second
	linkedInScrolls = 15
	// linkedInStop is how long a rate-limit page stops the account (AGENTS.md:222-223).
	linkedInStop = 24 * time.Hour
)

var linkedInCadence = Cadence{Gap: 10 * time.Second, Every: 5, Cooldown: 30 * time.Second}

// linkedInSite is LinkedIn as the shared sign-in knows it; li_at is the session cookie linkedin-mcp-server checks
// (core/auth.py:390-395, setup.py:315).
var linkedInSite = signin.Site{
	Name: "LinkedIn", URL: "https://www.linkedin.com/login", Domains: []string{"linkedin.com"},
	Login: [][]string{{"li_at"}},
}

// linkedInBarriers are LinkedIn's sign-in and challenge routes (core/auth.py:29-36).
var linkedInBarriers = []string{"/login", "/authwall", "/checkpoint", "/challenge", "/uas/login",
	"/uas/consumer-email-challenge"}

// linkedInNoise are the media player's lines a post's text leaves out (linkedin/text.py:172-176).
var linkedInNoise = regexp.MustCompile(`^(?:Play|Pause|Playback speed|Turn fullscreen on|Fullscreen|Show captions|` +
	`Close modal window|Media player modal window|Loaded:.*|Remaining time.*|Stream Type.*)$`)

// linkedInURN is a post's URN (linkedin/feed_payload.py:34-36).
var linkedInURN = regexp.MustCompile(`^urn:li:(activity|share|ugcPost):(\d{10,})$`)

// linkedInSnapshot reads what the page rendered at once, so its address, status and posts are one document's
// (core/auth.py:156-159 reads one snapshot for the same reason): the boundaries of linkedin/feed.py:44-77.
const linkedInSnapshot = `(() => {
  const POST_URN = /^urn:li:(activity|share|ugcPost):\d+$/;
  const nav = performance.getEntriesByType('navigation')[0];
  const main = document.querySelector('main');
  const posts = [];
  if (main) {
    const roots = Array.from(main.querySelectorAll('article, [data-urn^="urn:li:"], [data-id^="urn:li:activity:"]'));
    const outermost = roots.filter(root => !roots.some(other => other !== root && other.contains(root)));
    for (const root of outermost) {
      const owned = el => { const a = el.closest('article'); return !a || a === root || !root.contains(a); };
      const ids = [...new Set([root, ...root.querySelectorAll('[data-urn],[data-id]')].filter(owned)
        .flatMap(el => [el.getAttribute('data-urn'), el.getAttribute('data-id')])
        .filter(v => POST_URN.test(v || '')))];
      posts.push({ text: (root.innerText || '').trim(), urn: ids.length === 1 ? ids[0] : '' });
    }
  }
  const empty = !!document.querySelector('[class*="no-results"]') ||
    /\bNo results found\b/.test(main ? main.innerText : '');
  return { href: location.href, status: nav ? (nav.responseStatus || 0) : 0, ready: document.readyState,
    posts, empty };
})()`

// linkedInPage is what one snapshot read.
type linkedInPage struct {
	Href   string `json:"href"`
	Status int    `json:"status"`
	Ready  string `json:"ready"`
	Posts  []struct {
		Text string `json:"text"`
		URN  string `json:"urn"`
	} `json:"posts"`
	Empty bool `json:"empty"`
}

// linkedInBackend is LinkedIn's Posts search, read in the account's tab.
func linkedInBackend() Backend {
	return sessionBackend(Backend{Name: BackendRenderedPage, Host: "linkedin.com", Search: linkedInSearch})
}

func linkedInSearch(ctx context.Context, r Request) (Result, error) {
	page, err := r.Session.Page(ctx)
	if err != nil {
		return Result{}, err
	}

	address := linkedInSearchURL(r.Query, r.From, r.To)

	if r.Read != nil {
		done, err := r.Read(ctx, "linkedin.com", linkedInCadence)
		if err != nil {
			return Result{}, AsFailure(err)
		}

		defer done()
	}

	if err := page.Navigate(ctx, address); err != nil {
		return Result{}, Fail(CodeBroken, "opening LinkedIn's search: %s", err.Error())
	}

	snap, err := linkedInWait(ctx, r, page, address, func(s linkedInPage) bool { return len(s.Posts) > 0 || s.Empty })
	if err != nil {
		return Result{}, err
	}

	for i := 0; i < linkedInScrolls && len(linkedInItems(snap, r.now())) < r.Limit && !snap.Empty; i++ {
		before := len(snap.Posts)

		if err := page.Evaluate(ctx, "window.scrollBy(0, 2400)", nil); err != nil {
			break
		}

		if err := r.wait(ctx, linkedInScroll); err != nil {
			return Result{}, AsFailure(err)
		}

		next, err := linkedInRead(ctx, page)
		if err != nil {
			break
		}

		if f := linkedInBarrier(next); f != nil {
			return Result{}, f
		}

		if !linkedInOurs(next, address) {
			break
		}

		snap = next
		if len(snap.Posts) == before {
			break
		}
	}

	items := linkedInItems(snap, r.now())
	if len(items) > r.Limit {
		items = items[:r.Limit]
	}

	// A refused request of the page's own, its results' above all, is LinkedIn throttling the account; a read that
	// found posts anyway is left as it is, as linkedin-mcp-server leaves a call that succeeds untouched by its record
	// (docs/decisions/2026-09-23-throttling-is-a-status.md:28-30), and an empty state beside it is no clean zero.
	if len(items) == 0 {
		if f := linkedInThrottled(ctx, page, r.now()); f != nil {
			return Result{}, f
		}
	}

	switch {
	case len(items) > 0:
		return Result{Items: items, Detail: fmt.Sprintf("LinkedIn's Posts search showed %d posts", len(items))}, nil
	case !linkedInOurs(snap, address):
		return Result{}, Fail(CodeBroken, "LinkedIn's tab did not show this search's results in time")
	case snap.Empty:
		return Result{Detail: "LinkedIn's Posts search shows no result"}, nil
	default:
		return Result{}, Fail(CodeBroken, "LinkedIn's Posts search rendered %d boundaries and no post with its URN, "+
			"nor its empty state: the page may have changed", len(snap.Posts))
	}
}

// linkedInSearchURL is the Posts tab's search, with the window as its datePosted facet when a past day, week or month
// holds it (linkedin/search_urls.py:62-69, 277-284); a longer window, or none, asks any time.
func linkedInSearchURL(query string, from, to time.Time) string {
	q := "keywords=" + url.QueryEscape(query) + "&origin=FACETED_SEARCH"

	if !from.IsZero() {
		days := to.Sub(from).Hours() / 24
		token := ""

		switch {
		case days <= 1:
			token = "past-24h"
		case days <= 7:
			token = "past-week"
		case days <= 31:
			token = "past-month"
		}

		if token != "" {
			q += "&datePosted=" + url.QueryEscape(`["`+token+`"]`)
		}
	}

	return linkedInURL + "/search/results/content/?" + q
}

// linkedInWait reads the page until done says this search's page rendered, a barrier shows, LinkedIn refused one of the
// page's requests before any post showed, or the load time ends, counted in looks a poll apart. A page that is not this
// search's, an earlier query's still shown, is not taken (radar facebook-topic-adapter.md:7-9: confirm the page is the
// frozen query's before reading it).
func linkedInWait(ctx context.Context, r Request, page Page, address string, done func(linkedInPage) bool,
) (linkedInPage, error) {
	for look := 1; ; look++ {
		snap, err := linkedInRead(ctx, page)
		if err == nil {
			if f := linkedInBarrier(snap); f != nil {
				return linkedInPage{}, f
			}

			ours := linkedInOurs(snap, address)
			if ours && snap.Ready == "complete" && done(snap) {
				return snap, nil
			}

			if !ours || len(snap.Posts) == 0 {
				if f := linkedInThrottled(ctx, page, r.now()); f != nil {
					return linkedInPage{}, f
				}
			}
		}

		if look >= int(linkedInLoad/linkedInPoll) {
			if err != nil {
				return linkedInPage{}, Fail(CodeBroken, "reading LinkedIn's page: %s", err.Error())
			}

			return snap, nil
		}

		if err := r.wait(ctx, linkedInPoll); err != nil {
			return linkedInPage{}, AsFailure(err)
		}
	}
}

// linkedInOurs says a snapshot is of the search at address: LinkedIn's Posts search for the same keywords.
func linkedInOurs(s linkedInPage, address string) bool {
	got, err := url.Parse(s.Href)
	if err != nil {
		return false
	}

	want, err := url.Parse(address)
	if err != nil {
		return false
	}

	return strings.EqualFold(got.Hostname(), want.Hostname()) &&
		strings.TrimSuffix(got.Path, "/") == strings.TrimSuffix(want.Path, "/") &&
		got.Query().Get("keywords") == want.Query().Get("keywords")
}

// linkedInThrottled is LinkedIn refusing a request the page sent since it was opened, 429 or its 999, on its own
// hosts only (linkedin-mcp-server core/throttle.py:43 and 190-210: another host's 429 is no word from LinkedIn, and
// only the requests the call sent count); nil when none was.
func linkedInThrottled(ctx context.Context, page Page, now time.Time) *SourceError {
	// A record that cannot be read says nothing of a throttle.
	responses, err := page.Responses(ctx)
	if err != nil {
		responses = nil
	}

	for i := len(responses) - 1; i >= 0; i-- {
		got := responses[i]
		if got.Status != 429 && got.Status != 999 {
			continue
		}

		u, err := url.Parse(got.URL)
		if err != nil {
			continue
		}

		host := strings.ToLower(u.Hostname())
		if host != "linkedin.com" && !strings.HasSuffix(host, ".linkedin.com") {
			continue
		}

		f := &SourceError{Code: CodeRateLimited, transient: true, Detail: "LinkedIn answered HTTP " +
			strconv.Itoa(got.Status) + " to " + u.Path + ": it is throttling this account"}

		// A numeric Retry-After, the only form linkedin-mcp-server reads (core/throttle.py:152-168).
		if secs, err := strconv.Atoi(strings.TrimSpace(got.RetryAfter)); err == nil && secs > 0 {
			f.Until = now.Add(time.Duration(secs) * time.Second)
		}

		return f
	}

	return nil
}

func linkedInRead(ctx context.Context, page Page) (linkedInPage, error) {
	var snap linkedInPage

	err := page.Evaluate(ctx, linkedInSnapshot, &snap)

	return snap, err
}

// linkedInBarrier is what the page's status and landing say: a throttle, a restriction, a challenge or a sign-in
// wall; nil when none.
func linkedInBarrier(s linkedInPage) *SourceError {
	if s.Status == 429 || s.Status == 999 {
		return &SourceError{Code: CodeRateLimited, Detail: "LinkedIn answered HTTP " + strconv.Itoa(s.Status) +
			": it is throttling this account", transient: true}
	}

	if s.Href == "" {
		return nil
	}

	u, err := url.Parse(s.Href)
	if err != nil {
		return Fail(CodeBroken, "LinkedIn's page has an address that does not parse")
	}

	host := strings.ToLower(u.Hostname())
	if host != "linkedin.com" && !strings.HasSuffix(host, ".linkedin.com") {
		if u.Scheme == "about" {
			return nil
		}

		return Fail(CodeBroken, "LinkedIn's search landed on %s, not LinkedIn", host)
	}

	path := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(path, "/login/login-restriction") || path == "/login/login-restriction" {
		return Fail(CodeRefused, "LinkedIn restricted the account (its login-restriction page)")
	}

	for _, b := range linkedInBarriers {
		if path != b && !strings.HasPrefix(path, b+"/") {
			continue
		}

		if strings.Contains(b, "challenge") || b == "/checkpoint" {
			return Fail(CodeRefused, "LinkedIn showed a security check (%s)", b)
		}

		return Fail(CodeNeedLogin, "LinkedIn sent the account to its sign-in (%s)", b)
	}

	return nil
}

// linkedInItems are the snapshot's posts that name their URN, once each.
func linkedInItems(s linkedInPage, now time.Time) []Item {
	var out []Item

	seen := map[string]bool{}

	for _, p := range s.Posts {
		m := linkedInURN.FindStringSubmatch(p.URN)
		if m == nil || seen[p.URN] {
			continue
		}

		text := linkedInText(p.Text)
		if text == "" {
			continue
		}

		seen[p.URN] = true
		it := Item{
			NativeID: p.URN, URL: linkedInURL + "/feed/update/" + p.URN + "/", Text: text, Direct: true,
		}

		if at, ok := linkedInTime(m[2], now); ok {
			it.PublishedAt = &at
		}

		out = append(out, it)
	}

	return out
}

func linkedInText(text string) string {
	var lines []string

	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !linkedInNoise.MatchString(line) {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, "\n")
}

// linkedInTime is when a LinkedIn post was made, from its id: its high 41 bits are milliseconds since 1970, as X's
// snowflake ids hold theirs since X's epoch (x_api.py:138-151). Inferred: no reference reads LinkedIn's ids so, and
// linkedin-mcp-server leaves a post's date to the page's text (tools/post.py:75-78); a time before LinkedIn's first
// post (2003) or after now is no date.
func linkedInTime(id string, now time.Time) (time.Time, bool) {
	v, err := strconv.ParseUint(id, 10, 63)
	if err != nil {
		return time.Time{}, false
	}

	at := time.UnixMilli(int64(v >> 22)).UTC()
	if at.Year() < 2003 || at.After(now.Add(24*time.Hour)) {
		return time.Time{}, false
	}

	return at, true
}
