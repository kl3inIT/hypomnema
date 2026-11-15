package orchestration

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
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
	assert.Equal(t, DefaultBotID, bots[0].ID)
	assert.Equal(t, "coordinator", bots[0].Name, "the main Bot keeps the name it had")
	assert.True(t, bots[0].Main())
	assert.Contains(t, bots[0].Persona, "coordinator", "the main Bot says it gives work to workers")

	_, err = w.s.ApplyProvider(ctx, "", "name: router\nkind: openai_compatible\n"+
		"base_url: https://router.example/v1\napi_key_ref: ROUTER_KEY\nmodels: [sonnet]\n")
	require.NoError(t, err)

	for _, c := range []struct {
		bot  Bot
		want string
	}{
		{Bot{Name: "  "}, "1 to 64 characters"},
		{Bot{Name: strings.Repeat("b", 65)}, "1 to 64 characters"},
		{Bot{Name: "coordinator"}, "another bot is named"},
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
	assert.Equal(t, DefaultBotID, untouched.Bot)

	_, err = w.s.ChangeBotMemory(ctx, vmap.ID, MemoryOp{Kind: MemoryAdd, Text: "Linh leads the maps team."})
	require.NoError(t, err)

	vmap, err = w.s.PutBot(ctx, Bot{ID: vmap.ID, Name: "vmap", Persona: "You answer about VMAP and Tasco."})
	require.NoError(t, err)
	assert.Empty(t, vmap.Model, "the model is what was put")
	assert.Equal(t, "Linh leads the maps team.", vmap.Memory, "a change of the Bot keeps its memory")

	require.ErrorIs(t, w.s.DeleteBot(ctx, vmap.ID), ErrConflict, "it has a memory")

	_, err = w.s.ChangeBotMemory(ctx, vmap.ID, MemoryOp{Kind: MemoryRemove, Match: "Linh"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, vmap.ID, "")
	require.NoError(t, err)
	assert.Equal(t, vmap.ID, chat.Bot)
	require.ErrorIs(t, w.s.DeleteBot(ctx, vmap.ID), ErrConflict, "it has a chat")

	spareBot, err := w.s.PutBot(ctx, Bot{Name: "spare"})
	require.NoError(t, err)
	require.NoError(t, w.s.DeleteBot(ctx, spareBot.ID))
	require.ErrorIs(t, w.s.DeleteBot(ctx, spareBot.ID), ErrNotFound)
	require.ErrorIs(t, w.s.DeleteBot(ctx, DefaultBotID), ErrConflict, "the main Bot is always there")

	_, err = w.s.PutBot(ctx, Bot{ID: "bot_nobodyaaaaaaaaaaaaaaaaaaaa", Persona: "x"})
	require.ErrorIs(t, err, ErrNotFound, "a change names a Bot that is there")
}

// Scenario: any Bot is renamed, the main one too (issue #466). Its id, chats, Routines, Channels and memory stay,
// and so does what the main Bot alone may do. A name is trimmed, may hold any letters, and is one Bot's only.
func TestABotIsRenamedAndKeepsWhatNamesIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	maps, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, maps.ID, "")
	require.NoError(t, err)

	_, err = w.s.ChangeBotMemory(ctx, maps.ID, MemoryOp{Kind: MemoryAdd, Text: "Linh leads the maps team."})
	require.NoError(t, err)

	renamed, err := w.s.RenameBot(ctx, maps.ID, "  Bản đồ VMAP ")
	require.NoError(t, err)
	assert.Equal(t, maps.ID, renamed.ID)
	assert.Equal(t, "Bản đồ VMAP", renamed.Name, "trimmed, any letters")
	assert.Equal(t, "Linh leads the maps team.", renamed.Memory)

	read, err := w.s.GetChat(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, maps.ID, read.Bot, "its chat names it by its id")

	main, err := w.s.RenameBot(ctx, DefaultBotID, "Trợ lý")
	require.NoError(t, err)
	assert.Equal(t, "Trợ lý", main.Name)
	assert.True(t, main.Main(), "the main Bot is the main one whatever it is called")

	fresh, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, DefaultBotID, fresh.Bot, "a chat that names no Bot is the main one's")

	for name, c := range map[string]struct {
		id, name string
		want     error
	}{
		"another bot's name": {maps.ID, "Trợ lý", ErrConflict},
		"no name":            {maps.ID, " ", ErrInvalid},
		"too long a name":    {maps.ID, strings.Repeat("ả", 65), ErrInvalid},
		"no such bot":        {"bot_nobodyaaaaaaaaaaaaaaaaaaaa", "x", ErrNotFound},
	} {
		_, err := w.s.RenameBot(ctx, c.id, c.name)
		require.ErrorIs(t, err, c.want, name)
	}

	_, err = w.s.PutBot(ctx, Bot{Name: "Trợ lý"})
	require.ErrorIs(t, err, ErrConflict, "a new Bot takes no name another has")

	coordinator, err := w.s.PutBot(ctx, Bot{Name: "coordinator"})
	require.NoError(t, err, "a name given up is free")
	assert.False(t, coordinator.Main())
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
		return w.s.ChangeBotMemory(ctx, DefaultBotID, MemoryOp{Kind: MemoryAdd, Text: text})
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
		_, err := w.s.ChangeBotMemory(ctx, DefaultBotID, op)
		require.ErrorIs(t, err, ErrInvalid, want)
		require.ErrorContains(t, err, want)
	}

	b, err = w.s.GetBot(ctx, DefaultBotID)
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese.\nDeploys wait for the owner's yes.", b.Memory,
		"a refused change changed nothing")

	b, err = w.s.ChangeBotMemory(ctx, DefaultBotID, MemoryOp{Kind: MemoryReplace, Match: "Deploys", Text: "x\ny"})
	require.NoError(t, err)
	assert.Equal(t, "The owner writes in Vietnamese.\nx y", b.Memory)

	b, err = w.s.ChangeBotMemory(ctx, DefaultBotID, MemoryOp{Kind: MemoryRemove, Match: "Vietnamese"})
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

	vmapBot, err := w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, vmapBot.ID, "")
	require.NoError(t, err)

	fromChat, err := w.s.CreateRun(ctx, NewRun{Objective: "add the depot", CreatedBy: vmapBot.ID, ChatID: chat.ID})
	require.NoError(t, err)
	assert.Equal(t, vmapBot.ID, fromChat.Bot)

	byOwner, err := w.s.CreateRun(ctx, NewRun{Objective: "rotate the keys", CreatedBy: "owner"})
	require.NoError(t, err)
	assert.Empty(t, byOwner.Bot)

	read, err := w.s.GetRun(ctx, fromChat.ID)
	require.NoError(t, err)
	assert.Equal(t, vmapBot.ID, read.Bot)

	page, err := w.s.ListRuns(ctx, RunFilter{}, nil, 0)
	require.NoError(t, err)

	bots := map[string]string{}
	for _, r := range page.Runs {
		bots[r.ID] = r.Bot
	}

	assert.Equal(t, vmapBot.ID, bots[fromChat.ID])
	assert.Contains(t, bots, byOwner.ID)
	assert.Empty(t, bots[byOwner.ID])
}

// Scenario: a Bot has a memory and a chat with messages and an artifact; a Run was started from that chat and the
// chat has a gateway key. The plain removal is refused; the owner's forced one removes the Bot, its chat and the messages,
// keeps the Run without its chat and keeps the key, revoked. A Bot a Routine or a Lark Channel uses, and the first
// Bot, are refused even when forced.
func TestTheOwnerRemovesABotWithItsChats(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	// The gateway's own revocation, as the control plane wires it (issue #405).
	w.s.chatKeys = modelgateway.RevokeDeletedChatKeysTx
	ctx := t.Context()

	trialBot, err := w.s.PutBot(ctx, Bot{Name: "trial"})
	require.NoError(t, err)
	_, err = w.s.ChangeBotMemory(ctx, trialBot.ID, MemoryOp{Kind: MemoryAdd, Text: "A test Bot."})
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, trialBot.ID, "")
	require.NoError(t, err)
	_, err = w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "Which model are you?")
	require.NoError(t, err)
	// Its artifact goes with the chat (issue #484): its row would otherwise hold the chat by its foreign key.
	_, _, err = w.s.ShowArtifact(ctx, ArtifactMaker{Bot: trialBot.ID, Chat: chat.ID},
		ShowArtifact{Title: "Usage", Pack: page(t, "<p>usage")})
	require.NoError(t, err)

	_, err = w.db.ExecContext(ctx, "UPDATE runs SET chat_id = ? WHERE id = ?", chat.ID, w.run.ID)
	require.NoError(t, err)
	_, err = w.db.ExecContext(ctx, "INSERT INTO gateway_keys (id, name, key_hash, models, created_at, chat_id) "+
		"VALUES ('gk_1', 'chat key', 'hash', '[\"*\"]', 1, ?)", chat.ID)
	require.NoError(t, err)

	require.ErrorIs(t, w.s.DeleteBot(ctx, trialBot.ID), ErrConflict, "it has a chat and a memory")
	deleted, err := w.s.ForceDeleteBot(ctx, trialBot.ID)
	require.NoError(t, err)
	require.Len(t, deleted, 1, "the deleted chats come back, for their files (issue #503)")
	assert.Equal(t, chat.ID, deleted[0].ID)
	assert.Equal(t, trialBot.ID, deleted[0].Bot)

	_, err = w.s.GetBot(ctx, trialBot.ID)
	require.ErrorIs(t, err, ErrNotFound)

	var chats, messages int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM chats WHERE id = ?", chat.ID).Scan(&chats))
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM chat_messages WHERE chat_id = ?",
		chat.ID).Scan(&messages))
	assert.Zero(t, chats)
	assert.Zero(t, messages)

	var artifacts int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM artifacts").Scan(&artifacts))
	assert.Zero(t, artifacts)

	var runChat sql.NullString
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT chat_id FROM runs WHERE id = ?", w.run.ID).Scan(&runChat))
	assert.False(t, runChat.Valid, "the Run stays without its chat")

	var keyChat sql.NullString
	var revoked sql.NullInt64
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT chat_id, revoked_at FROM gateway_keys WHERE id = 'gk_1'").
		Scan(&keyChat, &revoked))
	assert.False(t, keyChat.Valid)
	assert.True(t, revoked.Valid, "the chat's key is revoked and kept")

	_, err = w.s.ForceDeleteBot(ctx, trialBot.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.ForceDeleteBot(ctx, DefaultBotID)
	require.ErrorIs(t, err, ErrConflict)

	scheduledBot, err := w.s.PutBot(ctx, Bot{Name: "scheduled"})
	require.NoError(t, err)
	spec := daily("morning")
	spec.Bot = scheduledBot.ID
	w.routine(spec)
	_, err = w.s.ForceDeleteBot(ctx, scheduledBot.ID)
	require.ErrorIs(t, err, ErrConflict, "a Routine uses it")
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

	_, err = w.s.PutBot(ctx, Bot{ID: reporter.ID, Name: "reporter", ToolDisplay: "loud"})
	require.ErrorIs(t, err, ErrInvalid)

	chat, err := w.s.CreateChat(ctx, reporter.ID, "")
	require.NoError(t, err)

	coordinatorChat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = w.s.PutBotFromChat(ctx, coordinatorChat.ID,
		Bot{ID: reporter.ID, Name: "reporter", Persona: "You send the report.", ToolDisplay: ToolsOff})
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
