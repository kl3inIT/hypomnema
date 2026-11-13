package subscription_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// claudeWindows are the headers Anthropic puts on an answer: each window's used
// part and the second it starts again.
func claudeWindows(
	now time.Time, fiveUsed float64, fiveReset time.Duration, weekUsed float64, weekReset time.Duration,
) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", strconv.FormatFloat(fiveUsed, 'f', -1, 64))
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(fiveReset).Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", strconv.FormatFloat(weekUsed, 'f', -1, 64))
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(weekReset).Unix(), 10))

	return h
}

// Scenario: two Claude accounts each have 60 % of their week left; one starts
// again tomorrow, the other in five days. A new conversation goes to the one
// that resets tomorrow, whose quota would otherwise be lost (omp's required
// drain); so does a request with no conversation.
func TestANewConversationGoesToTheAccountWhoseWeekEndsSoonest(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	ids := w.loginN(2)
	now := w.clock.Now()

	w.subs.NoteAnswer("claude", ids[0], claudeWindows(now, 0.2, 3*time.Hour, 0.4, 5*24*time.Hour))
	w.subs.NoteAnswer("claude", ids[1], claudeWindows(now, 0.2, 3*time.Hour, 0.4, 24*time.Hour))

	for _, conversation := range []string{"conversation-1", "conversation-2", "conversation-3", ""} {
		assert.Equal(t, ids[1], w.account(conversation), "conversation %q", conversation)
	}
}

// Scenario: a conversation stays on the account it began on while its cache
// may be warm, though another account comes to rank higher; after an hour
// without a request its cache is cold, and it goes where the ranking says.
func TestAWarmConversationKeepsItsAccount(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	ids := w.loginN(2)
	now := w.clock.Now()

	w.subs.NoteAnswer("claude", ids[0], claudeWindows(now, 0.1, 3*time.Hour, 0.2, 24*time.Hour))
	w.subs.NoteAnswer("claude", ids[1], claudeWindows(now, 0.1, 3*time.Hour, 0.2, 5*24*time.Hour))
	require.Equal(t, ids[0], w.account("conversation-1"))

	// The other account now resets sooner.
	now = w.clock.Now()
	w.subs.NoteAnswer("claude", ids[1], claudeWindows(now, 0.1, 3*time.Hour, 0.2, 2*time.Hour))

	// Twenty minutes, not thirty: a reading is believed for thirty minutes (quotaTrusted), and past it both
	// accounts would rank alike and the order would hang on their random ids.
	w.clock.advance(20 * time.Minute)
	assert.Equal(t, ids[0], w.account("conversation-1"), "twenty minutes later its cache is warm: it stays")
	assert.Equal(t, ids[1], w.account("conversation-2"), "a new conversation goes to the other")

	w.clock.advance(61 * time.Minute)
	now = w.clock.Now()
	w.subs.NoteAnswer("claude", ids[1], claudeWindows(now, 0.1, 3*time.Hour, 0.2, 2*time.Hour))
	assert.Equal(t, ids[1], w.account("conversation-1"), "an hour idle, it goes where the ranking says")
}

// Scenario: an account whose five hours are 88 % used goes after one at 30 %,
// though its week is the more urgent: a conversation is not begun where it
// would soon be stopped (omp's hot window).
func TestAnAccountWithItsFiveHoursNearlyUsedGoesAfter(t *testing.T) {
	t.Parallel()

	w := newClaudeWorld(t)
	ids := w.loginN(2)
	now := w.clock.Now()

	w.subs.NoteAnswer("claude", ids[0], claudeWindows(now, 0.88, 2*time.Hour, 0.3, 24*time.Hour))
	w.subs.NoteAnswer("claude", ids[1], claudeWindows(now, 0.30, 2*time.Hour, 0.3, 5*24*time.Hour))

	assert.Equal(t, ids[1], w.account("conversation-1"))
}

// The Codex backend's windows are read from its answer's headers.
func TestCodexWindowsAreReadFromAnAnswer(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set("X-Codex-Primary-Used-Percent", "42")
	h.Set("X-Codex-Primary-Window-Minutes", "300")
	h.Set("X-Codex-Primary-Reset-At", "1900000000")
	h.Set("X-Codex-Secondary-Used-Percent", "7.5")
	h.Set("X-Codex-Secondary-Window-Minutes", "10080")

	windows := (&subscription.Codex{}).LimitsInHeaders(h)
	require.Len(t, windows, 2)
	assert.Equal(t, subscription.Window{
		Name: subscription.WindowFiveHours, UsedPercent: 42, ResetsAt: time.Unix(1900000000, 0).UTC(),
	}, windows[0])
	assert.Equal(t, subscription.WindowWeek, windows[1].Name)
	assert.InDelta(t, 7.5, windows[1].UsedPercent, 0.001)
}
