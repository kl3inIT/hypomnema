package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1336): a Bot's settings are versioned as a profile's are. Each save that changes them keeps a
// version with who saved it, the owner or the main Bot from its chat; a save that changes nothing keeps none. A
// restore brings back a version's instructions and model as a new version restored from it, while the Bot's name and
// memory stay. A Bot made before versions keeps what it had as its first version at its next save.
func TestABotsSaveKeepsAVersionAndARestoreBringsItBack(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.Profiles().ApplyProvider(ctx, "",
		"name: gateway\nkind: model_gateway\nmodels: [primary, second]\n")
	require.NoError(t, err)

	maps, err := w.s.PutBot(ctx, Bot{
		Name: "maps", AgentSpec: AgentSpec{Provider: "gateway", Model: "primary", Instructions: "You answer about maps."},
	})
	require.NoError(t, err)

	w.clock.now = w.clock.now.Add(time.Minute)

	changed := maps
	changed.Model, changed.Instructions = "second", "You answer about roads."
	_, err = w.s.PutBot(ctx, changed)
	require.NoError(t, err)

	_, err = w.s.PutBot(ctx, changed)
	require.NoError(t, err)

	_, err = w.s.ChangeBotMemory(ctx, maps.ID, MemoryOp{Kind: MemoryAdd, Text: "The maps team meets on Monday."})
	require.NoError(t, err)

	_, err = w.s.RenameBot(ctx, maps.ID, "atlas")
	require.NoError(t, err)

	versions, err := w.s.ListBotVersions(ctx, maps.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2, "a save that changes nothing, the memory and the name keep no version")
	assert.Equal(t, 2, versions[0].Version, "newest first")
	assert.Equal(t, "second", versions[0].Bot.Model)
	assert.Equal(t, "You answer about roads.", versions[0].Bot.Instructions)
	assert.Equal(t, BotVersionSave, versions[0].Source)
	assert.Equal(t, SavedByOwner, versions[0].SavedBy)
	assert.Empty(t, versions[0].ChatID)
	assert.Equal(t, w.clock.now, versions[0].CreatedAt)
	assert.Equal(t, "primary", versions[1].Bot.Model)
	assert.Equal(t, "You answer about maps.", versions[1].Bot.Instructions)

	restored, err := w.s.RestoreBotVersion(ctx, maps.ID, versions[1].ID)
	require.NoError(t, err)
	assert.Equal(t, "primary", restored.Model)
	assert.Equal(t, "You answer about maps.", restored.Instructions)
	assert.Equal(t, "atlas", restored.Name, "the name stays")
	assert.Equal(t, "The maps team meets on Monday.", restored.Memory, "the memory stays")

	versions, err = w.s.ListBotVersions(ctx, maps.ID)
	require.NoError(t, err)
	require.Len(t, versions, 3)
	assert.Equal(t, BotVersionRestore, versions[0].Source)
	assert.Equal(t, versions[2].ID, versions[0].RestoredFrom)

	_, err = w.s.RestoreBotVersion(ctx, DefaultBotID, versions[2].ID)
	require.ErrorIs(t, err, ErrNotFound, "a version is restored only to its own Bot")

	// The main Bot saves from its chat: the version says so, with the chat.
	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	fromChat := restored
	fromChat.Instructions = "You answer about maps and roads."
	_, err = w.s.PutBotFromChat(ctx, chat.ID, fromChat)
	require.NoError(t, err)

	versions, err = w.s.ListBotVersions(ctx, maps.ID)
	require.NoError(t, err)
	assert.Equal(t, SavedByMainBot, versions[0].SavedBy)
	assert.Equal(t, chat.ID, versions[0].ChatID)

	// A Bot made before versions, as the migration leaves every Bot, has none until its next save.
	_, err = w.db.ExecContext(ctx, `DELETE FROM bot_versions WHERE bot_id = ?`, DefaultBotID)
	require.NoError(t, err)

	main, err := w.s.GetBot(ctx, DefaultBotID)
	require.NoError(t, err)

	versions, err = w.s.ListBotVersions(ctx, DefaultBotID)
	require.NoError(t, err)
	assert.Empty(t, versions)

	had := main.Instructions
	main.Instructions = "You coordinate."
	_, err = w.s.PutBot(ctx, main)
	require.NoError(t, err)

	versions, err = w.s.ListBotVersions(ctx, DefaultBotID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, BotVersionBefore, versions[1].Source)
	assert.Empty(t, versions[1].SavedBy)
	assert.Equal(t, had, versions[1].Bot.Instructions, "what it had before")
	assert.Equal(t, "You coordinate.", versions[0].Bot.Instructions)

	_, err = w.s.ListBotVersions(ctx, "bot_nobodyaaaaaaaaaaaaaaaaaaaa")
	require.ErrorIs(t, err, ErrNotFound)
}

// A new Bot version keeps the instructions under their name (issue #1336, review of PR #1344); only the setup
// changes' undo hash keeps the key they had while they were the Bot's persona.
func TestABotVersionKeepsTheInstructionsUnderTheirName(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.Profiles().ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [primary]\n")
	require.NoError(t, err)

	maps, err := w.s.PutBot(ctx, Bot{
		Name: "maps", AgentSpec: AgentSpec{Provider: "gateway", Model: "primary", Instructions: "You answer about maps."},
	})
	require.NoError(t, err)

	rows, err := w.s.read().ListBotVersions(ctx, maps.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].Settings, `"instructions":"You answer about maps."`)
	assert.NotContains(t, rows[0].Settings, `"persona"`)

	versions, err := w.s.ListBotVersions(ctx, maps.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, "You answer about maps.", versions[0].Bot.Instructions)
}
