package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario (issue #1336): a Bot's instructions are named as a profile's. The API returns instructions, and persona
// beside them for one release; a client that still sends persona is read. A Bot's memory comes in the shape a
// profile's does, under its own path.
func TestABotsInstructionsAndMemoryHaveTheProfilesNames(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	old := "You answer about maps."
	made, err := owner.CreateBotWithResponse(ctx, openapi.NewBot{
		Name: "maps", Settings: &openapi.BotSettings{Persona: &old},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	assert.Equal(t, old, made.JSON201.Instructions, "persona, the old name, is read")
	assert.Equal(t, old, val(made.JSON201.Persona), "and given for one release")

	id := made.JSON201.Id
	now := "You answer about roads."
	put, err := owner.PutBotWithResponse(ctx, id, openapi.BotSettings{Instructions: &now, Persona: &old})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, put.StatusCode(), string(put.Body))
	assert.Equal(t, now, put.JSON200.Instructions, "instructions win over persona")

	set, err := owner.SetBotMemoryWithResponse(ctx, id, openapi.MemoryText{Memory: "The team meets on Monday."})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode(), string(set.Body))
	assert.Equal(t, "The team meets on Monday.", set.JSON200.Memory)
	assert.Equal(t, id, val(set.JSON200.Bot))

	read, err := owner.GetBotMemoryWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, read.StatusCode(), string(read.Body))
	assert.Equal(t, "The team meets on Monday.", read.JSON200.Memory)
	assert.Positive(t, read.JSON200.MemoryLimit)

	entry := "Roads close in winter."
	changed, err := owner.ChangeBotMemoryWithResponse(ctx, id, openapi.MemoryChange{Kind: openapi.Add, Text: &entry})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, changed.StatusCode(), string(changed.Body))
	assert.Equal(t, "The team meets on Monday.\nRoads close in winter.", changed.JSON200.Memory)

	profile, err := owner.SetProfileMemoryWithResponse(ctx, "reviewer", openapi.MemoryText{Memory: "Run make verify."})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, profile.StatusCode(), string(profile.Body))
	assert.Equal(t, "reviewer", val(profile.JSON200.Profile))
	assert.Equal(t, "reviewer", val(profile.JSON200.Agent), "agent, the old name, for one release")

	got, err := owner.GetProfileMemoryWithResponse(ctx, "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "Run make verify.", got.JSON200.Memory)

	// A memory's notes are named by profile, or by agent for one release.
	text := "A note."
	path := "/memories/x.md"
	note, err := owner.PutMemoryNoteWithResponse(ctx,
		&openapi.PutMemoryNoteParams{Profile: new("reviewer"), Path: path}, openapi.MemoryNoteText{Text: text})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, note.StatusCode(), string(note.Body))

	for _, params := range []openapi.GetMemoryNoteParams{
		{Profile: new("reviewer"), Path: path}, {Agent: new("reviewer"), Path: path},
	} {
		got, err := owner.GetMemoryNoteWithResponse(ctx, &params)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, got.StatusCode(), string(got.Body))
		assert.Equal(t, text, got.JSON200.Text)
	}

	both, err := owner.GetMemoryNoteWithResponse(ctx,
		&openapi.GetMemoryNoteParams{Profile: new("reviewer"), Agent: new("explorer"), Path: path})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, both.StatusCode(), "two profiles named")
}

// Scenario (issue #1336): each save of a Bot keeps a version the owner lists, and a restore brings back its
// instructions and model. Only the owner, or the main Bot from its chat, restores one.
func TestTheOwnerRestoresABotsVersion(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	laptop, _ := w.token("laptop", bearertoken.Coordinator)
	ctx := t.Context()

	first := "You answer about maps."
	made, err := owner.CreateBotWithResponse(ctx, openapi.NewBot{
		Name: "maps", Settings: &openapi.BotSettings{Instructions: &first, Effort: new("low")},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))

	id := made.JSON201.Id
	second := "You answer about roads."
	_, err = owner.PutBotWithResponse(ctx, id, openapi.BotSettings{Instructions: &second, Effort: new("high")})
	require.NoError(t, err)

	versions, err := laptop.ListBotVersionsWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, versions.StatusCode(), string(versions.Body))
	require.Len(t, versions.JSON200.Data, 2)

	newest, oldest := versions.JSON200.Data[0], versions.JSON200.Data[1]
	assert.Equal(t, 2, newest.Version)
	assert.Equal(t, second, val(newest.Settings.Instructions))
	assert.Equal(t, openapi.BotVersionSave, newest.Source)
	assert.Equal(t, openapi.SavedByOwner, val(newest.SavedBy))
	assert.Equal(t, first, val(oldest.Settings.Instructions))
	assert.Equal(t, "low", val(oldest.Settings.Effort))

	refused, err := laptop.RestoreBotVersionWithResponse(ctx, id, oldest.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "a token from no chat of the main Bot")

	restored, err := owner.RestoreBotVersionWithResponse(ctx, id, oldest.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, restored.StatusCode(), string(restored.Body))
	assert.Equal(t, first, restored.JSON200.Instructions)
	assert.Equal(t, "low", val(restored.JSON200.Effort))

	versions, err = owner.ListBotVersionsWithResponse(ctx, id)
	require.NoError(t, err)
	require.Len(t, versions.JSON200.Data, 3)
	assert.Equal(t, openapi.BotVersionRestore, versions.JSON200.Data[0].Source)
	assert.Equal(t, oldest.Id, val(versions.JSON200.Data[0].RestoredFrom))

	missing, err := owner.RestoreBotVersionWithResponse(ctx, id, "bov_nobody")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode())
}
