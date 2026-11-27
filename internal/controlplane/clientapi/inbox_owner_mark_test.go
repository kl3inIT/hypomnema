package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario: review of PR #1237, issue #1229 rule 1. The main Bot's pi adds an item for another Bot through the client
// API, in its chat where the owner's message is the last: the item is handed off from his line as the agent API's
// inbox_add hands it, and the chat it wakes is his turn.
func TestReviewOwnerMarkFromTheClientAPI(t *testing.T) {
	t.Parallel()

	w := newInboxWorld(t)
	ctx := t.Context()

	resp, err := w.chatBot.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{},
		openapi.NewTodo{Title: "Đọc nhóm Lark của VMAP", Assignee: &w.vmap})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))

	td, err := w.svc.Inbox().Get(ctx, resp.JSON201.Id)
	require.NoError(t, err)
	require.NotEmpty(t, td.Chat)

	mine, err := w.svc.OwnerTurn(ctx, td.Chat)
	require.NoError(t, err)
	assert.True(t, mine)
}
