package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the owner writes to pi; the message waits until pi has it, and
// is delivered once. pi's reply is a record only.
func TestOwnerMessagesWaitForPi(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "Chat", chat.Title)

	first, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "add a health page to demo")
	require.NoError(t, err)

	pending, err := w.s.ChatsWithUndelivered(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{chat.ID}, pending)

	require.NoError(t, w.s.MarkDelivered(ctx, chat.ID, first.Seq))

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatCoordinator, "On it.")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatSystem, "ran: hypomnema runs create")
	require.NoError(t, err)

	undelivered, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, undelivered, "replies and tool lines are never delivered")

	second, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "and a test")
	require.NoError(t, err)

	undelivered, err = w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, undelivered, 1)
	assert.Equal(t, second.Seq, undelivered[0].Seq)

	all, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 4)
	assert.NotNil(t, all[0].DeliveredAt)

	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "   ")
	require.ErrorIs(t, err, ErrInvalid)

	_, err = w.s.AddChatMessage(ctx, "cht_none", ChatOwner, "hello")
	require.ErrorIs(t, err, ErrNotFound)
}

// Scenario: pi created a Run from a chat. Events that need a coordinator
// wake the chat, each once; others do not.
func TestRunEventsWakeTheirChatOnce(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "health page")
	require.NoError(t, err)

	run, err := w.s.CreateRun(ctx, NewRun{Objective: "health page", CreatedBy: "pi", ChatID: chat.ID})
	require.NoError(t, err)
	assert.Equal(t, chat.ID, run.ChatID)

	_, err = w.s.CreateRun(ctx, NewRun{Objective: "x", CreatedBy: "pi", ChatID: "cht_none"})
	require.ErrorIs(t, err, ErrNotFound)

	tasks, err := w.s.CreateTasks(ctx, run.ID, []NewTask{{Ref: "a", Spec: "a"}})
	require.NoError(t, err)

	n, err := w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "creating a Task needs no coordinator")

	require.NoError(t, w.s.CancelTask(ctx, tasks[0].ID))

	n, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	n, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "an event wakes a chat once")

	err = w.s.FinishRun(ctx, run.ID)
	require.NoError(t, err)

	n, err = w.s.WakeChats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	wakes, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, wakes, 2)
	assert.Contains(t, wakes[0].Text, "Task "+tasks[0].ID+" of run "+run.ID+" is canceled")
	assert.Contains(t, wakes[0].Text, "hypomnema tasks show "+tasks[0].ID+" --json")
	assert.Contains(t, wakes[1].Text, "Run "+run.ID+" is finished")
	assert.Equal(t, ChatSystem, wakes[0].Role)
	assert.NotZero(t, wakes[0].EventSeq)
}

// Scenario: a chat started without a title, as from the CLI, takes its first
// owner message's first line; a chat with a title keeps it.
func TestAChatIsNamedByItsFirstMessage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	untitledChat, err := w.s.CreateChat(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "Chat", untitledChat.Title)

	_, err = w.s.AddChatMessage(ctx, untitledChat.ID, ChatOwner, "  add a health page\nwith a test")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, untitledChat.ID, ChatOwner, "and docs")
	require.NoError(t, err)

	got, err := w.s.GetChat(ctx, untitledChat.ID)
	require.NoError(t, err)
	assert.Equal(t, "add a health page", got.Title, "the first message names it, once")

	named, err := w.s.CreateChat(ctx, "release notes")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, named.ID, ChatOwner, "write them")
	require.NoError(t, err)

	got, err = w.s.GetChat(ctx, named.ID)
	require.NoError(t, err)
	assert.Equal(t, "release notes", got.Title)
}
