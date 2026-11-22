package coordinator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario (issue #1058): a pi that stops while its background command runs takes the command with its box; the
// chat is woken saying it was killed, and the pi that follows answers it.
func TestABackgroundCommandWhosePiStoppedIsReportedKilled(t *testing.T) {
	t.Parallel()

	tokens := &fakeTokens{}
	svc, _ := newSupervisorConfig(t, Config{Tokens: tokens})
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	first := "hyp_bot_v1_1_" + chat.ID + "_" + orchestration.DefaultBotID

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what is your token?")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "my token: "+first)

	require.NoError(t, svc.StartBackground(ctx, chat.ID, bearertoken.Hash(first), "bg_aaaaaa"))

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "crash please")
	require.NoError(t, err)

	waitFor(t, svc, chat.ID, orchestration.ChatSystem, "Background command bg_aaaaaa (started ")
	waitFor(t, svc, chat.ID, orchestration.ChatSystem, "was killed: this chat's pi stopped while it ran")
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: Message from the Hypomnema control plane")

	running, err := svc.ChatsWithBackground(ctx)
	require.NoError(t, err)
	assert.Empty(t, running)
}

// Scenario (issue #1058): the commands left from before a restart of the control plane are reported killed as the
// supervisor starts, each in its chat.
func TestBackgroundCommandsLeftByARestartAreReportedKilled(t *testing.T) {
	t.Parallel()

	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, svc.StartBackground(ctx, chat.ID, "an old pi", "bg_bbbbbb"))

	runSupervisor(t, svc, Config{})

	waitFor(t, svc, chat.ID, orchestration.ChatSystem, "bg_bbbbbb")
	waitFor(t, svc, chat.ID, orchestration.ChatSystem, "was killed: the control plane restarted while it ran")
}

// Scenario (issue #1058): an idle pi that runs a background command is not stopped, as its box would end the
// command; once the command ended and woke the chat, an idle pi is stopped as before.
func TestAnIdlePiIsKeptWhileItsBackgroundCommandRuns(t *testing.T) {
	t.Parallel()

	tokens := &fakeTokens{}
	svc, sup := newSupervisorConfig(t, Config{Tokens: tokens, IdleAfter: 50 * time.Millisecond})
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	token := "hyp_bot_v1_1_" + chat.ID + "_" + orchestration.DefaultBotID

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "what is your token?")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "my token: "+token)
	require.NoError(t, svc.StartBackground(ctx, chat.ID, bearertoken.Hash(token), "bg_cccccc"))

	// Each write wakes the supervisor, which stops the pis idle for too long.
	for range 5 {
		time.Sleep(100 * time.Millisecond)

		_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatSystem, "a tool line")
		require.NoError(t, err)
	}

	assert.True(t, sup.Idle(chat.ID), "the pi runs on, idle, while its command runs")

	code := 0
	woke, err := svc.EndBackground(ctx, chat.ID, bearertoken.Hash(token),
		orchestration.BackgroundEnd{ID: "bg_cccccc", ExitCode: &code, Output: "built\n", OutputChars: 6})
	require.NoError(t, err)
	require.True(t, woke)

	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: Message from the Hypomnema control plane")

	require.Eventually(t, func() bool {
		_, err := svc.AddChatMessage(ctx, chat.ID, orchestration.ChatSystem, "a tool line")

		return err == nil && !sup.Idle(chat.ID)
	}, 10*time.Second, 100*time.Millisecond, "an idle pi with no command is stopped")
}
