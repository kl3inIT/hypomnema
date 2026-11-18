package clientapi

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: a Bot's main chat through the API (issue #471). The first call for a Bot with none makes it (201) and
// the next finds it (200); the web's New chat, under the id its page chose, moves it, and the Bot says so; a first
// message that is refused keeps the chat made, still the main one, and the message sent again goes to it. Only the
// owner opens a main chat.
func TestABotsMainChatThroughTheAPI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)
	ctx := t.Context()

	made, err := owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{})
	require.NoError(t, err)
	require.NotNil(t, made.JSON201, "%s", made.Body)
	assert.Equal(t, orchestration.DefaultBotID, made.JSON201.Bot)

	found, err := owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{Bot: ptr(orchestration.DefaultBotID)})
	require.NoError(t, err)
	require.NotNil(t, found.JSON200, "%s", found.Body)
	assert.Equal(t, made.JSON201.Id, found.JSON200.Id)

	id := "cht_" + strings.Repeat("a", 26)
	fresh, err := owner.CreateChatWithResponse(ctx, openapi.NewChat{Id: &id, Title: ptr("a new topic")})
	require.NoError(t, err)
	require.NotNil(t, fresh.JSON201, "%s", fresh.Body)
	assert.Equal(t, id, mainChat(t, owner, orchestration.DefaultBotID), "New chat moves the main chat")

	refused, err := owner.SendChatMessageWithResponse(ctx, id, openapi.NewChatMessage{
		Text: strings.Repeat("x", orchestration.MaxChatText+1),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, refused.StatusCode())
	assert.Equal(t, id, mainChat(t, owner, orchestration.DefaultBotID), "a refused first message keeps the chat")

	sent, err := owner.SendChatMessageWithResponse(ctx, id, openapi.NewChatMessage{Text: "a new topic"})
	require.NoError(t, err)
	require.NotNil(t, sent.JSON201, "%s", sent.Body)

	listed, err := owner.ListChatsWithResponse(ctx, &openapi.ListChatsParams{Bot: ptr(orchestration.DefaultBotID)})
	require.NoError(t, err)
	require.NotNil(t, listed.JSON200)
	assert.Len(t, listed.JSON200.Data, 2, "the former main chat stays")

	forbidden, err := pi.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, forbidden.StatusCode())

	unknown, err := owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{Bot: ptr("bot_none")})
	require.NoError(t, err)
	errorCode(t, unknown.Body, unknown.StatusCode(), http.StatusNotFound)
}

// Scenario: the web's New chat and the CLI write to a Bot with no main chat at the same moment (issue #471).
// Whichever comes first, the Bot ends with one main chat, the web's: the CLI's call either made a chat that the web's
// then replaced, or found the web's.
func TestNewChatAndTheCLIAtOnceEndInOneMainChat(t *testing.T) {
	t.Parallel()

	for range 5 {
		w := newWorld(t)
		owner, _ := w.token("owner", bearertoken.Owner)
		ctx := t.Context()

		id := "cht_" + strings.Repeat("b", 26)

		var (
			wg  sync.WaitGroup
			cli string
		)

		wg.Go(func() {
			r, err := owner.CreateChatWithResponse(ctx, openapi.NewChat{Id: &id})
			assert.NoError(t, err)
			assert.NotNil(t, r.JSON201)
		})
		wg.Go(func() {
			r, err := owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{})
			assert.NoError(t, err)

			if r.JSON200 != nil {
				cli = r.JSON200.Id
			} else if assert.NotNil(t, r.JSON201) {
				cli = r.JSON201.Id
			}
		})
		wg.Wait()

		assert.Equal(t, id, mainChat(t, owner, orchestration.DefaultBotID))
		assert.NotEmpty(t, cli)
	}
}

func mainChat(t *testing.T, c *openapi.ClientWithResponses, bot string) string {
	t.Helper()

	r, err := c.GetBotWithResponse(t.Context(), bot)
	require.NoError(t, err)
	require.NotNil(t, r.JSON200, "%s", r.Body)

	return val(r.JSON200.MainChat)
}
