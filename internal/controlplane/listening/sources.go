package listening

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The sources this control plane reads (issue #1287): the web, through the web search chain, and Reddit, first
// through its keyless public JSON, then through the web search's Reddit threads filled in from the Arctic Shift
// archive (owner, 2026-11-27: Reddit's JSON answers 403 from the control plane's network, as last30days found from
// every network since 2026-05-28, reddit_public.py:3-6; its keyless floor is that second route, reddit_keyless.py
// _web_floor and lib/backends.py:505-532).

// The sources' and backends' names, as a plan and the answer give them.
const (
	SourceWeb    = "web"
	SourceReddit = "reddit"

	BackendWebSearch  = "web_search"
	BackendRedditJSON = "reddit_json"
	BackendWebArctic  = "web_arctic"
)

const (
	redditURL = "https://www.reddit.com"
	arcticURL = "https://arctic-shift.photon-reddit.com"
	// redditUserAgent is in the form Reddit's API rules ask, <platform>:<app id>:<version> (<contact>), which its
	// keyless feeds serve when they refuse a browser's or a library's (openmagpie connectors/reddit/connector.py:39-60).
	redditUserAgent = "linux:hypomnema:v1 (+https://github.com/kl3inIT/hypomnema)"
	// redditGap is one keyless Reddit call a second (last30days DEFAULT_REDDIT_KEYLESS_RATE, lib/http.py:2413-2416).
	redditGap = time.Second
	// arcticGap is Arctic Shift's pace between two lookups (last30days reddit_arctic.PACE_SECONDS: it answers 422
	// "slow down" otherwise).
	arcticGap = 400 * time.Millisecond
	// arcticIDs is the most ids one lookup asks (last30days reddit_arctic.IDS_BATCH).
	arcticIDs = 100
	// redditThreads is how many results the web search is asked for Reddit threads (last30days
	// reddit_discover.RESULTS_PER_QUERY).
	redditThreads = 20
	// pendingScore is how young an archived post's score is withheld: Arctic Shift reads it once, soon after the post
	// (last30days reddit_refs.PENDING_SECONDS, 36 hours).
	pendingScore = 36 * time.Hour
	// maxBody bounds a source's answer.
	maxBody = 8 << 20
	// callTimeout bounds one call to a source (last30days reddit_public: 15 seconds).
	callTimeout = 15 * time.Second
)

// Endpoints are the sources' addresses; empty: theirs. Tests name their own.
type Endpoints struct {
	RedditURL string
	ArcticURL string
}

// Sources are the sources and their backends, in order.
func Sources(e Endpoints, client *http.Client) []Platform {
	if e.RedditURL == "" {
		e.RedditURL = redditURL
	}

	if e.ArcticURL == "" {
		e.ArcticURL = arcticURL
	}

	e.RedditURL = strings.TrimRight(e.RedditURL, "/")
	e.ArcticURL = strings.TrimRight(e.ArcticURL, "/")

	return []Platform{
		{Name: SourceWeb, Backends: []Backend{webBackend()}},
		{Name: SourceReddit, Backends: []Backend{redditJSONBackend(e, client), webArcticBackend(e, client)}},
	}
}

// needsWeb is the probe of a backend that asks the web search chain.
func needsWeb(r Request) *SourceError {
	if r.Web == nil {
		return Fail(CodeBroken, "this control plane has no web search")
	}

	return nil
}

// webBackend reads the web through the web search chain: its providers in the owner's order, each failure noted
// (websearch.Service.Search).
func webBackend() Backend {
	return Backend{
		Name: BackendWebSearch, Host: BackendWebSearch, Probe: needsWeb,
		Search: func(ctx context.Context, r Request) (Result, error) {
			a, err := r.Web(ctx, r.Query, min(r.Limit, websearch.MaxResults))
			if errors.Is(err, websearch.ErrNoResults) {
				return Result{Detail: "no provider found anything"}, nil
			}

			if err != nil {
				return Result{}, webFailure(err)
			}

			items := make([]Item, 0, len(a.Results))
			for _, res := range a.Results {
				items = append(items, Item{
					NativeID: NormalizeURL(res.URL), URL: res.URL, Title: res.Title, Text: res.Snippet,
					Container: hostOf(res.URL), PublishedAt: parseDate(res.PublishedAt),
				})
			}

			return Result{Items: items, Detail: answeredBy(a)}, nil
		},
	}
}

// webFailure is a web search's error as a failure: every provider resting is a rate limit until the first is free.
func webFailure(err error) error {
	var resting *websearch.RestingError
	if errors.As(err, &resting) {
		return &SourceError{Code: CodeRateLimited, Detail: "every web search provider rests", Until: resting.Until}
	}

	return AsFailure(err)
}

// answeredBy says which provider answered, after which failed.
func answeredBy(a websearch.Answer) string {
	out := "answered by " + a.Provider
	for _, f := range a.Errors {
		out += "; " + f.Provider + " failed (" + f.Reason + ")"
	}

	return out
}

// redditJSONBackend reads Reddit's keyless public search, /search.json (last30days reddit_public.py endpoints; cn
// lib/reddit.py). A 403, or a page in place of JSON, is a refusal of this network, and rests 6 hours.
func redditJSONBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.RedditURL)

	return Backend{
		Name: BackendRedditJSON, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			q := url.Values{
				"q": {r.Query}, "sort": {"relevance"}, "t": {redditBucket(r.To.Sub(r.From))},
				"limit": {strconv.Itoa(r.Limit)}, "raw_json": {"1"}, "type": {"link"},
			}

			var listing struct {
				Data struct {
					Children []struct {
						Data redditPost `json:"data"`
					} `json:"children"`
				} `json:"data"`
			}

			address := e.RedditURL + "/search.json?" + q.Encode()
			if err := getJSON(ctx, client, r, host, redditGap, false, address, &listing); err != nil {
				return Result{}, err
			}

			items := make([]Item, 0, len(listing.Data.Children))
			for _, c := range listing.Data.Children {
				items = append(items, c.Data.item(r.To, false))
			}

			return Result{Items: items}, nil
		},
	}
}

// redditBucket is Reddit's coarse window for a span: the smallest of hour, day, week, month and year that holds it
// (last30days reddit._window_to_time_filter).
func redditBucket(span time.Duration) string {
	days := span.Hours() / 24

	switch {
	case days <= 1:
		return "day"
	case days <= 7:
		return "week"
	case days <= 31:
		return "month"
	case days <= 365:
		return "year"
	default:
		return "all"
	}
}

// redditPost is a post as Reddit's listing and Arctic Shift's archive give it.
type redditPost struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Selftext    string   `json:"selftext"`
	Author      string   `json:"author"`
	Subreddit   string   `json:"subreddit"`
	Permalink   string   `json:"permalink"`
	CreatedUTC  float64  `json:"created_utc"`
	Score       *float64 `json:"score"`
	NumComments *float64 `json:"num_comments"`
	UpvoteRatio *float64 `json:"upvote_ratio"`
}

// item is the post as an item; archived, a score younger than pendingScore is left out, not read as 0.
func (p redditPost) item(now time.Time, archived bool) Item {
	id := strings.TrimPrefix(strings.ToLower(p.ID), "t3_")
	link := p.Permalink

	if strings.HasPrefix(link, "/") {
		link = "https://www.reddit.com" + link
	}

	if link == "" {
		link = "https://www.reddit.com/comments/" + id + "/"
	}

	it := Item{NativeID: id, URL: link, Title: p.Title, Text: p.Selftext, Engagement: map[string]float64{}}

	if p.Author != "" && p.Author != "[deleted]" && p.Author != "[removed]" {
		it.Author = "u/" + p.Author
	}

	if p.Subreddit != "" {
		it.Container = "r/" + p.Subreddit
	}

	if p.CreatedUTC > 0 {
		t := time.Unix(int64(p.CreatedUTC), 0).UTC()
		it.PublishedAt = &t
	}

	fresh := archived && it.PublishedAt != nil && now.Sub(*it.PublishedAt) < pendingScore
	if p.Score != nil && !fresh {
		it.Engagement["score"] = *p.Score
	}

	if p.NumComments != nil {
		it.Engagement["num_comments"] = *p.NumComments
	}

	if p.UpvoteRatio != nil {
		it.Engagement["upvote_ratio"] = *p.UpvoteRatio
	}

	if len(it.Engagement) == 0 {
		it.Engagement = nil
	}

	return it
}

// webArcticBackend finds Reddit threads with the web search, `site:reddit.com <query>`, then reads each thread's
// score, comments, author and date from Arctic Shift's /api/posts/ids, keyless (last30days reddit_discover.web_refs
// and reddit_arctic.fetch_posts_by_ids). When the archive cannot be read, the threads stay as the search showed them,
// without counts, and the detail says so (last30days reddit_hydrate._from_search_result).
func webArcticBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.ArcticURL)

	return Backend{
		Name: BackendWebArctic, Host: host, Probe: needsWeb,
		Search: func(ctx context.Context, r Request) (Result, error) {
			a, err := r.Web(ctx, "site:reddit.com "+r.Query, redditThreads)
			if errors.Is(err, websearch.ErrNoResults) {
				return Result{Detail: "the web search found no Reddit thread"}, nil
			}

			if err != nil {
				return Result{}, webFailure(err)
			}

			type found struct {
				id   string
				item Item
			}

			var threads []found

			seen := map[string]bool{}

			for _, res := range a.Results {
				id, sub, ok := redditThread(res.URL)
				if !ok || seen[id] {
					continue
				}

				seen[id] = true
				it := Item{
					NativeID: id, URL: res.URL, Title: redditTitle(res.Title), Text: res.Snippet,
					PublishedAt: parseDate(res.PublishedAt),
				}

				if sub != "" {
					it.Container = "r/" + sub
				}

				threads = append(threads, found{id: id, item: it})
			}

			if len(threads) == 0 {
				return Result{Detail: "the web search found no Reddit thread"}, nil
			}

			threads = threads[:min(len(threads), arcticIDs, r.Limit)]
			ids := make([]string, 0, len(threads))

			for _, t := range threads {
				ids = append(ids, t.id)
			}

			var archived struct {
				Data  []redditPost `json:"data"`
				Error string       `json:"error"`
			}

			err = getJSON(ctx, client, r, host, arcticGap, true,
				e.ArcticURL+"/api/posts/ids?ids="+strings.Join(ids, ","), &archived)

			byID := map[string]redditPost{}
			for _, p := range archived.Data {
				byID[strings.TrimPrefix(strings.ToLower(p.ID), "t3_")] = p
			}

			detail := "web search " + answeredBy(a)

			switch {
			case err != nil:
				detail += "; arctic-shift not read: " + AsFailure(err).Error()
			case archived.Data == nil:
				detail += "; arctic-shift not read: " + oneLine(archived.Error)
			default:
				detail += fmt.Sprintf("; arctic-shift filled %d of %d", len(byID), len(threads))
			}

			items := make([]Item, 0, len(threads))
			for _, t := range threads {
				if p, ok := byID[t.id]; ok {
					items = append(items, p.item(r.To, true))

					continue
				}

				items = append(items, t.item)
			}

			return Result{Items: items, Detail: detail}, nil
		},
	}
}

// redditTitle is a web result's title of a thread without the search engine's Reddit frame ("r/x on Reddit: …",
// "… : r/x", "… - Reddit"), as last30days reddit_discover strips it.
func redditTitle(title string) string {
	title = strings.TrimSpace(title)
	if i := strings.Index(title, " on Reddit: "); i >= 0 && strings.HasPrefix(title, "r/") {
		title = title[i+len(" on Reddit: "):]
	}

	title = strings.TrimSuffix(title, " - Reddit")
	if i := strings.LastIndex(title, " : r/"); i >= 0 {
		title = title[:i]
	}

	return strings.TrimSpace(title)
}

// getJSON reads one JSON answer from a source, in its turn at the host: a failure's code is its status's
// (HTTPFailure), and a page where JSON was asked is a refusal, a challenge or a sign-in page (last30days
// reddit_public: "Anti-bot HTML response"; reddit_health: a challenge or a logged-out page is "blocked").
//
// slowDown reads a 422 as Arctic Shift's "slow down", a rate limit (last30days reddit_arctic._is_slow_down).
func getJSON(ctx context.Context, client *http.Client, r Request, host string, gap time.Duration, slowDown bool,
	address string, out any,
) error {
	if r.Pace != nil {
		done, err := r.Pace(ctx, host, gap)
		if err != nil {
			return AsFailure(err)
		}

		defer done()
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return Fail(CodeBroken, "%s", err.Error())
	}

	req.Header.Set("User-Agent", redditUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return AsFailure(err)
	}

	defer resp.Body.Close() //nolint:errcheck // A read body's close has nothing to report.

	if resp.StatusCode == http.StatusUnprocessableEntity && slowDown {
		return Fail(CodeRateLimited, "HTTP 422 slow down")
	}

	if resp.StatusCode != http.StatusOK {
		return HTTPFailure(resp.StatusCode, resp.Header, r.To)
	}

	if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); !strings.Contains(media, "json") {
		return Fail(CodeRefused, "the source answered a page (%s), not JSON: a challenge or a sign-in page", media)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return Fail(CodeBroken, "the answer is not the source's JSON: %s", err.Error())
	}

	return nil
}

// hostOf is an address's host and port, lowercased, without "www.".
func hostOf(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}

	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}

// parseDate reads the dates the web search's providers give, in the forms they give them; nil when none parses.
func parseDate(s string) *time.Time {
	s = strings.TrimSpace(s)

	for _, layout := range []string{
		time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", time.DateOnly, time.RFC1123,
		time.RFC1123Z, "January 2, 2006", "Jan 2, 2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()

			return &t
		}
	}

	return nil
}
