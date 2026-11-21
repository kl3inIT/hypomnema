package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// Scenario: the supervisor starts pi for a chat (issue #260). Its token says
// the chat and the Bot and acts as the coordinator token it is made under;
// it ends when pi ends, when that coordinator token is revoked, and when the
// control plane starts again, since it is kept in memory only.
func TestABotTokenIsBoundToItsChatAndEndsWithItsPi(t *testing.T) {
	t.Parallel()

	s, db, clock := newTestService(t)
	ctx := t.Context()

	parent, _, err := s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	secret, err := s.AddBotToken(ctx, "pi-coordinator", chat.ID, DefaultBotID)
	require.NoError(t, err)

	kind, err := bearertoken.Parse(secret)
	require.NoError(t, err)
	assert.Equal(t, bearertoken.Bot, kind)

	got, err := s.AuthenticateToken(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, Token{
		ID: parent.ID, Name: "pi-coordinator", Kind: bearertoken.Bot, CreatedAt: clock.Now(), Chat: chat.ID,
		Bot: DefaultBotID,
	}, got)

	restarted := New(db, clock, s.newID)
	_, err = restarted.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "no pi outlives the control plane, nor its token")

	s.RevokeBotToken(secret)
	s.RevokeBotToken(secret)

	_, err = s.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "the token ends with its pi")

	again, err := s.AddBotToken(ctx, "pi-coordinator", chat.ID, DefaultBotID)
	require.NoError(t, err)
	require.NoError(t, s.RevokeToken(ctx, parent.ID))

	_, err = s.AuthenticateToken(ctx, again)
	require.ErrorIs(t, err, ErrNotFound, "a revoked coordinator token ends the tokens made under it")
}

// A token is made only under a working coordinator token, and only for the
// Bot the chat talks with.
func TestABotTokenNeedsItsCoordinatorAndItsChatsBot(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	chat, err := s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = s.AddBotToken(ctx, "pi-coordinator", chat.ID, DefaultBotID)
	require.ErrorIs(t, err, ErrNotFound, "no such coordinator token")

	_, _, err = s.AddToken(ctx, "web", bearertoken.Owner)
	require.NoError(t, err)

	_, err = s.AddBotToken(ctx, "web", chat.ID, DefaultBotID)
	require.ErrorIs(t, err, ErrInvalid, "an owner's token is not a coordinator's")

	_, _, err = s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	_, err = s.AddBotToken(ctx, "pi-coordinator", chat.ID, "vmap")
	require.ErrorIs(t, err, ErrInvalid, "the chat talks with another Bot")

	_, err = s.AddBotToken(ctx, "pi-coordinator", "cht_missing", DefaultBotID)
	require.ErrorIs(t, err, ErrNotFound)
}

// A Routine's token (issue #211) is made only for a Routine that runs a
// script: a prompt Routine's run is a chat's, whose pi has its own. The
// token is bound to the Routine's Bot and to no chat, and ends with the run.
func TestARoutineTokenIsMadeForAScriptRoutineOnly(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	ctx := t.Context()

	parent, _, err := s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	asked, err := s.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: DefaultBotID, Name: "digest", Kind: routinestore.RoutinePrompt, Prompt: "Sum up the day.", EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	_, err = s.AddRoutineToken(ctx, "pi-coordinator", asked.ID)
	require.ErrorIs(t, err, ErrConflict, "no run of it is live")

	_, err = s.RunRoutineNow(ctx, asked.ID)
	require.NoError(t, err)
	_, err = s.AddRoutineToken(ctx, "pi-coordinator", asked.ID)
	require.ErrorIs(t, err, ErrInvalid, "its run runs no script")

	_, err = s.AddRoutineToken(ctx, "pi-coordinator", "rtn_missing")
	require.ErrorIs(t, err, ErrNotFound)

	syncRoutine, err := s.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: DefaultBotID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh", ScriptText: syncScript,
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := s.RunRoutineNow(ctx, syncRoutine.ID)
	require.NoError(t, err)
	secret, err := s.AddRoutineToken(ctx, "pi-coordinator", syncRoutine.ID)
	require.NoError(t, err)

	got, err := s.AuthenticateToken(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, Token{
		ID: parent.ID, Name: "pi-coordinator", Kind: bearertoken.Bot, CreatedAt: clock.Now(),
		Bot: DefaultBotID, Routine: syncRoutine.ID, RoutineRun: run.Run.ID,
	}, got)
	assert.Empty(t, got.Chat, "a script answers no chat")

	s.RevokeBotToken(secret)

	_, err = s.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "the token ends with the run")
}

// Scenario: review of #1037, round 3: a Routine's run ends, and its token is not revoked yet (the runner revokes it
// after the run's end is written). It no longer works: the run's end, in its own transaction, ends it, so no token
// outlives the run whose snapshot it acts by, even when the Routine was changed meanwhile.
func TestARoutinesTokenEndsWithItsRun(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	_, _, err := s.AddToken(ctx, "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	triage, err := s.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: DefaultBotID, Name: "triage", Kind: routinestore.RoutinePrompt, Prompt: "Triage.", Script: "check.mjs",
		ScriptText: syncScript, EveryMinutes: 15, Access: routinestore.AccessRead,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := s.RunRoutineNow(ctx, triage.ID)
	require.NoError(t, err)
	secret, err := s.AddRoutineToken(ctx, "pi-coordinator", triage.ID)
	require.NoError(t, err)

	_, err = s.Routines().SetRoutineAccess(ctx, triage.ID, routinestore.AccessFull)
	require.NoError(t, err)

	reads, err := s.RoutineScriptReads(ctx, run.Run.ID)
	require.NoError(t, err)
	assert.True(t, reads, "while it runs it reads only, as it started")

	_, err = s.FinishRoutineRun(ctx, run.Run.ID, routinestore.RunResult{State: routinestore.RunSucceeded})
	require.NoError(t, err)

	_, err = s.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "the run ended: so did its token, before it was revoked")
}

// Scenario: review of #1037, round 3: the triage Routine's run starts reading only; then its check and its prompt
// are changed, and the read grant taken back, as a Bot's change does. The run goes on as it started: the launcher is given the check the
// run started with, and the chat its check opens holds the prompt it started with, reading only.
func TestARunLaunchesAndWakesWhatItStartedWith(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	triage, err := s.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: DefaultBotID, Name: "triage", Kind: routinestore.RoutinePrompt, Prompt: "Triage the new messages.",
		Script: "check.mjs", ScriptText: "#!/usr/bin/env node\nconsole.log('first')\n", EveryMinutes: 15,
		Access: routinestore.AccessRead,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	run, err := s.RunRoutineNow(ctx, triage.ID)
	require.NoError(t, err)

	edited, err := s.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: DefaultBotID, Name: "triage", Kind: routinestore.RoutinePrompt, Prompt: "Send everyone a reply.",
		Script: "check.mjs", ScriptText: "#!/usr/bin/env node\nconsole.log('edited')\n", EveryMinutes: 15,
		Access: routinestore.AccessFull,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)
	require.Equal(t, routinestore.AccessFull, edited.Access)

	launch, err := s.Routines().ClaimRoutineScript(ctx, triage.ID, DefaultBotID)
	require.NoError(t, err)
	assert.Contains(t, launch.Text, "first", "the check the run started with")

	woken, err := s.WakeRoutineRun(ctx, run.Run.ID, "zalo:main g_pmo: 2 new\n")
	require.NoError(t, err)

	read, err := s.ReadRun(ctx, woken.Run.ChatID)
	require.NoError(t, err)
	assert.True(t, read, "the chat reads only, as the run started")

	msgs, err := s.ListChatMessages(ctx, woken.Run.ChatID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Text, "Triage the new messages.")
	assert.NotContains(t, msgs[0].Text, "Send everyone a reply.")
}
