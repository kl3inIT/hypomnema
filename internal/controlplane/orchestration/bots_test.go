package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the owner makes a second Bot beside the one that is always
// there, gives it a model and a persona, and opens a chat with it. Changing
// the Bot keeps its memory. A Bot with a chat or a memory is not removed, and
// the first Bot never is; a chat cannot name a Bot that does not exist.
func TestTheOwnerMakesASecondBot(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	bots, err := w.s.ListBots(ctx)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	assert.Equal(t, DefaultBot, bots[0].Name)
	assert.Contains(t, bots[0].Persona, "coordinator", "the first Bot says it gives work to workers")

	_, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\n"+
		"base_url: https://router.example/v1\napi_key_ref: ROUTER_KEY\nmodels: [sonnet]\n")
	require.NoError(t, err)

	for bot, want := range map[Bot]string{
		{Name: "Bad Name"}:                             "lowercase",
		{Name: "x", Model: "opus"}:                     "no provider",
		{Name: "x", Model: "sonnet", Effort: "huge"}:   "unknown effort",
		{Name: "x", Persona: strings.Repeat("a", 2e4)}: "at most",
	} {
		_, err := w.s.PutBot(ctx, bot)
		require.ErrorContains(t, err, want, bot.Name)
	}

	vmap, err := w.s.PutBot(ctx, Bot{Name: "vmap", Model: "sonnet", Persona: "  You answer about VMAP.\n"})
	require.NoError(t, err)
	assert.Equal(t, "router", vmap.Provider, "only router offers sonnet to pi")
	assert.Equal(t, "You answer about VMAP.", vmap.Persona)

	_, err = w.s.CreateChat(ctx, "nobody", "")
	require.ErrorIs(t, err, ErrNotFound)

	untouched, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, DefaultBot, untouched.Bot)

	_, err = w.s.ChangeBotMemory(ctx, "vmap", MemoryOp{Kind: MemoryAdd, Text: "Linh leads the maps team."})
	require.NoError(t, err)

	vmap, err = w.s.PutBot(ctx, Bot{Name: "vmap", Persona: "You answer about VMAP and Tasco."})
	require.NoError(t, err)
	assert.Empty(t, vmap.Model, "the model is what was put")
	assert.Equal(t, "Linh leads the maps team.", vmap.Memory, "a change of the Bot keeps its memory")

	require.ErrorIs(t, w.s.DeleteBot(ctx, "vmap"), ErrConflict, "it has a memory")

	_, err = w.s.ChangeBotMemory(ctx, "vmap", MemoryOp{Kind: MemoryRemove, Match: "Linh"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)
	assert.Equal(t, "vmap", chat.Bot)
	require.ErrorIs(t, w.s.DeleteBot(ctx, "vmap"), ErrConflict, "it has a chat")

	_, err = w.s.PutBot(ctx, Bot{Name: "spare"})
	require.NoError(t, err)
	require.NoError(t, w.s.DeleteBot(ctx, "spare"))
	require.ErrorIs(t, w.s.DeleteBot(ctx, "spare"), ErrNotFound)
	require.ErrorIs(t, w.s.DeleteBot(ctx, DefaultBot), ErrConflict, "the first Bot is always there")
}

// Scenario: a Bot keeps what it learned across its chats. It adds entries,
// one line each; the same entry twice is kept once. It replaces or removes
// the one entry that has a text, and is told when the text is in none or in
// several. A change that would pass the limit is refused and changes
// nothing, so that the Bot makes room itself.
func TestABotKeepsAMemory(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	add := func(text string) (Bot, error) {
		return w.s.ChangeBotMemory(ctx, DefaultBot, MemoryOp{Kind: MemoryAdd, Text: text})
	}

	_, err := add("The owner writes in Vietnamese.")
	require.NoError(t, err)

	_, err = add("Deploys wait for\n  the owner's yes.")
	require.NoError(t, err)

	b, err := add("The owner writes in Vietnamese.")
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese.\nDeploys wait for the owner's yes.", b.Memory)

	for op, want := range map[MemoryOp]string{
		{Kind: MemoryAdd}:                                  "needs text",
		{Kind: "forget", Match: "owner"}:                   "unknown change",
		{Kind: MemoryRemove}:                               "say part of the entry",
		{Kind: MemoryRemove, Match: "owner"}:               "more than one entry",
		{Kind: MemoryRemove, Match: "Lark"}:                "no entry has",
		{Kind: MemoryReplace, Match: "Deploys"}:            "what replaces",
		{Kind: MemoryAdd, Text: strings.Repeat("a", 2200)}: "over its limit",
	} {
		_, err := w.s.ChangeBotMemory(ctx, DefaultBot, op)
		require.ErrorIs(t, err, ErrInvalid, want)
		require.ErrorContains(t, err, want)
	}

	b, err = w.s.GetBot(ctx, DefaultBot)
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese.\nDeploys wait for the owner's yes.", b.Memory,
		"a refused change changed nothing")

	b, err = w.s.ChangeBotMemory(ctx, DefaultBot, MemoryOp{Kind: MemoryReplace, Match: "Deploys", Text: "x\ny"})
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese.\nx y", b.Memory)

	b, err = w.s.ChangeBotMemory(ctx, DefaultBot, MemoryOp{Kind: MemoryRemove, Match: "Vietnamese"})
	require.NoError(t, err)
	assert.Equal(t, "x y", b.Memory)

	_, err = w.s.ChangeBotMemory(ctx, "nobody", MemoryOp{Kind: MemoryAdd, Text: "x"})
	require.ErrorIs(t, err, ErrNotFound)
}
