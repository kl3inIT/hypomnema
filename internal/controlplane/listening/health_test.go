package listening_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// The Social listening page's design rows (issue #1290), one test each.

// set changes what a backend answers from its next call.
func (b *backend) set(items []listening.Item, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.items, b.err = items, err
}

// told keeps what the owner was told.
type told struct {
	mu  sync.Mutex
	all []listening.Broken
}

func (t *told) tell(_ context.Context, b listening.Broken) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.all = append(t.all, b)
}

func (t *told) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.all)
}

func migrated(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

// kept is a Service that keeps its settings and readings in a migrated database.
func kept(t *testing.T, c *clock, tell *told, platforms ...listening.Platform) *listening.Service {
	t.Helper()

	cfg := listening.Config{Platforms: platforms, Now: c.Now, Sleep: noSleep, DB: migrated(t)}
	if tell != nil {
		cfg.Tell = tell.tell
	}

	return listening.New(cfg)
}

func stateOf(t *testing.T, s *listening.Service, source string) listening.SourceState {
	t.Helper()

	states, err := s.States(t.Context())
	require.NoError(t, err)

	for _, st := range states {
		if st.Name == source {
			return st
		}
	}

	require.Failf(t, "no state", "no state for %s in %+v", source, states)

	return listening.SourceState{}
}

func search(t *testing.T, s *listening.Service, sources ...string) listening.Answer {
	t.Helper()

	a, err := s.Search(t.Context(), plan(sources, "VETC"), listening.Caller{})
	require.NoError(t, err)

	return a
}

// Design row "Four counted groups": a source switched off could be on, one whose last reading in the day passed is
// working (a clean zero too), one read in part or not read for a day is unverified, any other is broken
// (last30days doctor.py audit_state; Agent-Reach's 24 hours).
func TestEachSourceIsInOneOfFourGroups(t *testing.T) {
	t.Parallel()

	c := newClock()
	ok := &backend{items: []listening.Item{post("p1", "VETC", 1)}}
	empty := &backend{}
	down := &backend{err: listening.Fail(listening.CodeBroken, "a page, not JSON")}
	unread := &backend{items: []listening.Item{post("u1", "VETC", 1)}}
	off := &backend{err: listening.Fail(listening.CodeBroken, "down")}
	// half reads one subquery and is refused the other: read in part.
	half := listening.Backend{
		Name: "api", Host: "half.example",
		Search: func(_ context.Context, r listening.Request) (listening.Result, error) {
			if r.Query == "ETC" {
				return listening.Result{}, listening.Fail(listening.CodeRefused, "HTTP 403")
			}

			return listening.Result{Items: []listening.Item{post("h1", "VETC", 1)}}, nil
		},
	}

	s := kept(t, c, nil,
		listening.Platform{Name: "ok", Backends: []listening.Backend{ok.spec("api", "ok.example")}},
		listening.Platform{Name: "empty", Backends: []listening.Backend{empty.spec("api", "empty.example")}},
		listening.Platform{Name: "half", Backends: []listening.Backend{half}},
		listening.Platform{Name: "down", Backends: []listening.Backend{down.spec("api", "down.example")}},
		listening.Platform{Name: "unread", Backends: []listening.Backend{unread.spec("api", "unread.example")}},
		listening.Platform{Name: "off", Backends: []listening.Backend{off.spec("api", "off.example")}},
	)

	search(t, s, "unread")
	c.Advance(listening.Fresh + time.Minute)

	search(t, s, "ok", "empty", "down", "off")
	_, err := s.Search(t.Context(), listening.Plan{
		Sources: []string{"half"}, Subqueries: []listening.Subquery{{Query: "VETC"}, {Query: "ETC"}},
	}, listening.Caller{})
	require.NoError(t, err)
	require.NoError(t, s.SetSource(t.Context(), "off", false))

	groups := map[string]string{}
	for _, name := range []string{"ok", "empty", "half", "down", "unread", "off"} {
		groups[name] = stateOf(t, s, name).Group
	}

	assert.Equal(t, map[string]string{
		"ok": listening.GroupWorking, "empty": listening.GroupWorking, "half": listening.GroupUnverified,
		"down": listening.GroupBroken, "unread": listening.GroupUnverified, "off": listening.GroupCouldBeOn,
	}, groups)
	assert.Equal(t, listening.CodeRefused, stateOf(t, s, "half").Code, "why it was read in part")
	assert.Equal(t, listening.CodeBroken, stateOf(t, s, "down").Code)
}

// Design row "An optional source that is off is never counted as broken" (quality_nudge.py:16-19): a source that
// failed and was then switched off could be on, and its card says what it adds.
func TestASourceSwitchedOffIsNeverBroken(t *testing.T) {
	t.Parallel()

	c := newClock()
	down := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	s := kept(t, c, nil, listening.Platform{
		Name: "forum", Adds: "Threads from the forum", Backends: []listening.Backend{down.spec("api", "f.example")},
	})

	search(t, s, "forum")
	require.Equal(t, listening.GroupBroken, stateOf(t, s, "forum").Group)

	require.NoError(t, s.SetSource(t.Context(), "forum", false))

	st := stateOf(t, s, "forum")
	assert.Equal(t, listening.GroupCouldBeOn, st.Group)
	assert.False(t, st.On)
	assert.Equal(t, "Threads from the forum", st.Adds)
	assert.False(t, st.Regressed)

	on, err := s.On(t.Context())
	require.NoError(t, err)
	assert.Empty(t, on, "a source that is off is not offered")

	_, err = s.Search(t.Context(), plan([]string{"forum"}, "VETC"), listening.Caller{})
	require.ErrorIs(t, err, listening.ErrInvalid, "a plan may not name a source that is off")
	assert.Contains(t, err.Error(), "switched off")
	assert.Equal(t, 1, down.Calls())

	require.NoError(t, s.SetSource(t.Context(), "forum", true))
	assert.Equal(t, listening.GroupBroken, stateOf(t, s, "forum").Group, "on again, its last reading counts")
}

// Design row "A card per platform": its backends in its order, the last success, the items of this week, and the
// last error with one fix step (harken source_metrics, BettaFish crawling_tasks, last30days prescriptions.py).
func TestACardHasItsBackendsLastSuccessItemsThisWeekAndLastError(t *testing.T) {
	t.Parallel()

	c := newClock()
	first := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	second := &backend{items: []listening.Item{post("p1", "VETC", 0), post("p2", "VETC thu phí", 0)}}
	s := kept(t, c, nil, listening.Platform{Name: "reddit", Backends: []listening.Backend{
		first.spec("reddit_json", "reddit.example"), second.spec("web_arctic", "arctic.example"),
	}})

	// Eight days ago: not this week.
	c.Advance(-7 * 24 * time.Hour)
	search(t, s, "reddit")
	c.Advance(7 * 24 * time.Hour)

	search(t, s, "reddit")

	st := stateOf(t, s, "reddit")
	assert.Equal(t, listening.GroupWorking, st.Group, "the second backend served")
	assert.Equal(t, 2, st.ItemsThisWeek, "the reading of eight days ago is not this week's")
	assert.Equal(t, start, st.LastSuccess)
	assert.Equal(t, start, st.CheckedAt)
	require.Len(t, st.Backends, 2)
	assert.Equal(t, "reddit_json", st.Backends[0].Name)
	assert.Equal(t, "web_arctic", st.Backends[1].Name)
	assert.Equal(t, start, st.Backends[1].LastOK)

	refused := st.Backends[0]
	require.NotNil(t, refused.Resting, "a refusal rests its backend 6 hours")
	assert.Equal(t, start.Add(listening.RefusedRest), refused.Resting.Until)
	require.NotNil(t, refused.LastError)
	assert.Equal(t, listening.CodeRefused, refused.LastError.Code)
	assert.Equal(t, "HTTP 403", refused.LastError.Detail)
	assert.Equal(t, listening.Fix("reddit", listening.CodeRefused, start.Add(listening.RefusedRest)),
		refused.LastError.Fix)
	assert.Nil(t, st.LastError, "the source never failed: a backend did")

	second.set(nil, listening.Fail(listening.CodeBroken, "a page, not JSON"))
	c.Advance(time.Hour)
	search(t, s, "reddit")

	st = stateOf(t, s, "reddit")
	assert.Equal(t, listening.GroupBroken, st.Group)
	require.NotNil(t, st.LastError)
	assert.Equal(t, listening.CodeRefused, st.LastError.Code, "the most actionable code of the reading")
	assert.Equal(t, "reddit_json", st.LastError.Backend)
	assert.Equal(t, start.Add(time.Hour), st.LastError.At)
	assert.NotEmpty(t, st.LastError.Fix)
	assert.Equal(t, start, st.LastSuccess, "the last success stays")
}

// Design row "the owner is told once when a source breaks" (postiz refreshNeeded, flagged where it was not, and only
// the attempt that flagged it notifies): a second failure tells nothing; a success clears the flag; a rate limit,
// which rests and comes back by itself, is not told.
func TestTheOwnerIsToldOnceWhenASourceBreaks(t *testing.T) {
	t.Parallel()

	c := newClock()
	b := &backend{err: listening.Fail(listening.CodeRateLimited, "HTTP 429")}
	tell := &told{}
	s := kept(t, c, tell, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("api", "f.example")}})

	search(t, s, "forum")
	assert.Equal(t, 0, tell.Count(), "a rate limit is not told")

	c.Advance(2 * time.Hour)
	b.set(nil, listening.Fail(listening.CodeBroken, "a page, not JSON"))
	search(t, s, "forum")
	c.Advance(time.Hour)
	search(t, s, "forum")
	require.Equal(t, 1, tell.Count(), "told once")
	assert.Equal(t, listening.Broken{
		Source: "forum", Code: listening.CodeBroken, Detail: "a page, not JSON",
		Fix: listening.Fix("forum", listening.CodeBroken, time.Time{}),
	}, tell.all[0])

	c.Advance(time.Hour)
	b.set([]listening.Item{post("p1", "VETC", 0)}, nil)
	search(t, s, "forum")

	c.Advance(time.Hour)
	b.set(nil, listening.Fail(listening.CodeBroken, "a page, not JSON"))
	search(t, s, "forum")
	assert.Equal(t, 2, tell.Count(), "it worked in between: told again")
}

// Design row "Regressed": a source that passed yesterday and fails today regressed (Agent-Reach regressed());
// one that failed both days did not.
func TestASourceThatPassedYesterdayAndFailsTodayRegressed(t *testing.T) {
	t.Parallel()

	c := newClock()
	b := &backend{items: []listening.Item{post("p1", "VETC", 0)}}
	s := kept(t, c, nil, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("api", "f.example")}})

	search(t, s, "forum")

	c.Advance(24 * time.Hour)
	b.set(nil, listening.Fail(listening.CodeBroken, "down"))
	search(t, s, "forum")

	st := stateOf(t, s, "forum")
	assert.Equal(t, listening.GroupBroken, st.Group)
	assert.True(t, st.Regressed)

	c.Advance(24 * time.Hour)
	search(t, s, "forum")
	assert.False(t, stateOf(t, s, "forum").Regressed, "it failed yesterday too")
}

// Design row "API: set the backend order": the owner's order is the order a search tries; an order that does not
// name each backend once is refused.
func TestTheOwnersOrderIsTheOrderASearchTries(t *testing.T) {
	t.Parallel()

	c := newClock()
	first := &backend{items: []listening.Item{post("a1", "VETC", 0)}}
	second := &backend{items: []listening.Item{post("b1", "VETC", 0)}}
	s := kept(t, c, nil, listening.Platform{Name: "reddit", Backends: []listening.Backend{
		first.spec("reddit_json", "reddit.example"), second.spec("web_arctic", "arctic.example"),
	}})

	require.ErrorIs(t, s.SetOrder(t.Context(), "reddit", []string{"web_arctic"}), listening.ErrInvalid)
	require.ErrorIs(t, s.SetOrder(t.Context(), "reddit", []string{"web_arctic", "web_arctic"}), listening.ErrInvalid)
	require.ErrorIs(t, s.SetOrder(t.Context(), "nowhere", []string{"x"}), listening.ErrNotFound)
	require.ErrorIs(t, s.SetSource(t.Context(), "nowhere", false), listening.ErrNotFound)

	require.NoError(t, s.SetOrder(t.Context(), "reddit", []string{"web_arctic", "reddit_json"}))

	a := search(t, s, "reddit")
	assert.Equal(t, "web_arctic", coverageOf(t, a, "reddit").Streams[0].ServedBy)
	assert.Equal(t, 0, first.Calls())

	st := stateOf(t, s, "reddit")
	assert.Equal(t, []string{"web_arctic", "reddit_json"}, []string{st.Backends[0].Name, st.Backends[1].Name})
}

// Design rows "Regressed" and the "!" card's click: a source not read for a day is tested with the live test, each
// backend asked in turn (Agent-Reach check), and passes when one reads an item; the owner's test clears its rests
// first (doctor clears its skips). A test's items are not this week's; its clean zero is a failure.
func TestASourceNotReadForADayIsTestedAndTheOwnersTestClearsItsRests(t *testing.T) {
	t.Parallel()

	c := newClock()
	refused := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	served := &backend{items: []listening.Item{post("p1", "python", 0)}}
	other := &backend{items: []listening.Item{post("o1", "python", 0)}}
	s := kept(t, c, nil,
		listening.Platform{Name: "reddit", Backends: []listening.Backend{
			refused.spec("reddit_json", "reddit.example"), served.spec("web_arctic", "arctic.example"),
		}},
		listening.Platform{Name: "forum", Backends: []listening.Backend{other.spec("api", "f.example")}},
	)

	search(t, s, "forum")
	s.TestStale(t.Context(), nil)

	assert.Equal(t, 1, refused.Calls(), "each backend is asked")
	assert.Equal(t, 1, served.Calls())
	assert.Equal(t, 1, other.Calls(), "forum was read in the last day: not tested")

	st := stateOf(t, s, "reddit")
	assert.Equal(t, listening.GroupWorking, st.Group, "one backend read an item")
	assert.Equal(t, 0, st.ItemsThisWeek, "a test's items are not counted")

	s.TestStale(t.Context(), nil)
	assert.Equal(t, 1, served.Calls(), "tested an hour ago")

	require.NoError(t, s.Test(t.Context(), "reddit", nil, false))
	assert.Equal(t, 1, refused.Calls(), "it rests: not asked")

	require.NoError(t, s.Test(t.Context(), "reddit", nil, true))
	assert.Equal(t, 2, refused.Calls(), "the owner's test clears its rest")

	served.set(nil, nil)
	refused.set(nil, nil)
	require.NoError(t, s.Test(t.Context(), "reddit", nil, true))

	st = stateOf(t, s, "reddit")
	assert.Equal(t, listening.GroupBroken, st.Group, "the live test found nothing")
	assert.Equal(t, listening.CodeNoResults, st.Code)

	require.NoError(t, s.SetSource(t.Context(), "forum", false))
	require.ErrorIs(t, s.Test(t.Context(), "forum", nil, true), listening.ErrInvalid)
	require.ErrorIs(t, s.Test(t.Context(), "nowhere", nil, true), listening.ErrNotFound)
}

// Design row "one fix text, shared by the page and the tool's answer" (last30days prescriptions.py): the coverage
// line of a source that was not read says the same fix the page shows.
func TestTheToolsCoverageAndThePageSayTheSameFix(t *testing.T) {
	t.Parallel()

	c := newClock()
	b := &backend{err: listening.Fail(listening.CodeRefused, "HTTP 403")}
	s := kept(t, c, nil, listening.Platform{Name: "forum", Backends: []listening.Backend{b.spec("api", "f.example")}})

	cov := coverageOf(t, search(t, s, "forum"), "forum")
	require.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.Fix("forum", listening.CodeRefused, start.Add(listening.RefusedRest)), cov.Fix)

	st := stateOf(t, s, "forum")
	require.NotNil(t, st.LastError)
	assert.Equal(t, cov.Fix, st.LastError.Fix)

	for _, code := range listening.Codes() {
		assert.NotEmpty(t, listening.Fix("forum", code, time.Time{}), "every code has its fix: %s", code)
	}
}

// The live test asks whether a backend reads, not whether people spoke lately: it reads with no window, as
// Agent-Reach's live tests name none (channels/reddit.py:50). Read live on 2026-11-28: with 30 days, and later the
// same day with 365, the archive's route filled 15 of 15 Python threads and none was in the window.
func TestTheLiveTestReadsWithNoWindow(t *testing.T) {
	t.Parallel()

	c := newClock()
	old := &backend{items: []listening.Item{post("o1", "python", 800)}}
	s := kept(t, c, nil, listening.Platform{Name: "forum", Backends: []listening.Backend{old.spec("api", "f.example")}})

	require.NoError(t, s.Test(t.Context(), "forum", nil, false))
	assert.Equal(t, listening.GroupWorking, stateOf(t, s, "forum").Group)
}
