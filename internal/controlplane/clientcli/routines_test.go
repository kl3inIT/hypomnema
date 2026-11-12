package clientcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// startsRuns stands in for the control plane's runner: a run is recorded and
// nothing executes it, which is the runner's own test.
type startsRuns struct{ svc *orchestration.Service }

func (s *startsRuns) RunNow(ctx context.Context, id string) (orchestration.RoutineRun, error) {
	started, err := s.svc.RunRoutineNow(ctx, id)

	return started.Run, err
}

func (s *startsRuns) Remove(ctx context.Context, id string) error {
	_, err := s.svc.DeleteRoutine(ctx, id)

	return err
}

func (s *startsRuns) Undo(ctx context.Context, id string) (orchestration.Routine, bool, error) {
	r, removed, _, err := s.svc.UndoRoutine(ctx, id)

	return r, removed, err
}

func newRoutineWorld(t *testing.T) *world {
	t.Helper()

	runner := &startsRuns{}
	w := newWorldWith(t, nil, func(srv *clientapi.Server) { srv.WithRoutines(runner) })
	runner.svc = w.svc

	return w
}

// Scenario: the owner makes an hourly script Routine from the laptop, with
// the script in a file, and later moves it to a cron schedule: the script
// stays. He pauses it, resumes it and runs it now; a second run now is told
// one is running. The launcher's command, with the coordinator's token, is
// given the script and the run's key once a run. A Bot makes a Routine with
// the same command from its chat: it is on at once, the Bot runs it once,
// and the owner undoes it. A Bot neither pauses, removes nor undoes one.
func TestRoutinesFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newRoutineWorld(t)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "routines", "list")
	require.NoError(t, err)
	assert.Equal(t, "no routine\n", out)

	dir := t.TempDir()
	file := filepath.Join(dir, "sync.sh")
	text := "#!/bin/sh\nhypomnema superset chart 9 164 --format csv\n"
	require.NoError(t, os.WriteFile(file, []byte(text), 0o600))

	_, err = w.cmd(t, "routines", "set", "sync", "--every", "60")
	require.ErrorContains(t, err, "--prompt-file or --script-file")

	_, err = w.cmd(t, "routines", "set", "sync", "--every", "3", "--script-file", file)
	require.ErrorContains(t, err, "every_minutes", "the contract refuses an interval under five minutes")

	var sync openapi.Routine
	w.jsonCmd(t, &sync, "", "routines", "set", "sync", "--every", "60", "--script-file", file, "--summary",
		"the usage figures go to the Lark Base")
	assert.Equal(t, openapi.Script, sync.Kind)
	assert.Equal(t, "sync.sh", *sync.Script, "the script is named after the file it came from")
	assert.Equal(t, "every hour", sync.Schedule)
	assert.Equal(t, 600, sync.TimeoutSeconds)
	assert.Equal(t, openapi.On, sync.State)
	assert.True(t, sync.Announce)
	assert.False(t, sync.CanUndo)
	assert.Equal(t, "Every hour, coordinator runs the script sync.sh: the usage figures go to the Lark Base.",
		sync.Sentence)

	var moved openapi.Routine
	w.jsonCmd(t, &moved, "", "routines", "set", "sync", "--cron", "0 7 * * *", "--tz", "Asia/Ho_Chi_Minh",
		"--timeout", "7200", "--quiet")
	assert.Equal(t, sync.Id, moved.Id)
	assert.Equal(t, text, *moved.ScriptText, "what the flags leave out stays")
	assert.Nil(t, moved.EveryMinutes)
	assert.Equal(t, "every day at 7:00 Ho Chi Minh time", moved.Schedule)
	assert.Equal(t, 7200, moved.TimeoutSeconds, "a script's limit has no upper bound")
	assert.False(t, moved.Announce)

	_, err = w.cmd(t, "routines", "set", "sync", "--script", "../sync.sh")
	require.ErrorContains(t, err, "never a path or a command line")

	out, err = w.cmd(t, "routines", "pause", sync.Id)
	require.NoError(t, err)
	assert.Contains(t, out, "\tpaused\t")

	out, err = w.cmd(t, "routines", "resume", sync.Id)
	require.NoError(t, err)
	assert.Contains(t, out, "\ton\t")

	// The launcher, as the coordinator's user: nothing is handed out while no run is starting.
	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	pi.login(t, bearertoken.Coordinator)

	into := filepath.Join(dir, "bots", "coordinator", "routines")

	_, err = pi.cmd(t, "routines", "script", sync.Id, "--bot", "coordinator", "--into", into)
	require.ErrorContains(t, err, "no run of routine")

	var run openapi.RoutineRun
	w.jsonCmd(t, &run, "", "routines", "run", sync.Id)
	assert.Equal(t, openapi.RoutineRunStateRunning, run.State)
	assert.Equal(t, openapi.Manual, run.StartedBy)

	_, err = w.cmd(t, "routines", "run", sync.Id)
	require.ErrorContains(t, err, run.Id+" of the routine is still running")
	require.ErrorContains(t, err, "conflict")

	out, err = w.cmd(t, "routines", "show", sync.Id)
	require.NoError(t, err)
	assert.Contains(t, out, "script sync.sh (stops after 7200 s):\n"+text)
	assert.Contains(t, out, "runs, the newest first:")
	assert.Contains(t, out, run.Id+"\trunning\tmanual\t")

	require.NoError(t, os.MkdirAll(into, 0o700))
	//nolint:gosec // A script, which is run by nothing here.
	require.NoError(t, os.WriteFile(filepath.Join(into, "sync.sh"), []byte("#!/bin/sh\nrm -rf ~\n"), 0o700))

	_, err = pi.cmd(t, "routines", "script", sync.Id, "--bot", "vmap", "--into", into)
	require.ErrorContains(t, err, "is not bot vmap's")

	out, err = pi.cmd(t, "routines", "script", sync.Id, "--bot", "coordinator", "--into", into)
	require.NoError(t, err)
	assert.Equal(t, "sync.sh\n7200\n", out, "the file's name and the seconds it may run, and never the key")

	written, err := os.ReadFile(filepath.Join(into, "sync.sh")) //nolint:gosec // the test's own directory
	require.NoError(t, err)
	assert.Equal(t, text, string(written), "what runs is the text the control plane keeps, whatever the file held")

	key, err := os.ReadFile(runKeyFile(into, sync.Id))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(key), "hrk_"), "the run's key is beside the script")

	_, err = pi.cmd(t, "routines", "script", sync.Id, "--bot", "coordinator", "--into", into)
	require.ErrorContains(t, err, "was started already", "a run's script and key are given once")

	// A Bot makes a Routine with the owner's command, from the chat it works in.
	_, err = pi.cmdIn(t, "Summarize what changed overnight.\n", "routines", "set", "brief", "--every", "1440",
		"--prompt-file", "-")
	require.ErrorContains(t, err, "from the chat it works in")

	chat, err := w.svc.CreateChat(t.Context(), "", "A brief every morning")
	require.NoError(t, err)

	var brief openapi.Routine
	pi.jsonCmd(t, &brief, "Summarize what changed overnight.\n", "routines", "set", "brief", "--chat", chat.ID,
		"--cron", "0 7 * * 1-5", "--tz", "Asia/Ho_Chi_Minh", "--prompt-file", "-", "--summary",
		"summarizes what changed overnight")
	assert.Equal(t, openapi.On, brief.State, "it is on at once")
	assert.Equal(t, "bot", brief.CreatedBy)
	assert.True(t, brief.CanUndo)
	assert.Equal(t, chat.ID, *brief.ChatId)
	assert.NotNil(t, brief.NextRunAt)
	assert.Equal(t, "Every weekday at 7:00 Ho Chi Minh time, coordinator works on a prompt: summarizes what "+
		"changed overnight.", brief.Sentence)

	out, err = pi.cmd(t, "routines", "run", brief.Id)
	require.NoError(t, err, "the Bot runs it once")
	assert.Contains(t, out, "\trunning\tmanual\t")

	out, err = pi.cmd(t, "routines", "list", "--bot", "coordinator")
	require.NoError(t, err)
	assert.Contains(t, out, "coordinator/sync\tscript\t")
	assert.Contains(t, out, "coordinator/brief\tprompt\t")

	for _, refused := range [][]string{
		{"routines", "pause", sync.Id},
		{"routines", "resume", sync.Id},
		{"routines", "rm", sync.Id},
		{"routines", "undo", brief.Id},
		{"routines", "revoke", sync.Id, "lark:work", "lark.base.write"},
	} {
		_, err = pi.cmd(t, refused...)
		require.ErrorContains(t, err, "forbidden", refused[1])
	}

	_, err = w.cmd(t, "routines", "undo", sync.Id)
	require.ErrorContains(t, err, "was not a bot's")

	out, err = w.cmd(t, "routines", "undo", brief.Id)
	require.NoError(t, err)
	assert.Equal(t, "routine "+brief.Id+", which a bot made, is removed\n", out)

	out, err = w.cmd(t, "routines", "rm", sync.Id)
	require.NoError(t, err)
	assert.Equal(t, "routine "+sync.Id+" is removed\n", out)

	_, err = w.cmd(t, "routines", "show", sync.Id)
	require.ErrorContains(t, err, "not_found")
}

// Scenario: a script Routine changes a chart in Superset. Its launcher was
// given the run's key, which the command presents: the change waits for the
// owner where it is, as an Approval of the Routine's run. He approves it
// always for this Routine: the same command goes on, and the next run's
// change passes unasked. He sees the grant on the Routine and takes it back:
// the change asks again. The script makes and starts no Routines.
func TestAScriptsRequestsBelongToItsRun(t *testing.T) { //nolint:paralleltest // sets the launcher's environment
	w := newSupersetWorld(t, 20*time.Second)
	w.connect(t)

	// connect logged the owner in; the script has the coordinator's token, in a home of its own.
	owner := w.world
	script := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	script.login(t, bearertoken.Coordinator)

	_, err := w.svc.SetConnectionChats(t.Context(), integration.SupersetID, "vmaps", true)
	require.NoError(t, err)

	sync, err := w.svc.PutRoutine(t.Context(), orchestration.RoutineSpec{
		Bot: orchestration.DefaultBot, Name: "sync", Kind: orchestration.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\nhypomnema superset api PUT /api/v1/chart/166 --data -\n", EveryMinutes: 60,
	}, orchestration.RoutineAuthor{})
	require.NoError(t, err)

	into := filepath.Join(t.TempDir(), "routines")

	// launch is the launcher starting a run: the script and the run's key are written, and the script's
	// environment names the key's file.
	launch := func() orchestration.RoutineRun {
		started, err := w.svc.RunRoutineNow(t.Context(), sync.ID)
		require.NoError(t, err)

		_, err = script.cmd(t, "routines", "script", sync.ID, "--bot", "coordinator", "--into", into)
		require.NoError(t, err)

		return started.Run
	}

	first := launch()

	t.Setenv(routineEnv, sync.ID)
	t.Setenv(agenttool.EnvRoutineRun, runKeyFile(into, sync.ID))

	rename := []string{"superset", "api", "PUT", "/api/v1/chart/166", "--data", "-"}
	done := make(chan error, 1)

	go func() {
		_, err := script.cmdIn(t, `{"slice_name":"renamed"}`, rename...)
		done <- err
	}()

	var pending []orchestration.Approval

	require.Eventually(t, func() bool {
		pending, err = w.svc.ListApprovals(t.Context(), orchestration.ApprovalFilter{RoutineID: sync.ID})

		return err == nil && len(pending) == 1
	}, 10*time.Second, 10*time.Millisecond, "the change waits for the owner")
	assert.Equal(t, first.ID, pending[0].RoutineRunID)
	assert.Empty(t, pending[0].ChatID)

	waiting, err := w.svc.GetRoutineRun(t.Context(), first.ID)
	require.NoError(t, err)
	assert.Equal(t, orchestration.RunWaiting, waiting.State)

	select {
	case err := <-done:
		require.Fail(t, "the command ended while its request waited", "%v", err)
	default:
	}

	_, err = owner.cmd(t, "approvals", "approve", pending[0].ID, "--session")
	require.ErrorContains(t, err, "no Run or chat", "a script's run has no session to approve for")

	var decided openapi.Approval
	owner.jsonCmd(t, &decided, "", "approvals", "approve", pending[0].ID, "--routine")
	require.NotNil(t, decided.Scope)
	assert.Equal(t, openapi.ApprovalScopeRoutine, *decided.Scope)
	assert.Equal(t, sync.ID, *decided.RoutineId)

	require.NoError(t, <-done, "the same command goes on where it waited")

	_, err = w.svc.FinishRoutineRun(t.Context(), first.ID, orchestration.RunResult{State: orchestration.RunSucceeded})
	require.NoError(t, err)

	launch()

	_, err = script.cmdIn(t, `{"slice_name":"renamed again"}`, rename...)
	require.NoError(t, err, "a later run's change is not asked")

	out, err := owner.cmd(t, "routines", "show", sync.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "allowed always for this routine:\n- superset:vmaps superset.charts.write (Create or change)")

	out, err = owner.cmd(t, "routines", "revoke", sync.ID, "superset:vmaps", "superset.charts.write")
	require.NoError(t, err)
	assert.Contains(t, out, "asks again for superset.charts.write of superset:vmaps")

	go func() {
		_, err := script.cmdIn(t, `{"slice_name":"renamed once more"}`, rename...)
		done <- err
	}()

	require.Eventually(t, func() bool {
		pending, err = w.svc.ListApprovals(t.Context(), orchestration.ApprovalFilter{
			RoutineID: sync.ID, Status: orchestration.ApprovalPending,
		})

		return err == nil && len(pending) == 1
	}, 10*time.Second, 10*time.Millisecond, "taken back, the change asks again")

	_, err = owner.cmd(t, "approvals", "reject", pending[0].ID)
	require.NoError(t, err)
	require.ErrorContains(t, <-done, "Rejected by the owner")

	chat, err := w.svc.CreateChat(t.Context(), "", "")
	require.NoError(t, err)

	_, err = script.cmdIn(t, "Do it again.\n", "routines", "set", "more", "--chat", chat.ID, "--every", "60",
		"--prompt-file", "-")
	require.ErrorContains(t, err, "a routine's run cannot make or change routines")

	_, err = script.cmd(t, "routines", "run", sync.ID)
	require.ErrorContains(t, err, "a routine's run starts no routines")

	assert.Contains(t, w.logs.String(), `verdict="approved for the routine"`)
	assert.NotContains(t, w.logs.String(), "hrk_", "a run's key is in no log line")
}
