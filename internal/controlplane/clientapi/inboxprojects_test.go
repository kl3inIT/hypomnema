package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario (issue #1157): the owner keeps the Inbox's projects and a Bot reads them; he puts an item in one with a
// priority and lists the project's items; a Bot's token cannot add, rename or remove a project.
func TestTheOwnerKeepsTheProjectsAndPutsAnItemInOne(t *testing.T) {
	t.Parallel()

	w := newInboxWorld(t)
	ctx := t.Context()

	made, err := w.owner.CreateInboxProjectWithResponse(ctx, openapi.NewInboxProject{Name: "VMAP", Color: "#2F80ED"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	vmap := *made.JSON201
	assert.Equal(t, "#2f80ed", vmap.Color)

	again, err := w.owner.CreateInboxProjectWithResponse(ctx, openapi.NewInboxProject{Name: "vmap", Color: "#000000"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, again.StatusCode(), string(again.Body))

	byBot, err := w.chatBot.CreateInboxProjectWithResponse(ctx, openapi.NewInboxProject{Name: "X", Color: "#000000"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, byBot.StatusCode())

	name := "VMAP AI"
	renamed, err := w.owner.EditInboxProjectWithResponse(ctx, vmap.Id, openapi.InboxProjectEdit{Name: &name})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, renamed.StatusCode(), string(renamed.Body))
	assert.Equal(t, name, renamed.JSON200.Name)

	listed, err := w.chatBot.ListInboxProjectsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 1)
	assert.Equal(t, vmap.Id, listed.JSON200.Data[0].Id)

	high, why := openapi.TodoPriority(2), "Laura waits for it"
	item, err := w.owner.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{
		Title: "Send the report", Priority: &high, PriorityReason: &why, ProjectId: &vmap.Id,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, item.StatusCode(), string(item.Body))
	assert.Equal(t, high, item.JSON201.Priority)
	assert.Equal(t, why, item.JSON201.PriorityReason)
	assert.Equal(t, vmap.Id, *item.JSON201.ProjectId)
	w.add(ctx, t, "Another", openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindMe})

	only, err := w.owner.ListTodosWithResponse(ctx, &openapi.ListTodosParams{Project: &vmap.Id})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, only.StatusCode(), string(only.Body))
	require.Len(t, only.JSON200.Data, 1)
	assert.Equal(t, item.JSON201.Id, only.JSON200.Data[0].Id)

	low, none := openapi.TodoPriority(4), ""
	edited, err := w.owner.EditTodoWithResponse(ctx, item.JSON201.Id, openapi.TodoEdit{
		Revision: item.JSON201.Revision, Priority: &low, ProjectId: &none,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, edited.StatusCode(), string(edited.Body))
	assert.Equal(t, low, edited.JSON200.Priority)
	assert.Empty(t, edited.JSON200.PriorityReason)
	assert.Nil(t, edited.JSON200.ProjectId)

	removed, err := w.chatBot.DeleteInboxProjectWithResponse(ctx, vmap.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, removed.StatusCode())

	removed, err = w.owner.DeleteInboxProjectWithResponse(ctx, vmap.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, removed.StatusCode(), string(removed.Body))
}
