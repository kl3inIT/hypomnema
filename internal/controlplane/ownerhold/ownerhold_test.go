package ownerhold

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// held returns a Hold the owner took with one view open at start.
func held(t *testing.T, start time.Time) *Hold {
	t.Helper()

	h := New()
	require.True(t, h.Opened(4))
	h.Take(t.Context(), start)

	return h
}

// The owner's last view going, however it went, keeps the hold for the
// grace: the page closing it, as switching to another tab in a pane does, is
// no hand back (issue #603). The grace then ends it.
func TestTheHoldOutlivesTheLastViewForTheGrace(t *testing.T) {
	t.Parallel()

	start := time.Unix(1_700_000_000, 0)
	h := held(t, start)

	h.Closed(start)

	on, _ := h.State()
	assert.True(t, on, "a view closed is not a hand back")
	assert.Empty(t, h.Expire(start.Add(Grace-time.Second)))
	assert.Equal(t, ReleasedGone, h.Expire(start.Add(Grace)))

	on, _ = h.State()
	assert.False(t, on)
}

// A view opened within the grace carries the hold on, and the grace starts
// again when that one goes.
func TestAViewOpenedWithinTheGraceCarriesTheHoldOn(t *testing.T) {
	t.Parallel()

	start := time.Unix(1_700_000_000, 0)
	h := held(t, start)

	h.Closed(start)
	require.True(t, h.Opened(4))
	assert.Empty(t, h.Expire(start.Add(Grace)), "a view is open again")

	h.Closed(start.Add(Grace))
	assert.Empty(t, h.Expire(start.Add(2*Grace-time.Second)))
	assert.Equal(t, ReleasedGone, h.Expire(start.Add(2*Grace)))
}

// Handing back is the one way the hold ends at once.
func TestHandingBackEndsTheHoldAtOnce(t *testing.T) {
	t.Parallel()

	h := held(t, time.Unix(1_700_000_000, 0))

	assert.True(t, h.Release())

	on, _ := h.State()
	assert.False(t, on)
	assert.False(t, h.Release(), "nothing is held any more")
}

// With a view open the hold ends after the idle time from the owner's last
// input, and input keeps it.
func TestTheHoldEndsAfterTheIdleTime(t *testing.T) {
	t.Parallel()

	start := time.Unix(1_700_000_000, 0)
	h := held(t, start)

	assert.True(t, h.Input(start.Add(Idle-time.Second)))
	assert.Empty(t, h.Expire(start.Add(2*Idle-2*time.Second)), "input keeps the hold")
	assert.Equal(t, ReleasedIdle, h.Expire(start.Add(2*Idle-time.Second)))
}
