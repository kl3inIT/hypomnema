package modelgateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario: the runs of overflows of keys that are gone (issue #1052, review of PR #1057). A Dispatch's key is
// revoked when it ends and never comes back, so a run with no request in flight is forgotten once overflowCooldown
// passed since its last answer, as LiteLLM's count of a deployment's failures lives for its cooldown; the map holds
// only recent runs.
func TestARunOfOverflowsIsForgottenAfterTheCooldown(t *testing.T) {
	t.Parallel()

	o := newOverflows()
	start := time.Unix(1_800_000_000, 0)

	for range allowedOverflows + 1 {
		_, run := o.begin("gwk_gone", "sol", start)
		o.end("gwk_gone", "sol", start, run, answerFailed)
	}

	_, run := o.begin("gwk_live", "sol", start.Add(overflowCooldown+time.Second))
	o.end("gwk_live", "sol", start.Add(overflowCooldown+time.Second), run, answerFailed)

	o.mu.Lock()
	defer o.mu.Unlock()
	assert.Len(t, o.runs, 1)
	assert.Contains(t, o.runs, [2]string{"gwk_live", "sol"})
}

// Scenario: the answers fail slowly, as on 2026-11-19, when each of a reviewer's truncated answers took 7-19 s
// (issue #1052, review round 2 of PR #1057). An answer lasts longer than the cooldown, so the count is kept while the
// key's next request is in flight, and another key's request does not forget it: after four such answers the fifth
// request is refused.
func TestSlowFailedAnswersStillEndTheTurn(t *testing.T) {
	t.Parallel()

	o := newOverflows()
	now := time.Unix(1_800_000_000, 0)

	for range allowedOverflows + 1 {
		refused, run := o.begin("gwk_reviewer", "sol", now)
		assert.False(t, refused)

		// Another Dispatch's request, while this one's answer is still coming.
		_, other := o.begin("gwk_other", "sol", now.Add(6*time.Second))
		o.end("gwk_other", "sol", now.Add(6*time.Second), other, answerOK)

		now = now.Add(12 * time.Second)
		o.end("gwk_reviewer", "sol", now, run, answerFailed)
		now = now.Add(100 * time.Millisecond)
	}

	refused, _ := o.begin("gwk_reviewer", "sol", now)
	assert.True(t, refused)
}

// Scenario: an answer that ends well between failed ones (issue #1052). The count starts again: the owner's "no ok
// in between".
func TestAnAnswerThatEndsWellStartsTheCountAgain(t *testing.T) {
	t.Parallel()

	o := newOverflows()
	now := time.Unix(1_800_000_000, 0)

	for _, kind := range []answer{answerFailed, answerFailed, answerFailed, answerOK, answerFailed, answerFailed,
		answerFailed} {
		refused, run := o.begin("gwk", "sol", now)
		assert.False(t, refused)
		o.end("gwk", "sol", now, run, kind)
		now = now.Add(time.Second)
	}

	refused, _ := o.begin("gwk", "sol", now)
	assert.False(t, refused)
}
