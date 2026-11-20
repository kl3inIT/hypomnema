package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func TestAdoptionWakesTheChatOnceWithoutReplayingItsRunsHistory(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	parent, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	chat, _, err := w.s.OpenMainChat(ctx, "", "", "", ChatModel{})
	require.NoError(t, err)

	oldTask := w.tasks(NewTask{Ref: "old", Spec: "previous work"})[0]
	require.NoError(t, w.s.CancelTask(ctx, oldTask.ID))
	other, err := w.s.CreateRun(ctx, NewRun{Objective: "another Run", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)
	require.NoError(t, w.s.FinishRun(ctx, other.ID))

	for range 2 {
		adopted, err := w.s.AdoptRun(ctx, w.run.ID, "", parent.Name)
		require.NoError(t, err)
		assert.Equal(t, chat.ID, adopted.ChatID)
		assert.Equal(t, parent.ID, adopted.CoordinatorTokenID)
		assert.Equal(t, w.run.CreatedBy, adopted.CreatedBy)
	}

	restarted := New(w.db, w.clock, platform.NewID)
	wakes, err := restarted.WakeChats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, wakes, "the other Run's unprocessed event is not skipped")
	pending, err := restarted.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	assert.Contains(t, pending[0].Text, "The owner assigned run "+w.run.ID)
	assert.Zero(t, pending[0].EventSeq)
	assert.Contains(t, pending[1].Text, other.ID)
	require.NoError(t, restarted.MarkDelivered(ctx, chat.ID, pending[1].Seq))

	beforeCursor, err := restarted.CreateRun(ctx, NewRun{Objective: "older work", CreatedBy: "owner"})
	require.NoError(t, err)
	advance, err := restarted.CreateRun(ctx, NewRun{Objective: "advance cursor", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)
	require.NoError(t, restarted.FinishRun(ctx, advance.ID))
	_, err = restarted.WakeChats(ctx)
	require.NoError(t, err)
	pending, err = restarted.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, restarted.MarkDelivered(ctx, chat.ID, pending[0].Seq))
	_, err = restarted.AdoptRun(ctx, beforeCursor.ID, chat.ID, parent.Name)
	require.NoError(t, err)
	require.NoError(t, restarted.FinishRun(ctx, w.run.ID))
	wakes, err = restarted.WakeChats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, wakes)
	pending, err = restarted.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	assert.Contains(t, pending[0].Text, "The owner assigned run "+beforeCursor.ID)
	assert.Contains(t, pending[1].Text, "Run "+w.run.ID+" is finished")
}

func TestAdoptionDoesNotTakeAWorkingCoordinatorsRunOrARoutinesRun(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	parent, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	incumbent, _, err := w.s.AddToken(ctx, "laptop", bearertoken.Coordinator)
	require.NoError(t, err)
	legacy, err := w.s.CreateRun(ctx, NewRun{
		Objective: "legacy", CreatedBy: incumbent.Name, CoordinatorTokenID: incumbent.ID,
	})
	require.NoError(t, err)
	routine, err := w.s.CreateRun(ctx, NewRun{
		Objective: "script work", CreatedBy: parent.Name, CoordinatorTokenID: parent.ID, Bot: DefaultBotID,
	})
	require.NoError(t, err)

	for _, run := range []Run{legacy, routine} {
		_, err := w.s.AdoptRun(ctx, run.ID, "", parent.Name)
		require.ErrorIs(t, err, ErrConflict)
		unchanged, err := w.s.GetRun(ctx, run.ID)
		require.NoError(t, err)
		assert.Empty(t, unchanged.ChatID)
		assert.Equal(t, run.CoordinatorTokenID, unchanged.CoordinatorTokenID)
	}
	require.NoError(t, w.s.RevokeToken(ctx, incumbent.ID))
	adopted, err := w.s.AdoptRun(ctx, legacy.ID, "", parent.Name)
	require.NoError(t, err)
	assert.Equal(t, parent.ID, adopted.CoordinatorTokenID)
	assert.NotEmpty(t, adopted.ChatID)
	other, err := w.s.CreateChat(ctx, "", "another chat")
	require.NoError(t, err)
	_, err = w.s.AdoptRun(ctx, adopted.ID, other.ID, parent.Name)
	require.ErrorIs(t, err, ErrConflict)
}

func TestAssigningARunDoesNotGrantTheDestinationAnUnallowedConnection(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := t.Context()
	parent, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	_, err = w.s.SetConnection(ctx, "github", "work", "lab", "vault:work")
	require.NoError(t, err)
	request := NewRun{Objective: "use work", CreatedBy: "owner", Uses: []string{"github:work"}}
	old, err := w.s.CreateRun(ctx, request)
	require.NoError(t, err)
	_, err = w.s.CreateChatRunOnce(ctx, Once{}, request, parent.Name)
	require.ErrorContains(t, err, "allows it for chats")
	_, err = w.s.AdoptRun(ctx, old.ID, "", parent.Name)
	require.ErrorContains(t, err, "allows it for chats")
	unchanged, err := w.s.GetRun(ctx, old.ID)
	require.NoError(t, err)
	assert.Empty(t, unchanged.ChatID)
	assert.Equal(t, old.Uses, unchanged.Uses)
	main, err := w.s.GetBot(ctx, DefaultBotID)
	require.NoError(t, err)
	assert.Empty(t, main.MainChat, "the rejected requests do not create a destination chat")
}
