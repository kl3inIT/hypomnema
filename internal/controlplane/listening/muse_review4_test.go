package listening_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// Review round 4 of PR #1325, P1: the pool confirms a spent quota and then reads Usage, which may meet the deadline
// (muse/ask.go outcome: ErrQuota, then readUsage); its answer is still the quota's, OutcomeError with "out of quota"
// and no Go error. A confirmed quota stays limit_reached, resting until 00:00 UTC, however late it came.
func TestReviewFourAConfirmedQuotaAtTheDeadlineStaysLimitReached(t *testing.T) {
	t.Parallel()

	c := newClock()
	m := &fakeMuse{answer: func(int, muse.Question) (muse.Result, error) {
		c.Advance(6*time.Minute - 25*time.Second)

		return muse.Result{Outcome: muse.OutcomeError, Thread: museThread,
			Note: "Muse account 1 is out of quota and rests until its Usage shows room."}, nil
	}}

	got, err := museService(c, m, newPages(t)).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"),
		listening.Caller{})
	require.NoError(t, err)

	cov := coverageOf(t, got, listening.SourceThreads)
	assert.Equal(t, listening.CodeLimitReached, cov.Code)
	require.NotEmpty(t, cov.Streams[0].Tried)
	assert.Equal(t, "2026-11-28T00:00:00Z", cov.Streams[0].Tried[0].Until, "rests until 00:00 UTC")
}
