package listening_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// Review round 3 of PR #1325: each test is a reviewer's probe.

// wrongThenAt is a Muse that gives a list of another platform twice, then answers last on its third call, the clock at
// the questions' end (six minutes less museCheckTime's 25 s).
func wrongThenAt(c *clock, last muse.Result) *fakeMuse {
	wrong := answeredWith(block(1, "https://vnexpress.net/vetc-1.html", "VnExpress", "VnExpress", "2026-11-08",
		"caption", "VETC", "none"))

	return &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n < 2 {
			c.Advance(90 * time.Second)

			return wrong, nil
		}

		c.Advance(start.Add(6*time.Minute - 25*time.Second).Sub(c.Now()))

		return last, nil
	}}
}

// P1: the pool answers a question its deadline cut before it was sent with OutcomeError and the deadline's error
// (muse/ask.go: "Muse was not asked: …"); that is the search's time running out, a timeout, not broken.
func TestReviewThreeTimeRunningOutBeforeAContinuationIsSentIsATimeout(t *testing.T) {
	t.Parallel()

	c := newClock()
	m := wrongThenAt(c, muse.Result{Outcome: muse.OutcomeError, Note: "Muse was not asked: context deadline exceeded",
		Thread: museThread})

	got, err := museService(c, m, newPages(t)).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), nil)
	require.NoError(t, err)

	require.Len(t, m.questions(), 3)

	cov := coverageOf(t, got, listening.SourceThreads)
	assert.Equal(t, listening.CodeTimeout, cov.Code)
}

// P2: a continuation sent and cut by the deadline keeps the reason the lists before it were not taken.
func TestReviewThreeAContinuationCutByTheDeadlineKeepsTheRejection(t *testing.T) {
	t.Parallel()

	c := newClock()
	m := wrongThenAt(c, muse.Result{Outcome: muse.OutcomeUnknown,
		Note: "Sent, result unknown: context deadline exceeded", Thread: museThread})

	got, err := museService(c, m, newPages(t)).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), nil)
	require.NoError(t, err)

	cov := coverageOf(t, got, listening.SourceThreads)
	assert.Equal(t, listening.CodeTimeout, cov.Code)
	require.NotEmpty(t, cov.Streams[0].Tried)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "none with a link to a Threads post")
}

// P2: an account asked with all three extra calls, Muse still working each time, was asked: the detail says so.
func TestReviewThreeTheExtraDetailSaysWhatWasAsked(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)
	u := threadsURL("a.one", codeNov4)
	p.post(u, u, "VETC một")

	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(block(1, u, "@a.one", "@a.one", "2026-11-04", "caption", "VETC một", "none")), nil
		}

		return answeredWith("I'm still opening those posts; I'll send them once the check returns."), nil
	}}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), nil)
	require.NoError(t, err)
	require.Len(t, m.questions(), 4)

	detail := coverageOf(t, got, listening.SourceThreads).Streams[0].Tried[0].Detail
	assert.NotContains(t, detail, "more from @a.one not asked")
	assert.Contains(t, detail, "more from @a.one: asked 3 times")
}
