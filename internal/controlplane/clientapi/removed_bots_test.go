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

// Scenario: issue #1139. The owner removes the Thư ký Bot, whose id stays in the Inbox's items and the gateway's
// usage. The Bots the owner reads come with the removed ones by name, so the web names it "Thư ký (removed)"; a
// project Bot's pi, shown only its own Bot, is shown no removed one. The usage by key names the removed Bot's keys
// by its name too, and a Bot whose name is not known as "A removed Bot", never by an id.
func TestARemovedBotIsNamedNeverShownByItsID(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	secretary, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "Thư ký"})
	require.NoError(t, err)
	require.NoError(t, w.svc.DeleteBot(ctx, secretary.ID))

	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "VMAP"})
	require.NoError(t, err)

	listed, err := owner.ListBotsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	assert.Equal(t, []openapi.RemovedBot{{Id: secretary.ID, Name: "Thư ký"}}, listed.JSON200.Removed)

	chat, err := w.svc.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)

	own, err := w.piToken("pi-coordinator", chat.ID, vmap.ID).ListBotsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, own.StatusCode(), string(own.Body))
	assert.Empty(t, own.JSON200.Removed, "a project Bot's pi reads no other Bot")

	groups := w.srv.nameKeyGroups(ctx, []openapi.GatewayUsageGroup{
		{Id: vmap.ID}, {Id: secretary.ID}, {Id: "bot_unknown"},
	})
	assert.Equal(t, []string{"Bot VMAP", "Bot Thư ký (removed)", "A removed Bot"},
		[]string{groups[0].Name, groups[1].Name, groups[2].Name})
}
