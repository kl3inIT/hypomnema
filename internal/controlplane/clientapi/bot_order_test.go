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

// Scenario: the order and the pins of the Bots are the owner's (issue #470). He moves Bots and pins one; every
// client reads the same order and pin with the Bots. The main Bot's pi, which changes Bots from its chat, may rename
// one but neither moves nor pins them; the main Bot is not named in an order, and an unknown id is refused.
func TestTheOwnerAloneOrdersAndPinsTheBots(t *testing.T) {
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

	pinned, err := owner.RenameBotWithResponse(ctx, maps.ID, openapi.BotChange{Pinned: new(true)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, pinned.StatusCode(), string(pinned.Body))
	assert.True(t, pinned.JSON200.Pinned)
	assert.Equal(t, "maps", pinned.JSON200.Name, "a pin keeps the name")

	listed, err := owner.ListBotsWithResponse(ctx)
	require.NoError(t, err)

	got := map[string]openapi.Bot{}
	for _, b := range listed.JSON200.Data {
		got[b.Id] = b
	}

	assert.Equal(t, 0, got[orchestration.DefaultBotID].Position)
	assert.Equal(t, 1, got[docs.ID].Position)
	assert.Equal(t, 2, got[maps.ID].Position)
	assert.True(t, got[maps.ID].Pinned)

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

	main, err := owner.RenameBotWithResponse(ctx, orchestration.DefaultBotID, openapi.BotChange{Pinned: new(true)})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, main.StatusCode(), "the main Bot is always first, not pinned")

	byPi, err := pi.ReorderBotsWithResponse(ctx, openapi.BotOrder{BotIds: []string{maps.ID, docs.ID}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, byPi.StatusCode(), "the order is the owner's")

	unpinned, err := pi.RenameBotWithResponse(ctx, maps.ID, openapi.BotChange{Pinned: new(false)})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, unpinned.StatusCode(), "so are the pins")
}
