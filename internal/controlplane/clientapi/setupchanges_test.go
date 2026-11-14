package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// Scenario: who manages Bots and profiles (issue #169). The main Bot's pi
// makes a Bot and a profile from its chat, and changes and renames another
// Bot (issue #466); each change is listed there for the owner, who undoes it.
// Another Bot's pi, a Routine's script and the coordinator token outside every
// box are refused, and the main Bot does not remove itself.
func TestOnlyTheMainBotManagesBotsAndProfiles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	shared, _ := w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	vmapBot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)

	mine, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	theirs, err := w.svc.CreateChat(ctx, vmapBot.ID, "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", mine.ID, orchestration.DefaultBotID)
	vmap := w.piToken("pi-coordinator", theirs.ID, vmapBot.ID)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	script, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", routine.ID)
	require.NoError(t, err)

	persona := "You find sources."
	researcher := openapi.NewBot{Name: "researcher", Settings: &openapi.BotSettings{Persona: &persona}}
	source := openapi.Source{Source: "name: checker\ndescription: Reads a pull request and reports.\nruntime: claude\n"}

	for name, c := range map[string]*openapi.ClientWithResponses{
		"another bot's pi": vmap, "a routine's script": w.client(script), "the coordinator token": shared,
	} {
		put, err := c.CreateBotWithResponse(ctx, researcher)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, put.StatusCode(), name)

		renamed, err := c.RenameBotWithResponse(ctx, vmapBot.ID, openapi.BotChange{Name: "maps"})
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, renamed.StatusCode(), name)

		applied, err := c.ApplyProfileWithResponse(ctx, "checker", source)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, applied.StatusCode(), name)

		removed, err := c.DeleteBotWithResponse(ctx, vmapBot.ID, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, removed.StatusCode(), name)
	}

	itself, err := pi.DeleteBotWithResponse(ctx, orchestration.DefaultBotID, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, itself.StatusCode(), "it does not remove itself")

	force := true
	forced, err := pi.DeleteBotWithResponse(ctx, vmapBot.ID, &openapi.DeleteBotParams{Force: &force})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, forced.StatusCode(), "only the owner removes a Bot with its chats")

	made, err := pi.CreateBotWithResponse(ctx, researcher)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))

	// What a change answers is the whole Bot, its memory too: the main Bot has every right on Bots.
	_, err = w.svc.SetBotMemory(ctx, vmapBot.ID, "Linh leads the maps team.")
	require.NoError(t, err)

	vmapPersona := "You answer about maps."
	changed, err := pi.PutBotWithResponse(ctx, vmapBot.ID, openapi.BotSettings{Persona: &vmapPersona})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, changed.StatusCode(), string(changed.Body))
	assert.Contains(t, string(changed.Body), "Linh leads")

	renamed, err := pi.RenameBotWithResponse(ctx, vmapBot.ID, openapi.BotChange{Name: "Bản đồ"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, renamed.StatusCode(), string(renamed.Body))
	assert.Equal(t, "Bản đồ", renamed.JSON200.Name)

	applied, err := pi.ApplyProfileWithResponse(ctx, "checker", source)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, applied.StatusCode(), string(applied.Body))

	listed, err := pi.ListSetupChangesWithResponse(ctx, &openapi.ListSetupChangesParams{ChatId: mine.ID})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, listed.StatusCode(), "the changes are the owner's to read")

	listed, err = owner.ListSetupChangesWithResponse(ctx, &openapi.ListSetupChangesParams{ChatId: mine.ID})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 4)

	// An undo applies to the last change of its Bot only: the rename is undone, the change of the persona before
	// it is not.
	for _, c := range listed.JSON200.Data {
		if c.Name == vmapBot.ID && c.Id != listed.JSON200.Data[2].Id {
			assert.Equal(t, openapi.Changed, c.Action)
			assert.False(t, c.CanUndo, "a later change of the Bot came after it")

			continue
		}

		want := openapi.Made
		if c.Name == vmapBot.ID {
			want = openapi.Changed
		}

		assert.Equal(t, want, c.Action, c.Name)
		assert.True(t, c.CanUndo, c.Name)

		undone, err := owner.UndoSetupChangeWithResponse(ctx, c.Id)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, undone.StatusCode(), string(undone.Body))
	}

	gone, err := owner.GetBotWithResponse(ctx, made.JSON201.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, gone.StatusCode())

	back, err := owner.GetBotWithResponse(ctx, vmapBot.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, back.StatusCode())
	assert.Equal(t, "vmap", back.JSON200.Name, "the undone rename gives the Bot its name back")
	assert.Equal(t, vmapPersona, back.JSON200.Persona, "and keeps what the change before it set")

	again, err := owner.UndoSetupChangeWithResponse(ctx, listed.JSON200.Data[0].Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, again.StatusCode(), "undone once")
}

// Scenario: a Bot with a list of workers (issue #169) creates a Run and
// gives it Tasks. A Task for a profile on the list is taken; one for another,
// or one that names none (the implementer's), is refused with the allowed
// ones named, as OpenClaw's spawn refusal. The owner's Tasks are not limited.
func TestABotGivesWorkOnlyToItsWorkers(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	made, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "vmap", Workers: orchestration.AllowList{Limited: true, Names: []string{"reviewer"}},
	})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, made.ID, "")
	require.NoError(t, err)

	vmap := w.piToken("pi-coordinator", chat.ID, made.ID)
	run := w.createRun(vmap, "review the map tiles")

	reviewer, implementer := "reviewer", "implementer"

	for name, profile := range map[string]*string{"another profile": &implementer, "no profile": nil} {
		refused, err := vmap.CreateTasksWithResponse(ctx, run.Id, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
			{Ref: "a", Spec: "write it", Profile: profile},
		}})
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, refused.StatusCode(), name)
		assert.Contains(t, string(refused.Body), "(allowed: reviewer)", name)
	}

	taken, err := vmap.CreateTasksWithResponse(ctx, run.Id, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "a", Spec: "review it", Profile: &reviewer},
	}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, taken.StatusCode(), string(taken.Body))

	ownRun := w.createRun(owner, "the owner's work")
	ownTasks, err := owner.CreateTasksWithResponse(ctx, ownRun.Id, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "a", Spec: "write it", Profile: &implementer},
	}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, ownTasks.StatusCode(), string(ownTasks.Body))
}
