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

func TestOnlyTheOwnerGrantsReadRoutineAccess(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)
	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	pi := w.piToken("pi-coordinator", chat.ID, orchestration.DefaultBotID)
	prompt, minutes := "Write a brief.", 60
	spec := openapi.RoutineSettings{Kind: openapi.Prompt, Prompt: &prompt, EveryMinutes: &minutes}

	made, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "brief", spec)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))
	assert.Equal(t, openapi.Full, made.JSON200.Access)
	grant := openapi.PatchRoutineJSONRequestBody{Access: openapi.Read}

	denied, err := pi.PatchRoutineWithResponse(ctx, made.JSON200.Id, grant)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, denied.StatusCode())

	attempt := spec
	attempt.Access = &grant.Access
	put, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "brief", attempt)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, put.StatusCode())

	allowed, err := owner.PatchRoutineWithResponse(ctx, made.JSON200.Id, grant)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, allowed.StatusCode(), string(allowed.Body))
	assert.Equal(t, openapi.Read, allowed.JSON200.Access)
	started, err := w.svc.RunRoutineNow(ctx, made.JSON200.Id)
	require.NoError(t, err)

	changed := "Write a new brief."
	spec.Prompt = &changed
	updated, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "brief", spec)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updated.StatusCode(), string(updated.Body))
	assert.Equal(t, openapi.Full, updated.JSON200.Access, "a Bot edit resets access for future runs")

	readPi := w.piToken("pi-coordinator", started.Run.ChatID, orchestration.DefaultBotID)
	blocked, err := readPi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "other", spec)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, blocked.StatusCode())
	assert.Contains(t, string(blocked.Body), "This run reads only")
	read, err := owner.GetChatWithResponse(ctx, started.Run.ChatID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, read.StatusCode(), string(read.Body))
	require.NotNil(t, read.JSON200.ReadRun)
	assert.True(t, *read.JSON200.ReadRun, "in-flight access is not reopened by the edit")
}

// Scenario: the web's form asks for a schedule in words and its next three
// times; a schedule a Routine may not have is a bad request that says why.
func TestTheFormPreviewsASchedule(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)

	cron, zone := "0 7 * * 1-5", "Asia/Ho_Chi_Minh"
	ok, err := c.PreviewRoutineScheduleWithResponse(t.Context(), openapi.RoutineSchedule{Cron: &cron, Timezone: &zone})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ok.StatusCode(), string(ok.Body))
	assert.Equal(t, "every weekday at 7:00 Ho Chi Minh time", ok.JSON200.Schedule)
	require.Len(t, ok.JSON200.Next, 3)
	assert.True(t, ok.JSON200.Next[0].Before(ok.JSON200.Next[1]))

	four := 4
	bad, err := c.PreviewRoutineScheduleWithResponse(t.Context(), openapi.RoutineSchedule{EveryMinutes: &four})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, bad.Body, bad.StatusCode(), http.StatusBadRequest))
	assert.Contains(t, string(bad.Body), "every 5 to 1440 minutes")
}

// Scenario: a Routine has run three times. The runs come two at a time: the
// first page says how many there are and where the next begins; the last
// has no next.
func TestARoutinesRunsArePaged(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	r, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	for range 3 {
		started, err := w.svc.RunRoutineNow(ctx, r.ID)
		require.NoError(t, err)
		_, err = w.svc.FinishRoutineRun(ctx, started.Run.ID, routinestore.RunResult{State: routinestore.RunSucceeded})
		require.NoError(t, err)
	}

	limit := 2
	first, err := c.ListRoutineRunsWithResponse(ctx, r.ID, &openapi.ListRoutineRunsParams{Limit: &limit})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.StatusCode(), string(first.Body))
	assert.Len(t, first.JSON200.Data, 2)
	assert.Equal(t, 3, first.JSON200.Total)
	require.NotNil(t, first.JSON200.NextOffset)
	assert.Equal(t, 2, *first.JSON200.NextOffset)

	last, err := c.ListRoutineRunsWithResponse(ctx, r.ID,
		&openapi.ListRoutineRunsParams{Limit: &limit, Offset: first.JSON200.NextOffset})
	require.NoError(t, err)
	assert.Len(t, last.JSON200.Data, 1)
	assert.Nil(t, last.JSON200.NextOffset)
}

// Scenario: a pi's token (issue #260) makes the Routines of its own Bot, in
// its own chat, and runs only those. A Routine's script's token (issue #211)
// makes and runs none. Neither is given a run's script: only the
// coordinator's token, outside every box, is.
func TestBotTokensActOnTheirOwnBotsRoutinesOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	coordinator, _ := w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	vmapBot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap", Persona: "You answer about VMAP."})
	require.NoError(t, err)

	mine, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	theirs, err := w.svc.CreateChat(ctx, vmapBot.ID, "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", mine.ID, orchestration.DefaultBotID)
	script, text, every := "sync.sh", "#!/bin/sh\n", 60
	spec := openapi.RoutineSettings{Kind: openapi.Script, Script: &script, ScriptText: &text, EveryMinutes: &every}

	other, err := pi.PutRoutineWithResponse(ctx, vmapBot.ID, "sync", spec)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, other.StatusCode(), "another Bot's routine")

	elsewhere := spec
	elsewhere.ChatId = &theirs.ID
	named, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "sync", elsewhere)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, named.StatusCode(), "a routine made from another chat")

	made, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "sync", spec)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))
	require.NotNil(t, made.JSON200.ChatId)
	assert.Equal(t, mine.ID, *made.JSON200.ChatId, "the chat comes from the token")

	launched, err := pi.LaunchRoutineScriptWithResponse(ctx, made.JSON200.Id,
		openapi.RoutineLaunchRequest{Bot: orchestration.DefaultBotID})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, launched.StatusCode(), "a run's key is not a pi's")

	secret, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", made.JSON200.Id)
	require.NoError(t, err)

	run := w.client(secret)
	work, err := run.CreateRunWithResponse(ctx, nil, openapi.NewRun{Objective: "script work"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, work.StatusCode(), string(work.Body))
	assert.Nil(t, work.JSON201.ChatId, "script Routines do not create interactive chats")
	owner, _ := w.token("owner", bearertoken.Owner)
	adopted, err := owner.AdoptRunWithResponse(ctx, work.JSON201.Id, openapi.RunAdoption{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, adopted.StatusCode(), "a script's Run cannot be adopted")

	again, err := run.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "more", spec)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, again.StatusCode(), "a routine's run makes no routines")

	started, err := run.RunRoutineWithResponse(ctx, made.JSON200.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, started.StatusCode(), "a routine's run starts no routines")

	launched, err = run.LaunchRoutineScriptWithResponse(ctx, made.JSON200.Id,
		openapi.RoutineLaunchRequest{Bot: orchestration.DefaultBotID})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, launched.StatusCode())

	launched, err = coordinator.LaunchRoutineScriptWithResponse(ctx, made.JSON200.Id,
		openapi.RoutineLaunchRequest{Bot: orchestration.DefaultBotID})
	require.NoError(t, err)
	assert.NotEqual(t, http.StatusForbidden, launched.StatusCode(), "the launcher's token asks; no run has started")
}

// Scenario: issue #1023, with the owner's decision of 2026-11-21: the owner marks read the Thư ký Bot's prompt
// Routine whose check script counts the new messages first. The check's token, the one any Routine's script has,
// has the rights of the read run's pi in the client API, which are none: it makes no Run and writes no Inbox item,
// and reads the owner's accounts only through the agent API's read tools. A Bot that changes the check takes the
// grant back, as it does for a prompt.
func TestAReadRoutineMayHaveACheckThatHasNoRightsHere(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)
	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	pi := w.piToken("pi-coordinator", chat.ID, orchestration.DefaultBotID)

	prompt, script, text, minutes := "Triage the new messages.", "new-messages.mjs", "#!/usr/bin/env node\n", 15
	spec := openapi.RoutineSettings{Kind: openapi.Prompt, Prompt: &prompt, Script: &script, ScriptText: &text,
		EveryMinutes: &minutes}
	made, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "triage", spec)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))

	read, err := owner.PatchRoutineWithResponse(ctx, made.JSON200.Id,
		openapi.PatchRoutineJSONRequestBody{Access: openapi.Read})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, read.StatusCode(), string(read.Body))
	assert.Equal(t, openapi.Read, read.JSON200.Access)

	secret, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", made.JSON200.Id)
	require.NoError(t, err)
	check := w.client(secret)

	work, err := check.CreateRunWithResponse(ctx, nil, openapi.NewRun{Objective: "script work"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, work.StatusCode(), string(work.Body))

	todo, err := check.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{Title: "Reply to Lan"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, todo.StatusCode(), string(todo.Body))
	assert.Contains(t, string(todo.Body), "reads only")

	changed := "#!/usr/bin/env node\nconsole.log('wake')\n"
	spec.ScriptText = &changed
	updated, err := pi.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "triage", spec)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updated.StatusCode(), string(updated.Body))
	assert.Equal(t, openapi.Full, updated.JSON200.Access, "a Bot's change of the check takes the grant back")

	script2 := "sync.sh"
	alone := openapi.RoutineSettings{Kind: openapi.Script, Script: &script2, ScriptText: &text, EveryMinutes: &minutes,
		Access: new(openapi.Read)}
	refused, err := owner.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "sync", alone)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, refused.StatusCode(), "a script alone is never read: %s", refused.Body)
}

// Scenario: review of #1037, round 2: while the check of a read Routine runs, the owner switches the Routine to
// full and a Bot edits it. The check's token keeps the rights of its run, which are none in the client API.
func TestAReadChecksTokenKeepsItsRunsAccess(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)

	prompt, script, text, minutes, read := "Triage.", "new-messages.mjs", "#!/usr/bin/env node\n", 15, openapi.Read
	made, err := owner.PutRoutineWithResponse(ctx, orchestration.DefaultBotID, "triage", openapi.RoutineSettings{
		Kind: openapi.Prompt, Prompt: &prompt, Script: &script, ScriptText: &text, EveryMinutes: &minutes,
		Access: &read,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode(), string(made.Body))

	_, err = w.svc.RunRoutineNow(ctx, made.JSON200.Id)
	require.NoError(t, err)
	secret, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", made.JSON200.Id)
	require.NoError(t, err)
	check := w.client(secret)

	full, err := owner.PatchRoutineWithResponse(ctx, made.JSON200.Id,
		openapi.PatchRoutineJSONRequestBody{Access: openapi.Full})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, full.StatusCode(), string(full.Body))

	work, err := check.CreateRunWithResponse(ctx, nil, openapi.NewRun{Objective: "script work"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, work.StatusCode(), "the run reads only as it started: %s", work.Body)

	todo, err := check.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{}, openapi.NewTodo{Title: "Reply to Lan"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, todo.StatusCode(), string(todo.Body))
}

// Scenario: issue #1023 with the owner's choice of 2026-11-21 (review of #1037, round 2): the owner makes the Thư
// ký Bot read his own accounts only; the main Bot, from its chat, cannot make it use them again.
func TestOnlyTheOwnerSetsWhatABotDoesWithHisAccounts(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)

	read, use := openapi.OwnerAccountsRead, openapi.OwnerAccountsUse
	made, err := owner.CreateBotWithResponse(ctx, openapi.NewBot{Name: "Thư ký",
		Settings: &openapi.BotSettings{OwnerAccounts: &read}})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	require.NotNil(t, made.JSON201.OwnerAccounts)
	assert.Equal(t, read, *made.JSON201.OwnerAccounts)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	main := w.piToken("pi-coordinator", chat.ID, orchestration.DefaultBotID)

	changed, err := main.PutBotWithResponse(ctx, made.JSON201.Id, openapi.BotSettings{OwnerAccounts: &use})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, changed.StatusCode(), string(changed.Body))

	got, err := owner.GetBotWithResponse(ctx, made.JSON201.Id)
	require.NoError(t, err)
	assert.Equal(t, read, *got.JSON200.OwnerAccounts)
}
