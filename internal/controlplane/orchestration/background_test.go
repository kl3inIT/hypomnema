package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1058): a Bot's background command that ends wakes its chat once, with its exit code, how long it
// ran and the end of its output; a second report of the same end, or one from another pi, wakes nothing.
func TestABackgroundCommandThatEndsWakesItsChatOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-1", "bg_0123456789ab"))

	running, err := w.s.ChatsWithBackground(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{chat.ID}, running, "its pi is not stopped while it runs")

	w.clock.now = w.clock.now.Add(4*time.Minute + 12*time.Second)
	code := 3
	tail := strings.Repeat("x", BackgroundTail-5) + "done\n"
	end := BackgroundEnd{ID: "bg_0123456789ab", ExitCode: &code, Output: tail, OutputChars: 9000,
		OutputFile: "/tmp/pi-background-bg_0123456789ab.log"}

	woke, err := w.s.EndBackground(ctx, chat.ID, "pi-2", end)
	require.NoError(t, err)
	assert.False(t, woke, "another pi cannot end it")

	woke, err = w.s.EndBackground(ctx, chat.ID, "pi-1", end)
	require.NoError(t, err)
	assert.True(t, woke)

	woke, err = w.s.EndBackground(ctx, chat.ID, "pi-1", end)
	require.NoError(t, err)
	assert.False(t, woke, "an end wakes once")

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, ChatSystem, waiting[0].Role)
	assert.Equal(t, ChatWake, waiting[0].Kind)
	assert.Equal(t, "Background command bg_0123456789ab exited with code 3 after 4m12s, the last 2000 of 9000 output "+
		"characters:\n"+strings.TrimSpace(tail)+"\nFull output: /tmp/pi-background-bg_0123456789ab.log; read only "+
		"the part you need.", waiting[0].Text)

	running, err = w.s.ChatsWithBackground(ctx)
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

	_, err = w.s.EndBackground(ctx, chat.ID, "pi-1", BackgroundEnd{ID: "bg_aaaaaa", Signal: "SIGTERM", OutputChars: 0})
	require.NoError(t, err)

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, "Background command bg_aaaaaa was killed by SIGTERM after 0s with no output.", waiting[0].Text)
}

// Scenario (issue #1058): the commands a pi had when it stopped are reported killed, its own only; at a start of the
// control plane every one left is.
func TestTheBackgroundCommandsOfAStoppedPiAreReportedKilled(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	other, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-old", "bg_111111"))
	require.NoError(t, w.s.StartBackground(ctx, chat.ID, "pi-new", "bg_222222"))
	require.NoError(t, w.s.StartBackground(ctx, other.ID, "pi-other", "bg_333333"))

	n, err := w.s.LoseBackground(ctx, chat.ID, "pi-old", "this chat's pi stopped")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	waiting, err := w.s.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Equal(t, "Background command bg_111111 (started "+t0.UTC().Format(time.RFC3339)+") was killed: this chat's "+
		"pi stopped while it ran, so its result will not arrive. If you still need it, check its output and side "+
		"effects, then run it again.", waiting[0].Text)

	woke, err := w.s.EndBackground(ctx, chat.ID, "pi-old", BackgroundEnd{ID: "bg_111111", ExitCode: new(int)})
	require.NoError(t, err)
	assert.False(t, woke, "a command reported lost is not reported again")

	n, err = w.s.LoseBackground(ctx, "", "", "the control plane restarted")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "at a start every command left is lost")

	waiting, err = w.s.Undelivered(ctx, other.ID)
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	assert.Contains(t, waiting[0].Text, "bg_333333")
	assert.Contains(t, waiting[0].Text, "was killed: the control plane restarted while it ran")
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
		_, err := w.s.EndBackground(ctx, chat.ID, "pi", end)
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
	_, err = w.s.EndBackground(ctx, chat.ID, "pi-1", BackgroundEnd{
		ID: "bg_dddddd", Signal: "SIGKILL", TimedOut: true, Output: "still fetching\n", OutputChars: 15,
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
