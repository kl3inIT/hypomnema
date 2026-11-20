package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: the order and the departments of the Bots are the owner's (issues #470 and #471). He moves Bots, makes
// a department, renames, reorders and removes departments and puts a Bot in one; every client reads the same order
// and department with the Bots. The main Bot's pi, which changes Bots from its chat, may rename one but neither
// moves a Bot nor touches a department; the main Bot is in no department and not named in an order, and an unknown
// id is refused.
func TestTheOwnerAloneOrdersTheBotsAndTheirDepartments(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	maps, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "maps"})
	require.NoError(t, err)

	docs, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "docs"})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", chat.ID, orchestration.DefaultBotID)

	moved, err := owner.ReorderBotsWithResponse(ctx, openapi.BotOrder{BotIds: []string{docs.ID, maps.ID}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, moved.StatusCode(), string(moved.Body))
	assert.Equal(t, []string{orchestration.DefaultBotID, docs.ID, maps.ID}, moved.JSON200.BotIds)

	dep, err := owner.CreateDepartmentWithResponse(ctx, openapi.NewDepartment{Name: "VMAP"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, dep.StatusCode(), string(dep.Body))

	put, err := owner.RenameBotWithResponse(ctx, maps.ID, openapi.BotChange{Department: &dep.JSON201.Id})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, put.StatusCode(), string(put.Body))
	assert.Equal(t, dep.JSON201.Id, *put.JSON200.Department)
	assert.Equal(t, "maps", put.JSON200.Name, "a move keeps the name")

	other, err := owner.CreateDepartmentWithResponse(ctx, openapi.NewDepartment{Name: "Docs", BotIds: &[]string{docs.ID}})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, other.StatusCode(), string(other.Body))

	twice, err := owner.CreateDepartmentWithResponse(ctx, openapi.NewDepartment{Name: "Docs"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, twice.StatusCode(), "a name is one department's")

	renamed, err := owner.RenameDepartmentWithResponse(ctx, other.JSON201.Id, openapi.DepartmentChange{Name: "Writing"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, renamed.StatusCode(), string(renamed.Body))
	assert.Equal(t, "Writing", renamed.JSON200.Name)

	ordered, err := owner.ReorderDepartmentsWithResponse(ctx,
		openapi.DepartmentOrder{DepartmentIds: []string{other.JSON201.Id, dep.JSON201.Id}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ordered.StatusCode(), string(ordered.Body))

	deps, err := owner.ListDepartmentsWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, []openapi.Department{
		{Id: other.JSON201.Id, Name: "Writing", Position: 0}, {Id: dep.JSON201.Id, Name: "VMAP", Position: 1},
	}, deps.JSON200.Data)

	gone, err := owner.DeleteDepartmentWithResponse(ctx, other.JSON201.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, gone.StatusCode())

	listed, err := owner.ListBotsWithResponse(ctx)
	require.NoError(t, err)

	got := map[string]openapi.Bot{}
	for _, b := range listed.JSON200.Data {
		got[b.Id] = b
	}

	assert.Equal(t, 0, got[orchestration.DefaultBotID].Position)
	assert.Equal(t, 1, got[docs.ID].Position)
	assert.Equal(t, 2, got[maps.ID].Position)
	assert.Equal(t, dep.JSON201.Id, *got[maps.ID].Department)
	assert.Nil(t, got[docs.ID].Department, "a removed department's Bots go back to none")

	for name, c := range map[string]struct {
		order []string
		want  int
	}{
		"the main bot":  {[]string{orchestration.DefaultBotID, maps.ID}, http.StatusBadRequest},
		"an unknown id": {[]string{maps.ID, "bot_nobodyaaaaaaaaaaaaaaaaaaaa"}, http.StatusBadRequest},
		"no id":         {[]string{}, http.StatusBadRequest},
	} {
		refused, err := owner.ReorderBotsWithResponse(ctx, openapi.BotOrder{BotIds: c.order})
		require.NoError(t, err)
		assert.Equal(t, c.want, refused.StatusCode(), name)
	}

	main, err := owner.RenameBotWithResponse(ctx, orchestration.DefaultBotID,
		openapi.BotChange{Department: &dep.JSON201.Id})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, main.StatusCode(), "the main Bot is always first, in no department")

	byPi, err := pi.ReorderBotsWithResponse(ctx, openapi.BotOrder{BotIds: []string{maps.ID, docs.ID}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, byPi.StatusCode(), "the order is the owner's")

	out, err := pi.RenameBotWithResponse(ctx, maps.ID, openapi.BotChange{Department: new("")})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, out.StatusCode(), "so are the departments")

	made, err := pi.CreateDepartmentWithResponse(ctx, openapi.NewDepartment{Name: "Mine"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, made.StatusCode())
}
