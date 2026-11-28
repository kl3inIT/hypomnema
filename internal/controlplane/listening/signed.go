package listening

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The signed-in sources (issue #1291): X and LinkedIn, each read first as the owner's account and then, while the
// account cannot be read (none signed in, signed out, refused, stopped), by the web search's results on its site,
// which need no account: the next free backend (research note §2, the free routes in order: X's cookies first and
// `site:x.com` web search last, LinkedIn's signed-in account then `site:linkedin.com/posts` web search). TikTok
// (issue #1294, tiktok.go) is read as the account, then with yt-dlp, then by its web search's cards (research note §2).
// Each is off until the owner agrees to its consent card (last30days references/reddit-session.md; research note
// §5b, "Off until the owner signs in").

// The signed-in sources' and their backends' names.
const (
	SourceX        = "x"
	SourceLinkedIn = "linkedin"

	BackendXGraphQL     = "graphql"
	BackendRenderedPage = "rendered_page"
	BackendSiteSearch   = "site_search"
)

// The consent cards' fixed wording, each its own source's (last30days references/reddit-session.md:13: what the
// engine then does as the account, that automated use of a signed-in account "may breach Reddit's terms and put that
// account at risk", which cookie is read and where it is sent).
const (
	xConsent = "Reading X as your account may break X's terms: automated use of a signed-in account can get it " +
		"limited, locked or suspended, and users of twscrape, which reads X the same way, report suspensions since " +
		"2026-11-21. Sign in a secondary account made for this, never your own. Hypomnema then searches X as that " +
		"account, one search at a time, 10 seconds apart. Only X's cookies are kept, in the vault: they are never " +
		"shown or logged and go only to x.com. If X refuses the account or asks for a check, Hypomnema stops reading " +
		"it until you sign in again."
	linkedInConsent = "LinkedIn's terms forbid automated access, so reading it as your account may bring a security " +
		"check (a challenge or a CAPTCHA) or a restriction of the account. Sign in a secondary account made for this, " +
		"never your own. Hypomnema then opens LinkedIn's Posts search as that account and reads only the pages " +
		"LinkedIn shows, one page at a time, 10 seconds apart. Only LinkedIn's cookies are kept, in the vault: they " +
		"are never shown or logged and go only to linkedin.com. After a security check it stops until you sign in " +
		"again; after a rate-limit page, for 24 hours."
	// tiktokConsent names, besides the terms, the failure TikTok-Api's users report (README.md:110, its issues #1116,
	// #1242 and #1311: empty answers once TikTok takes the reader for a bot).
	tiktokConsent = "TikTok's terms forbid automated access, so reading it as your account may bring a security " +
		"check (a CAPTCHA), a limit or a ban of the account; users of TikTok-Api, which reads TikTok the same way, " +
		"report TikTok answering empty pages once it takes the reader for a bot. Sign in a secondary account made " +
		"for this, never your own. Hypomnema then opens TikTok as that account in its own tab of the shared browser " +
		"and asks TikTok's web API from there for a search's videos and the top comments of a few of them, one " +
		"request at a time, 12 seconds apart. Only TikTok's cookies are kept, in the vault: they are never shown or " +
		"logged and go only to tiktok.com. If TikTok refuses the account or answers empty, Hypomnema stops reading " +
		"it until you sign in again; it never solves a CAPTCHA."
)

// SignedSources are X, LinkedIn and TikTok; client reads X's search, e.YtDlp TikTok's videos the web search finds (nil:
// the yt-dlp on the control plane's PATH, as YouTube's). Inferred, no reference names them: each one's live query,
// which a sign-in's verification searches too, is a topic the owner researches (VETC, the web's and Reddit's live
// query, sources.go), and the LinkedIn and TikTok cards' "a secondary account" extends to them what the owner asks of
// X in issue #1291.
func SignedSources(e Endpoints, client *http.Client) []Platform {
	if e.YtDlp == nil {
		e.YtDlp = runYtDlp
	}

	return []Platform{
		{
			Name: SourceX, LiveQuery: "VETC",
			Backends: []Backend{xBackend(client), siteSearchBackend("x.com", xStatus)},
			Signed:   &Signed{Site: xSite, Consent: xConsent, Backend: BackendXGraphQL},
			Adds: "Posts on X with their author, date and likes, reposts and replies, searched as your account; " +
				"without one, X's posts the web search finds.",
		},
		{
			Name: SourceLinkedIn, LiveQuery: "Vietnam",
			Backends: []Backend{linkedInBackend(), siteSearchBackend("linkedin.com/posts", linkedInPost)},
			Signed: &Signed{
				Site: linkedInSite, Consent: linkedInConsent, Backend: BackendRenderedPage, Throttle: linkedInStop,
			},
			Adds: "LinkedIn posts from its Posts search, read as your account; without one, LinkedIn posts the web " +
				"search finds.",
		},
		{
			Name: SourceTikTok, LiveQuery: "VETC",
			Backends: []Backend{
				tiktokBackend(), tiktokYtDlpBackend(e.YtDlp), siteSearchBackend("tiktok.com", tiktokPost),
			},
			Signed: &Signed{Site: tiktokSite, Consent: tiktokConsent, Backend: BackendWebAPI},
			Adds: "TikTok videos with their author, date, views, likes, comments and shares, and the top comments of " +
				"the most watched, searched as your account; without one, the TikTok videos the web search finds, " +
				"read with yt-dlp.",
		},
	}
}

var (
	// xStatusPath is a post's address on X: /<user>/status/<id> (bird_x.py:1227-1236).
	xStatusPath = regexp.MustCompile(`^/([A-Za-z0-9_]{1,15})/status/(\d+)`)
	// linkedInPostPath is a post's public address, its slug ending in the post's kind and id
	// (linkedin/feed_payload.py:21-24).
	linkedInPostPath = regexp.MustCompile(`^/posts/[A-Za-z0-9_%-]+?-(?:ugcPost|activity|share)-(\d+)-[A-Za-z0-9_-]+`)
)

// xStatus is a web result that is a post on X: its id, address, author and the date its id holds.
func xStatus(u *url.URL, _ time.Time) (Item, bool) {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "x.com" && host != "twitter.com" && host != "mobile.twitter.com" {
		return Item{}, false
	}

	m := xStatusPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Item{}, false
	}

	at, ok := snowflakeTime(m[2])
	if !ok {
		return Item{}, false
	}

	return Item{NativeID: m[2], URL: xURL + "/" + m[1] + "/status/" + m[2], Author: m[1], PublishedAt: &at}, true
}

// linkedInPost is a web result that is a LinkedIn post, known by the same URN its rendered search gives it.
func linkedInPost(u *url.URL, now time.Time) (Item, bool) {
	host := strings.ToLower(u.Hostname())
	if host != "linkedin.com" && !strings.HasSuffix(host, ".linkedin.com") {
		return Item{}, false
	}

	m := linkedInPostPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Item{}, false
	}

	it := Item{NativeID: "urn:li:activity:" + m[1], URL: "https://www.linkedin.com" + u.Path}
	if at, ok := linkedInTime(m[1], now); ok {
		it.PublishedAt = &at
	}

	return it, true
}

// siteSearchBackend reads a site's posts through the web search, `site:<site> <query>`, keeping the results that are
// its posts (post says which, with the post's id and the date it holds), as last30days reads Reddit's threads the web
// search shows (reddit_discover.py:322-350) and the research note's last free route for X and LinkedIn.
func siteSearchBackend(site string, post func(*url.URL, time.Time) (Item, bool)) Backend {
	return Backend{
		Name: BackendSiteSearch, Host: BackendWebSearch, Probe: needsWeb,
		Search: func(ctx context.Context, r Request) (Result, error) {
			if !r.From.IsZero() {
				ctx = websearch.WithWindow(ctx, r.From, r.To)
			}

			a, err := r.Web(ctx, "site:"+site+" "+r.Query, min(r.Limit, websearch.MaxResults))
			if errors.Is(err, websearch.ErrNoResults) {
				return Result{Detail: "no provider found anything"}, nil
			}

			if err != nil {
				return Result{}, webFailure(err)
			}

			var res Result

			for _, w := range a.Results {
				u, err := url.Parse(w.URL)
				if err != nil {
					continue
				}

				it, ok := post(u, r.now())
				if !ok {
					res.dropped.OffTopic++

					continue
				}

				it.Title, it.Text = w.Title, w.Snippet
				res.Items = append(res.Items, it)
			}

			res.Detail, res.provider, res.windowBasis = answeredBy(a), a.Provider, a.WindowBasis

			return res, nil
		},
	}
}
