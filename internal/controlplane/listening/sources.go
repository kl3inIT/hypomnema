package listening

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The sources this control plane read first (issue #1287): the web, through the web search chain, and Reddit, first
// through its keyless public JSON, then through the web search's Reddit threads filled in from the Arctic Shift
// archive (owner, 2026-11-27: Reddit's JSON answers 403 from the control plane's network, as last30days found from
// every network since 2026-05-28, reddit_public.py:3-6; its keyless floor is that second route, reddit_keyless.py
// _web_floor and lib/backends.py:505-532).

// The sources' and backends' names, as a plan and the answer give them.
const (
	SourceWeb        = "web"
	SourceReddit     = "reddit"
	SourceYouTube    = "youtube"
	SourceGooglePlay = "google_play"
	SourceAppStore   = "app_store"
	SourceHackerNews = "hackernews"
	SourceGitHub     = "github"
	SourceArxiv      = "arxiv"
	SourceTechmeme   = "techmeme"
	SourceDevTo      = "devto"
	SourceBluesky    = "bluesky"
	SourceCareers    = "careers"
	SourceVoz        = "voz"
	SourceTinhte     = "tinhte"
	SourceOtofun     = "otofun"

	BackendWebSearch  = "web_search"
	BackendRedditJSON = "reddit_json"
	BackendWebArctic  = "web_arctic"
	BackendYtDlp      = "yt_dlp"
	BackendPlay       = "play_reviews"
	BackendAppStore   = "rss_reviews"
	BackendAlgolia    = "algolia"
	BackendGitHub     = "issues_search"
	BackendArxiv      = "arxiv_api"
	BackendTechmeme   = "techmeme_search"
	BackendForem      = "forem_api"
	BackendAppView    = "appview"
	BackendCareers    = "careers_page"
	BackendWebThread  = "web_thread"
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

// Endpoints are where the sources are reached; empty: theirs. Tests name their own.
type Endpoints struct {
	RedditURL   string
	ArcticURL   string
	PlayURL     string
	ITunesURL   string
	AlgoliaURL  string
	GitHubURL   string
	ArxivURL    string
	TechmemeURL string
	DevToURL    string
	BlueskyURL  string
	// ForumURLs put a forum source's threads at another address than its site's (tests); the web search still
	// asks for the site's.
	ForumURLs map[string]string
	// Pages is the client a page on a host the web search named is read with (a careers page, a forum thread): the
	// web fetch's, which dials only public addresses (websearch.PageClient); nil: the sources' client.
	Pages *http.Client
	// YtDlp runs yt-dlp; nil: the yt-dlp on the control plane's PATH.
	YtDlp Command
}

// Sources are the sources and their backends, in order: the web and Reddit (issue #1287), then the keyless sources
// of issue #1289, every one on by default (last30days doctor.KEYLESS_ALWAYS_ON; Agent-Reach channels' level
// "default").
func Sources(e Endpoints, client *http.Client) []Platform {
	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}

		*v = strings.TrimRight(*v, "/")
	}

	def(&e.RedditURL, redditURL)
	def(&e.ArcticURL, arcticURL)
	def(&e.PlayURL, playURL)
	def(&e.ITunesURL, itunesURL)
	def(&e.AlgoliaURL, algoliaURL)
	def(&e.GitHubURL, githubURL)
	def(&e.ArxivURL, arxivURL)
	def(&e.TechmemeURL, techmemeURL)
	def(&e.DevToURL, devtoURL)
	def(&e.BlueskyURL, blueskyURL)

	pages := e.Pages
	if pages == nil {
		pages = client
	}

	if e.YtDlp == nil {
		e.YtDlp = runYtDlp
	}

	out := []Platform{
		{Name: SourceWeb, Backends: []Backend{webBackend()}, LiveQuery: "VETC"},
		{
			Name: SourceReddit, Backends: []Backend{redditJSONBackend(e, client), webArcticBackend(e, client)},
			LiveQuery: "VETC Vietnam",
		},
		{Name: SourceYouTube, Backends: []Backend{youtubeBackend(e.YtDlp)}, LiveQuery: "VETC"},
		{Name: SourceGooglePlay, Backends: []Backend{playBackend(e, client)}, LiveQuery: "VETC"},
		{Name: SourceAppStore, Backends: []Backend{appStoreBackend(e, client)}, LiveQuery: "VETC"},
		{Name: SourceHackerNews, Backends: []Backend{hackerNewsBackend(e, client)}, LiveQuery: "Vietnam"},
		{Name: SourceGitHub, Backends: []Backend{gitHubBackend(e, client)}, LiveQuery: "Vietnamese"},
		{Name: SourceArxiv, Backends: []Backend{arxivBackend(e, client)}, LiveQuery: "Vietnamese"},
		{Name: SourceTechmeme, Backends: []Backend{techmemeBackend(e, client)}, LiveQuery: "Vietnam"},
		{Name: SourceDevTo, Backends: []Backend{devToBackend(e, client)}, LiveQuery: "vietnam"},
		{Name: SourceBluesky, Backends: []Backend{blueskyBackend(e, client)}, LiveQuery: "Vietnam"},
		{Name: SourceCareers, Backends: []Backend{careersBackend(pages)}, LiveQuery: "Cốc Cốc"},
	}

	robots := newRobots(pages)
	for _, f := range forums {
		out = append(out, Platform{
			Name: f.source, Backends: []Backend{forumBackend(f, e.ForumURLs[f.source], pages, robots)}, LiveQuery: "VETC",
		})
	}

	return out
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

	// Providers that answered with nothing are not failures; a failure that did not rest makes it broken.
	if failed.Others > 0 || len(failed.Rests) == 0 {
		return f
	}

	codes := make([]Code, 0, len(failed.Rests))
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
				return Result{Detail: "the web search found no Reddit thread"}, nil
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
				}
			}

			for _, it := range inWindow {
				if namesTopic(it.Title, r.Topic) {
					named = append(named, it)
				}
			}

			if dropped := len(inWindow) - len(named); dropped > 0 {
				// last30days BarResult.note (reddit_discover.py:125-135).
				detail += fmt.Sprintf("; %d of %d threads dropped for not naming the topic", dropped, len(inWindow))
			}

			// With the archive not read, a title the bar refuses is the search's, not the thread's: that the bar
			// leaves nothing says the threads were not read, never that nobody talked (issue #1287's coverage rule;
			// last30days judges threads after hydrating them, reddit_hydrate.py:467-510).
			if len(named) == 0 && len(inWindow) > 0 && archiveFailed != nil {
				archiveFailed.Detail = detail

				return Result{}, archiveFailed
			}

			return Result{Items: named[:min(len(named), r.Limit)], Detail: detail}, nil
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
		time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05", "2006-01-02 15:04:05", time.DateOnly, time.RFC1123,
		time.RFC1123Z, "January 2, 2006", "Jan 2, 2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()

			return &t
		}
	}

	return nil
}
