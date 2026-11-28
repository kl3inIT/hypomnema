package listening

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
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
	// webGap is the gap between two calls to one web search route: last30days' keyless pace, a call a second
	// (lib/http.py:2413-2416), the only keyless API pace the references set (Agent-Reach's 10 s,
	// BROWSER_SEARCH_INTERVAL, is for a browser's searches).
	webGap = time.Second
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
	// maxBody bounds a source's answer (last30days http.MAX_RESPONSE_BYTES, lib/http.py:29).
	maxBody = 16 << 20
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
		{
			Name: SourceWeb, Backends: []Backend{webBackend()}, Read: readPage,
			Adds: "Forum threads, news, reviews and blog posts the web search's keyless providers find, each with its " +
				"link, snippet and date when the page gives one.",
		},
		{
			Name: SourceReddit, Backends: []Backend{redditJSONBackend(e, client), webArcticBackend(e, client)},
			Read: redditRead(e, client),
			Adds: "Reddit threads with their subreddit, author, date, score and number of comments.",
		},
	}
}

// readPage reads a web item's page through the web fetch, one page at a time per site, webGap apart (pace.go), with
// the date its metadata gives.
func readPage(ctx context.Context, r Request, it Item) (Document, error) {
	if r.Fetch == nil {
		return Document{}, Fail(CodeBroken, "this control plane has no web fetch")
	}

	if r.Pace != nil {
		done, err := r.Pace(ctx, "page:"+hostOf(it.URL), webGap)
		if err != nil {
			return Document{}, AsFailure(err)
		}

		defer done()
	}

	page, err := r.Fetch(ctx, it.URL)
	if err != nil {
		return Document{}, Fail(CodeBroken, "%s", err.Error())
	}

	return Document{Text: page.Content, Published: page.Published}, nil
}

// arcticComment is a comment of Arctic Shift's comment tree, its replies beneath it (last30days
// reddit_arctic._tree_rows, lib/reddit_arctic.py:825-842).
type arcticComment struct {
	Kind string `json:"kind"`
	Data struct {
		Author  string          `json:"author"`
		Body    string          `json:"body"`
		Score   float64         `json:"score"`
		Replies json.RawMessage `json:"replies"`
	} `json:"data"`
}

// redditRead reads a thread in full: its post as the source gave it, or, for a thread only a search showed, as Arctic
// Shift's /api/posts/ids has it, with its date; then its top comments by score from Arctic Shift's keyless
// /api/comments/tree, deleted ones left out, each cut at maxQuote (last30days reddit_arctic.fetch_post_comments,
// lib/reddit_arctic.py:941-1000, and snippet.cut_quote). When the comments cannot be read, the post is read alone and
// the error says so.
func redditRead(e Endpoints, client *http.Client) func(ctx context.Context, r Request, it Item) (Document, error) {
	host := hostOf(e.ArcticURL)
	path := host + "/api/comments/tree"
	postsPath := host + "/api/posts/ids"

	return func(ctx context.Context, r Request, it Item) (Document, error) {
		var doc Document

		title, body := it.Title, it.body

		if !it.Direct {
			var archived struct {
				Data []redditPost `json:"data"`
			}

			err := getJSON(ctx, client, r, host, arcticGap, true, postsPath,
				e.ArcticURL+"/api/posts/ids?ids="+url.QueryEscape(it.NativeID), &archived)
			// The post itself was not read: no text, so the item stays the search's card (radar's search_card).
			if err != nil {
				return Document{}, err
			}

			if len(archived.Data) == 0 {
				return Document{}, Fail(CodeNotFound, "arctic-shift has no post %s", it.NativeID)
			}

			// The post as the archive has it, its own title included: the search's title is not the post's.
			p := archived.Data[0].item(r.now(), true)
			title, body, doc.Published = p.Title, p.Text, p.PublishedAt
			doc.Title = title
		}

		post := strings.TrimSpace(title + "\n\n" + body)

		var tree struct {
			Data  []arcticComment `json:"data"`
			Error string          `json:"error"`
		}

		query := url.Values{"link_id": {"t3_" + it.NativeID}, "limit": {strconv.Itoa(arcticCommentRows)}}

		err := getJSON(ctx, client, r, host, arcticGap, true, path, e.ArcticURL+"/api/comments/tree?"+query.Encode(),
			&tree)
		if err != nil {
			doc.Text = post

			return doc, err
		}

		if tree.Data == nil {
			doc.Text = post

			return doc, Fail(CodeBroken, "arctic-shift gave no comments: %s", oneLine(tree.Error))
		}

		var rows []arcticComment

		stack := slices.Clone(tree.Data)
		for len(stack) > 0 {
			n := stack[0]
			stack = stack[1:]

			if n.Kind != "t1" {
				continue
			}

			rows = append(rows, n)

			var replies struct {
				Data struct {
					Children []arcticComment `json:"children"`
				} `json:"data"`
			}

			if json.Unmarshal(n.Data.Replies, &replies) == nil {
				stack = append(stack, replies.Data.Children...)
			}
		}

		rows = slices.DeleteFunc(rows, func(c arcticComment) bool {
			gone := func(s string) bool { return s == "[deleted]" || s == "[removed]" }

			return gone(c.Data.Author) || gone(c.Data.Body) || strings.TrimSpace(c.Data.Body) == ""
		})
		slices.SortStableFunc(rows, func(a, b arcticComment) int { return cmp.Compare(b.Data.Score, a.Data.Score) })

		var b strings.Builder

		b.WriteString(post)

		for _, c := range rows[:min(len(rows), readComments)] {
			fmt.Fprintf(&b, "\n\n[comment by u/%s, score %d]\n%s", c.Data.Author, int(c.Data.Score),
				cut(c.Data.Body, maxQuote))
		}

		doc.Text = b.String()

		return doc, nil
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
			// A request with no start has no window, the live test's (health.go): no provider is asked one.
			if !r.From.IsZero() {
				ctx = websearch.WithWindow(ctx, r.From, r.To)
			}

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

			basis := a.WindowBasis
			if basis == "" {
				basis = websearch.WindowNone
			}

			return Result{Items: items, Detail: answeredBy(a), provider: a.Provider, windowBasis: basis}, nil
		},
	}
}

// webFailure is a web search's error as a failure, keeping the chain's typed rests (websearch.FailedError): when
// every provider that failed now rests, the failure is the rest's (a rate limit, a spent quota, a refusal) until the
// first is free again, as Agent-Reach keeps a backend's typed code to the answer (runtime/result.py:64-79); every
// provider resting before it was asked is a rate limit until the first is free.
func webFailure(err error) error {
	var resting *websearch.RestingError
	if errors.As(err, &resting) {
		return &SourceError{Code: CodeRateLimited, Detail: "every web search provider rests", Until: resting.Until}
	}

	var failed *websearch.FailedError
	if !errors.As(err, &failed) {
		return AsFailure(err)
	}

	f := &SourceError{Code: CodeBroken, Detail: oneLine(failed.Error())}

	// Whatever code the failure takes, a provider that timed out or rests for a rate limit or a spent quota comes
	// back by itself; that is kept up to whether the owner is told (review round 2 of PR #1322: one 504 or 429 among
	// four 500s is no broken source to report). A refusal is not: it rests for hours and may need a person.
	f.transient = failed.Timeouts > 0
	for _, r := range failed.Rests {
		f.transient = f.transient || r.Why != websearch.RestRefused
	}

	// Providers that answered with nothing are not failures; a failure that neither rested nor timed out makes it
	// broken. A provider that did not answer in time is a timeout, which comes back by itself, kept as one through the
	// chain (issue #1290, review round 1; a network timeout is TIMEOUT in Agent-Reach, runtime/result.py:74-78, and a
	// 408 or 504 is a timeout by this package's own rule, failure.go:132-133; the most actionable code of several is
	// reported, last30days lib/health.py:51-59).
	if failed.Others > failed.Timeouts || len(failed.Rests)+failed.Timeouts == 0 {
		return f
	}

	codes := make([]Code, 0, len(failed.Rests)+1)
	if failed.Timeouts > 0 {
		codes = append(codes, CodeTimeout)
	}

	for _, r := range failed.Rests {
		codes = append(codes, restCode(r.Why))

		if f.Until.IsZero() || r.Until.Before(f.Until) {
			f.Until = r.Until
		}
	}

	f.Code = slices.MinFunc(codes, func(a, b Code) int { return specificity(a) - specificity(b) })

	return f
}

// restCode is a web search route's rest as a code.
func restCode(why string) Code {
	switch why {
	case websearch.RestRefused:
		return CodeRefused
	case websearch.RestNoCredits, websearch.RestDailyLimit:
		return CodeLimitReached
	default:
		return CodeRateLimited
	}
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
			if err := getJSON(ctx, client, r, host, redditGap, false, host+"/search.json", address, &listing); err != nil {
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

	it := Item{
		NativeID: id, URL: link, Title: p.Title, Text: p.Selftext, Engagement: map[string]float64{}, Direct: true,
	}

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

// webArcticBackend finds Reddit threads with the web search, `site:reddit.com <query>`, reads every thread found from
// Arctic Shift's /api/posts/ids, keyless, then keeps those dated in the window, then those whose title names the
// topic, then cuts at the stream's limit: last30days hydrates before it filters by date (reddit_hydrate.py:417-532,
// _in_window after the archive) and puts the threads through its topic bar before they are items
// (reddit_keyless.py:500-508, reddit_discover.apply_bar). When the archive cannot be read, or rests, the threads stay
// as the search showed them, without counts, and the detail says so (reddit_hydrate.py:470-477, _from_search_result).
func webArcticBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.ArcticURL)
	path := host + "/api/posts/ids"

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

			threads := webThreads(a)
			if len(threads) == 0 {
				return Result{
					Detail: "the web search found no Reddit thread", provider: a.Provider, windowBasis: websearch.WindowNone,
				}, nil
			}

			threads = threads[:min(len(threads), arcticIDs)]
			ids := make([]string, 0, len(threads))

			for _, t := range threads {
				ids = append(ids, t.NativeID)
			}

			var archived struct {
				Data  []redditPost `json:"data"`
				Error string       `json:"error"`
			}

			err = getJSON(ctx, client, r, host, arcticGap, true, path,
				e.ArcticURL+"/api/posts/ids?ids="+strings.Join(ids, ","), &archived)

			byID := map[string]redditPost{}
			for _, p := range archived.Data {
				byID[strings.TrimPrefix(strings.ToLower(p.ID), "t3_")] = p
			}

			detail := "web search " + answeredBy(a)
			res := Result{provider: a.Provider, windowBasis: websearch.WindowNone}

			var archiveFailed *SourceError

			switch {
			case err != nil:
				f := AsFailure(err)
				archiveFailed = &SourceError{
					Code: f.Code, Detail: "arctic-shift not read: " + f.Detail, Until: f.Until, pathRest: true,
				}
				detail += "; arctic-shift not read: " + f.Error()

				if !f.Until.IsZero() {
					detail += " until " + f.Until.UTC().Format(time.RFC3339)
				}
			case archived.Data == nil:
				archiveFailed = Fail(CodeBroken, "arctic-shift not read: %s", oneLine(archived.Error))
				archiveFailed.pathRest = true
				detail += "; arctic-shift not read: " + oneLine(archived.Error)
			default:
				detail += fmt.Sprintf("; arctic-shift filled %d of %d", len(byID), len(threads))
			}

			var inWindow, named []Item

			for _, t := range threads {
				it := t
				if p, ok := byID[t.NativeID]; ok {
					it = p.item(r.To, true)
				}

				if inDays(it.PublishedAt, r.From, r.To) {
					inWindow = append(inWindow, it)
				} else {
					res.dropped.OutsideWindow++
				}
			}

			for _, it := range inWindow {
				if namesTopic(it.Title, r.Topic) {
					named = append(named, it)
				}
			}

			res.dropped.OffTopic = len(inWindow) - len(named)
			if dropped := res.dropped.OffTopic; dropped > 0 {
				// last30days BarResult.note (reddit_discover.py:125-135).
				detail += fmt.Sprintf("; %d of %d threads dropped for not naming the topic", dropped, len(inWindow))
			}

			// With the archive not read, a title the bar refuses is the search's, not the thread's: that the bar
			// leaves nothing says the threads were not read, never that nobody talked (issue #1287's coverage rule;
			// last30days judges threads after hydrating them, reddit_hydrate.py:467-510).
			if len(named) == 0 && len(inWindow) > 0 && archiveFailed != nil {
				archiveFailed.Detail = detail

				return res, archiveFailed
			}

			res.Items, res.Detail = named[:min(len(named), r.Limit)], detail

			return res, nil
		},
	}
}

// webThreads are the Reddit threads a web search found, each once, in its order, as the search showed them.
func webThreads(a websearch.Answer) []Item {
	var out []Item

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

		out = append(out, it)
	}

	return out
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
//
// path names the endpoint for its rest (Request.Resting): it is checked once the call has its turn, and a failure
// that rests is recorded on it, so another search queued behind this one is not sent to a path that just refused.
func getJSON(ctx context.Context, client *http.Client, r Request, host string, gap time.Duration, slowDown bool,
	path, address string, out any,
) error {
	if r.Pace != nil {
		done, err := r.Pace(ctx, host, gap)
		if err != nil {
			return AsFailure(err)
		}

		defer done()
	}

	if r.Resting != nil {
		if rs := r.Resting(path); rs != nil {
			return &SourceError{Code: rs.Code, Detail: rs.Detail, Until: rs.Until, resting: true}
		}
	}

	err := fetchJSON(ctx, client, r, slowDown, address, out)
	if err != nil && r.Rest != nil {
		f := AsFailure(err)
		if until := r.Rest(path, f); !until.IsZero() {
			f.Until = until
		}

		return f
	}

	return err
}

// fetchJSON is one GET of a JSON answer.
func fetchJSON(ctx context.Context, client *http.Client, r Request, slowDown bool, address string, out any) error {
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
		f := HTTPFailure(http.StatusTooManyRequests, resp.Header, r.now())
		f.Detail = "HTTP 422 slow down"

		return f
	}

	if resp.StatusCode != http.StatusOK {
		return HTTPFailure(resp.StatusCode, resp.Header, r.now())
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
