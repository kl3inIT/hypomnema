package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// The keyless public APIs of issue #1289: Hacker News, GitHub, arXiv, Techmeme, DEV.to and Bluesky. Each test
// serves the source's answer from a test server and checks what was asked, what is kept and how it is read.

// one reads a single source with a single subquery.
func one(t *testing.T, s *listening.Service, source, query string) listening.Answer {
	t.Helper()

	p := plan([]string{source}, query)
	p.Topic = strings.Fields(query)[0]

	a, err := s.Search(t.Context(), p, listening.Caller{})
	require.NoError(t, err)

	return a
}

// pinned reads a single source with a single subquery, pinned to a backend: how an opt-in source is asked.
func pinned(t *testing.T, s *listening.Service, source, backend, query string) listening.Answer {
	t.Helper()

	p := plan([]string{source}, query)
	p.Topic = strings.Fields(query)[0]
	p.Backends = map[string]string{source: backend}

	a, err := s.Search(t.Context(), p, listening.Caller{})
	require.NoError(t, err)

	return a
}

func keyless(c *clock, e listening.Endpoints) *listening.Service {
	return listening.New(listening.Config{Platforms: listening.Sources(e, http.DefaultClient), Now: c.Now, Sleep: noSleep})
}

func byNative(items []listening.Item) map[string]listening.Item {
	out := map[string]listening.Item{}
	for _, it := range items {
		out[it.NativeID] = it
	}

	return out
}

func jsonAnswer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(mustJSON(v))
}

// mustJSON is a fixture as JSON.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return b
}

// Design row "HN": Algolia's keyless search, stories and comments in the window (obsei and harken read both; last30days
// its stories), every query word but the first optional, a story with 2 points or fewer left out, each item linked to
// its discussion, a comment to its story.
func TestHackerNewsReadsStoriesAndCommentsInTheWindow(t *testing.T) {
	t.Parallel()

	hn, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"hits": []map[string]any{
			{"objectID": "1001", "title": "VETC toll app charges twice", "url": "https://news.example/vetc",
				"author": "an", "points": 50, "num_comments": 10, "created_at_i": start.AddDate(0, 0, -2).Unix(),
				"_tags": []string{"story"}},
			{"objectID": "1003", "title": "VETC quiet story", "author": "x", "points": 1,
				"created_at_i": start.AddDate(0, 0, -3).Unix(), "_tags": []string{"story"}},
			{"objectID": "1002", "comment_text": "<p>VETC app is down again</p>", "story_id": 1001,
				"story_title": "VETC toll app charges twice", "author": "binh", "points": nil,
				"created_at_i": start.AddDate(0, 0, -1).Unix(), "_tags": []string{"comment"}},
			{"objectID": "1004", "comment_text": "unrelated words only", "story_id": 9, "author": "y",
				"created_at_i": start.AddDate(0, 0, -1).Unix(), "_tags": []string{"comment"}},
		}})
	})

	a := one(t, keyless(newClock(), listening.Endpoints{AlgoliaURL: base}), listening.SourceHackerNews,
		"VETC toll app")

	require.Len(t, hn.Requests(), 1)
	q := hn.Requests()[0].URL.Query()
	assert.Equal(t, "/api/v1/search", hn.Requests()[0].URL.Path)
	assert.Equal(t, "VETC toll app", q.Get("query"))
	assert.Equal(t, "toll app", q.Get("optionalWords"))
	assert.Equal(t, "(story,comment)", q.Get("tags"))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).Unix()
	to := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC).Unix()
	assert.Equal(t, fmt.Sprintf("created_at_i>%d,created_at_i<%d", from, to), q.Get("numericFilters"))

	items := byNative(a.Items)
	require.Len(t, items, 2, "the 1-point story and the comment that names nothing asked are left out")

	story := items["1001"]
	assert.Equal(t, "https://news.ycombinator.com/item?id=1001", story.URL)
	assert.Equal(t, "an", story.Author)
	assert.Equal(t, map[string]float64{"points": 50, "comments": 10}, story.Engagement)

	comment := items["1002"]
	assert.Equal(t, "1001", comment.Parent)
	assert.Equal(t, "VETC app is down again", comment.Text)
	assert.Equal(t, "https://news.ycombinator.com/item?id=1002", comment.URL)
	assert.Equal(t, "VETC toll app charges twice", comment.Container)
	assert.Equal(t, "algolia", comment.Backend)
}

// Design row "GitHub": the public issue search without a token, by title in the window, sorted by reactions, at most
// 10 a call (last30days UNAUTH_COUNT_CAP), a result kept only when its title or repository names the topic; a 403
// with no requests left is a rate limit until GitHub's reset, not a refusal (Agent-Reach channels/github.py).
func TestGitHubSearchesPublicIssuesByTitle(t *testing.T) {
	t.Parallel()

	gh, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"items": []map[string]any{
			{"id": 1, "title": "VETC login fails on Android", "html_url": "https://github.com/acme/vetc-sdk/issues/7",
				"user": map[string]any{"login": "dev"}, "created_at": "2026-11-20T10:00:00Z", "comments": 3,
				"reactions": map[string]any{"total_count": 5}, "body": "Steps to reproduce"},
			{"id": 2, "title": "Bump dependencies", "html_url": "https://github.com/x/y/pull/1",
				"user": map[string]any{"login": "bot"}, "created_at": "2026-11-21T10:00:00Z",
				"pull_request": map[string]any{}},
		}})
	})

	a := one(t, keyless(newClock(), listening.Endpoints{GitHubURL: base}), listening.SourceGitHub, "VETC")

	require.Len(t, gh.Requests(), 1)
	q := gh.Requests()[0].URL.Query()
	assert.Equal(t, "/search/issues", gh.Requests()[0].URL.Path)
	assert.Equal(t, "is:public VETC created:2026-10-28..2026-11-27 in:title", q.Get("q"))
	assert.Equal(t, "reactions", q.Get("sort"))
	assert.Equal(t, "10", q.Get("per_page"))
	assert.Empty(t, gh.Requests()[0].Header.Get("Authorization"), "no token: keyless")

	require.Len(t, a.Items, 1)
	assert.Equal(t, "acme/vetc-sdk", a.Items[0].Container)
	assert.Equal(t, map[string]float64{"reactions": 5, "comments": 3}, a.Items[0].Engagement)

	reset := start.Add(40 * time.Second)
	_, limited := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", fmt.Sprint(reset.Unix()))
		w.WriteHeader(http.StatusForbidden)
	})

	a = one(t, keyless(newClock(), listening.Endpoints{GitHubURL: limited}), listening.SourceGitHub, "VETC")
	tried := coverageOf(t, a, listening.SourceGitHub).Streams[0].Tried[0]
	assert.Equal(t, "rate_limited", tried.Result)
	assert.Equal(t, reset.UTC().Format(time.RFC3339), tried.Until)
}

const arxivEntry = `<entry><id>http://arxiv.org/abs/%s</id><title>%s</title><summary>%s</summary>
<published>%s</published><link href="https://arxiv.org/abs/%s" rel="alternate" type="text/html"/>
<author><name>%s</name></author><author><name>Le B</name></author></entry>`

// Design row "arXiv": the open Atom API, the phrase quoted, by relevance, in the window; a clean zero retried once
// with each word quoted (last30days arxiv.py); one call every three seconds (arXiv's API terms of use).
func TestArxivQuotesThePhraseThenItsWords(t *testing.T) {
	t.Parallel()

	var calls int

	var mu sync.Mutex

	ax, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()

		w.Header().Set("Content-Type", "application/atom+xml")

		entries := ""
		if n > 1 {
			entries = fmt.Sprintf(arxivEntry, "2611.0001v1", "Vietnamese  speech\n recognition", "We study ASR.",
				"2026-11-20T10:00:00Z", "2611.0001v1", "Nguyen A") +
				fmt.Sprintf(arxivEntry, "2001.0001v1", "Old speech paper", "Old.", "2020-01-01T00:00:00Z",
					"2001.0001v1", "X")
		}

		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">%s</feed>`, entries)
	})

	var (
		slept []time.Duration
		smu   sync.Mutex
	)

	c := newClock()
	s := listening.New(listening.Config{
		Platforms: listening.Sources(listening.Endpoints{ArxivURL: base}, http.DefaultClient), Now: c.Now,
		Sleep: func(_ context.Context, d time.Duration) error {
			smu.Lock()
			defer smu.Unlock()

			slept = append(slept, d)

			return nil
		},
	})

	a := one(t, s, listening.SourceArxiv, "vietnamese speech")

	require.Len(t, ax.Requests(), 2)
	window := " AND submittedDate:[202610280000 TO 202611272359]"
	assert.Equal(t, `all:"vietnamese speech"`+window, ax.Requests()[0].URL.Query().Get("search_query"))
	assert.Equal(t, `all:"vietnamese" AND all:"speech"`+window, ax.Requests()[1].URL.Query().Get("search_query"))
	assert.Equal(t, "relevance", ax.Requests()[0].URL.Query().Get("sortBy"))
	assert.Equal(t, []time.Duration{3 * time.Second}, slept)

	require.Len(t, a.Items, 1, "the 2020 paper is outside the window")
	assert.Equal(t, "Vietnamese speech recognition", a.Items[0].Title)
	assert.Equal(t, "Nguyen A et al.", a.Items[0].Author)
	assert.Equal(t, "https://arxiv.org/abs/2611.0001v1", a.Items[0].URL)
	assert.Nil(t, a.Items[0].EngagementScore, "a paper has no readers' counts")
}

const techmemeItem = `<DIV CLASS="ii"><A HREF="%[1]s"><IMG CLASS="ill" src="/i.jpg"></A>
<STRONG CLASS="L2"><A HREF="%[1]s">%[2]s</A></STRONG>&nbsp; &mdash;&nbsp; %[3]s</DIV><!--end ii-->
<div class="iinf"><SPAN CLASS="idate">%[4]s</SPAN>&ndash;</div>`

// Design row "Techmeme": its archive search, the window as its date:[M/D/YY TO M/D/YY] operator, pages of 10 read
// until the stream's limit or a short page, a headline under four words (a publication's header row) left out
// (last30days techmeme.py).
func TestTechmemeReadsItsArchiveSearch(t *testing.T) {
	t.Parallel()

	tm, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, "<html><body>"+techmemeItem+techmemeItem+"</body></html>",
			"https://news.example/vetc-ipo", "VETC operator files for an IPO in Hanoi", "The toll operator said.",
			"Nov 26, 2026, 4:35 PM", "https://news.example/reuters", "Reuters", "", "Nov 25, 2026, 1:00 PM")
	})

	a := one(t, keyless(newClock(), listening.Endpoints{TechmemeURL: base}), listening.SourceTechmeme, "VETC")

	require.Len(t, tm.Requests(), 1, "a short page is the last")
	assert.Equal(t, "/search/d3results.jsp", tm.Requests()[0].URL.Path)
	assert.Equal(t, "VETC date:[10/28/26 TO 11/27/26]", tm.Requests()[0].URL.Query().Get("q"))

	require.Len(t, a.Items, 1)
	assert.Equal(t, "VETC operator files for an IPO in Hanoi", a.Items[0].Title)
	assert.Equal(t, "https://news.example/vetc-ipo", a.Items[0].URL)
	assert.Equal(t, "The toll operator said.", a.Items[0].Text)
	require.NotNil(t, a.Items[0].PublishedAt)
	assert.Equal(t, "2026-11-26", a.Items[0].PublishedAt.Format(time.DateOnly))
}

// Design row "DEV.to": Forem's keyless search over the window counted back from today, its versioned Accept header,
// a hit kept only when it is about the topic (last30days devto.py: token overlap of 0.25 or more).
func TestDevToKeepsArticlesAboutTheTopic(t *testing.T) {
	t.Parallel()

	dev, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, []map[string]any{
			{"id": 1, "title": "Building a VETC toll payment client", "description": "Notes on the VETC API",
				"url": "https://dev.to/an/vetc", "published_at": "2026-11-20T10:00:00Z",
				"public_reactions_count": 12, "comments_count": 2, "user": map[string]any{"username": "an"},
				"tag_list": []string{"go"}},
			{"id": 2, "title": "Ten Rust tips", "description": "Ownership", "url": "https://dev.to/b/rust",
				"published_at": "2026-11-21T10:00:00Z", "user": map[string]any{"username": "b"}},
		})
	})

	a := one(t, keyless(newClock(), listening.Endpoints{DevToURL: base}), listening.SourceDevTo, "VETC toll")

	require.Len(t, dev.Requests(), 1)
	r := dev.Requests()[0]
	assert.Equal(t, "/api/articles/search", r.URL.Path)
	assert.Equal(t, "VETC toll", r.URL.Query().Get("q"))
	assert.Equal(t, "31", r.URL.Query().Get("top"))
	assert.Equal(t, "application/vnd.forem.api-v1+json", r.Header.Get("Accept"))

	require.Len(t, a.Items, 1)
	assert.Equal(t, "an", a.Items[0].Author)
	assert.Equal(t, map[string]float64{"reactions": 12, "comments": 2}, a.Items[0].Engagement)
}

// Design row "Bluesky": the public AppView's searchPosts without a session (harken sources/bluesky.py; last30days
// signs in, but api.bsky.app answered keyless on 2026-11-28), top posts in the window, each linked on bsky.app.
func TestBlueskyReadsThePublicAppView(t *testing.T) {
	t.Parallel()

	bs, base := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonAnswer(w, map[string]any{"posts": []map[string]any{{
			"uri":    "at://did:plc:abc/app.bsky.feed.post/3kxyz",
			"author": map[string]any{"handle": "minh.bsky.social", "displayName": "Minh"},
			"record": map[string]any{"text": "VETC trừ tiền hai lần ở trạm Long Thành",
				"createdAt": "2026-11-25T08:00:00Z"},
			"indexedAt": "2026-11-25T08:00:01Z", "likeCount": 7, "repostCount": 2, "replyCount": 1, "quoteCount": 0,
		}}})
	})

	a := one(t, keyless(newClock(), listening.Endpoints{BlueskyURL: base}), listening.SourceBluesky, "VETC")

	require.Len(t, bs.Requests(), 1)
	r := bs.Requests()[0]
	assert.Equal(t, "/xrpc/app.bsky.feed.searchPosts", r.URL.Path)
	assert.Equal(t, "top", r.URL.Query().Get("sort"))
	assert.Equal(t, "2026-10-28T00:00:00Z", r.URL.Query().Get("since"))
	assert.Equal(t, "2026-11-28T00:00:00Z", r.URL.Query().Get("until"))
	assert.Empty(t, r.Header.Get("Authorization"))

	require.Len(t, a.Items, 1)
	it := a.Items[0]
	assert.Equal(t, "https://bsky.app/profile/minh.bsky.social/post/3kxyz", it.URL)
	assert.Equal(t, "minh.bsky.social", it.Author)
	assert.Equal(t, map[string]float64{"likes": 7, "reposts": 2, "replies": 1, "quotes": 0}, it.Engagement)
	require.NotNil(t, it.EngagementScore)
}

// Design rows "Every source follows the same three rules": every keyless source is on by default (no backend is
// opt-in), each names the live test a daily check runs (Agent-Reach Action.live_test), and a source shows as working
// only after a real run read it (last30days doctor.audit_state: run evidence first, unverified without it).
func TestEveryKeylessSourceIsOnAndWorkingOnlyAfterARealRun(t *testing.T) {
	t.Parallel()

	platforms := listening.Sources(listening.Endpoints{}, http.DefaultClient)

	var names []string

	for _, p := range platforms {
		names = append(names, p.Name)
		assert.NotEmpty(t, p.LiveQuery, "%s has a live test", p.Name)

		for _, b := range p.Backends {
			// Toutiao is off until the owner decides on its terms (review round 1 of PR #1321).
			assert.Equal(t, p.Name == listening.SourceToutiao, b.OptIn, "%s/%s is on by default", p.Name, b.Name)
		}
	}

	assert.Equal(t, []string{
		"web", "reddit", "youtube", "google_play", "app_store", "hackernews", "github", "arxiv", "techmeme", "devto",
		"bluesky", "toutiao", "bilibili", "careers", "voz", "tinhte", "otofun",
	}, names)

	c := newClock()
	ok, failing := &backend{items: []listening.Item{post("a", "VETC", 1)}}, &backend{err: listening.Fail(
		listening.CodeRefused, "HTTP 403")}
	s := service(c, listening.Platform{Name: "good", Backends: []listening.Backend{ok.spec("b", "good.example")}},
		listening.Platform{Name: "bad", Backends: []listening.Backend{failing.spec("b", "bad.example")}},
		listening.Platform{Name: "idle", Backends: []listening.Backend{ok.spec("b", "idle.example")}})

	before := s.Health()
	require.Len(t, before, 3)

	for _, h := range before {
		assert.Equal(t, listening.HealthUnverified, h.State, h.Source)
	}

	_, err := s.Search(t.Context(), plan([]string{"good", "bad"}, "VETC"), listening.Caller{})
	require.NoError(t, err)

	health := map[string]listening.Health{}
	for _, h := range s.Health() {
		health[h.Source] = h
	}

	assert.Equal(t, listening.HealthWorking, health["good"].State)
	require.NotNil(t, health["good"].LastSuccess)
	assert.Equal(t, start, *health["good"].LastSuccess)
	assert.Equal(t, listening.HealthNotWorking, health["bad"].State)
	assert.Equal(t, listening.CodeRefused, health["bad"].LastCode)
	assert.Equal(t, listening.HealthUnverified, health["idle"].State)
	assert.True(t, slices.IsSortedFunc(s.Health(), func(a, b listening.Health) int {
		return slices.Index([]string{"good", "bad", "idle"}, a.Source) -
			slices.Index([]string{"good", "bad", "idle"}, b.Source)
	}))
}
