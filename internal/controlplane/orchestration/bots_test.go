package orchestration

import (
	"database/sql"
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

	for _, c := range []struct {
		bot  Bot
		want string
	}{
		{Bot{Name: "Bad Name"}, "lowercase"},
		{Bot{Name: "x", Model: "opus"}, "no provider"},
		{Bot{Name: "x", Model: "sonnet", Effort: "huge"}, "unknown effort"},
		{Bot{Name: "x", Persona: strings.Repeat("a", 2e4)}, "at most"},
	} {
		_, err := w.s.PutBot(ctx, c.bot)
		require.ErrorContains(t, err, c.want, c.bot.Name)
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

// Scenario: a Run started from a Bot's chat names that Bot when it is read
// and when Runs are listed; a Run the owner started without a chat names
// none.
func TestARunNamesTheBotOfItsChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	fromChat, err := w.s.CreateRun(ctx, NewRun{Objective: "add the depot", CreatedBy: "vmap", ChatID: chat.ID})
	require.NoError(t, err)
	assert.Equal(t, "vmap", fromChat.Bot)

	byOwner, err := w.s.CreateRun(ctx, NewRun{Objective: "rotate the keys", CreatedBy: "owner"})
	require.NoError(t, err)
	assert.Empty(t, byOwner.Bot)

	read, err := w.s.GetRun(ctx, fromChat.ID)
	require.NoError(t, err)
	assert.Equal(t, "vmap", read.Bot)

	page, err := w.s.ListRuns(ctx, RunFilter{}, nil, 0)
	require.NoError(t, err)

	bots := map[string]string{}
	for _, r := range page.Runs {
		bots[r.ID] = r.Bot
	}

	assert.Equal(t, "vmap", bots[fromChat.ID])
	assert.Contains(t, bots, byOwner.ID)
	assert.Empty(t, bots[byOwner.ID])
}

// Scenario: a Bot has a memory and a chat with messages; a Run was started from that chat and the chat has a
// gateway key. The plain removal is refused; the owner's forced one removes the Bot, its chat and the messages,
// keeps the Run without its chat and keeps the key, revoked. A Bot a Routine or a Lark Channel uses, and the first
// Bot, are refused even when forced.
func TestTheOwnerRemovesABotWithItsChats(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.PutBot(ctx, Bot{Name: "trial"})
	require.NoError(t, err)
	_, err = w.s.ChangeBotMemory(ctx, "trial", MemoryOp{Kind: MemoryAdd, Text: "A test Bot."})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "trial", "")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "Which model are you?")
	require.NoError(t, err)

	_, err = w.db.ExecContext(ctx, "UPDATE runs SET chat_id = ? WHERE id = ?", chat.ID, w.run.ID)
	require.NoError(t, err)
	_, err = w.db.ExecContext(ctx, "INSERT INTO gateway_keys (id, name, key_hash, models, created_at, chat_id) "+
		"VALUES ('gk_1', 'chat key', 'hash', '[\"*\"]', 1, ?)", chat.ID)
	require.NoError(t, err)

	require.ErrorIs(t, w.s.DeleteBot(ctx, "trial"), ErrConflict, "it has a chat and a memory")
	require.NoError(t, w.s.ForceDeleteBot(ctx, "trial"))

	_, err = w.s.GetBot(ctx, "trial")
	require.ErrorIs(t, err, ErrNotFound)

	var chats, messages int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM chats WHERE id = ?", chat.ID).Scan(&chats))
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM chat_messages WHERE chat_id = ?",
		chat.ID).Scan(&messages))
	assert.Zero(t, chats)
	assert.Zero(t, messages)

	var runChat sql.NullString
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT chat_id FROM runs WHERE id = ?", w.run.ID).Scan(&runChat))
	assert.False(t, runChat.Valid, "the Run stays without its chat")

	var keyChat sql.NullString
	var revoked sql.NullInt64
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT chat_id, revoked_at FROM gateway_keys WHERE id = 'gk_1'").
		Scan(&keyChat, &revoked))
	assert.False(t, keyChat.Valid)
	assert.True(t, revoked.Valid, "the chat's key is revoked and kept")

	require.ErrorIs(t, w.s.ForceDeleteBot(ctx, "trial"), ErrNotFound)
	require.ErrorIs(t, w.s.ForceDeleteBot(ctx, DefaultBot), ErrConflict)

	_, err = w.s.PutBot(ctx, Bot{Name: "scheduled"})
	require.NoError(t, err)
	spec := daily("morning")
	spec.Bot = "scheduled"
	w.routine(spec)
	require.ErrorIs(t, w.s.ForceDeleteBot(ctx, "scheduled"), ErrConflict, "a Routine uses it")
}

// Scenario: what a Bot's chats show of its tool calls (issue #441) is stored with it, on unless set, read for each
// of its chats, and put back by an undo of the Bot coordinator's change; a level that is not one is refused.
func TestABotKeepsWhatItsChatsShowOfItsTools(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	reporter, err := w.s.PutBot(ctx, Bot{Name: "reporter", Persona: "You send the report."})
	require.NoError(t, err)
	assert.Equal(t, ToolsOn, reporter.ToolDisplay)

	_, err = w.s.PutBot(ctx, Bot{Name: "reporter", ToolDisplay: "loud"})
	require.ErrorIs(t, err, ErrInvalid)

	chat, err := w.s.CreateChat(ctx, "reporter", "")
	require.NoError(t, err)

	coordinatorChat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = w.s.PutBotFromChat(ctx, coordinatorChat.ID,
		Bot{Name: "reporter", Persona: "You send the report.", ToolDisplay: ToolsOff})
	require.NoError(t, err)

	shown, err := w.s.ChatToolDisplay(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, ToolsOff, shown, "read for the Bot's chat")

	changes, err := w.s.ListSetupChanges(ctx, coordinatorChat.ID)
	require.NoError(t, err)
	require.Len(t, changes, 1, "a change of the setting alone is a change")

	_, err = w.s.UndoSetupChange(ctx, changes[0].ID)
	require.NoError(t, err)

	shown, err = w.s.ChatToolDisplay(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, ToolsOn, shown, "the undo puts it back")

	_, err = w.s.ChatToolDisplay(ctx, "cht_nobody")
	require.ErrorIs(t, err, ErrNotFound)
}
