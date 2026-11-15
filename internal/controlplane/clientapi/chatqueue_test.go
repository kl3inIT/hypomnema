package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario: the owner writes while pi works (issue #497). The message waits in the queue (202), off the
// transcript; the owner sends one now, which puts it in the chat, and removes another; once gone, both are 404s.
func TestAMessageWrittenWhilePiWorksIsQueued(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	created, err := owner.CreateChatWithResponse(ctx, openapi.NewChat{})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(), string(created.Body))
	chat := created.JSON201.Id

	w.pi.working.Store(chat)

	first, err := owner.SendChatMessageWithResponse(ctx, chat, openapi.NewChatMessage{Text: "and the totals"})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, first.StatusCode(), string(first.Body))
	assert.Equal(t, "and the totals", first.JSON202.Text)

	second, err := owner.SendChatMessageWithResponse(ctx, chat, openapi.NewChatMessage{Text: "never mind"})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, second.StatusCode())

	queue, err := owner.ListChatQueueWithResponse(ctx, chat)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, queue.StatusCode())
	require.Len(t, queue.JSON200.Items, 2)

	msgs, err := owner.ListChatMessagesWithResponse(ctx, chat, nil)
	require.NoError(t, err)
	assert.Empty(t, msgs.JSON200.Data, "a queued message is off the transcript")

	sent, err := owner.SendQueuedChatMessageWithResponse(ctx, chat, first.JSON202.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, sent.StatusCode(), string(sent.Body))
	assert.Equal(t, "and the totals", sent.JSON201.Text)

	removed, err := owner.RemoveQueuedChatMessageWithResponse(ctx, chat, second.JSON202.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, removed.StatusCode())
	assert.Equal(t, "never mind", removed.JSON200.Text)

	again, err := owner.RemoveQueuedChatMessageWithResponse(ctx, chat, first.JSON202.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, again.StatusCode())

	twice, err := owner.SendQueuedChatMessageWithResponse(ctx, chat, second.JSON202.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, twice.StatusCode())

	w.pi.working.Store("")

	direct, err := owner.SendChatMessageWithResponse(ctx, chat, openapi.NewChatMessage{Text: "now"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, direct.StatusCode(), "an idle pi takes it into the chat")
}
