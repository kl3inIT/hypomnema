package listening

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// How each source reads, for the Social listening page (issue #1290): what the last readings showed, in four groups,
// learned from last30days' doctor (lib/doctor.py:97-175), which puts every source in one of working, turned on but
// unverified, not working and could be on, from what its last run and a live probe showed:
//
//   - a source the owner switched off could be on, and is never counted as broken (doctor's tier off, audit_state;
//     an optional source that is off is no failure, quality_nudge.py:16-19);
//   - a source whose last reading in the last 24 hours passed is working: a real search that read it, items or a
//     clean zero (doctor's _RUN_WORKING_STATES, doctor.py:131), or its live test with items (Agent-Reach: a backend
//     passes "when the call succeeds and returns at least one item", channels/base.py:36-37); a reading older than a
//     day no longer counts, as Agent-Reach's checks are fresh for 24 hours (runtime/manage.py:27 and 93-104) and
//     doctor reads only a fresh run (doctor.py:1351-1366);
//   - a source read in part, or not read in the last day, is unverified (doctor's _RUN_UNVERIFIED_STATES and its
//     "no recent run", doctor.py:132 and 1887-1893; Agent-Reach "unverified", manage.py:245);
//   - any other last reading is broken (doctor.py:128-130).
//
// A source whose reading right before the latest passed and whose latest fails is "regressed", Agent-Reach's flag on a
// channel whose action passed in the previous check and fails in the latest (manage.py:189-216, which keeps the
// previous check's results beside the latest's; troubleshooting.md:37), whatever the time between the two: every
// reading counts, a real search's or a live test's (review round 1 of PR #1322: the issue's "yesterday" is the
// previous check, as Agent-Reach's).
//
// A source not read for a day is tested again: doctor runs a live probe when there is no fresh run ("if no recent
// runs, run a live test", doctor.py:2954-2958), with Agent-Reach's live test, a fixed search asked of each backend
// (channels/reddit.py:50, {"query": "python", "limit": 2}; manage.py:143-208). The owner tests one again with a press
// on its card, which first clears its rests, as an explicit doctor run clears Reddit's path skips ("a doctor run is
// how a user says 'try that path again'", doctor.py:2945-2950).
//
// When a source breaks the owner is told once, and told again only after a reading read items in between, passed or
// read in part: postiz flags a channel whose refresh failed only where the flag was not set, only the attempt that
// set it notifies (integration.repository.ts:603-616, refresh.integration.service.ts:148-166), and only a refresh
// that returns an access token clears it (refresh.integration.service.ts:20-54); a clean zero is no such proof. A
// failure that may come back by itself is not told: any backend it tried, or any provider behind one, failed with a
// rate limit, a spent quota or a timeout (reading.tells).

// The four groups of the page.
const (
	GroupWorking    = "working"
	GroupUnverified = "unverified"
	GroupBroken     = "broken"
	GroupCouldBeOn  = "could_be_on"
)

const (
	// Fresh is how long a reading counts: Agent-Reach CHECK_FRESH_SECONDS (runtime/manage.py:27).
	Fresh = 24 * time.Hour
	// TestEvery is how often the sources are looked at for one not read in the last day: the hour doctor keeps a
	// run's report fresh for (DEFAULT_REPORT_CACHE_TTL_SECONDS, doctor.py:1341).
	TestEvery = time.Hour
	// testQuery and testLimit are the live test's search: Agent-Reach's for Reddit (channels/reddit.py:50).
	testQuery = "python"
	testLimit = 2
	// readingDays are how many UTC days of items are kept: the week "items this week" counts (inference: no
	// reference counts a week; harken keeps running totals, store.py:102-121).
	readingDays = 7
)

// The result of a reading, as kept.
const (
	resultPassed  = "passed"
	resultPartial = "partial"
	resultFailed  = "failed"
)

// SourceState is a source as the page shows it.
type SourceState struct {
	Name  string
	Label string
	// Adds is what the source brings when it is on (last30days quality_nudge.py: each missing source says what it
	// would add).
	Adds string
	// Page is the page of Settings where its accounts are, if it has one (the Muse card links to Muse's).
	Page string
	On   bool
	// Group is GroupWorking, GroupUnverified, GroupBroken or GroupCouldBeOn.
	Group     string
	Regressed bool
	// CheckedAt is its last reading; LastSuccess its last that passed.
	CheckedAt   time.Time
	LastSuccess time.Time
	// ItemsThisWeek are the items real searches read in the last 7 UTC days, before duplicates are removed.
	ItemsThisWeek int
	LastError     *Failure
	// Code is why the last reading did not pass; empty when it passed or there is none.
	Code     Code
	Backends []BackendState
	// Signed is a signed-in source's consent and account (accounts.go); nil for a source read without one.
	Signed *SignedState
}

// BackendState is one of a source's backends, in its order.
type BackendState struct {
	Name  string
	OptIn bool
	// Resting is its rest now; nil when it may be asked.
	Resting   *SourceError
	LastOK    time.Time
	LastError *Failure
}

// Failure is a failure as kept: its code, what happened, where and when, and the one step that fixes it.
type Failure struct {
	Code    Code
	Detail  string
	Backend string
	At      time.Time
	Fix     string
}

// Broken is what the owner is told when a source breaks.
type Broken struct {
	Source string
	Code   Code
	Detail string
	Fix    string
}

// sourceRead is how a source read once: a real search's or a live test's.
type sourceRead struct {
	source string
	result string
	code   Code
	detail string
	// backend is the backend whose failure the detail is.
	backend string
	items   int
	// tried are its backends' answers, those asked.
	tried []Attempt
	// test: a live test, whose clean zero is a failure and whose items are not counted.
	test bool
	// off: the owner switched the source off while it was read; it is not kept.
	off bool
	// untested: a live test that asked no backend, every one being opt-in (issue #1289's Toutiao): it is not kept.
	untested bool
}

// tells says the reading tells the owner the source broke: it failed with a code a person must act on, and no backend
// it tried failed with one that rests and comes back by itself, as a fallback that is throttled serves again once
// its wait ends (review round 1 of PR #1322; Agent-Reach reports a channel whose failures are rate limits as a
// "cooldown", manage.py:236-238, and falls back on a retryable error, runtime/dispatch.py:276-322).
func (r sourceRead) tells() bool {
	if r.result != resultFailed || !tells(r.code) {
		return false
	}

	for _, a := range r.tried {
		if !tells(Code(a.Result)) || a.transient {
			return false
		}
	}

	return true
}

// coverageReading is what a real search's coverage of a source says of it.
func coverageReading(c Coverage) sourceRead {
	r := sourceRead{source: c.Source, code: c.Code, items: c.Items}

	switch c.State {
	case StateOK, StateNoResults:
		r.result = resultPassed
	case StatePartial:
		r.result = resultPartial
	default:
		r.result = resultFailed
	}

	for _, st := range c.Streams {
		r.tried = append(r.tried, st.Tried...)
	}

	r.detail, r.backend = attemptFailure(r.tried, r.code)

	return r
}

// attemptFailure is the detail and the backend of the first attempt that failed with the code, else of the first that
// failed at all.
func attemptFailure(tried []Attempt, code Code) (detail, backend string) {
	for _, pass := range []bool{true, false} {
		for _, a := range tried {
			if a.Result == resultOK || a.Result == string(CodeNoResults) && code != CodeNoResults {
				continue
			}

			if !pass || a.Result == string(code) {
				return a.Detail, a.Backend
			}
		}
	}

	return "", ""
}

// record keeps a reading and tells the owner when it is the one that broke the source. It never fails the search
// it is of: a write that fails is logged.
func (s *Service) record(ctx context.Context, r sourceRead) {
	if s.db == nil || r.off || r.untested {
		return
	}

	ctx = context.WithoutCancel(ctx)

	var flagged bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		day := time.UnixMilli(now).UTC().Format(time.DateOnly)

		for _, a := range r.tried {
			if a.Skipped || a.Backend == "" {
				continue
			}

			ok := a.Result == resultOK || a.Result == string(CodeNoResults) && !r.test
			if ok {
				if err := q.RecordListeningBackendOK(ctx, store.RecordListeningBackendOKParams{
					Source: r.source, Backend: a.Backend, Now: now,
				}); err != nil {
					return err
				}

				continue
			}

			if err := q.RecordListeningBackendError(ctx, store.RecordListeningBackendErrorParams{
				Source: r.source, Backend: a.Backend, Code: a.Result, Detail: a.Detail, Now: now,
			}); err != nil {
				return err
			}
		}

		if !r.test {
			if err := q.AddListeningItems(ctx, store.AddListeningItemsParams{
				Source: r.source, Day: day, Items: int64(r.items),
			}); err != nil {
				return err
			}
		}

		if err := q.PruneListeningReadings(ctx, time.UnixMilli(now).UTC().AddDate(0, 0, 1-readingDays).
			Format(time.DateOnly)); err != nil {
			return err
		}

		if err := q.RecordListeningRead(ctx, store.RecordListeningReadParams{
			Source: r.source, Now: now, Result: r.result, Code: string(r.code),
		}); err != nil {
			return err
		}

		switch r.result {
		case resultPassed:
			if err := q.RecordListeningSuccess(ctx, store.RecordListeningSuccessParams{
				Source: r.source, Now: now,
			}); err != nil {
				return err
			}

			if r.items == 0 {
				return nil
			}

			return q.ClearListeningTold(ctx, store.ClearListeningToldParams{Source: r.source, Now: now})
		case resultPartial, resultFailed:
			if err := q.RecordListeningError(ctx, store.RecordListeningErrorParams{
				Source: r.source, Code: string(r.code), Detail: r.detail, Backend: r.backend, Now: now,
			}); err != nil {
				return err
			}

			// A read in part that read items proves the source reads again, as a passed one with items does: it
			// clears the flag too (review round 2 of PR #1322).
			if r.result == resultPartial && r.items > 0 {
				return q.ClearListeningTold(ctx, store.ClearListeningToldParams{Source: r.source, Now: now})
			}
		}

		if !r.tells() {
			return nil
		}

		// A signed-in source failing with its account's own state was told when the account came to it (accounts.go
		// met): the same expired session is not told again, after its fallback served in between too.
		if r.code == CodeNeedLogin || r.code == CodeRefused {
			a, err := q.GetListeningAccount(ctx, r.source)
			if err == nil && a.State == string(r.code) {
				return nil
			}
		}

		n, err := q.FlagListeningTold(ctx, store.FlagListeningToldParams{Source: r.source, Now: now})
		flagged = n == 1

		return err
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "a listening reading was not kept", slog.String("event", "listening.record_failed"),
			slog.String("source", r.source), slog.String("error", err.Error()))

		return
	}

	if flagged && s.tell != nil {
		s.tell(ctx, Broken{Source: r.source, Code: r.code, Detail: r.detail, Fix: Fix(r.source, r.code, time.Time{})})
	}
}

// States are the sources as the page shows them, in order.
func (s *Service) States(ctx context.Context) ([]SourceState, error) {
	platforms, on, err := s.live(ctx)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	kept := s.kept(ctx, now)

	if kept.err != nil {
		return nil, kept.err
	}

	signed, err := s.signedStates(ctx, kept)
	if err != nil {
		return nil, err
	}

	out := make([]SourceState, 0, len(platforms))

	for _, p := range platforms {
		st := SourceState{
			Name: p.Name, Label: Label(p.Name), Adds: p.Adds, Page: p.Page, On: on[p.Name], Signed: signed[p.Name],
		}

		src := kept.sources[p.Name]
		st.LastSuccess = at(src.LastSuccessAt)

		if src.LastErrorAt.Valid {
			code := Code(src.LastErrorCode)
			st.LastError = &Failure{
				Code: code, Detail: src.LastErrorDetail, Backend: src.LastErrorBackend, At: at(src.LastErrorAt),
				Fix: Fix(p.Name, code, s.untilOf(p, src.LastErrorBackend)),
			}
		}

		for _, d := range kept.readings[p.Name] {
			if d.Day >= now.AddDate(0, 0, 1-readingDays).Format(time.DateOnly) {
				st.ItemsThisWeek += int(d.Items)
			}
		}

		st.CheckedAt = at(src.LastReadAt)
		st.Group, st.Code, st.Regressed = groupOf(st.On, src, now)

		for _, b := range p.Backends {
			bs := BackendState{Name: b.Name, OptIn: b.OptIn, Resting: s.restOf(p.Name + "/" + b.Name)}

			row := kept.backends[p.Name+"/"+b.Name]
			bs.LastOK = at(row.LastOkAt)

			if row.LastErrorAt.Valid {
				code := Code(row.LastCode)
				bs.LastError = &Failure{
					Code: code, Detail: row.LastDetail, Backend: b.Name, At: at(row.LastErrorAt),
					Fix: Fix(p.Name, code, s.untilOf(p, b.Name)),
				}
			}

			st.Backends = append(st.Backends, bs)
		}

		out = append(out, st)
	}

	return out, nil
}

// groupOf is a source's group from its last reading, with that reading's code when it did not pass, and whether it
// regressed: it fails and the reading right before it passed (Agent-Reach regressed(), manage.py:211-216).
func groupOf(on bool, src store.ListeningSource, now time.Time) (string, Code, bool) {
	if !on {
		return GroupCouldBeOn, "", false
	}

	if !src.LastReadAt.Valid || now.Sub(at(src.LastReadAt)) >= Fresh {
		return GroupUnverified, "", false
	}

	code := Code(src.LastCode)

	switch src.LastResult {
	case resultPassed:
		return GroupWorking, "", false
	case resultPartial:
		return GroupUnverified, code, false
	}

	return GroupBroken, code, src.PreviousResult == resultPassed
}

// untilOf is when a source's backend's rest ends; zero when it does not rest.
func (s *Service) untilOf(p Platform, backend string) time.Time {
	if backend == "" {
		return time.Time{}
	}

	if r := s.restOf(p.Name + "/" + backend); r != nil {
		return r.Until
	}

	return time.Time{}
}

// keptState is what is kept of every source, read at once.
type keptState struct {
	sources  map[string]store.ListeningSource
	backends map[string]store.ListeningBackend
	readings map[string][]store.ListeningReading
	err      error
}

func (s *Service) kept(ctx context.Context, now time.Time) keptState {
	out := keptState{
		sources: map[string]store.ListeningSource{}, backends: map[string]store.ListeningBackend{},
		readings: map[string][]store.ListeningReading{},
	}
	if s.db == nil {
		return out
	}

	q := store.New(s.db)

	sources, err := q.ListListeningSources(ctx)
	if err != nil {
		out.err = fmt.Errorf("reading the listening sources: %w", err)

		return out
	}

	for _, r := range sources {
		out.sources[r.Source] = r
	}

	backends, err := q.ListListeningBackends(ctx)
	if err != nil {
		out.err = fmt.Errorf("reading the listening backends: %w", err)

		return out
	}

	for _, r := range backends {
		out.backends[r.Source+"/"+r.Backend] = r
	}

	readings, err := q.ListListeningReadings(ctx, now.AddDate(0, 0, 1-readingDays).Format(time.DateOnly))
	if err != nil {
		out.err = fmt.Errorf("reading the listening readings: %w", err)

		return out
	}

	for _, r := range readings {
		out.readings[r.Source] = append(out.readings[r.Source], r)
	}

	return out
}

// Test reads a source with its live test, each of its backends asked in turn (Agent-Reach run_checks,
// runtime/manage.py:143-208), and keeps what it found; again clears its rests first, the owner asking to try it now
// (doctor.py:2945-2950). It passes when one backend reads an item (manage.py:181-185).
func (s *Service) Test(ctx context.Context, source string, web WebSearch, again bool) error {
	platforms, on, err := s.live(ctx)
	if err != nil {
		return err
	}

	i := slices.IndexFunc(platforms, func(p Platform) bool { return p.Name == source })

	switch {
	case i < 0:
		return fmt.Errorf("%w: no listening source %q", ErrNotFound, source)
	case !on[source]:
		return fmt.Errorf("%w: %s is switched off; switch it on to test it", ErrInvalid, source)
	}

	p := platforms[i]
	if again {
		s.clearRests(p)
	}

	s.record(ctx, s.test(ctx, p, web))

	return nil
}

// test asks each of a source's backends the live test's search, as Agent-Reach checks each backend of an action
// (only_backend, runtime/manage.py:165-167). An opt-in backend, never asked unless pinned, is not tested.
func (s *Service) test(ctx context.Context, p Platform, web WebSearch) sourceRead {
	// A live test reads the source now, never the web search's cached answer (websearch.Fresh).
	ctx = websearch.Fresh(s.searchContext(ctx))
	now := s.now().UTC()

	// The source's own live query when it names one (issue #1289's LiveQuery, Agent-Reach's live_test per action),
	// else Agent-Reach's Reddit one.
	query := p.LiveQuery
	if query == "" {
		query = testQuery
	}

	sq := Subquery{Label: "test", Query: query, Weight: 1}
	r := sourceRead{source: p.Name, test: true, result: resultFailed, untested: true}

	var failed []Code

	for _, b := range p.Backends {
		if b.OptIn {
			continue
		}

		r.untested = false

		// No window: the test asks whether the backend reads, not whether people spoke lately, and Agent-Reach's
		// live tests name none (channels/reddit.py:50). Read on 2026-11-28, 30 days and then 365 left the
		// archive's route with no Python thread in the window although it filled 15 of 15.
		req := s.owned(s.request(Plan{Topic: query}, Caller{Web: web}), p.Name)
		req.Query, req.Limit, req.To = query, testLimit, now
		st := s.runStream(ctx, p, b.Name, sq, req)
		r.tried = append(r.tried, st.report.Tried...)

		if st.off {
			r.off = true

			return r
		}

		if st.report.ServedBy != "" {
			r.result = resultPassed
			r.items += st.report.Items
		} else {
			failed = append(failed, st.report.Code)
		}
	}

	if r.result == resultFailed {
		r.code = CodeBroken
		if len(failed) > 0 {
			r.code = slices.MinFunc(failed, func(a, b Code) int { return specificity(a) - specificity(b) })
		}

		r.detail, r.backend = attemptFailure(r.tried, r.code)
		if r.code == CodeNoResults && r.detail == "" {
			r.detail = "the live test's search found nothing"
		}
	}

	return r
}

// TestStale tests each source that is on and was not read in the last day.
func (s *Service) TestStale(ctx context.Context, web WebSearch) {
	states, err := s.States(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "the listening sources could not be read", slog.String("event",
			"listening.states_failed"), slog.String("error", err.Error()))

		return
	}

	for _, st := range states {
		if !st.On || !st.CheckedAt.IsZero() && s.now().Sub(st.CheckedAt) < Fresh {
			continue
		}

		if err := s.Test(ctx, st.Name, web, false); err != nil {
			s.logger.WarnContext(ctx, "a listening source could not be tested", slog.String("event",
				"listening.test_failed"), slog.String("source", st.Name), slog.String("error", err.Error()))
		}
	}
}

// Run tests the sources not read in the last day, at once and then every TestEvery, until ctx ends.
func (s *Service) Run(ctx context.Context, web WebSearch) {
	tick := time.NewTicker(TestEvery)
	defer tick.Stop()

	for {
		s.TestStale(ctx, web)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
