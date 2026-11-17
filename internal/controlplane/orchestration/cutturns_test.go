package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A turn's mark (issue #716): set when it starts, kept with its attempts when it is continued, and gone when it
// settles or is ended with a line. The continuing line goes to pi; the ending line does not.
func TestACutTurnIsMarkedContinuedAndEnded(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	ctx := t.Context()

	chat, err := s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.NoError(t, s.MarkChatTurn(ctx, chat.ID))

	clock.now = t0.Add(3 * time.Minute)

	cut, err := s.CutTurns(ctx)
	require.NoError(t, err)
	assert.Equal(t, []CutTurn{{ChatID: chat.ID, Age: 3 * time.Minute}}, cut)

	require.NoError(t, s.ContinueCutTurn(ctx, chat.ID))

	cut, err = s.CutTurns(ctx)
	require.NoError(t, err)
	assert.Equal(t, []CutTurn{{ChatID: chat.ID, Attempts: 1}}, cut, "the mark starts again, one attempt more")

	undelivered, err := s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, undelivered, 1)
	assert.Equal(t, ContinuedText, undelivered[0].Text)
	assert.Equal(t, ChatSystem, undelivered[0].Role)

	// The turn that continues it starts: the attempts stay until it settles.
	require.NoError(t, s.MarkChatTurn(ctx, chat.ID))

	cut, err = s.CutTurns(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, cut[0].Attempts)

	require.NoError(t, s.EndCutTurn(ctx, chat.ID))

	cut, err = s.CutTurns(ctx)
	require.NoError(t, err)
	assert.Empty(t, cut)

	msgs, err := s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, CutText, msgs[1].Text)

	undelivered, err = s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Len(t, undelivered, 1, "the line that ends the turn is not for pi")

	require.NoError(t, s.MarkChatTurn(ctx, chat.ID))

	cut, err = s.CutTurns(ctx)
	require.NoError(t, err)
	assert.Equal(t, []CutTurn{{ChatID: chat.ID}}, cut, "a turn after an ended one starts with no attempts")

	require.NoError(t, s.SettleChatTurn(ctx, chat.ID))

	cut, err = s.CutTurns(ctx)
	require.NoError(t, err)
	assert.Empty(t, cut)
}

// The restart-loop guard's chain (Hermes's gateway/restart_loop_guard.py): starts chain while each gap is within
// the bound; a wider gap ends the chain and the starts before it are forgotten.
func TestRestartBootsChainWhileTheGapsAreShort(t *testing.T) {
	t.Parallel()

	s, db, clock := newTestService(t)
	ctx := t.Context()
	gap := 5 * time.Minute

	for i, step := range []struct {
		after time.Duration
		want  int
	}{
		{0, 1},
		{4 * time.Minute, 2},
		{5 * time.Minute, 3},
		{6 * time.Minute, 1},
		{time.Minute, 2},
	} {
		clock.now = clock.now.Add(step.after)

		n, err := s.RecordRestartBoot(ctx, gap)
		require.NoError(t, err)
		assert.Equal(t, step.want, n, "start %d", i)
	}

	var kept int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM restart_boots").Scan(&kept))
	assert.Equal(t, 2, kept, "only the chain is kept")
}

func TestARestartChainKeepsTheNewestStarts(t *testing.T) {
	t.Parallel()

	s, db, clock := newTestService(t)
	ctx := t.Context()

	for range maxRestartBoots + 5 {
		clock.now = clock.now.Add(time.Second)

		_, err := s.RecordRestartBoot(ctx, time.Minute)
		require.NoError(t, err)
	}

	var kept int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM restart_boots").Scan(&kept))
	assert.Equal(t, maxRestartBoots, kept)
}

func TestABootAfterNowIsInTheChain(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []int64{50, 200}, chainEndingAt([]int64{200, 50, -10}, 100, 50),
		"a start after now (the clock went back) is adjacent; one 60 before the next is not")
	assert.Equal(t, []int64{200}, chainEndingAt([]int64{200, 10}, 100, 50))
}
