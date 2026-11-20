package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (review of PR #1000): a Bot and a chat stored before issue #1002 with a model and no provider. Changing
// only the Bot's persona, or only the chat's effort, is accepted, and the model gets the one provider that offers it,
// as the code before stored it.
func TestAnEditLeavingAStoredModelAloneIsAccepted(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\n"+
		"base_url: https://router.example/v1\napi_key_ref: ROUTER_KEY\nmodels: [sonnet, haiku]\n")
	require.NoError(t, err)

	bot, err := w.s.PutBot(ctx, Bot{Name: "vmap", Provider: "router", Model: "sonnet"})
	require.NoError(t, err)
	chat, err := w.s.CreateChatWithID(ctx, "", bot.ID, "", ChatModel{Provider: "router", Model: "haiku"})
	require.NoError(t, err)

	_, err = w.db.ExecContext(ctx, `UPDATE bots SET provider = NULL WHERE id = ?`, bot.ID)
	require.NoError(t, err)
	_, err = w.db.ExecContext(ctx, `UPDATE chats SET provider = NULL WHERE id = ?`, chat.ID)
	require.NoError(t, err)

	bot.Provider, bot.Persona = "", "You answer about VMAP."
	changed, err := w.s.PutBot(ctx, bot)
	require.NoError(t, err)
	assert.Equal(t, [3]string{"router", "sonnet", "You answer about VMAP."},
		[3]string{changed.Provider, changed.Model, changed.Persona})

	low := "low"
	chat, err = w.s.SetChatModel(ctx, chat.ID, ChatModelChange{Effort: &low})
	require.NoError(t, err)
	assert.Equal(t, ChatModel{Provider: "router", Model: "haiku", Effort: "low"}, chat.Choice)
}
