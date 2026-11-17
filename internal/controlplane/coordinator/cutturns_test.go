package coordinator

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// cutTurn makes a chat whose turn on the owner's message was cut: the message was delivered, and the turn's mark
// started age ago, after attempts continuations, each delivered.
func cutTurn(t *testing.T, svc *orchestration.Service, clock *testClock, age time.Duration, attempts int) string {
	t.Helper()

	ctx := t.Context()

	clock.set(time.Now().Add(-age))
	defer clock.set(time.Time{})

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "write the report")
	require.NoError(t, err)

	for range attempts {
		require.NoError(t, svc.ContinueCutTurn(ctx, chat.ID))
	}

	msgs, err := svc.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.NoError(t, svc.MarkDelivered(ctx, chat.ID, msgs[len(msgs)-1].Seq))
	require.NoError(t, svc.MarkChatTurn(ctx, chat.ID))

	return chat.ID
}

// lockedBuffer is a log's output, read while the log is written.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func marked(t *testing.T, svc *orchestration.Service) []string {
	t.Helper()

	cut, err := svc.CutTurns(t.Context())
	require.NoError(t, err)

	ids := make([]string, 0, len(cut))
	for _, c := range cut {
		ids = append(ids, c.ChatID)
	}

	return ids
}

func transcript(t *testing.T, svc *orchestration.Service, chatID string) []string {
	t.Helper()

	msgs, err := svc.ListChatMessages(t.Context(), chatID, 0, 0)
	require.NoError(t, err)

	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == orchestration.ChatOwner || m.Role == orchestration.ChatSystem {
			out = append(out, fmt.Sprintf("%s/%s: %s", m.Role, m.Kind, m.Text))
		}
	}

	return out
}

// Scenario (issue #716): a turn runs while the control plane stops. Its mark stays; at the next start the chat says
// it continues, pi is woken to go on with the turn, and once that turn settles the mark is gone.
func TestATurnCutByAStopContinuesAtTheNextStart(t *testing.T) {
	t.Parallel()

	svc, _ := newStore(t)
	ctx := t.Context()

	sup, stop := runSupervisor(t, svc, Config{})

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hang settled")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return sup.Working(chat.ID) }, 10*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(marked(t, svc)) == 1 }, 10*time.Second, 10*time.Millisecond,
		"a running turn is marked")
	require.Eventually(t, func() bool {
		u, err := svc.Undelivered(ctx, chat.ID)

		return err == nil && len(u) == 0
	}, 10*time.Second, 10*time.Millisecond, "pi has the message")
	assert.Equal(t, []string{chat.ID}, sup.WorkingChats())

	stop()

	assert.Equal(t, []string{chat.ID}, marked(t, svc), "a turn the stop cut keeps its mark")

	runSupervisor(t, svc, Config{})

	waitFor(t, svc, chat.ID, orchestration.ChatSystem, orchestration.ContinuedText)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator,
		"echo: Message from the Hypomnema control plane: The control plane restarted for an update")
	require.Eventually(t, func() bool { return len(marked(t, svc)) == 0 }, 10*time.Second, 10*time.Millisecond,
		"the continued turn settled and cleared its mark")

	assert.Equal(t, []string{
		"owner/: hang settled", "system/: " + orchestration.ContinuedText,
	}, transcript(t, svc, chat.ID), "the partial turn stays, and one line says the turn continues")
}

// Scenario: a turn that settles, and a pi that dies mid-turn while the control plane runs, leave no mark: nothing is
// continued at the next start.
func TestATurnThatEndedLeavesNoMark(t *testing.T) {
	t.Parallel()

	svc, _ := newStore(t)
	ctx := t.Context()

	sup, stop := runSupervisor(t, svc, Config{})

	answered, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, answered.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)
	waitFor(t, svc, answered.ID, orchestration.ChatCoordinator, "echo: hello")

	died, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, died.ID, orchestration.ChatOwner, "die mid-turn")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		u, err := svc.Undelivered(ctx, died.ID)

		return err == nil && len(u) == 0 && !sup.Working(died.ID) && !sup.Working(answered.ID)
	}, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return len(marked(t, svc)) == 0 }, 10*time.Second, 10*time.Millisecond)

	stop()

	assert.Empty(t, marked(t, svc))
}

// Scenario: at start, a cut turn older than 15 minutes, or one already continued twice, is not continued: its mark
// is cleared and the chat says a restart cut the turn. That line is not given to pi.
func TestAStaleCutTurnIsClearedWithALine(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		age      time.Duration
		attempts int
	}{
		"stale":              {age: 16 * time.Minute},
		"no attempts left":   {age: time.Minute, attempts: 2},
		"stale and attempts": {age: time.Hour, attempts: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, clock := newStore(t)
			chat := cutTurn(t, svc, clock, c.age, c.attempts)

			runSupervisor(t, svc, Config{})

			waitFor(t, svc, chat, orchestration.ChatSystem, orchestration.CutText)
			assert.Empty(t, marked(t, svc))

			undelivered, err := svc.Undelivered(t.Context(), chat)
			require.NoError(t, err)
			assert.Empty(t, undelivered, "pi is not woken")
			got := transcript(t, svc, chat)
			assert.Equal(t, "owner/: write the report", got[0])
			assert.Equal(t, "system/: "+orchestration.CutText, got[len(got)-1])
			assert.Len(t, got, 2+c.attempts, "one line, after those of the earlier continuations")
		})
	}
}

// Scenario: a fresh cut turn with an attempt left is continued, its attempts counted; the turn that continues it
// settles and clears both.
func TestAFreshCutTurnIsContinuedOnceMore(t *testing.T) {
	t.Parallel()

	svc, clock := newStore(t)
	chat := cutTurn(t, svc, clock, 14*time.Minute, 1)

	runSupervisor(t, svc, Config{})

	waitFor(t, svc, chat, orchestration.ChatCoordinator, "echo: Message from the Hypomnema control plane")
	require.Eventually(t, func() bool { return len(marked(t, svc)) == 0 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{
		"owner/: write the report", "system/: " + orchestration.ContinuedText, "system/: " + orchestration.ContinuedText,
	}, transcript(t, svc, chat))
}

// Scenario: the third start in a row, each a few minutes after the last, finds cut turns again: restarts are in a
// loop, so no turn is continued and every mark stays, as Hermes's restart-loop guard.
func TestARestartLoopContinuesNothing(t *testing.T) {
	t.Parallel()

	svc, clock := newStore(t)
	chat := cutTurn(t, svc, clock, time.Minute, 0)
	booted(t, svc, clock, 8*time.Minute, 4*time.Minute)

	log := &lockedBuffer{}
	runSupervisor(t, svc, Config{Logger: slog.New(slog.NewTextHandler(log, nil))})

	require.Eventually(t, func() bool { return strings.Contains(log.String(), "event=coordinator.restart_loop") },
		10*time.Second, 10*time.Millisecond, "the loop is found")
	assert.Contains(t, log.String(), "chained_starts=3")

	assert.Equal(t, []string{"owner/: write the report"}, transcript(t, svc, chat), "nothing is continued")
	assert.Equal(t, []string{chat}, marked(t, svc), "the mark stays")
}

// Scenario: two earlier starts are farther apart than the guard's gap: the chain is broken, and the turn continues.
func TestRestartsFarApartAreNoLoop(t *testing.T) {
	t.Parallel()

	svc, clock := newStore(t)
	chat := cutTurn(t, svc, clock, time.Minute, 0)
	booted(t, svc, clock, 20*time.Minute, 6*time.Minute)

	runSupervisor(t, svc, Config{})

	waitFor(t, svc, chat, orchestration.ChatSystem, orchestration.ContinuedText)
}

// booted records starts that found cut turns, each the given time ago.
func booted(t *testing.T, svc *orchestration.Service, clock *testClock, ago ...time.Duration) {
	t.Helper()

	defer clock.set(time.Time{})

	for _, a := range ago {
		clock.set(time.Now().Add(-a))

		_, err := svc.RecordRestartBoot(t.Context(), restartLoopGap)
		require.NoError(t, err)
	}
}

func TestTheContinuedLineIsGivenAsWhatItAsks(t *testing.T) {
	t.Parallel()

	got := promptText([]orchestration.ChatMessage{
		{Role: orchestration.ChatSystem, Text: orchestration.ContinuedText},
		{Role: orchestration.ChatSystem, Text: "Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json"},
		{Role: orchestration.ChatOwner, Text: "also add docs"},
	})
	assert.Equal(t, fmt.Sprint(
		"Message from the Hypomnema control plane: ", continuedPrompt, "\n",
		"\nMessage from the Hypomnema control plane: these need you.\n",
		"- Task t1 of run r1 is succeeded: hypomnema tasks show t1 --json\n",
		"Act on them, then end your turn; the control plane wakes you again.\n",
		"\nThe owner also wrote:\nalso add docs"), got)
}
