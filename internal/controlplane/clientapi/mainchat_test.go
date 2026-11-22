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
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
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

// Scenario: the web's page is open on a Bot with no main chat, with an id chosen for its first message, when the CLI
// writes to the Bot first and makes its main chat (issue #471). The web's first message then finds that main chat
// (200, its own id) and makes no second chat: both messages are in one chat. Sent at the same moment, whichever
// comes first, they end in one chat too.
func TestTheWebAndTheCLIFirstMessagesEndInOneChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	ctx := t.Context()

	cli, err := owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{})
	require.NoError(t, err)
	require.NotNil(t, cli.JSON201, "%s", cli.Body)

	draft := "cht_" + strings.Repeat("c", 26)
	web, err := owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{
		Id: &draft, Title: ptr("from the web"), Effort: ptr("high"),
	})
	require.NoError(t, err)
	require.NotNil(t, web.JSON200, "the main chat the CLI made: %s", web.Body)
	assert.Equal(t, cli.JSON201.Id, web.JSON200.Id)
	assert.Nil(t, web.JSON200.Effort, "the main chat keeps its own model and effort")

	listed, err := owner.ListChatsWithResponse(ctx, &openapi.ListChatsParams{Bot: ptr(orchestration.DefaultBotID)})
	require.NoError(t, err)
	require.NotNil(t, listed.JSON200)
	assert.Len(t, listed.JSON200.Data, 1, "no second chat")

	for i := range 5 {
		w := newWorld(t)
		owner, _ := w.token("owner", bearertoken.Owner)
		id := "cht_" + strings.Repeat(string(rune('d'+i)), 26)

		var (
			wg             sync.WaitGroup
			web, cli       *openapi.OpenMainChatResponse
			webErr, cliErr error
		)

		wg.Go(func() { web, webErr = owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{Id: &id}) })
		wg.Go(func() { cli, cliErr = owner.OpenMainChatWithResponse(ctx, openapi.MainChatRequest{}) })
		wg.Wait()

		require.NoError(t, webErr)
		require.NoError(t, cliErr)
		assert.Equal(t, openedChat(t, web), openedChat(t, cli), "one chat")
		assert.Equal(t, openedChat(t, web), mainChat(t, owner, orchestration.DefaultBotID))
	}
}

func openedChat(t *testing.T, r *openapi.OpenMainChatResponse) string {
	t.Helper()

	switch {
	case r.JSON200 != nil:
		return r.JSON200.Id
	case r.JSON201 != nil:
		return r.JSON201.Id
	}

	require.Failf(t, "no chat", "%d %s", r.StatusCode(), r.Body)

	return ""
}

func mainChat(t *testing.T, c *openapi.ClientWithResponses, bot string) string {
	t.Helper()

	r, err := c.GetBotWithResponse(t.Context(), bot)
	require.NoError(t, err)
	require.NotNil(t, r.JSON200, "%s", r.Body)

	return val(r.JSON200.MainChat)
}

// Scenario: the owner makes an older chat of a Bot its main chat through the API (issue #923). The Bot names it at
// once, and the former main chat stays; a chat of another Bot and a Routine's chat are refused (400) and say why;
// the main Bot's pi, which changes Bots from its chat, may not choose it (403).
func TestTheOwnerAloneSetsABotsMainChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	older, err := w.svc.CreateChatWithID(ctx, "", "", "an older topic", orchestration.ChatModel{})
	require.NoError(t, err)

	home, _, err := w.svc.OpenMainChat(ctx, "", "", "", orchestration.ChatModel{})
	require.NoError(t, err)

	side, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "side"})
	require.NoError(t, err)

	other, err := w.svc.CreateChatWithID(ctx, "", side.ID, "", orchestration.ChatModel{})
	require.NoError(t, err)

	r, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "digest", Kind: routinestore.RoutinePrompt, Prompt: "sum up",
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	started, err := w.svc.RunRoutineNow(ctx, r.ID)
	require.NoError(t, err)
	require.NotEmpty(t, started.Run.ChatID, "a prompt run has its own chat")

	run := orchestration.Chat{ID: started.Run.ChatID}

	set, err := owner.RenameBotWithResponse(ctx, orchestration.DefaultBotID, openapi.BotChange{MainChat: &older.ID})
	require.NoError(t, err)
	require.NotNil(t, set.JSON200, "%s", set.Body)
	assert.Equal(t, older.ID, val(set.JSON200.MainChat))

	kept, err := owner.GetChatWithResponse(ctx, home.ID)
	require.NoError(t, err)
	require.NotNil(t, kept.JSON200, "the former main chat stays: %s", kept.Body)

	read, err := owner.GetChatWithResponse(ctx, run.ID)
	require.NoError(t, err)
	require.NotNil(t, read.JSON200, "%s", read.Body)
	assert.True(t, read.JSON200.Routine, "a Routine's chat says so")

	for name, refused := range map[string]string{"another Bot's chat": other.ID, "a Routine's chat": run.ID} {
		r, err := owner.RenameBotWithResponse(ctx, orchestration.DefaultBotID, openapi.BotChange{MainChat: &refused})
		require.NoError(t, err)
		errorCode(t, r.Body, r.StatusCode(), http.StatusBadRequest)
		assert.Equal(t, older.ID, mainChat(t, owner, orchestration.DefaultBotID), name)
	}

	pi := w.piToken("pi-coordinator", home.ID, orchestration.DefaultBotID)
	byPi, err := pi.RenameBotWithResponse(ctx, orchestration.DefaultBotID, openapi.BotChange{MainChat: &home.ID})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, byPi.StatusCode(), "the main chat is the owner's to choose")
	assert.Equal(t, older.ID, mainChat(t, owner, orchestration.DefaultBotID))
}
