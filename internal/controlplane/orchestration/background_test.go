package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1058): a Bot's background command that ends wakes its chat once, with its exit code, how long it
// ran and the end of its output; a second report of the same end wakes nothing. Since issue #1084 a command is its
// chat's, whichever pi of the chat runs when it ends, and its unit says how long it ran.
func TestABackgroundCommandThatEndsWakesItsChatOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-1", "bg_0123456789ab"))

	running, err := w.s.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Equal(t, []BackgroundCommand{{ChatID: chat.ID, ID: "bg_0123456789ab", StartedAt: t0}}, running)

	w.clock.now = w.clock.now.Add(time.Hour)
	code := 3
	tail := strings.Repeat("x", BackgroundTail-5) + "done\n"
	file := "/home/coordinator/.hypomnema-coordinator/run/" + chat.ID + "/background/bg_0123456789ab.log"
	end := BackgroundEnd{ID: "bg_0123456789ab", ExitCode: &code, Ran: 4*time.Minute + 12*time.Second,
		Output: tail, OutputBytes: 9000, OutputFile: file}

	woke, err := w.s.EndBackground(ctx, chat.ID, end)
	require.NoError(t, err)
	assert.True(t, woke)

	woke, err = w.s.EndBackground(ctx, chat.ID, end)
	require.NoError(t, err)
	assert.False(t, woke, "an end wakes once")

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, ChatSystem, waiting[0].Role)
	assert.Equal(t, ChatWake, waiting[0].Kind)
	assert.Equal(t, "Background command bg_0123456789ab exited with code 3 after 4m12s, the end of its 9000 bytes of "+
		"output:\n"+strings.TrimSpace(tail)+"\nFull output: "+file+"; read only the part you need.", waiting[0].Text)

	running, err = w.s.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Empty(t, running)
}

// Scenario (issue #1058): a command a signal ended, with all its output in the wake, says so and names no file.
func TestABackgroundCommandKilledBySignalSaysSo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-1", "bg_aaaaaa"))

	_, err = w.s.EndBackground(ctx, chat.ID, BackgroundEnd{ID: "bg_aaaaaa", Signal: "SIGTERM"})
	require.NoError(t, err)

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, "Background command bg_aaaaaa was killed by SIGTERM after 0s with no output.", waiting[0].Text)
}

// Scenario (issue #1084): a command whose unit is gone is reported lost in its own chat, once, naming its output;
// the others run on.
func TestABackgroundCommandWhoseUnitIsGoneIsReportedLost(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	other, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-old", "bg_111111"))
	require.NoError(t, w.s.StartBackground(ctx, other.ID, "pi-other", "bg_333333"))

	woke, err := w.s.LoseBackground(ctx, chat.ID, "bg_111111", "its unit is gone", "/run/bg_111111.log")
	require.NoError(t, err)
	assert.True(t, woke)

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, "Background command bg_111111 (started "+t0.UTC().Format(time.RFC3339)+") ended with no "+
		"result: its unit is gone, so its outcome is unknown. If you still need it, check its output and side "+
		"effects, then run it again.\nIts output: /run/bg_111111.log; read only the part you need.", waiting[0].Text)

	woke, err = w.s.EndBackground(ctx, chat.ID, BackgroundEnd{ID: "bg_111111", ExitCode: new(int)})
	require.NoError(t, err)
	assert.False(t, woke, "a command reported lost is not reported again")

	running, err := w.s.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Equal(t, []BackgroundCommand{{ChatID: other.ID, ID: "bg_333333", StartedAt: t0}}, running)
}

// Scenario (issue #1084): a command its chat stopped says so, without the advice of a timeout.
func TestABackgroundCommandItsChatStoppedSaysSo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-1", "bg_eeeeee"))

	w.clock.now = w.clock.now.Add(90 * time.Second)
	_, err = w.s.EndBackground(ctx, chat.ID, BackgroundEnd{
		ID: "bg_eeeeee", Signal: "SIGTERM", Stopped: true, Output: "scanned 40\n", OutputBytes: 11,
	})
	require.NoError(t, err)

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, "Background command bg_eeeeee was stopped, as this chat asked, after 1m30s:\nscanned 40",
		waiting[0].Text)
}

func TestABackgroundReportIsChecked(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.ErrorIs(t, w.s.StartBackground(ctx, chat.ID, "pi", "../etc"), ErrInvalid)

	code := 0
	for name, end := range map[string]BackgroundEnd{
		"no way it ended":   {ID: "bg_aaaaaa"},
		"both ways":         {ID: "bg_aaaaaa", ExitCode: &code, Signal: "SIGKILL"},
		"a signal unnamed":  {ID: "bg_aaaaaa", Signal: "kill -9"},
		"too much output":   {ID: "bg_aaaaaa", ExitCode: &code, Output: strings.Repeat("é", BackgroundTail+1)},
		"a file of 2 lines": {ID: "bg_aaaaaa", ExitCode: &code, OutputFile: "/tmp/a\nb"},
	} {
		_, err := w.s.EndBackground(ctx, chat.ID, end)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

// Scenario (issue #1058, review round 1): a command its deadline ended says so, with OpenClaw's advice on retrying.
func TestABackgroundCommandItsTimeoutEndedSaysSo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-1", "bg_dddddd"))

	w.clock.now = w.clock.now.Add(30*time.Minute + 5*time.Second)
	_, err = w.s.EndBackground(ctx, chat.ID, BackgroundEnd{
		ID: "bg_dddddd", Signal: "SIGKILL", TimedOut: true, Output: "still fetching\n", OutputBytes: 15,
	})
	require.NoError(t, err)

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, "Background command bg_dddddd was killed (SIGKILL) when its timeout ran out after 30m5s:\n"+
		"still fetching\nThe command was terminated, but external side effects may already have completed. Verify "+
		"the resulting state before retrying. Do not automatically rerun commands unless repeating them is safe. Use "+
		"a higher timeout only when the command is known to be safe to retry.", waiting[0].Text)
}
