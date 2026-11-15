package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the owner orders his Bots and pins some (issue #470). A new Bot goes last; a reorder names only the
// Bots it moves, which take the places they held together, as OpenMausBot's does; the others keep theirs. The main
// Bot stays first and is neither moved nor pinned. A removed Bot leaves no gap, and a change of a Bot's settings
// keeps its place and pin.
func TestTheOwnerOrdersAndPinsHisBots(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	ids := []string{DefaultBotID}

	for _, name := range []string{"a", "b", "c", "d"} {
		b, err := w.s.PutBot(ctx, Bot{Name: name})
		require.NoError(t, err)
		assert.Equal(t, len(ids), b.Position, "a new Bot goes last")

		ids = append(ids, b.ID)
	}

	main, a, b, c, d := ids[0], ids[1], ids[2], ids[3], ids[4]

	order, err := w.s.ReorderBots(ctx, []string{d, b})
	require.NoError(t, err)
	assert.Equal(t, []string{main, a, d, c, b}, order, "d and b swap the places they held; a and c stay")

	order, err = w.s.ReorderBots(ctx, []string{c, a})
	require.NoError(t, err)
	assert.Equal(t, []string{main, c, d, a, b}, order)

	for name, moved := range map[string][]string{
		"no bot":     {},
		"the main":   {a, main},
		"twice":      {a, a},
		"an unknown": {a, "bot_nobodyaaaaaaaaaaaaaaaaaaaa"},
	} {
		_, err := w.s.ReorderBots(ctx, moved)
		require.ErrorIs(t, err, ErrInvalid, name)
	}

	assert.Equal(t, []string{main, c, d, a, b}, botOrder(t, w), "a refused reorder changes nothing")

	pinned, err := w.s.PinBot(ctx, d, true)
	require.NoError(t, err)
	assert.True(t, pinned.Pinned)

	_, err = w.s.PinBot(ctx, DefaultBotID, true)
	require.ErrorIs(t, err, ErrInvalid, "the main Bot is always first, not pinned")

	_, err = w.s.PinBot(ctx, "bot_nobodyaaaaaaaaaaaaaaaaaaaa", true)
	require.ErrorIs(t, err, ErrNotFound)

	changed, err := w.s.PutBot(ctx, Bot{ID: d, Persona: "You keep the maps."})
	require.NoError(t, err)
	assert.True(t, changed.Pinned, "a change of its settings keeps its pin")
	assert.Equal(t, 2, changed.Position, "and its place")

	require.NoError(t, w.s.DeleteBot(ctx, c))
	assert.Equal(t, []string{main, d, a, b}, botOrder(t, w), "the Bots after a removed one move up")

	e, err := w.s.PutBot(ctx, Bot{Name: "e"})
	require.NoError(t, err)
	assert.Equal(t, 4, e.Position, "a new Bot takes the place after the last")

	unpinned, err := w.s.PinBot(ctx, d, false)
	require.NoError(t, err)
	assert.False(t, unpinned.Pinned)
}

// botOrder is every Bot's id by its position, checked to be 0 to n-1.
func botOrder(t *testing.T, w *world) []string {
	t.Helper()

	bots, err := w.s.ListBots(t.Context())
	require.NoError(t, err)

	out := make([]string, len(bots))
	for _, b := range bots {
		require.Less(t, b.Position, len(bots), b.Name)
		require.Empty(t, out[b.Position], "two Bots at %d", b.Position)
		out[b.Position] = b.ID
	}

	return out
}
