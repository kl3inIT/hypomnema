package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// nowRunner stands for the control plane's runner: it starts a run as the
// runner does, by the store's RunRoutineNow, and starts no script or pi.
type nowRunner struct{ svc *orchestration.Service }

func (n nowRunner) RunNow(ctx context.Context, id string) (routinestore.RoutineRun, error) {
	started, err := n.svc.RunRoutineNow(ctx, id)

	return started.Run, err
}

// routineArgs is a routines call's arguments as a tools server sends them.
func routineArgs(t *testing.T, args map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(args)
	require.NoError(t, err)

	return string(raw)
}

// Scenario: a Bot's pi finds the routines tool on its one list (issue #536)
// and a worker of a Run from the same chat does not, nor is it let call it,
// as Hermes keeps cronjob_manage from its sub-agents. The pi makes a script
// Routine from its chat, with the text its tools server read, on at once and
// undoable by the owner; changes only its summary, the rest staying; runs it
// once, and not again while that run goes; and lists its own Routines, one
// with its runs. What the client API refuses a pi's token is refused here in
// words it can act on: a schedule out of bounds, a Routine that is not its
// Bot's, a change from a Routine's own run's chat. A script sent as a path
// alone, by a tools server of an earlier release, is refused.
func TestOnlyABotsPiSetsAndRunsItsRoutines(t *testing.T) {
	t.Parallel()

	w, _, _ := gatewayWorld(t, false, map[string]string{})
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	worker := w.workerOf("node_a", chat.ID)

	assert.Contains(t, toolNames(w.tools(pi)), agenttool.ToolRoutines)
	assert.NotContains(t, toolNames(w.tools(worker)), agenttool.ToolRoutines, "a worker schedules no work")
	assert.NotContains(t, toolNames(w.tools(coordinator)), agenttool.ToolRoutines, "the CLI's token has no MCP")

	status, _, _ := w.use(worker, agenttool.ToolRoutines, routineArgs(t, map[string]any{"action": "list"}))
	assert.Equal(t, http.StatusNotFound, status, "a worker is refused the tool")

	_, text, isError := w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{"action": "list"}))
	require.False(t, isError, text)
	assert.Equal(t, "You have no Routine.", text)

	script := "#!/bin/sh\necho copied\n"
	_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "set", "name": "sales-copy", "every_minutes": 60, "script": "routines/copy.sh",
		"script_text": script, "summary": "copies the Sales figures",
	}))
	require.False(t, isError, text)
	assert.Contains(t, text, "Every hour, ")
	assert.Contains(t, text, "runs the script copy.sh: copies the Sales figures. It is on.")

	all, err := w.svc.Routines().ListRoutines(ctx, routinestore.RoutineFilter{Bot: orchestration.DefaultBotID})
	require.NoError(t, err)
	require.Len(t, all, 1)

	made := all[0]
	assert.Equal(t, routinestore.RoutineScript, made.Kind)
	assert.Equal(t, "copy.sh", made.Script, "a script is named by its file")
	assert.Equal(t, script, made.ScriptText)
	assert.Equal(t, chat.ID, made.ChatID, "made from the pi's chat")
	assert.True(t, made.Announce)
	assert.True(t, made.CanUndo, "a Bot's change, which the owner can undo")

	_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "set", "name": "sales-copy", "summary": "copies the Sales figures to the Base",
	}))
	require.False(t, isError, text)

	changed, err := w.svc.Routines().GetRoutine(ctx, made.ID)
	require.NoError(t, err)
	assert.Equal(t, "copies the Sales figures to the Base", changed.Summary)
	assert.Equal(t, script, changed.ScriptText, "what is left out stays")
	assert.Equal(t, 60, changed.EveryMinutes)

	for _, c := range []struct {
		args    map[string]any
		refusal string
	}{
		{map[string]any{"action": "set", "name": "sales-copy", "every_minutes": 2}, "every 5 to 1440 minutes"},
		{map[string]any{"action": "set", "name": "fresh", "summary": "s"}, "a prompt, or a script file"},
		{map[string]any{"action": "set", "name": "fresh", "every_minutes": 60, "script": "routines/copy.sh"},
			"cannot send a script yet"},
		{map[string]any{"action": "run", "name": "missing"}, "you have no routine missing"},
		{map[string]any{"action": "pause", "name": "sales-copy"}, "action is list, set or run"},
	} {
		_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, c.args))
		assert.True(t, isError, c.args)
		assert.Contains(t, text, c.refusal, c.args)
	}

	_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "run", "name": "sales-copy",
	}))
	require.False(t, isError, text)
	assert.Contains(t, text, "of routine sales-copy started")

	_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "run", "name": "sales-copy",
	}))
	assert.True(t, isError, "not again while a run of it goes: "+text)

	_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "list", "name": "sales-copy",
	}))
	require.False(t, isError, text)
	assert.Contains(t, text, "- sales-copy ("+made.ID+")")
	assert.Contains(t, text, "runs, the newest first:")

	// Another Bot's Routine, which the owner made, is not the pi's to list or run.
	vmap, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)

	theirs, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: vmap.ID, Name: "digest", Kind: routinestore.RoutinePrompt, Prompt: "Write the digest.", EveryMinutes: 60,
		Announce: true,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	_, text, _ = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{"action": "list"}))
	assert.NotContains(t, text, "digest")

	_, text, isError = w.use(pi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "run", "name": "digest",
	}))
	assert.True(t, isError, text)

	// A prompt run's own chat makes and changes no Routines (Hermes's loop prevention).
	started, err := w.svc.RunRoutineNow(ctx, theirs.ID)
	require.NoError(t, err)

	runPi, err := w.svc.AddBotToken(ctx, "pi", started.Run.ChatID, vmap.ID)
	require.NoError(t, err)

	_, text, isError = w.use(runPi, agenttool.ToolRoutines, routineArgs(t, map[string]any{
		"action": "set", "name": "digest", "summary": "a longer digest",
	}))
	assert.True(t, isError, text)
	assert.Contains(t, text, "a routine's run cannot make or change routines")
}
