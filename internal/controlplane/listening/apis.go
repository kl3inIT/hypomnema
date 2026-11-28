package listening

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The keyless public APIs of issue #1289, each read as the references read it: Hacker News through Algolia (last30days
// hackernews.py, obsei and harken sources/hackernews.py), GitHub's public issue search (last30days github.py,
// Agent-Reach channels/github.py), arXiv's Atom API (last30days arxiv.py), Techmeme's archive search (last30days
// techmeme.py), DEV.to's Forem API (last30days devto.py) and Bluesky's public AppView (harken sources/bluesky.py,
// last30days bluesky.py). None takes a key.

const (
	algoliaURL  = "https://hn.algolia.com"
	githubURL   = "https://api.github.com"
	arxivURL    = "https://export.arxiv.org"
	techmemeURL = "https://www.techmeme.com"
	devtoURL    = "https://dev.to"
	blueskyURL  = "https://api.bsky.app"

	// hnMinPoints: a story with this many points or fewer is left out (last30days hackernews.MIN_STORY_POINTS).
	hnMinPoints = 2
	// hnOverfetch: Algolia is asked for twice the stream's limit, the low-point stories being dropped after
	// (last30days HN_OVERFETCH_MULTIPLIER).
	hnOverfetch = 2
	// githubAnonymous is the most results a keyless GitHub search asks: its anonymous tier allows 10 searches a
	// minute (last30days github.UNAUTH_COUNT_CAP).
	githubAnonymous = 10
	// topicalFloor is the relevance a GitHub result's title or repository, and a DEV.to article, must reach to be
	// about the topic: just above what generic words alone score (last30days github.TOPICAL_FLOOR,
	// devto.SEARCH_RELEVANCE_FLOOR).
	topicalFloor = 0.25
	// arxivGap is arXiv's own rule for its API: one request every three seconds, one connection at a time
	// (info.arxiv.org/help/api/tou.html, read 2026-11-28).
	arxivGap = 3 * time.Second
	// techmemePage is how many results a page of Techmeme's search holds (last30days techmeme.SEARCH_PAGE_SIZE).
	techmemePage = 10
	// techmemeWords is the fewest words a headline has; a shorter one is a publication's header row (last30days
	// techmeme.MIN_HEADLINE_WORDS).
	techmemeWords = 4
	// devtoPerPage is the most articles a DEV.to search asks: last30days' deep depth (devto.SEARCH_PER_PAGE).
	devtoPerPage = 30
	// blueskyMax is the most posts searchPosts answers at once (AT Protocol's limit, last30days bluesky.py).
	blueskyMax = 100
)

// dayStart is the start of a time's day in UTC.
func dayStart(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour)
}

var htmlTag = regexp.MustCompile(`<[^>]+>`)

// stripHTML is an HTML fragment as one line of text (last30days hackernews._strip_html, harken base.strip_html).
func stripHTML(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(htmlTag.ReplaceAllString(s, " "))), " ")
}

// namesAnyWord says the text holds one of the query's words as a whole word: last30days hackernews
// _title_matches_query (any word, since Algolia's optional words match any), harken's check of the query in the text.
func namesAnyWord(text, query string) bool {
	have := set(strings.Fields(normalize(text)))

	for _, w := range strings.Fields(normalize(query)) {
		if have[w] {
			return true
		}
	}

	return false
}

// flatQuery is a query with its quotes, commas and hyphens made spaces, as Algolia and the post-filter read it
// (last30days hackernews._flatten_query_for_algolia).
func flatQuery(q string) string {
	return strings.Join(strings.Fields(strings.NewReplacer(",", " ", "-", " ", `"`, " ").Replace(q)), " ")
}

type hnHit struct {
	ObjectID    string   `json:"objectID"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Author      string   `json:"author"`
	Points      *float64 `json:"points"`
	NumComments *float64 `json:"num_comments"`
	CreatedAtI  int64    `json:"created_at_i"`
	CommentText string   `json:"comment_text"`
	StoryText   string   `json:"story_text"`
	StoryID     *int64   `json:"story_id"`
	StoryTitle  string   `json:"story_title"`
	Tags        []string `json:"_tags"`
}

// hackerNewsBackend reads Algolia's keyless search of Hacker News: stories and comments in the window (obsei
// sources/hackernews.py:52 and harken sources/hackernews.py:34 read both kinds; last30days its stories), by relevance, every query word
// after the first optional so a long query still matches (last30days hackernews.py:113-124).
func hackerNewsBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.AlgoliaURL)

	return Backend{
		Name: BackendAlgolia, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			q := flatQuery(r.Query)
			v := url.Values{
				"query": {q}, "tags": {"(story,comment)"}, "hitsPerPage": {strconv.Itoa(min(r.Limit*hnOverfetch, 100))},
				// The day after the window's last is out (last30days _date_to_unix(to_date) + 86400).
				"numericFilters": {fmt.Sprintf("created_at_i>%d,created_at_i<%d", dayStart(r.From).Unix(),
					dayStart(r.To).AddDate(0, 0, 1).Unix())},
			}

			if words := strings.Fields(q); len(words) > 1 {
				v.Set("optionalWords", strings.Join(words[1:], " "))
			}

			var res struct {
				Hits []hnHit `json:"hits"`
			}

			if err := callJSON(ctx, client, r, call{
				host: host, path: host + "/api/v1/search", address: e.AlgoliaURL + "/api/v1/search?" + v.Encode(),
			}, &res); err != nil {
				return Result{}, err
			}

			var items []Item

			for _, h := range res.Hits {
				comment := h.CommentText != "" || slices.Contains(h.Tags, "comment")
				if !comment && (h.Points == nil || *h.Points <= hnMinPoints) || h.ObjectID == "" {
					continue
				}

				it := Item{
					NativeID: h.ObjectID, URL: "https://news.ycombinator.com/item?id=" + h.ObjectID, Author: h.Author,
					Engagement: map[string]float64{},
				}

				if h.CreatedAtI > 0 {
					at := time.Unix(h.CreatedAtI, 0).UTC()
					it.PublishedAt = &at
				}

				if comment {
					it.Text = stripHTML(h.CommentText)
					it.Container = h.StoryTitle

					if h.StoryID != nil {
						it.Parent = strconv.FormatInt(*h.StoryID, 10)
					}
				} else {
					it.Title, it.Text = h.Title, stripHTML(h.StoryText)
				}

				if !namesAnyWord(it.Title+" "+it.Text, q) {
					continue
				}

				if h.Points != nil {
					it.Engagement["points"] = *h.Points
				}

				if h.NumComments != nil {
					it.Engagement["comments"] = *h.NumComments
				}

				if len(it.Engagement) == 0 {
					it.Engagement = nil
				}

				items = append(items, it)
			}

			return Result{Items: items}, nil
		},
	}
}

// githubQualifier is a GitHub search qualifier a planner may write into a query; the backend adds its own window
// and scope, and GitHub honors the first of two `created:` (last30days github.QUALIFIER_KEYS and
// strip_search_qualifiers, github.py:205-320, here by whole word).
var githubQualifier = regexp.MustCompile(`^[-+]?(answered|archived|assignee|author|base|category|closed|comments|` +
	`commenter|created|draft|fork|forks|head|in|interactions|involves|is|label|language|license|linked|mentions|` +
	`merged|milestone|no|org|owner|project|pushed|reactions|reason|repo|review|sha|size|sort|stars|state|status|` +
	`team|topic|topics|type|updated|user|visibility):`)

// gitHubBackend reads GitHub's public issue and pull request search without a token: by title, in the window, sorted
// by reactions, at most githubAnonymous a call, a result kept when its title or its repository names the topic
// (last30days github.py search_github and _clears_topical_floor, github.py:176-190 and 332-515).
func gitHubBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.GitHubURL)

	return Backend{
		Name: BackendGitHub, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			var kept []string

			for _, w := range strings.Fields(r.Query) {
				if !githubQualifier.MatchString(strings.ToLower(w)) {
					kept = append(kept, w)
				}
			}

			if len(kept) == 0 {
				return Result{Detail: "the query was only GitHub qualifiers"}, nil
			}

			core := strings.Join(kept, " ")
			v := url.Values{
				"q": {fmt.Sprintf("is:public %s created:%s..%s in:title", core, r.From.UTC().Format(time.DateOnly),
					r.To.UTC().Format(time.DateOnly))},
				"sort": {"reactions"}, "order": {"desc"}, "per_page": {strconv.Itoa(min(r.Limit, githubAnonymous))},
			}

			var res struct {
				Items []struct {
					ID          int64  `json:"id"`
					Title       string `json:"title"`
					HTMLURL     string `json:"html_url"`
					Body        string `json:"body"`
					CreatedAt   string `json:"created_at"`
					Comments    int    `json:"comments"`
					PullRequest any    `json:"pull_request"`
					User        struct {
						Login string `json:"login"`
					} `json:"user"`
					Reactions struct {
						TotalCount int `json:"total_count"`
					} `json:"reactions"`
				} `json:"items"`
			}

			if err := callJSON(ctx, client, r, call{
				host: host, path: host + "/search/issues", address: e.GitHubURL + "/search/issues?" + v.Encode(),
				accept: "application/vnd.github+json", failure: gitHubFailure,
			}, &res); err != nil {
				return Result{}, err
			}

			topic := r.Topic
			if strings.TrimSpace(topic) == "" {
				topic = core
			}

			var items []Item

			dropped := 0

			for _, it := range res.Items {
				repo := gitHubRepo(it.HTMLURL)
				repoWords := strings.NewReplacer("/", " ", "_", " ", ".", " ", "-", " ").Replace(repo)

				if math.Max(relevance(topic, it.Title), relevance(topic, repoWords)) < topicalFloor {
					dropped++

					continue
				}

				items = append(items, Item{
					NativeID: strconv.FormatInt(it.ID, 10), URL: it.HTMLURL, Title: it.Title, Text: it.Body,
					Author: it.User.Login, Container: repo, PublishedAt: parseDate(it.CreatedAt),
					Engagement: map[string]float64{
						"reactions": float64(it.Reactions.TotalCount), "comments": float64(it.Comments),
					},
				})
			}

			detail := ""
			if dropped > 0 {
				detail = fmt.Sprintf("%d of %d results dropped for not naming the topic", dropped, len(res.Items))
			}

			return Result{Items: items, Detail: detail}, nil
		},
	}
}

// gitHubFailure reads GitHub's refusals: a 403 or 429 with no requests left, or a Retry-After, is a rate limit until
// GitHub's reset, an epoch time; another 403 is GitHub's refusal of this request (Agent-Reach channels/github.py
// _api_get).
func gitHubFailure(status int, header http.Header, now time.Time) *SourceError {
	limited := header.Get("X-Ratelimit-Remaining") == "0" || header.Get("Retry-After") != ""
	if (status != http.StatusForbidden && status != http.StatusTooManyRequests) || !limited {
		return HTTPFailure(status, http.Header{}, now)
	}

	f := Fail(CodeRateLimited, "HTTP %d: GitHub's keyless rate limit", status)

	if wait, ok := retryAfter(http.Header{"Retry-After": header.Values("Retry-After")}); ok {
		f.Until = now.Add(wait)
	} else if reset, err := strconv.ParseInt(header.Get("X-Ratelimit-Reset"), 10, 64); err == nil && reset > 0 {
		f.Until = time.Unix(reset, 0).UTC()
	}

	return f
}

// gitHubRepo is the owner/repo of an issue's address.
func gitHubRepo(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}

	return parts[0] + "/" + parts[1]
}

type atomFeed struct {
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Summary   string `xml:"summary"`
		Published string `xml:"published"`
		Updated   string `xml:"updated"`
		Links     []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Authors []struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

// arxivBackend reads arXiv's open Atom API: the query as a quoted phrase in every field, by relevance, in the window,
// and when the phrase finds nothing, once more with each word quoted (last30days arxiv.py _build_search_query and
// search_arxiv's retry). last30days keeps a year of papers whatever the window (RECENCY_DAYS); here the plan's
// window holds, as for every source.
func arxivBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.ArxivURL)

	return Backend{
		Name: BackendArxiv, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			phrase := strings.Join(strings.Fields(strings.ReplaceAll(r.Query, `"`, " ")), " ")
			window := fmt.Sprintf(" AND submittedDate:[%s0000 TO %s2359]", r.From.UTC().Format("20060102"),
				r.To.UTC().Format("20060102"))

			ask := func(search string) (atomFeed, error) {
				v := url.Values{"search_query": {search + window}, "sortBy": {"relevance"},
					"max_results": {strconv.Itoa(r.Limit)}}

				a, err := call{
					host: host, gap: arxivGap, path: host + "/api/query", address: e.ArxivURL + "/api/query?" + v.Encode(),
					accept: "application/atom+xml",
				}.do(ctx, client, r)
				if err != nil {
					return atomFeed{}, err
				}

				var feed atomFeed
				if err := xml.Unmarshal(a.body, &feed); err != nil {
					return atomFeed{}, Fail(CodeBroken, "the answer is not arXiv's Atom feed: %s", err.Error())
				}

				return feed, nil
			}

			feed, err := ask(`all:"` + phrase + `"`)
			if err != nil {
				return Result{}, err
			}

			detail := ""

			if words := strings.Fields(phrase); len(feed.Entries) == 0 && len(words) > 1 {
				quoted := make([]string, 0, len(words))
				for _, w := range words {
					quoted = append(quoted, `all:"`+w+`"`)
				}

				if feed, err = ask(strings.Join(quoted, " AND ")); err != nil {
					return Result{}, err
				}

				detail = "the phrase found nothing; its words were searched"
			}

			var items []Item

			for _, en := range feed.Entries {
				published := parseDate(en.Published)
				if published == nil {
					published = parseDate(en.Updated)
				}

				title := strings.Join(strings.Fields(en.Title), " ")
				if published == nil || title == "" {
					// No date, no recency: last30days drops the paper (arxiv.py parse_arxiv_response).
					continue
				}

				link := strings.TrimSpace(en.ID)

				for _, l := range en.Links {
					if l.Rel == "alternate" && l.Href != "" {
						link = l.Href
					}
				}

				author := ""
				if len(en.Authors) > 0 {
					author = strings.TrimSpace(en.Authors[0].Name)
					if len(en.Authors) > 1 {
						author += " et al."
					}
				}

				items = append(items, Item{
					NativeID: strings.TrimSpace(en.ID), URL: link, Title: title, Text: en.Summary, Author: author,
					PublishedAt: published,
				})
			}

			return Result{Items: items, Detail: detail}, nil
		},
	}
}

var (
	techmemeStory = regexp.MustCompile(`(?is)<div class="ii">(.*?)</div>\s*<!--end ii-->\s*<div class="iinf">\s*` +
		`<span class="idate">([^<]*)</span>`)
	techmemeHeadline = regexp.MustCompile(`(?is)<strong class="L\d+"><a href="([^"]+)">(.*?)</a></strong>(.*)`)
)

// techmemeBackend reads Techmeme's live archive search, the page techmeme-pp-cli's `search` reads: the window as
// Techmeme's own `date:[M/D/YY TO M/D/YY]` operator, pages of 10 newest first until the stream's limit or a short
// page, a headline of fewer than techmemeWords words left out (last30days techmeme.py _build_search_args,
// _pages_for and _is_story_headline).
func techmemeBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.TechmemeURL)

	return Backend{
		Name: BackendTechmeme, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			q := r.Query
			if !strings.Contains(q, "date:") {
				short := func(t time.Time) string {
					t = t.UTC()

					return fmt.Sprintf("%d/%d/%02d", int(t.Month()), t.Day(), t.Year()%100)
				}

				q += " date:[" + short(r.From) + " TO " + short(r.To) + "]"
			}

			var items []Item

			for page := 0; page*techmemePage < r.Limit; page++ {
				v := url.Values{"q": {q}}
				if page > 0 {
					v.Set("start", strconv.Itoa(page*techmemePage))
				}

				a, err := call{
					host: host, path: host + "/search/d3results.jsp",
					address: e.TechmemeURL + "/search/d3results.jsp?" + v.Encode(), accept: "text/html",
				}.do(ctx, client, r)
				if err != nil {
					return Result{}, err
				}

				stories := techmemeStory.FindAllStringSubmatch(string(a.body), -1)

				for _, s := range stories {
					m := techmemeHeadline.FindStringSubmatch(s[1])
					if m == nil {
						continue
					}

					headline := stripHTML(m[2])
					if len(strings.Fields(headline)) < techmemeWords {
						continue
					}

					link := html.UnescapeString(m[1])
					it := Item{
						NativeID: NormalizeURL(link), URL: link, Title: headline, Container: hostOf(link),
						Text: strings.Trim(stripHTML(m[3]), "  —-"),
					}

					if at, err := time.Parse("Jan 2, 2006, 3:04 PM", strings.TrimSpace(stripHTML(s[2]))); err == nil {
						day := dayStart(at)
						it.PublishedAt = &day
					}

					items = append(items, it)
				}

				if len(stories) < techmemePage {
					break
				}
			}

			return Result{Items: items}, nil
		},
	}
}

// devToBackend reads DEV.to's keyless Forem search over the window counted back from today, with the versioned
// Accept header its search wants, a hit kept only when it is about the topic (last30days devto.py search_devto:
// SEARCH_URL, _SEARCH_HEADERS, _window_days, SEARCH_RELEVANCE_FLOOR).
func devToBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.DevToURL)

	return Backend{
		Name: BackendForem, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			q := strings.Join(strings.Fields(strings.ReplaceAll(r.Query, `"`, " ")), " ")
			days := int(dayStart(r.To).Sub(dayStart(r.From)).Hours()/24) + 1
			v := url.Values{"q": {q}, "top": {strconv.Itoa(days)}, "per_page": {strconv.Itoa(min(r.Limit, devtoPerPage))}}

			var rows []struct {
				ID          int64  `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
				URL         string `json:"url"`
				PublishedAt string `json:"published_at"`
				Reactions   *int   `json:"public_reactions_count"`
				Positive    *int   `json:"positive_reactions_count"`
				Comments    int    `json:"comments_count"`
				TagList     any    `json:"tag_list"`
				User        struct {
					Username string `json:"username"`
					Name     string `json:"name"`
				} `json:"user"`
			}

			if err := callJSON(ctx, client, r, call{
				host: host, path: host + "/api/articles/search", address: e.DevToURL + "/api/articles/search?" + v.Encode(),
				accept: "application/vnd.forem.api-v1+json",
			}, &rows); err != nil {
				return Result{}, err
			}

			topic := r.Topic
			if strings.TrimSpace(topic) == "" {
				topic = q
			}

			var items []Item

			for _, a := range rows {
				tags := ""
				switch t := a.TagList.(type) {
				case string:
					tags = strings.ReplaceAll(t, ",", " ")
				case []any:
					for _, x := range t {
						tags += fmt.Sprint(x) + " "
					}
				}

				if relevance(topic, a.Title+" "+a.Description+" "+tags) < topicalFloor {
					continue
				}

				reactions := 0
				switch {
				case a.Reactions != nil:
					reactions = *a.Reactions
				case a.Positive != nil:
					reactions = *a.Positive
				}

				author := a.User.Username
				if author == "" {
					author = a.User.Name
				}

				items = append(items, Item{
					NativeID: strconv.FormatInt(a.ID, 10), URL: a.URL, Title: a.Title, Text: a.Description,
					Author: author, PublishedAt: parseDate(a.PublishedAt),
					Engagement: map[string]float64{"reactions": float64(reactions), "comments": float64(a.Comments)},
				})
			}

			return Result{Items: items}, nil
		},
	}
}

// blueskyBackend reads Bluesky's searchPosts on the public AppView without a session (harken sources/bluesky.py:
// api.bsky.app "exposes the same public XRPC endpoint without a session"; it answered keyless on 2026-11-28, while
// last30days signs in, bluesky.py), the top posts in the window (last30days: sort top, since and until), each linked
// on bsky.app.
func blueskyBackend(e Endpoints, client *http.Client) Backend {
	host := hostOf(e.BlueskyURL)

	return Backend{
		Name: BackendAppView, Host: host,
		Search: func(ctx context.Context, r Request) (Result, error) {
			v := url.Values{
				"q": {r.Query}, "limit": {strconv.Itoa(min(r.Limit, blueskyMax))}, "sort": {"top"},
				"since": {dayStart(r.From).Format(time.RFC3339)},
				"until": {dayStart(r.To).AddDate(0, 0, 1).Format(time.RFC3339)},
			}

			var res struct {
				Posts []struct {
					URI    string `json:"uri"`
					Author struct {
						Handle string `json:"handle"`
					} `json:"author"`
					Record struct {
						Text      string `json:"text"`
						CreatedAt string `json:"createdAt"`
					} `json:"record"`
					IndexedAt string  `json:"indexedAt"`
					Likes     float64 `json:"likeCount"`
					Reposts   float64 `json:"repostCount"`
					Replies   float64 `json:"replyCount"`
					Quotes    float64 `json:"quoteCount"`
				} `json:"posts"`
			}

			path := "/xrpc/app.bsky.feed.searchPosts"
			if err := callJSON(ctx, client, r, call{
				host: host, path: host + path, address: e.BlueskyURL + path + "?" + v.Encode(),
			}, &res); err != nil {
				return Result{}, err
			}

			items := make([]Item, 0, len(res.Posts))

			for _, p := range res.Posts {
				rkey := p.URI[strings.LastIndex(p.URI, "/")+1:]
				if p.URI == "" || p.Author.Handle == "" || rkey == "" {
					continue
				}

				published := parseDate(p.IndexedAt)
				if published == nil {
					published = parseDate(p.Record.CreatedAt)
				}

				items = append(items, Item{
					NativeID: p.URI, URL: "https://bsky.app/profile/" + p.Author.Handle + "/post/" + rkey,
					Author: p.Author.Handle, Text: p.Record.Text, PublishedAt: published,
					Engagement: map[string]float64{
						"likes": p.Likes, "reposts": p.Reposts, "replies": p.Replies, "quotes": p.Quotes,
					},
				})
			}

			return Result{Items: items}, nil
		},
	}
}
