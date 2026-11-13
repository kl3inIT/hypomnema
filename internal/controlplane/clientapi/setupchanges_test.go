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

// Scenario: who manages Bots and profiles (issue #169). The Bot coordinator's
// pi makes a Bot and a profile from its chat; each change is listed there for
// the owner, who undoes it. Another Bot's pi, a Routine's script and the
// coordinator token outside every box are refused, and the Bot coordinator
// does not remove itself.
func TestOnlyTheCoordinatorBotManagesBotsAndProfiles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	shared, _ := w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	_, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)

	mine, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	theirs, err := w.svc.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", mine.ID, orchestration.DefaultBot)
	vmap := w.piToken("pi-coordinator", theirs.ID, "vmap")

	routine, err := w.svc.PutRoutine(ctx, orchestration.RoutineSpec{
		Bot: orchestration.DefaultBot, Name: "sync", Kind: orchestration.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, orchestration.RoutineAuthor{})
	require.NoError(t, err)

	script, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", routine.ID)
	require.NoError(t, err)

	persona := "You find sources."
	source := openapi.Source{Source: "name: checker\ndescription: Reads a pull request and reports.\nruntime: claude\n"}

	for name, c := range map[string]*openapi.ClientWithResponses{
		"another bot's pi": vmap, "a routine's script": w.client(script), "the coordinator token": shared,
	} {
		put, err := c.PutBotWithResponse(ctx, "researcher", openapi.BotSettings{Persona: &persona})
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, put.StatusCode(), name)

		applied, err := c.ApplyProfileWithResponse(ctx, "checker", source)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, applied.StatusCode(), name)

		removed, err := c.DeleteBotWithResponse(ctx, "vmap", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, removed.StatusCode(), name)
	}

	itself, err := pi.DeleteBotWithResponse(ctx, orchestration.DefaultBot, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, itself.StatusCode(), "it does not remove itself")

	force := true
	forced, err := pi.DeleteBotWithResponse(ctx, "vmap", &openapi.DeleteBotParams{Force: &force})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, forced.StatusCode(), "only the owner removes a Bot with its chats")

	made, err := pi.PutBotWithResponse(ctx, "researcher", openapi.BotSettings{Persona: &persona})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))

	// What a change answers is the whole Bot, its memory too: the coordinator has every right on Bots.
	_, err = w.svc.SetBotMemory(ctx, "vmap", "Linh leads the maps team.")
	require.NoError(t, err)

	vmapPersona := "You answer about maps."
	changed, err := pi.PutBotWithResponse(ctx, "vmap", openapi.BotSettings{Persona: &vmapPersona})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, changed.StatusCode(), string(changed.Body))
	assert.Contains(t, string(changed.Body), "Linh leads")

	applied, err := pi.ApplyProfileWithResponse(ctx, "checker", source)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, applied.StatusCode(), string(applied.Body))

	listed, err := pi.ListSetupChangesWithResponse(ctx, &openapi.ListSetupChangesParams{ChatId: mine.ID})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, listed.StatusCode(), "the changes are the owner's to read")

	listed, err = owner.ListSetupChangesWithResponse(ctx, &openapi.ListSetupChangesParams{ChatId: mine.ID})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	require.Len(t, listed.JSON200.Data, 3)

	for _, c := range listed.JSON200.Data {
		want := openapi.Made
		if c.Name == "vmap" {
			want = openapi.Changed
		}

		assert.Equal(t, want, c.Action, c.Name)
		assert.True(t, c.CanUndo, c.Name)

		undone, err := owner.UndoSetupChangeWithResponse(ctx, c.Id)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, undone.StatusCode(), string(undone.Body))
	}

	gone, err := owner.GetBotWithResponse(ctx, "researcher")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, gone.StatusCode())

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

	_, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "vmap", Workers: orchestration.AllowList{Limited: true, Names: []string{"reviewer"}},
	})
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "vmap", "")
	require.NoError(t, err)

	vmap := w.piToken("pi-coordinator", chat.ID, "vmap")
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
