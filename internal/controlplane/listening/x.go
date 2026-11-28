package listening

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
)

// X read as the owner's account (issue #1291): its own web client's GraphQL search, SearchTimeline, with the
// account's auth_token and ct0 cookies, as bird does, the route that heads last30days' X chain (env.py:1860-1867,
// `_X_BACKEND_ORDER = ("bird", "xai", "xurl", "xquik")`; bird_x.py runs the vendored bird-search, Peter
// Steinberger's @steipete/bird, MIT: lib/twitter-client-search.js, twitter-client-base.js, twitter-client-utils.js,
// runtime-query-ids.js, adapted here in Go; notice in third_party/bird). openmagpie (twikit) and Agent-Reach (opencli)
// read X the same way; only the official API is paid (owner, 2026-11-27: no paid route).
//
//   - The query is the subquery with the window as X's own operators, since:<from> until:<to+1>, until being
//     exclusive (bird_x.py:324-335 _date_filters), on the Top tab, which a topic search reads (bird_x.py:843-847).
//   - Pages of 20 (twitter-client-search.js:50), the next by the Bottom cursor (twitter-client-utils.js:512-550),
//     until the stream's limit, a page that adds nothing, or the third page.
//   - Each post's date is its snowflake id's (x_api.py:106-151 _decode_snowflake, as grok_x._validate_items,
//     lib/grok_x.py:415-433, dates a post); a post whose id does not decode is dropped, never kept undated.
//   - SearchTimeline's query ids rotate. They and the web client's bearer are read from X's own JavaScript bundles,
//     kept a day, and read again when X answers 404 or a GraphQL validation error (runtime-query-ids.js:5-45,
//     twitter-client-base.js:96-135, twitter-client-search.js:157-188); bird's baked ids stand when none is found
//     (query-ids.json, twitter-client-base.js:134-136). Departure: bird bakes the bearer into its code
//     (twitter-client-base.js:186); it is read here from the same bundles, so the repository holds no token.
//   - 401, or X's "Could not authenticate you" (code 32), is the session's end: need_login (bird_x.py:153-161); 403 a
//     refusal of the account (locked or suspended); 429 a rate limit until X's x-rate-limit-reset.
//   - One read at a time, 10 seconds apart and 30 seconds more after every five (trend-opportunity-radar
//     collection-pacing-contract.md:5-9; Agent-Reach sessions.md:12, two searches of a site at least 10 s apart).

const (
	xURL = "https://x.com"
	// xPageSize is a SearchTimeline page (twitter-client-search.js:50); xPages the most a stream reads: a stream of
	// MaxResults fills in three.
	xPageSize = 20
	xPages    = 3
	// xKnownFor is how long the query ids and the bearer read from X's bundles are kept (runtime-query-ids.js:6,
	// DEFAULT_TTL_MS, a day); xBundles how many bundles are read for them (:10, DEFAULT_MAX_BUNDLES); xBundleBytes the
	// most read of one (:9, DEFAULT_MAX_RESPONSE_BYTES).
	xKnownFor    = 24 * time.Hour
	xBundles     = 30
	xBundleBytes = 5 << 20
	// xUserAgent is the desktop Chrome bird sends (twitter-client-base.js:74-76).
	xUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/131.0.0.0 Safari/537.36"
)

// xCadence is X's pace as an account: radar's X interval and its cooldown after five reads.
var xCadence = Cadence{Gap: 10 * time.Second, Every: 5, Cooldown: 30 * time.Second}

// xSite is X as the shared sign-in knows it: its login page, its domains and the cookies a session has
// (last30days env.py and bird's cookies.js read auth_token and ct0; CONFIGURATION.md:318-319).
var xSite = signin.Site{
	Name: "X", URL: "https://x.com/i/flow/login", Domains: []string{"x.com", "twitter.com"},
	Login: [][]string{{"auth_token"}, {"ct0"}},
}

// xQueryIDs are bird's SearchTimeline ids, its query-ids.json's first, then those its client tries after it
// (twitter-client-base.js:134-136).
var xQueryIDs = []string{"6AAys3t42mosm_yTI_QENg", "M1jEez78PEfVfbQLvlWMvQ", "5h0kNbk3ii97rmfY6CdgAA",
	"Tp1sewRU1AsZpBWhqCZicQ"}

// xDiscoveryPages are where X's bundles are linked from (runtime-query-ids.js:14-19).
var xDiscoveryPages = []string{"https://x.com/?lang=en", "https://x.com/explore"}

var (
	// xBundleURL is one of X's web client's bundles (runtime-query-ids.js:20).
	xBundleURL = regexp.MustCompile(`https://abs\.twimg\.com/responsive-web/client-web(?:-legacy)?/[A-Za-z0-9.-]+\.js`)
	// xOperations find an operation's query id beside its name, either order (runtime-query-ids.js:23-44).
	xOperations = []*regexp.Regexp{
		regexp.MustCompile(`queryId\s*:\s*["']([A-Za-z0-9_-]+)["']\s*,\s*operationName\s*:\s*["']SearchTimeline["']`),
		regexp.MustCompile(`operationName\s*:\s*["']SearchTimeline["']\s*,\s*queryId\s*:\s*["']([A-Za-z0-9_-]+)["']`),
	}
	// xBearerToken is the web client's public bearer as its bundle writes it: a run of A's then its key (inferred
	// from the value bird bakes, twitter-client-base.js:186, which starts with 21 A's).
	xBearerToken = regexp.MustCompile(`["'](A{21}[A-Za-z0-9%]{40,})["']`)
	xQueryID     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// xSearchFeatures are the features bird sends with a search (twitter-client-features.js:69-110).
var xSearchFeatures = map[string]bool{
	"rweb_video_screen_enabled": true, "profile_label_improvements_pcf_label_in_post_enabled": true,
	"responsive_web_profile_redirect_enabled": true, "rweb_tipjar_consumption_enabled": true,
	"verified_phone_label_enabled": false, "creator_subscriptions_tweet_preview_api_enabled": true,
	"responsive_web_graphql_timeline_navigation_enabled": true, "responsive_web_graphql_exclude_directive_enabled": true,
	"responsive_web_graphql_skip_user_profile_image_extensions_enabled": false, "premium_content_api_read_enabled": false,
	"communities_web_enable_tweet_community_results_fetch": true, "c9s_tweet_anatomy_moderator_badge_enabled": true,
	"responsive_web_grok_analyze_button_fetch_trends_enabled": false,
	"responsive_web_grok_analyze_post_followups_enabled":      false, "responsive_web_grok_annotations_enabled": false,
	"responsive_web_jetfuel_frame": true, "post_ctas_fetch_enabled": true,
	"responsive_web_grok_share_attachment_enabled": true, "responsive_web_edit_tweet_api_enabled": true,
	"graphql_is_translatable_rweb_tweet_is_translatable_enabled": true, "view_counts_everywhere_api_enabled": true,
	"longform_notetweets_consumption_enabled": true, "responsive_web_twitter_article_tweet_consumption_enabled": true,
	"tweet_awards_web_tipping_enabled": false, "responsive_web_grok_show_grok_translated_post": false,
	"responsive_web_grok_analysis_button_from_backend": true, "creator_subscriptions_quote_tweet_preview_enabled": false,
	"freedom_of_speech_not_reach_fetch_enabled": true, "standardized_nudges_misinfo": true,
	"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
	"rweb_video_timestamps_enabled":                                           true, "longform_notetweets_rich_text_read_enabled": true,
	"longform_notetweets_inline_media_enabled": true, "responsive_web_grok_image_annotation_enabled": true,
	"responsive_web_grok_imagine_annotation_enabled":                 true,
	"responsive_web_grok_community_note_auto_translation_is_enabled": false, "articles_preview_enabled": true,
	"responsive_web_enhance_cards_enabled": false,
}

// xReader reads X's search as the account, and keeps what it read of X's bundles.
type xReader struct {
	client *http.Client

	mu     sync.Mutex
	bearer string
	ids    []string
	read   time.Time
}

// xBackend is X's GraphQL search as the account.
func xBackend(client *http.Client) Backend {
	x := &xReader{client: client}

	return sessionBackend(Backend{Name: BackendXGraphQL, Host: "x.com", Search: x.search})
}

// xClient is what X's GraphQL needs: the bearer and the query ids to try.
type xClient struct {
	bearer string
	ids    []string
}

// known is the bearer and the ids, read from X's bundles when they are not known or are a day old, or again.
func (x *xReader) known(ctx context.Context, r Request, again bool) (xClient, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	if !again && x.bearer != "" && r.now().Sub(x.read) < xKnownFor {
		return xClient{bearer: x.bearer, ids: x.ids}, nil
	}

	bearer, ids, err := x.discover(ctx, r)
	if err != nil {
		if x.bearer != "" {
			// What was read before stands while X's bundles cannot be read.
			return xClient{bearer: x.bearer, ids: x.ids}, nil
		}

		return xClient{}, err
	}

	x.bearer, x.read = bearer, r.now()
	x.ids = slices.Compact(append(ids, xQueryIDs...))

	return xClient{bearer: x.bearer, ids: x.ids}, nil
}

// discover reads X's bundles, linked from its pages, for the web client's bearer and SearchTimeline's query id; no
// cookie goes with them (runtime-query-ids.js:11-13, only x.com and abs.twimg.com are read).
func (x *xReader) discover(ctx context.Context, r Request) (string, []string, error) {
	var bundles []string

	for _, page := range xDiscoveryPages {
		a, err := x.discoveryPage(ctx, r, page)
		if err != nil {
			continue
		}

		for _, u := range xBundleURL.FindAllString(string(a.body), -1) {
			if !slices.Contains(bundles, u) && len(bundles) < xBundles {
				bundles = append(bundles, u)
			}
		}

		if len(bundles) > 0 {
			break
		}
	}

	if len(bundles) == 0 {
		return "", nil, Fail(CodeBroken, "x.com linked none of its web client's bundles")
	}

	var (
		bearer string
		ids    []string
	)

	for _, u := range bundles {
		if !xDiscoveryURL(u) {
			continue
		}

		a, err := call{host: "abs.twimg.com", gap: 0, address: u, userAgent: xUserAgent, accept: "*/*"}.do(ctx,
			x.client, r)
		if err != nil {
			continue
		}

		body := a.body[:min(len(a.body), xBundleBytes)]

		if m := xBearerToken.FindSubmatch(body); bearer == "" && m != nil {
			bearer = string(m[1])
		}

		for _, re := range xOperations {
			for _, m := range re.FindAllSubmatch(body, -1) {
				if id := string(m[1]); xQueryID.MatchString(id) && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}

		if bearer != "" && len(ids) > 0 {
			break
		}
	}

	if bearer == "" {
		return "", nil, Fail(CodeBroken, "x.com's web client bundles hold no bearer: its client may have changed")
	}

	return bearer, ids, nil
}

// discoveryPage reads one of X's pages for its bundles, following its redirects by hand, each hop only to an https
// address of x.com or abs.twimg.com with no credentials and no port (runtime-query-ids.js:12, 80-101,
// validateDiscoveryUrl; :11, at most five redirects).
func (x *xReader) discoveryPage(ctx context.Context, r Request, address string) (answer, error) {
	for range maxRedirects + 1 {
		if !xDiscoveryURL(address) {
			return answer{}, Fail(CodeBroken, "x.com sent its page to an address its bundles are not read from")
		}

		a, err := call{host: "x.com/bundles", gap: webGap, address: address, userAgent: xUserAgent,
			accept: "text/html", redirect: true}.do(ctx, x.client, r)
		if err != nil || a.location == "" {
			return a, err
		}

		address = a.location
	}

	return answer{}, Fail(CodeBroken, "x.com redirected its page too many times")
}

// xDiscoveryURL says an address is one X's bundles may be read from (runtime-query-ids.js:80-101).
func xDiscoveryURL(address string) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}

	host := strings.ToLower(u.Hostname())

	return host == "x.com" || host == "abs.twimg.com"
}

// search reads a subquery from X's search, page after page.
func (x *xReader) search(ctx context.Context, r Request) (Result, error) {
	jar, err := r.Session.Jar(ctx)
	if err != nil {
		return Result{}, err
	}

	if missing := xSite.Missing(jar); missing != "" {
		return Result{}, Fail(CodeNeedLogin, "the kept X session lacks %s", missing)
	}

	known, err := x.known(ctx, r, false)
	if err != nil {
		return Result{}, err
	}

	query := strings.TrimSpace(r.Query + " " + xDateFilters(r.From, r.To))

	var (
		res     Result
		cursor  string
		seen    = map[string]bool{}
		refresh = true
	)

	for page := 0; page < xPages && len(res.Items) < r.Limit; page++ {
		tweets, next, err := x.page(ctx, r, jar, known, query, cursor, min(xPageSize, r.Limit-len(res.Items)))

		var stale *xStaleError
		if errors.As(err, &stale) && refresh {
			refresh = false

			if known, err = x.known(ctx, r, true); err == nil {
				tweets, next, err = x.page(ctx, r, jar, known, query, cursor,
					min(xPageSize, r.Limit-len(res.Items)))
			}
		}

		if errors.As(err, &stale) {
			err = Fail(CodeBroken, "X refused every SearchTimeline query id (%s): its web client changed", stale.detail)
		}

		if err != nil {
			if len(res.Items) == 0 {
				return Result{}, err
			}

			res.Failed = AsFailure(err)

			break
		}

		added := 0

		for _, t := range tweets {
			it, ok := t.item()
			if !ok {
				res.dropped.MissingDate++

				continue
			}

			if !seen[it.NativeID] {
				seen[it.NativeID] = true
				res.Items = append(res.Items, it)
				added++
			}
		}

		if next == "" || next == cursor || added == 0 {
			break
		}

		cursor = next
	}

	res.Detail = fmt.Sprintf("X's search read %d posts as the account", len(res.Items))

	return res, nil
}

// xStaleError is X refusing the query ids: they rotated.
type xStaleError struct{ detail string }

func (e *xStaleError) Error() string { return "stale query ids: " + e.detail }

// page reads one page of X's search, trying the query ids in turn, a 404 passing to the next
// (twitter-client-search.js:86-156).
func (x *xReader) page(ctx context.Context, r Request, jar []browser.Cookie, known xClient, query, cursor string,
	count int,
) ([]xTweet, string, error) {
	variables := map[string]any{"rawQuery": query, "count": count, "querySource": "typed_query", "product": "Top"}
	if cursor != "" {
		variables["cursor"] = cursor
	}

	vars, err := json.Marshal(variables)
	if err != nil {
		return nil, "", err
	}

	lastStatus := ""

	for _, id := range known.ids {
		body, err := json.Marshal(map[string]any{"features": xSearchFeatures, "queryId": id})
		if err != nil {
			return nil, "", err
		}

		address := xURL + "/i/api/graphql/" + id + "/SearchTimeline?" + url.Values{"variables": {string(vars)}}.Encode()

		var status int

		a, err := call{
			host: "x.com", method: http.MethodPost, address: address,
			body: body, contentType: "application/json", accept: "*/*", userAgent: xUserAgent,
			header: x.headers(jar, known.bearer), redirect: true,
			failure: func(code int, h http.Header, now time.Time) *SourceError {
				status = code

				return xFailure(code, h, now)
			},
		}.paced(ctx, x.client, r, xCadence)

		if status == http.StatusNotFound || status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
			lastStatus = "HTTP " + strconv.Itoa(status)

			continue
		}

		if err != nil {
			return nil, "", err
		}

		if a.location != "" {
			return nil, "", Fail(CodeNeedLogin, "X sent the search to %s, its sign-in", hostOf(a.location))
		}

		return x.parse(a.body)
	}

	return nil, "", &xStaleError{detail: lastStatus}
}

// paced makes a call in a signed-in account's turn at its site, at its cadence (Pacer.Read), where call.do waits for
// a host's turn at a gap. A failure is the backend's to rest: no path of it rests apart.
func (c call) paced(ctx context.Context, client *http.Client, r Request, cadence Cadence) (answer, error) {
	if r.Read != nil {
		done, err := r.Read(ctx, c.host, cadence)
		if err != nil {
			return answer{}, AsFailure(err)
		}

		defer done()
	}

	c.host, c.path = "", ""

	return c.do(ctx, client, r)
}

// headers are the web client's own headers with the account's cookies, which go to x.com only
// (twitter-client-base.js:182-208).
func (x *xReader) headers(jar []browser.Cookie, bearer string) http.Header {
	id := make([]byte, 16)
	_, _ = rand.Read(id)

	return http.Header{
		"Authorization":             {"Bearer " + bearer},
		"X-Csrf-Token":              {signin.Value(jar, "ct0")},
		"X-Twitter-Auth-Type":       {"OAuth2Session"},
		"X-Twitter-Active-User":     {"yes"},
		"X-Twitter-Client-Language": {"en"},
		"X-Client-Transaction-Id":   {hex.EncodeToString(id)},
		"Accept-Language":           {"en-US,en;q=0.9"},
		"Cookie":                    {xSite.Header(jar, "x.com")},
		"Origin":                    {xURL},
		"Referer":                   {xURL + "/"},
	}
}

// xFailure is what an X status that is not 200 means: 401 the session's end, 403 a refusal of the account, 429 a
// rate limit until X's own reset (x-rate-limit-reset, a Unix time: retryAfter's epoch rule), the rest as HTTPFailure.
func xFailure(status int, h http.Header, now time.Time) *SourceError {
	f := HTTPFailure(status, h, now)

	switch status {
	case http.StatusUnauthorized:
		f.Detail = "X answered 401: the account's session ended"
	case http.StatusForbidden:
		f.Detail = "X answered 403: the account is locked, suspended or refused"
	case http.StatusTooManyRequests:
		if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-Rate-Limit-Reset")), 10, 64); err == nil &&
			time.Unix(reset, 0).After(now) {
			f.Until = time.Unix(reset, 0)
		}
	}

	return f
}

// xDateFilters are the window as X's operators: since its first day, until the day after its last, X's until being
// exclusive (bird_x.py:324-335). No window: none.
func xDateFilters(from, to time.Time) string {
	if from.IsZero() {
		return ""
	}

	out := "since:" + from.UTC().Format(time.DateOnly)
	if !to.IsZero() {
		out += " until:" + to.UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	}

	return out
}

// xAnswer is SearchTimeline's answer, the parts read.
type xAnswer struct {
	Data struct {
		SearchByRawQuery struct {
			SearchTimeline struct {
				Timeline struct {
					Instructions []xInstruction `json:"instructions"`
				} `json:"timeline"`
			} `json:"search_timeline"`
		} `json:"search_by_raw_query"`
	} `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Code       int    `json:"code"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

type xInstruction struct {
	Entries []xEntry `json:"entries"`
	Entry   *xEntry  `json:"entry"`
}

type xEntry struct {
	Content struct {
		CursorType  string        `json:"cursorType"`
		Value       string        `json:"value"`
		ItemContent *xItemContent `json:"itemContent"`
		Item        *struct {
			ItemContent *xItemContent `json:"itemContent"`
		} `json:"item"`
		Items []struct {
			Item struct {
				ItemContent *xItemContent `json:"itemContent"`
			} `json:"item"`
			ItemContent *xItemContent `json:"itemContent"`
		} `json:"items"`
	} `json:"content"`
}

type xItemContent struct {
	TweetResults struct {
		Result *xTweet `json:"result"`
	} `json:"tweet_results"`
}

// xTweet is one post as SearchTimeline gives it (twitter-client-utils.js:383-432 mapTweetResult).
type xTweet struct {
	RestID string  `json:"rest_id"`
	Tweet  *xTweet `json:"tweet"`
	Core   struct {
		UserResults struct {
			Result struct {
				Legacy struct {
					ScreenName string `json:"screen_name"`
				} `json:"legacy"`
				Core struct {
					ScreenName string `json:"screen_name"`
				} `json:"core"`
			} `json:"result"`
		} `json:"user_results"`
	} `json:"core"`
	Legacy struct {
		FullText          string  `json:"full_text"`
		FavoriteCount     float64 `json:"favorite_count"`
		RetweetCount      float64 `json:"retweet_count"`
		ReplyCount        float64 `json:"reply_count"`
		QuoteCount        float64 `json:"quote_count"`
		InReplyToStatusID string  `json:"in_reply_to_status_id_str"`
	} `json:"legacy"`
	NoteTweet struct {
		NoteTweetResults struct {
			Result struct {
				Text string `json:"text"`
			} `json:"result"`
		} `json:"note_tweet_results"`
	} `json:"note_tweet"`
}

// unwrapTweet is a post behind its visibility wrapper (twitter-client-utils.js:374-382).
func unwrapTweet(t *xTweet) *xTweet {
	if t != nil && t.Tweet != nil {
		return t.Tweet
	}

	return t
}

// parse reads a page: its posts and the Bottom cursor; GraphQL errors beside no post are the answer's failure.
func (x *xReader) parse(body []byte) ([]xTweet, string, error) {
	var a xAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, "", Fail(CodeBroken, "X's search answered what is not its JSON: %s", err.Error())
	}

	var (
		tweets []xTweet
		cursor string
		seen   = map[string]bool{}
	)

	add := func(c *xItemContent) {
		if c == nil {
			return
		}

		t := unwrapTweet(c.TweetResults.Result)
		if t != nil && t.RestID != "" && !seen[t.RestID] {
			seen[t.RestID] = true
			tweets = append(tweets, *t)
		}
	}

	for _, in := range a.Data.SearchByRawQuery.SearchTimeline.Timeline.Instructions {
		entries := in.Entries
		if in.Entry != nil {
			entries = append(entries, *in.Entry)
		}

		for _, e := range entries {
			if e.Content.CursorType == "Bottom" && e.Content.Value != "" {
				cursor = e.Content.Value
			}

			add(e.Content.ItemContent)

			if e.Content.Item != nil {
				add(e.Content.Item.ItemContent)
			}

			for _, it := range e.Content.Items {
				add(it.Item.ItemContent)
				add(it.ItemContent)
			}
		}
	}

	if len(tweets) == 0 && len(a.Errors) > 0 {
		var msgs []string

		for _, e := range a.Errors {
			// Could not authenticate you: the session ended (bird_x.py:153-161).
			if e.Code == 32 || strings.Contains(strings.ToLower(e.Message), "could not authenticate") {
				return nil, "", Fail(CodeNeedLogin, "X could not authenticate the account: its session ended")
			}

			if e.Extensions.Code == "GRAPHQL_VALIDATION_FAILED" {
				return nil, "", &xStaleError{detail: "GRAPHQL_VALIDATION_FAILED"}
			}

			msgs = append(msgs, e.Message)
		}

		return nil, "", Fail(CodeBroken, "X's search failed: %s", oneLine(strings.Join(msgs, "; ")))
	}

	return tweets, cursor, nil
}

// item is a post as an Item, dated by its snowflake id; false when the id gives no date or the post has no text or
// author.
func (t xTweet) item() (Item, bool) {
	user := t.Core.UserResults.Result.Legacy.ScreenName
	if user == "" {
		user = t.Core.UserResults.Result.Core.ScreenName
	}

	text := t.NoteTweet.NoteTweetResults.Result.Text
	if text == "" {
		text = t.Legacy.FullText
	}

	published, ok := snowflakeTime(t.RestID)
	if !ok || user == "" || strings.TrimSpace(text) == "" {
		return Item{}, false
	}

	return Item{
		NativeID: t.RestID, URL: xURL + "/" + user + "/status/" + t.RestID, Author: user, Text: text,
		PublishedAt: &published, Direct: true, Parent: t.Legacy.InReplyToStatusID,
		Engagement: map[string]float64{
			"likes": t.Legacy.FavoriteCount, "reposts": t.Legacy.RetweetCount, "replies": t.Legacy.ReplyCount,
			"quotes": t.Legacy.QuoteCount,
		},
	}, true
}

// snowflakeEpoch is X's snowflake epoch, 2010-11-04T01:42:54.657Z (x_api.py:106-107).
const snowflakeEpoch = 1288834974657

// snowflakeTime is when an X post was made, from its id (x_api.py:138-151): the id's high bits are milliseconds since
// X's epoch.
func snowflakeTime(id string) (time.Time, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(id), 10, 63)
	if err != nil || v == 0 {
		return time.Time{}, false
	}

	return time.UnixMilli(int64(v>>22) + snowflakeEpoch).UTC(), true
}
