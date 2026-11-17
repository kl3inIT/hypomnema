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
// started age ago, after attempts continuations, each delivered. pending leaves the last continuation as an earlier
// start left it when it stopped before pi had its line: the line not delivered, the mark no pi's.
func cutTurn(t *testing.T, svc *orchestration.Service, clock *testClock, age time.Duration, attempts int,
	pending bool,
) string {
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

	if pending {
		require.Positive(t, attempts)

		upto := int64(0)
		if len(msgs) > 1 {
			upto = msgs[len(msgs)-2].Seq
		}

		require.NoError(t, svc.MarkDelivered(ctx, chat.ID, upto))

		return chat.ID
	}

	require.NoError(t, svc.MarkDelivered(ctx, chat.ID, msgs[len(msgs)-1].Seq))
	require.NoError(t, svc.MarkChatTurn(ctx, chat.ID, "pi-of-the-cut-turn"))

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
		"owner/: hang settled", "system/continued: " + orchestration.ContinuedText,
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
			chat := cutTurn(t, svc, clock, c.age, c.attempts, false)

			runSupervisor(t, svc, Config{})

			waitFor(t, svc, chat, orchestration.ChatSystem, orchestration.CutText)
			assert.Empty(t, marked(t, svc))

			undelivered, err := svc.Undelivered(t.Context(), chat)
			require.NoError(t, err)
			assert.Empty(t, undelivered, "pi is not woken")
			got := transcript(t, svc, chat)
			assert.Equal(t, "owner/: write the report", got[0])
			assert.Equal(t, "system/cut: "+orchestration.CutText, got[len(got)-1])
			assert.Len(t, got, 2+c.attempts, "one line, after those of the earlier continuations")
		})
	}
}

// Scenario: a fresh cut turn with an attempt left is continued, its attempts counted; the turn that continues it
// settles and clears both.
func TestAFreshCutTurnIsContinuedOnceMore(t *testing.T) {
	t.Parallel()

	svc, clock := newStore(t)
	chat := cutTurn(t, svc, clock, 14*time.Minute, 1, false)

	runSupervisor(t, svc, Config{})

	waitFor(t, svc, chat, orchestration.ChatCoordinator, "echo: Message from the Hypomnema control plane")
	require.Eventually(t, func() bool { return len(marked(t, svc)) == 0 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{
		"owner/: write the report",
		"system/continued: " + orchestration.ContinuedText,
		"system/continued: " + orchestration.ContinuedText,
	}, transcript(t, svc, chat))
}

// Scenario: the third start in a row, each a few minutes after the last, finds cut turns again: restarts are in a
// loop, so no turn is continued and every mark stays, as Hermes's restart-loop guard.
func TestARestartLoopContinuesNothing(t *testing.T) {
	t.Parallel()

	svc, clock := newStore(t)
	chat := cutTurn(t, svc, clock, time.Minute, 0, false)
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
	chat := cutTurn(t, svc, clock, time.Minute, 0, false)
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
		{Role: orchestration.ChatSystem, Kind: orchestration.ChatContinued, Text: orchestration.ContinuedText},
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

// Scenario (review of #725, finding 1): an earlier start wrote the line that wakes pi to continue a cut turn and
// stopped before pi had it. When this start does not continue the turn, because the mark is stale, out of attempts
// or restarts are in a loop, that line is not given to pi: no pi starts.
func TestALineAnEarlierStartLeftIsNotGivenWhenTheTurnIsNotContinued(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		age      time.Duration
		attempts int
		loop     bool
	}{
		"stale":            {age: 16 * time.Minute, attempts: 1},
		"no attempts left": {age: time.Minute, attempts: 2},
		"restart loop":     {age: time.Minute, attempts: 1, loop: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, clock := newStore(t)
			chat := cutTurn(t, svc, clock, c.age, c.attempts, true)

			if c.loop {
				booted(t, svc, clock, 8*time.Minute, 4*time.Minute)
			}

			log := &lockedBuffer{}
			sup, _ := runSupervisor(t, svc, Config{Logger: slog.New(slog.NewTextHandler(log, nil))})

			if c.loop {
				require.Eventually(t, func() bool {
					return strings.Contains(log.String(), "event=coordinator.restart_loop")
				}, 10*time.Second, 10*time.Millisecond)
			} else {
				waitFor(t, svc, chat, orchestration.ChatSystem, orchestration.CutText)
			}

			require.Eventually(t, func() bool {
				u, err := svc.Undelivered(t.Context(), chat)

				return err == nil && len(u) == 0
			}, 10*time.Second, 10*time.Millisecond, "the earlier line is not for pi any more")

			// Two passes later, still no pi.
			<-time.After(2*tick + time.Second)

			assert.NotContains(t, log.String(), "event=coordinator.started", "no pi starts")
			assert.False(t, sup.Working(chat))
		})
	}
}

// Scenario (review of #725, finding 3): the chat's pi exits mid-turn and the next pi of the chat has begun a turn
// before the first is cleaned up. The first clears only its own mark: the new turn's stays.
func TestAnOldPiClearsOnlyItsOwnMark(t *testing.T) {
	t.Parallel()

	svc, _ := newStore(t)
	ctx := t.Context()
	sup := New(svc, Config{})

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	old, next := &proc{s: sup, chatID: chat.ID, token: "old"}, &proc{s: sup, chatID: chat.ID, token: "next"}

	sup.turnStarted(ctx, old)
	sup.turnStarted(ctx, next)
	sup.exitedMidTurn(ctx, old)

	assert.Equal(t, []string{chat.ID}, marked(t, svc), "the next pi's turn keeps its mark")

	sup.turnSettled(ctx, next)

	assert.Empty(t, marked(t, svc))
}

// Scenario (review of #725, finding 4): while the control plane is updated, no pi starts and an idle pi is given
// no new turn; what is written waits and goes once the update ended.
func TestNoPiStartsWhileTheControlPlaneIsUpdated(t *testing.T) {
	t.Parallel()

	var updating sync.Mutex

	held := true
	svc, _ := newStore(t)
	ctx := t.Context()

	log := &lockedBuffer{}
	sup, _ := runSupervisor(t, svc, Config{
		Logger: slog.New(slog.NewTextHandler(log, nil)),
		Updating: func() bool {
			updating.Lock()
			defer updating.Unlock()

			return held
		},
	})

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)

	// Two passes later, still no pi.
	<-time.After(2*tick + time.Second)

	assert.NotContains(t, log.String(), "event=coordinator.started", "no pi starts")

	undelivered, err := svc.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Len(t, undelivered, 1, "the message waits")
	require.ErrorIs(t, sup.Compact(ctx, chat.ID), orchestration.ErrConflict, "no compaction either")

	hold := func(on bool) {
		updating.Lock()
		held = on
		updating.Unlock()
	}

	hold(false)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: hello")
	require.Eventually(t, func() bool { return sup.Idle(chat.ID) }, 10*time.Second, 10*time.Millisecond)

	// A pi that runs and is idle is given no new turn either.
	hold(true)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "again")
	require.NoError(t, err)

	<-time.After(2*tick + time.Second)

	undelivered, err = svc.Undelivered(ctx, chat.ID)
	require.NoError(t, err)
	assert.Len(t, undelivered, 1, "the message waits")

	hold(false)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: again")
}
