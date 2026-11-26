package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario (issue #1188): an item's project is a department the owner made in the sidebar. He puts an item in one
// with a priority and lists the project's items; a project that is no department is refused; the Inbox has no list
// of projects of its own.
func TestTheOwnerPutsAnItemInADepartment(t *testing.T) {
	t.Parallel()

	w := newInboxWorld(t)
	ctx := t.Context()

	made, err := w.owner.CreateDepartmentWithResponse(ctx, openapi.NewDepartment{Name: "VMAP"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	vmap := *made.JSON201

	gone := "dep_gone"
	refused, err := w.owner.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{
		Title: "Send the report", ProjectId: &gone,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, refused.StatusCode(), string(refused.Body))

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
}
