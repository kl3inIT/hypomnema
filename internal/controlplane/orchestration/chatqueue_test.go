package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A message written while pi works waits in the queue, off the transcript; one written once pi is idle but while
// others still wait goes behind them; taking the queue moves them into the chat in order, for pi (issue #497).
func TestQueuedMessagesWaitOffTheChatAndKeepTheirOrder(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	files := []ChatAttachment{{Name: "a.png", Path: AttachmentsDir(chat.ID) + "/a.png", Size: 3, Type: "image/png"}}

	_, first, err := w.s.SendOwnerMessage(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "one", Attachments: files},
		true)
	require.NoError(t, err)
	require.NotNil(t, first)

	// pi is idle now, but "one" still waits: "two" goes behind it.
	kept, second, err := w.s.SendOwnerMessage(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "two"}, false)
	require.NoError(t, err)
	assert.Nil(t, kept)
	require.NotNil(t, second)

	msgs, err := w.s.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "a queued message is off the transcript")

	waiting, err := w.s.ChatsWithQueue(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{chat.ID}, waiting)

	n, err := w.s.TakeChatQueue(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	undelivered, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, undelivered, 2)
	assert.Equal(t, "one", undelivered[0].Text)
	assert.Equal(t, files, undelivered[0].Attachments)
	assert.Equal(t, "two", undelivered[1].Text)

	queue, err := w.s.ChatQueue(ctx, chat.ID)
	require.NoError(t, err)
	assert.Empty(t, queue)

	// With nothing queued and pi idle, a message goes straight into the chat.
	direct, queued, err := w.s.SendOwnerMessage(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "three"}, false)
	require.NoError(t, err)
	assert.Nil(t, queued)
	require.NotNil(t, direct)
	assert.Equal(t, "three", direct.Text)
}

// A queued message can be removed or sent now until it went to pi; then both are 404s. Sending it now moves only
// it, ahead of the others.
func TestAQueuedMessageIsRemovedOrSentNowOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	a, err := w.s.QueueChatMessage(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "a"})
	require.NoError(t, err)
	b, err := w.s.QueueChatMessage(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "b"})
	require.NoError(t, err)

	removed, err := w.s.RemoveQueued(ctx, chat.ID, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "a", removed.Text)

	_, err = w.s.RemoveQueued(ctx, chat.ID, a.ID)
	require.ErrorIs(t, err, ErrNotFound)

	sent, err := w.s.SendQueuedNow(ctx, chat.ID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, ChatOwner, sent.Role)
	assert.Equal(t, "b", sent.Text)
	assert.Nil(t, sent.DeliveredAt)

	_, err = w.s.SendQueuedNow(ctx, chat.ID, b.ID)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = w.s.QueueChatMessage(ctx, chat.ID, NewChatMessage{Role: ChatCoordinator, Text: "c"})
	require.ErrorIs(t, err, ErrInvalid)
}

// Only the messages named are marked delivered: a steer goes ahead of the wake-ups that wait for the turn.
func TestMarkDeliveredSeqsMarksOnlyThose(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	first, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "first")
	require.NoError(t, err)
	second, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "second")
	require.NoError(t, err)

	require.NoError(t, w.s.MarkDeliveredSeqs(ctx, chat.ID, []int64{second.Seq}))

	undelivered, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, undelivered, 1)
	assert.Equal(t, first.Seq, undelivered[0].Seq)
}

// A deleted chat takes its queue with it.
func TestDeletingAChatDropsItsQueue(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = w.s.QueueChatMessage(ctx, chat.ID, NewChatMessage{Role: ChatOwner, Text: "later"})
	require.NoError(t, err)

	_, err = w.s.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	waiting, err := w.s.ChatsWithQueue(ctx)
	require.NoError(t, err)
	assert.Empty(t, waiting)
}
