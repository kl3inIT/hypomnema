package listening_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

var start = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

// clock is a clock the test moves.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock { return &clock{at: start} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.at = c.at.Add(d)
}

func noSleep(context.Context, time.Duration) error { return nil }

// backend is a backend that answers what it is given and counts its calls.
type backend struct {
	mu    sync.Mutex
	calls int
	items []listening.Item
	err   error
}

func (b *backend) spec(name, host string) listening.Backend {
	search := func(context.Context, listening.Request) (listening.Result, error) {
		b.mu.Lock()
		defer b.mu.Unlock()

		b.calls++

		return listening.Result{Items: b.items}, b.err
	}

	return listening.Backend{Name: name, Host: host, Search: search}
}

func (b *backend) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.calls
}

func post(id, title string, daysAgo int) listening.Item {
	at := start.AddDate(0, 0, -daysAgo)

	return listening.Item{NativeID: id, URL: "https://example.com/" + id, Title: title, PublishedAt: &at}
}

func service(c *clock, platforms ...listening.Platform) *listening.Service {
	return listening.New(listening.Config{Platforms: platforms, Now: c.Now, Sleep: noSleep})
}

func plan(sources []string, queries ...string) listening.Plan {
	p := listening.Plan{Sources: sources}
	for _, q := range queries {
		p.Subqueries = append(p.Subqueries, listening.Subquery{Query: q})
	}

	return p
}

func coverageOf(t *testing.T, a listening.Answer, source string) listening.Coverage {
	t.Helper()

	for _, c := range a.Coverage {
		if c.Source == source {
			return c
		}
	}

	require.Failf(t, "no coverage", "no coverage line for %s in %+v", source, a.Coverage)

	return listening.Coverage{}
}

// Design row "Source registry": a source's backends are tried in order; one refused and one whose probe fails are
// passed, the first usable one serves, and the answer names it and lists each tried with its code.
func TestTheFirstUsableBackendServesAndEachTriedIsNamed(t *testing.T) {
	t.Parallel()

	c := newClock()
	refused := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	served := &backend{items: []listening.Item{post("p1", "VETC thu phí", 1)}}
	unprobed := (&backend{}).spec("needs_login", "b.example")
	unprobed.Probe = func(listening.Request) *listening.SourceError {
		return listening.Fail(listening.CodeNeedLogin, "no account is signed in")
	}

	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{
		refused.spec("json", "a.example"), unprobed, served.spec("archive", "c.example"),
	}})

	a, err := s.Search(t.Context(), plan([]string{"forum"}, "VETC"), listening.Caller{})
	require.NoError(t, err)

	cov := coverageOf(t, a, "forum")
	assert.Equal(t, listening.StateOK, cov.State)
	require.Len(t, cov.Streams, 1)
	assert.Equal(t, "archive", cov.Streams[0].ServedBy)
	assert.Equal(t, []listening.Attempt{
		{Backend: "json", Result: "refused", Detail: "HTTP 403", Until: start.Add(6 * time.Hour).Format(time.RFC3339)},
		{Backend: "needs_login", Result: "need_login", Detail: "no account is signed in", Skipped: true},
		{Backend: "archive", Result: "ok"},
	}, cov.Streams[0].Tried)
	require.Len(t, a.Items, 1)
	assert.Equal(t, "archive", a.Items[0].Backend)
	assert.Equal(t, "forum", a.Items[0].Source)
}

// Design row "Source registry": an opt-in backend is never asked unless the plan pins the source to it, and then it
// is the only one asked.
func TestAnOptInBackendIsAskedOnlyWhenPinned(t *testing.T) {
	t.Parallel()

	c := newClock()
	first := &backend{items: []listening.Item{post("p1", "first", 1)}}
	optIn := &backend{items: []listening.Item{post("p2", "opt-in", 1)}}
	spec := optIn.spec("signed_in", "b.example")
	spec.OptIn = true
	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{
		first.spec("json", "a.example"), spec,
	}})

	_, err := s.Search(t.Context(), plan([]string{"forum"}, "x"), listening.Caller{})
	require.NoError(t, err)
	assert.Equal(t, 0, optIn.Calls())

	p := plan([]string{"forum"}, "x")
	p.Backends = map[string]string{"forum": "signed_in"}
	a, err := s.Search(t.Context(), p, listening.Caller{})
	require.NoError(t, err)
	assert.Equal(t, 1, optIn.Calls())
	assert.Equal(t, 1, first.Calls(), "a pinned source asks only its pinned backend")
	assert.Equal(t, "signed_in", coverageOf(t, a, "forum").Streams[0].ServedBy)
}

// Design row "Source registry": after a refusal, a rate limit, a spent quota or a sign-in wall, the backends of the
// same site are not asked in this search, and a backend on another site still is.
func TestARefusalStopsTheSameSitesOtherBackendsOnly(t *testing.T) {
	t.Parallel()

	c := newClock()
	refused := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	sameSite := &backend{items: []listening.Item{post("p1", "x", 1)}}
	otherSite := &backend{items: []listening.Item{post("p2", "y", 1)}}
	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{
		refused.spec("json", "forum.example"), sameSite.spec("html", "forum.example"),
		otherSite.spec("archive", "archive.example"),
	}})

	a, err := s.Search(t.Context(), plan([]string{"forum"}, "x"), listening.Caller{})
	require.NoError(t, err)
	assert.Equal(t, 0, sameSite.Calls())
	assert.Equal(t, 1, otherSite.Calls())

	tried := coverageOf(t, a, "forum").Streams[0].Tried
	require.Len(t, tried, 3)
	assert.Equal(t, listening.Attempt{
		Backend: "html", Result: "refused", Detail: "its site refused this search a moment ago", Skipped: true,
	}, tried[1])
}

// Design row "SourceError codes": the codes are a closed set, and an HTTP answer or an error maps to one of them.
func TestFailuresMapToTheClosedSetOfCodes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []listening.Code{
		"need_login", "refused", "limit_reached", "rate_limited", "not_found", "timeout", "broken", "no_results",
	}, listening.Codes())

	h := http.Header{}
	h.Set("X-Ratelimit-Reset", "42")

	for status, want := range map[int]listening.Code{
		401: "need_login", 403: "refused", 404: "not_found", 410: "not_found", 429: "rate_limited",
		402: "limit_reached", 408: "timeout", 504: "timeout", 500: "broken", 502: "broken",
	} {
		f := listening.HTTPFailure(status, h, start)
		assert.Equal(t, want, f.Code, "HTTP %d", status)
		assert.Contains(t, f.Detail, fmt.Sprint(status))
	}

	assert.Equal(t, start.Add(42*time.Second), listening.HTTPFailure(429, h, start).Until,
		"a 429 rests as long as Reddit's x-ratelimit-reset says")
	assert.Equal(t, listening.CodeTimeout, listening.AsFailure(context.DeadlineExceeded).Code)
	assert.Equal(t, listening.CodeBroken, listening.AsFailure(errors.New("parse error")).Code)
	assert.Equal(t, listening.CodeRefused,
		listening.AsFailure(fmt.Errorf("wrapped: %w", listening.Fail(listening.CodeRefused, "x"))).Code)
}

// Design row "Coverage rule": only a clean zero is a zero; a source that failed is "not read" with its cause, and a
// source read in part says so.
func TestOnlyACleanZeroIsAZero(t *testing.T) {
	t.Parallel()

	c := newClock()
	refused := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	empty := &backend{}
	limited := &backend{err: listening.Fail(listening.CodeRateLimited, "HTTP 429")}
	found := &backend{items: []listening.Item{post("p1", "x", 1)}}

	half := listening.Platform{Name: "half", Backends: []listening.Backend{{
		Name: "flaky", Host: "half.example",
		Search: func(_ context.Context, r listening.Request) (listening.Result, error) {
			if r.Query == "second" {
				return listening.Result{}, listening.Fail(listening.CodeTimeout, "no answer")
			}

			return listening.Result{Items: []listening.Item{post("h1", "x", 1)}}, nil
		},
	}}}

	s := service(c,
		listening.Platform{Name: "blocked", Backends: []listening.Backend{refused.spec("json", "blocked.example")}},
		listening.Platform{Name: "quiet", Backends: []listening.Backend{empty.spec("json", "quiet.example")}},
		listening.Platform{Name: "mixed", Backends: []listening.Backend{
			limited.spec("json", "mixed.example"), found.spec("archive", "archive.example"),
		}},
		half,
	)

	a, err := s.Search(t.Context(), plan([]string{"blocked", "quiet", "mixed", "half"}, "first", "second"),
		listening.Caller{})
	require.NoError(t, err)

	blocked := coverageOf(t, a, "blocked")
	assert.Equal(t, listening.StateNotRead, blocked.State)
	assert.Equal(t, listening.CodeRefused, blocked.Code)
	assert.Equal(t, 0, blocked.Items)

	quiet := coverageOf(t, a, "quiet")
	assert.Equal(t, listening.StateNoResults, quiet.State)
	assert.Empty(t, quiet.Code)
	assert.Equal(t, listening.CodeNoResults, quiet.Streams[0].Code)

	mixed := coverageOf(t, a, "mixed")
	assert.Equal(t, listening.StateOK, mixed.State, "a fallback that served makes the source read")
	assert.Equal(t, "archive", mixed.Streams[0].ServedBy)

	partial := coverageOf(t, a, "half")
	assert.Equal(t, listening.StatePartial, partial.State)
	assert.Equal(t, listening.CodeTimeout, partial.Code)
	assert.Equal(t, 1, partial.Items)
}

// Design row "Item": a backend's item comes back stamped with its source, backend, subquery, fetch time, a stable id
// and its date's confidence; one dated outside the window is dropped.
func TestItemsCarryTheirOriginAndStayInTheWindow(t *testing.T) {
	t.Parallel()

	c := newClock()
	undated := listening.Item{NativeID: "u1", URL: "https://example.com/u1", Title: "undated"}
	b := &backend{items: []listening.Item{post("in", "inside", 3), post("old", "outside", 40), undated}}
	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("json", "a.example")}})

	p := plan([]string{"forum"}, "inside")
	p.Subqueries[0].Label = "main"
	a, err := s.Search(t.Context(), p, listening.Caller{})
	require.NoError(t, err)

	assert.Equal(t, "2026-10-28", a.From)
	assert.Equal(t, "2026-11-27", a.To)
	require.Len(t, a.Items, 2)

	byID := map[string]listening.Item{}
	for _, it := range a.Items {
		byID[it.NativeID] = it
	}

	in := byID["in"]
	assert.Equal(t, "forum", in.Source)
	assert.Equal(t, "json", in.Backend)
	assert.Equal(t, []string{"main"}, in.Subqueries)
	assert.Equal(t, start, in.FetchedAt)
	assert.Equal(t, "high", in.DateConfidence)
	assert.Regexp(t, `^it_[0-9a-f]{16}$`, in.ID)
	assert.Equal(t, "low", byID["u1"].DateConfidence)
	assert.Equal(t, 2, coverageOf(t, a, "forum").Items)

	again, err := s.Search(t.Context(), p, listening.Caller{})
	require.NoError(t, err)
	assert.Contains(t, slices.Collect(func(yield func(string) bool) {
		for _, it := range again.Items {
			if !yield(it.ID) {
				return
			}
		}
	}), in.ID, "the same post keeps its id")
}

// A plan that cannot run is refused with why, before any source is asked.
func TestAPlanThatCannotRunIsRefused(t *testing.T) {
	t.Parallel()

	c := newClock()
	b := &backend{}
	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("json", "a.example")}})

	for name, p := range map[string]listening.Plan{
		"no subquery":     {Sources: []string{"forum"}},
		"unknown source":  plan([]string{"tiktok"}, "x"),
		"no source":       plan(nil, "x"),
		"empty query":     plan([]string{"forum"}, " "),
		"too many":        plan([]string{"forum"}, "a", "b", "c", "d", "e", "f"),
		"window":          {Sources: []string{"forum"}, WindowDays: 400, Subqueries: []listening.Subquery{{Query: "x"}}},
		"unknown backend": {Sources: []string{"forum"}, Backends: map[string]string{"forum": "nope"}, Subqueries: []listening.Subquery{{Query: "x"}}},
	} {
		_, err := s.Search(t.Context(), p, listening.Caller{})
		require.ErrorIs(t, err, listening.ErrInvalid, name)
	}

	_, err := s.Search(t.Context(), plan([]string{"tiktok"}, "x"), listening.Caller{})
	require.ErrorContains(t, err, "the sources are forum")
	assert.Equal(t, 0, b.Calls())
}

// Design row "Web search backend": the web source reads the web search chain, an empty answer of every provider is a
// clean zero, every provider resting is a rate limit, and the answer says which provider answered.
func TestTheWebSourceReadsTheWebSearchChain(t *testing.T) {
	t.Parallel()

	c := newClock()
	s := listening.New(listening.Config{Platforms: listening.Sources(listening.Endpoints{}, http.DefaultClient),
		Now: c.Now, Sleep: noSleep})
	p := plan([]string{listening.SourceWeb}, "VETC")

	var asked []string

	web := func(_ context.Context, q string, limit int) (websearch.Answer, error) {
		asked = append(asked, fmt.Sprintf("%s/%d", q, limit))

		return websearch.Answer{Provider: "exa", Query: q, Results: []websearch.Result{
			{
				Title: "VETC lỗi", URL: "https://voz.vn/t/vetc.1/?utm_source=x", Snippet: "trừ tiền hai lần",
				PublishedAt: "2026-11-20",
			},
		}, Errors: []websearch.Failure{{Provider: "tavily", Reason: "HTTP 429"}}}, nil
	}

	a, err := s.Search(t.Context(), p, listening.Caller{Web: web})
	require.NoError(t, err)
	assert.Equal(t, []string{"VETC/20"}, asked, "the default 25, cut to the web search's 20")
	require.Len(t, a.Items, 1)
	assert.Equal(t, "voz.vn", a.Items[0].Container)
	assert.Equal(t, "https://voz.vn/t/vetc.1", a.Items[0].NativeID)
	assert.Equal(t, "answered by exa; tavily failed (HTTP 429)", coverageOf(t, a, "web").Streams[0].Tried[0].Detail)

	empty := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{}, fmt.Errorf("wrapped: %w", websearch.ErrNoResults)
	}
	a, err = s.Search(t.Context(), p, listening.Caller{Web: empty})
	require.NoError(t, err)
	assert.Equal(t, listening.StateNoResults, coverageOf(t, a, "web").State)

	resting := func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{}, &websearch.RestingError{Until: start.Add(time.Minute)}
	}
	a, err = s.Search(t.Context(), p, listening.Caller{Web: resting})
	require.NoError(t, err)
	assert.Equal(t, listening.CodeRateLimited, coverageOf(t, a, "web").Code)

	c.Advance(2 * time.Hour)
	a, err = s.Search(t.Context(), p, listening.Caller{})
	require.NoError(t, err)
	assert.Equal(t, listening.StateNotRead, coverageOf(t, a, "web").State, "with no web search the web is not read")
	assert.Equal(t, listening.CodeBroken, coverageOf(t, a, "web").Code)
}
