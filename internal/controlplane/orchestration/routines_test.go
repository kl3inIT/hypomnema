package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Routine scenarios (issue #211). Time is the test's clock: nothing sleeps.

const syncScript = "#!/bin/sh\nhypomnema superset chart 9 164 --format csv | lark-cli base rows put\n"

var owner = RoutineAuthor{}

func hourly(name string) RoutineSpec {
	return RoutineSpec{
		Bot: DefaultBot, Name: name, Kind: RoutineScript, Script: "sync.sh", ScriptText: syncScript, EveryMinutes: 60,
		Summary: "the usage figures go to the Lark Base", Announce: true,
	}
}

func daily(name string) RoutineSpec {
	return RoutineSpec{
		Bot: DefaultBot, Name: name, Kind: RoutinePrompt, Prompt: "Summarize what changed overnight.",
		EveryMinutes: 1440, Announce: true,
	}
}

func (w *world) routine(n RoutineSpec) Routine {
	w.t.Helper()

	r, err := w.s.PutRoutine(w.t.Context(), n, owner)
	require.NoError(w.t, err)

	return r
}

func (w *world) theRoutine(id string) Routine {
	w.t.Helper()

	r, err := w.s.GetRoutine(w.t.Context(), id)
	require.NoError(w.t, err)

	return r
}

// tick is one pass of the schedule: the runs that began.
func (w *world) tick() []StartedRun {
	w.t.Helper()

	started, more, err := w.s.ClaimDueRoutines(w.t.Context(), 0)
	require.NoError(w.t, err)
	require.False(w.t, more)

	return started
}

func (w *world) end(runID string, r RunResult) Settled {
	w.t.Helper()

	settled, err := w.s.FinishRoutineRun(w.t.Context(), runID, r)
	require.NoError(w.t, err)

	return settled
}

func (w *world) runs(routineID string) []RoutineRun {
	w.t.Helper()

	runs, err := w.s.ListRoutineRuns(w.t.Context(), routineID, 0)
	require.NoError(w.t, err)

	return runs
}

func (w *world) after(d time.Duration) { w.clock.now = w.clock.now.Add(d) }

func failed(reason string) RunResult { return RunResult{State: RunFailed, Error: reason} }
func passing(reason string) RunResult {
	return RunResult{State: RunFailed, Error: reason, Passing: true}
}

var succeeded = RunResult{State: RunSucceeded}

// What a Routine may be: a prompt or a script that is a file's name, every 5
// to 1440 minutes or by a 5-field cron expression with its time zone. A
// script's limit has no upper bound.
func TestWhatARoutineMayBe(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	cron := func(expr, zone string) func(*RoutineSpec) {
		return func(n *RoutineSpec) { n.EveryMinutes, n.Cron, n.Timezone = 0, expr, zone }
	}

	for name, tc := range map[string]struct {
		change func(*RoutineSpec)
		want   string
	}{
		"a name with capitals":      {func(n *RoutineSpec) { n.Name = "Sync" }, "lowercase"},
		"an unknown bot":            {func(n *RoutineSpec) { n.Bot = "nobody" }, "not found"},
		"every four minutes":        {func(n *RoutineSpec) { n.EveryMinutes = 4 }, "every 5 to 1440 minutes"},
		"every two days":            {func(n *RoutineSpec) { n.EveryMinutes = 2880 }, "every 5 to 1440 minutes"},
		"no schedule":               {func(n *RoutineSpec) { n.EveryMinutes = 0 }, "every 5 to 1440 minutes"},
		"an interval and a cron":    {func(n *RoutineSpec) { n.Cron, n.Timezone = "0 7 * * *", "UTC" }, "not both"},
		"a cron without its zone":   {cron("0 7 * * *", ""), "needs its time zone"},
		"a zone that is not one":    {cron("0 7 * * *", "Mars/Base"), "IANA"},
		"the server's own zone":     {cron("0 7 * * *", "Local"), "IANA"},
		"a cron with seconds":       {cron("0 0 7 * * *", "UTC"), "five fields"},
		"a cron word":               {cron("@every 1s", "UTC"), "five fields"},
		"a cron field out of range": {cron("61 7 * * *", "UTC"), "cron expression"},
		"a zone inside the cron":    {cron("TZ=UTC 0 7 * *", "UTC"), "five fields"},
		"a command line":            {func(n *RoutineSpec) { n.Script = "sh -c 'rm -rf /'" }, "never a path or a command line"},
		"a path":                    {func(n *RoutineSpec) { n.Script = "../../.ssh/authorized_keys" }, "never a path"},
		"a script with no text":     {func(n *RoutineSpec) { n.ScriptText = " \n" }, "needs the script's text"},
		"a prompt and a script":     {func(n *RoutineSpec) { n.Prompt = "also this" }, "not both"},
		"a limit below zero":        {func(n *RoutineSpec) { n.TimeoutSeconds = -1 }, "a number of seconds"},
		"neither kind":              {func(n *RoutineSpec) { n.Kind = "command" }, "a prompt or a script"},
		"a prompt with no prompt":   {func(n *RoutineSpec) { *n = daily("p"); n.Prompt = " " }, "needs its prompt"},
		"a prompt with a limit":     {func(n *RoutineSpec) { *n = daily("p"); n.TimeoutSeconds = 60 }, "only a script's limit"},
		"a chat that is not there":  {func(n *RoutineSpec) { n.ChatID = "cht_nothing" }, "not found"},
	} {
		spec := hourly("sync")
		tc.change(&spec)

		_, err := w.s.PutRoutine(t.Context(), spec, owner)
		require.ErrorContains(t, err, tc.want, name)
	}

	all, err := w.s.ListRoutines(t.Context(), RoutineFilter{})
	require.NoError(t, err)
	assert.Empty(t, all, "a refused routine is not kept")

	sync := w.routine(hourly("sync"))
	assert.Equal(t, RoutineOn, sync.State, "a routine is on at once")
	assert.Equal(t, ScriptTimeout, sync.Timeout, "a script stops after ten minutes unless its routine says otherwise")
	assert.Equal(t, t0.Add(time.Hour), *sync.NextRunAt)
	assert.Equal(t, "Every hour, coordinator runs the script sync.sh: the usage figures go to the Lark Base.",
		RoutineSentence(sync))

	long := hourly("long")
	long.TimeoutSeconds = 6 * 3600
	assert.Equal(t, 6*time.Hour, w.routine(long).Timeout, "a script's limit has no upper bound")

	brief := daily("brief")
	brief.EveryMinutes, brief.Cron, brief.Timezone = 0, "0  7 * * 1-5", "Asia/Ho_Chi_Minh"
	made := w.routine(brief)
	assert.Equal(t, PromptTimeout, made.Timeout)
	// t0 is Saturday 09:00 UTC, 16:00 in Ho Chi Minh City: the next weekday 07:00 there is Monday, 00:00 UTC.
	assert.Equal(t, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), *made.NextRunAt)
	assert.Equal(t, "every weekday at 7:00 Ho Chi Minh time", ScheduleWords(made))

	err = w.s.DeleteBot(t.Context(), DefaultBot)
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: an hourly script Routine. Before its time nothing starts. At its
// time one run starts and the next time is set in the same step, so a second
// look starts nothing. A time that comes while that run still goes is
// skipped and counted on the Routine, with no run of its own, and a run by
// hand then answers that one is running. After the control plane was down
// for five hours the Routine runs once, and its next time is on its grid.
func TestARoutineRunsOnceWhenItsTimeComes(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.routine(hourly("sync"))

	w.after(59 * time.Minute)
	assert.Empty(t, w.tick(), "not yet")

	w.after(time.Minute)

	started := w.tick()
	require.Len(t, started, 1)
	first := started[0].Run
	assert.Equal(t, RunRunning, first.State)
	assert.Equal(t, StartedBySchedule, first.StartedBy)
	assert.Empty(t, first.ChatID, "a script run has no chat")
	assert.Equal(t, t0.Add(2*time.Hour), *started[0].Routine.NextRunAt, "the next time is set before the run starts")
	assert.Empty(t, w.tick(), "a second look at the same minute starts nothing")

	w.after(time.Hour + 3*time.Minute)
	assert.Empty(t, w.tick(), "the run before still goes")

	skipped := w.theRoutine(sync.ID)
	assert.Equal(t, 1, skipped.Skipped, "the time is counted on the routine")
	assert.Equal(t, t0.Add(2*time.Hour), *skipped.LastSkippedAt)
	assert.Equal(t, t0.Add(3*time.Hour), *skipped.NextRunAt)
	assert.Len(t, w.runs(sync.ID), 1, "and has no run of its own")

	_, err := w.s.RunRoutineNow(t.Context(), sync.ID)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorContains(t, err, first.ID+" of the routine is still running")
	assert.Len(t, w.runs(sync.ID), 1)

	settled := w.end(first.ID, succeeded)
	assert.Equal(t, RunSucceeded, settled.Run.State)
	assert.False(t, settled.Alert)

	_, err = w.s.FinishRoutineRun(t.Context(), first.ID, failed("again"))
	require.ErrorIs(t, err, ErrConflict, "a run ends once")

	// Down from 11:03 to 16:13: the times at 12, 13, 14, 15 and 16 were missed.
	w.after(5*time.Hour + 10*time.Minute)

	started = w.tick()
	require.Len(t, started, 1, "overdue runs once; the missed times are not replayed")
	assert.Equal(t, t0.Add(8*time.Hour), *started[0].Routine.NextRunAt, "and the next time is on the grid")
	assert.Empty(t, w.tick())
	assert.Equal(t, 1, w.theRoutine(sync.ID).Skipped, "a time missed while nothing ran is not a skip")
}

// When the control plane starts with many Routines overdue, a limited
// number begin and the rest are still due for whoever asks next.
func TestOverdueRoutinesBeginAFewAtATime(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	for _, name := range []string{"a", "b", "c", "d"} {
		w.routine(hourly(name))
	}

	w.after(3 * time.Hour)

	started, more, err := w.s.ClaimDueRoutines(t.Context(), 3)
	require.NoError(t, err)
	assert.Len(t, started, 3)
	assert.True(t, more, "one is still due")

	started, more, err = w.s.ClaimDueRoutines(t.Context(), 1)
	require.NoError(t, err)
	assert.Len(t, started, 1)
	assert.False(t, more)
}

// The next run after a failure, for an hourly Routine: the later of the
// schedule's next time and the run's end plus 30 s, 60 s, 5 min, 15 min and
// 60 min by the failures in a row. Only a passing error is tried again
// earlier than the schedule's next time, three times in a row. The interval
// stays on its grid.
func TestTheNextRunAfterAFailure(t *testing.T) {
	t.Parallel()

	t.Run("a script's own failure waits for the schedule", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		sync := w.routine(hourly("sync"))

		for i := 1; i <= 4; i++ {
			w.after(time.Hour)

			started := w.tick()
			require.Len(t, started, 1, "failure %d", i)
			w.after(2 * time.Minute)

			settled := w.end(started[0].Run.ID, failed("the script exited with code 1"))
			assert.Equal(t, i, settled.Routine.Failures)
			assert.Equal(t, t0.Add(time.Duration(i+1)*time.Hour), *settled.Routine.NextRunAt,
				"failure %d: the schedule's next time is later than the backoff", i)

			w.after(-2 * time.Minute)
		}

		// The fifth failure asks for an hour after the run's end, which is past the schedule's next time.
		w.after(time.Hour)

		started := w.tick()
		require.Len(t, started, 1)
		w.after(2 * time.Minute)

		settled := w.end(started[0].Run.ID, failed("the script exited with code 1"))
		assert.Equal(t, w.clock.now.Add(time.Hour), *settled.Routine.NextRunAt, "the backoff is later")

		w.after(time.Hour)

		started = w.tick()
		require.Len(t, started, 1)
		assert.Equal(t, t0.Add(7*time.Hour), *started[0].Routine.NextRunAt, "the interval is back on its grid")
		assert.Equal(t, sync.ID, started[0].Routine.ID)
	})

	t.Run("a passing error is tried again early, three times", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		w.routine(hourly("sync"))
		w.after(time.Hour)

		for i, wait := range []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute} {
			started := w.tick()
			require.Len(t, started, 1, "try %d", i+1)

			settled := w.end(started[0].Run.ID, passing("429 too many requests"))
			assert.Equal(t, w.clock.now.Add(wait), *settled.Routine.NextRunAt, "the wait after passing failure %d", i+1)

			w.after(wait - time.Second)
			assert.Empty(t, w.tick(), "not before the wait is over")
			w.after(time.Second)
		}

		started := w.tick()
		require.Len(t, started, 1)

		settled := w.end(started[0].Run.ID, passing("429 too many requests"))
		assert.Equal(t, 4, settled.Routine.Failures)
		assert.Equal(t, t0.Add(2*time.Hour), *settled.Routine.NextRunAt,
			"the fourth in a row waits for the schedule: 15 minutes after its end is earlier")

		w.after(time.Hour)

		again := w.tick()
		require.Len(t, again, 1)
		assert.Zero(t, w.end(again[0].Run.ID, succeeded).Routine.Failures, "a success resets the count")
	})

	t.Run("a cron schedule takes its first time that is not before the backoff", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		spec := hourly("often")
		spec.EveryMinutes, spec.Cron, spec.Timezone = 0, "*/5 * * * *", "UTC"
		often := w.routine(spec)

		// Four failures before: the fifth asks for an hour.
		_, err := w.db.ExecContext(t.Context(), "UPDATE routines SET failures = 4 WHERE id = ?", often.ID)
		require.NoError(t, err)

		w.after(5 * time.Minute)

		started := w.tick()
		require.Len(t, started, 1)
		w.after(90 * time.Second)

		settled := w.end(started[0].Run.ID, failed("the script exited with code 1"))
		assert.Equal(t, t0.Add(70*time.Minute), *settled.Routine.NextRunAt,
			"an hour after 09:06:30 is 10:06:30; the cron's next time from there is 10:10")
	})
}

// Scenario: a Routine keeps failing. A failed run by hand counts as one by
// the schedule. After the second failure in a row the owner is to be told,
// and again only an hour later. After the tenth the Routine is paused with
// the reason. A canceled run and a run a restart interrupted are recorded
// and not counted; an interrupted prompt is run again after the backoff, a
// script is not. Resumed, it starts clean.
func TestARoutineThatKeepsFailingIsToldOfAndPaused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.routine(hourly("sync"))

	byHand := func(r RunResult) Settled {
		run, err := w.s.RunRoutineNow(t.Context(), sync.ID)
		require.NoError(t, err)

		return w.end(run.Run.ID, r)
	}

	first := byHand(failed("boom"))
	assert.Equal(t, 1, first.Routine.Failures, "a failed run by hand counts")
	assert.False(t, first.Alert, "one failure tells nobody")
	assert.Equal(t, t0.Add(time.Hour), *first.Routine.NextRunAt)

	assert.Equal(t, 1, byHand(RunResult{State: RunStopped, Error: "the owner rejected it"}).Routine.Failures,
		"a canceled run is not counted")
	assert.Equal(t, 1, byHand(RunResult{State: RunFailed, Interrupted: true, Error: "restarted"}).Routine.Failures,
		"nor is one a restart interrupted")
	assert.Equal(t, t0.Add(time.Hour), *w.theRoutine(sync.ID).NextRunAt, "an interrupted script waits for its schedule")

	assert.True(t, byHand(failed("boom")).Alert, "the second failure in a row tells the owner")

	w.after(59 * time.Minute)
	assert.False(t, byHand(failed("boom")).Alert, "not again within the hour")

	w.after(2 * time.Minute)
	assert.True(t, byHand(failed("boom")).Alert, "an hour later he is told again")

	var last Settled
	for range RoutinePauseAfter - 4 {
		last = byHand(failed("the script exited with code 1"))
	}

	assert.True(t, last.Paused)
	assert.Equal(t, RoutinePaused, last.Routine.State)
	assert.Nil(t, last.Routine.NextRunAt)
	assert.Equal(t, "paused after 10 failed runs in a row; the last: the script exited with code 1",
		last.Routine.PausedReason)

	w.after(24 * time.Hour)
	assert.Empty(t, w.tick(), "a paused routine does not run")

	resumed, err := w.s.ResumeRoutine(t.Context(), sync.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutineOn, resumed.State)
	assert.Zero(t, resumed.Failures)
	assert.Empty(t, resumed.PausedReason)
	assert.True(t, resumed.NextRunAt.After(w.clock.now))

	byHand(failed("boom"))
	assert.True(t, byHand(failed("boom")).Alert, "a new streak tells him at its second failure")

	paused, err := w.s.PauseRoutine(t.Context(), sync.ID)
	require.NoError(t, err)
	assert.Equal(t, "paused by the owner", paused.PausedReason)
	assert.Nil(t, paused.NextRunAt)

	// A prompt run a restart interrupted is run again after the backoff of its streak.
	brief := w.routine(daily("brief"))

	run, err := w.s.RunRoutineNow(t.Context(), brief.ID)
	require.NoError(t, err)

	again := w.end(run.Run.ID, RunResult{State: RunFailed, Interrupted: true, Error: "restarted"})
	assert.Zero(t, again.Routine.Failures)
	assert.Equal(t, w.clock.now.Add(30*time.Second), *again.Routine.NextRunAt)
}

// Scenario: a Bot makes a Routine from its chat with the owner's command. It
// is on at once and can be undone: the owner's undo removes it, with the run
// of it that was live. A Bot's change of a Routine is undone to what it was
// before; once the owner changed it too, the undo is refused. A Bot needs a
// chat it talks in, and a Routine's own run makes and changes none.
func TestABotMakesARoutineAndTheOwnerUndoesIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	bot := RoutineAuthor{Bot: true}

	chat, err := w.s.CreateChat(ctx, "", "The usage figures")
	require.NoError(t, err)

	_, err = w.s.PutBot(ctx, Bot{Name: "vmap"})
	require.NoError(t, err)

	spec := hourly("sync")
	spec.ChatID = chat.ID

	for name, tc := range map[string]struct {
		change func(*RoutineSpec)
		by     RoutineAuthor
		want   string
	}{
		"from no chat":          {func(n *RoutineSpec) { n.ChatID = "" }, bot, "from the chat it works in"},
		"for another bot":       {func(n *RoutineSpec) { n.Bot = "vmap" }, bot, "talks with bot coordinator, not vmap"},
		"from a script's run":   {func(*RoutineSpec) {}, RoutineAuthor{Bot: true, FromRun: true}, "a routine's run cannot make"},
		"the owner, from a run": {func(*RoutineSpec) {}, RoutineAuthor{FromRun: true}, "a routine's run cannot make"},
	} {
		n := spec
		tc.change(&n)

		_, err := w.s.PutRoutine(ctx, n, tc.by)
		require.ErrorContains(t, err, tc.want, name)
	}

	made, err := w.s.PutRoutine(ctx, spec, bot)
	require.NoError(t, err)
	assert.Equal(t, RoutineOn, made.State, "it is on at once")
	assert.Equal(t, "bot", made.CreatedBy)
	assert.True(t, made.CanUndo)
	assert.Equal(t, w.clock.now.Add(time.Hour), *made.NextRunAt)

	run, err := w.s.RunRoutineNow(ctx, made.ID)
	require.NoError(t, err)

	_, removed, live, err := w.s.UndoRoutine(ctx, made.ID)
	require.NoError(t, err)
	assert.True(t, removed, "a routine the bot made is removed")
	require.NotNil(t, live)
	assert.Equal(t, run.Run.ID, live.ID, "with the run that is to be stopped")

	_, err = w.s.GetRoutine(ctx, made.ID)
	require.ErrorIs(t, err, ErrNotFound)

	// The owner's Routine, which the Bot changes.
	mine := w.routine(hourly("mine"))
	assert.False(t, mine.CanUndo)

	_, _, _, err = w.s.UndoRoutine(ctx, mine.ID)
	require.ErrorContains(t, err, "was not a bot's")

	w.after(10 * time.Minute)

	changed := hourly("mine")
	changed.ChatID, changed.EveryMinutes, changed.ScriptText = chat.ID, 30, "#!/bin/sh\necho changed\n"

	after, err := w.s.PutRoutine(ctx, changed, bot)
	require.NoError(t, err)
	assert.Equal(t, mine.ID, after.ID)
	assert.Equal(t, "owner", after.CreatedBy)
	assert.True(t, after.CanUndo)
	assert.Equal(t, 2, after.Revision, "what runs changed")

	back, removed, _, err := w.s.UndoRoutine(ctx, mine.ID)
	require.NoError(t, err)
	assert.False(t, removed)
	assert.Equal(t, 60, back.EveryMinutes)
	assert.Equal(t, syncScript, back.ScriptText)
	assert.Empty(t, back.ChatID)
	assert.False(t, back.CanUndo, "an undo is done once")
	assert.Equal(t, 3, back.Revision, "undoing is a change of what runs too")

	_, err = w.s.PutRoutine(ctx, changed, bot)
	require.NoError(t, err)
	w.after(time.Minute)

	_, err = w.s.PauseRoutine(ctx, mine.ID)
	require.NoError(t, err)

	_, _, _, err = w.s.UndoRoutine(ctx, mine.ID)
	require.ErrorContains(t, err, "was not a bot's",
		"the owner's change came after: there is nothing of the bot's to undo")

	// A prompt Routine's run has a chat of its own, and the Bot cannot make Routines from it.
	brief := w.routine(daily("brief"))

	started, err := w.s.RunRoutineNow(ctx, brief.ID)
	require.NoError(t, err)
	require.NotEmpty(t, started.Run.ChatID)

	runChat, err := w.s.GetChat(ctx, started.Run.ChatID)
	require.NoError(t, err)
	assert.Equal(t, "Routine brief", runChat.Title)

	prompt, err := w.s.Undelivered(ctx, started.Run.ChatID)
	require.NoError(t, err)
	require.Len(t, prompt, 1, "the prompt waits for the Bot in the run's chat")
	assert.Contains(t, prompt[0].Text, "Summarize what changed overnight.")
	assert.Contains(t, prompt[0].Text, "by hand, now")

	fromRun := hourly("another")
	fromRun.ChatID = started.Run.ChatID

	_, err = w.s.PutRoutine(ctx, fromRun, bot)
	require.ErrorContains(t, err, "a routine's run cannot make or change routines")

	// The Bot's failed run keeps the undo: a run is no change of the Routine.
	fresh, err := w.s.PutRoutine(ctx, func() RoutineSpec { n := hourly("fresh"); n.ChatID = chat.ID; return n }(), bot)
	require.NoError(t, err)

	once, err := w.s.RunRoutineNow(ctx, fresh.ID)
	require.NoError(t, err)
	w.after(time.Minute)
	assert.True(t, w.end(once.Run.ID, failed("boom")).Routine.CanUndo)
}

// Scenario: a script Routine's run asks for something the owner must
// approve. The launcher of the run is given the run's key, once; a request
// that presents it is the run's, and the run waits for the owner while its
// call does. Naming the Routine is not enough: a request without the key is
// nobody's run's. The owner answers "always for this Routine": the run goes
// on, and the action passes in the Routine's later runs without asking, in
// no other Routine's. A change of the script ends the grant; so does the
// owner taking it back.
func TestAlwaysForThisRoutine(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	sync := w.routine(hourly("sync"))
	other := w.routine(hourly("other"))

	write := func(key, digest string) NewApproval {
		return NewApproval{
			Integration: "lark", Label: "work", Action: "lark.base.write", ActionName: "Change a Base",
			RunKey: key, AskedBy: "pi", Title: "Change a Base: usage", MatchHash: digest,
		}
	}

	_, err := w.s.ClaimRoutineScript(ctx, sync.ID, DefaultBot)
	require.ErrorContains(t, err, "no run of routine", "nothing is handed out while no run is starting")

	first, err := w.s.RunRoutineNow(ctx, sync.ID)
	require.NoError(t, err)

	_, err = w.s.ClaimRoutineScript(ctx, sync.ID, "vmap")
	require.ErrorContains(t, err, "is not bot vmap's")

	launch, err := w.s.ClaimRoutineScript(ctx, sync.ID, DefaultBot)
	require.NoError(t, err)
	assert.Equal(t, "sync.sh", launch.Script)
	assert.Equal(t, syncScript, launch.Text)
	assert.Equal(t, ScriptTimeout, launch.Timeout)
	require.NotEmpty(t, launch.Key)

	_, err = w.s.ClaimRoutineScript(ctx, sync.ID, DefaultBot)
	require.ErrorIs(t, err, ErrConflict, "a run's key is given once")

	// A request that names no key, or a key that is none, is nobody's run's.
	for _, key := range []string{"", sync.ID, "hrk_made_up"} {
		stray, granted, err := w.s.AskApproval(ctx, write(key, "stray-"+key))
		require.NoError(t, err)
		require.False(t, granted)
		assert.Empty(t, stray.RoutineID, "key %q", key)

		_, err = w.s.DecideApproval(ctx, stray.ID, true, ScopeRoutine)
		require.ErrorContains(t, err, "not asked in a routine's run")

		_, err = w.s.DecideApproval(ctx, stray.ID, false, "")
		require.NoError(t, err)
	}

	asked, granted, err := w.s.AskApproval(ctx, write(launch.Key, "rows-1"))
	require.NoError(t, err)
	require.False(t, granted)
	assert.Equal(t, sync.ID, asked.RoutineID)
	assert.Equal(t, first.Run.ID, asked.RoutineRunID)
	assert.Equal(t, RunWaiting, w.runs(sync.ID)[0].State, "the run waits for the owner while its call does")

	w.after(time.Hour)

	for _, started := range w.tick() {
		assert.NotEqual(t, sync.ID, started.Routine.ID, "a run that waits for the owner makes the next time a skip")
	}

	assert.Equal(t, 1, w.theRoutine(sync.ID).Skipped)

	decided, err := w.s.DecideApproval(ctx, asked.ID, true, ScopeRoutine)
	require.NoError(t, err)
	assert.Equal(t, ScopeRoutine, decided.Scope)
	assert.Equal(t, RunRunning, w.runs(sync.ID)[0].State, "answered, the run goes on")

	again, err := w.s.DecideApproval(ctx, asked.ID, true, ScopeRoutine)
	require.NoError(t, err, "the same answer again is the stored one")
	assert.Equal(t, decided.DecidedAt, again.DecidedAt)

	_, err = w.s.DecideApproval(ctx, asked.ID, true, ScopeSession)
	require.ErrorIs(t, err, ErrConflict, "another answer is not")

	w.end(first.Run.ID, succeeded)

	_, granted, err = w.s.AskApproval(ctx, write(launch.Key, "rows-2"))
	require.NoError(t, err)
	assert.False(t, granted, "the key of a run that ended is no run's")

	second, err := w.s.RunRoutineNow(ctx, sync.ID)
	require.NoError(t, err)

	next, err := w.s.ClaimRoutineScript(ctx, sync.ID, DefaultBot)
	require.NoError(t, err)
	require.NotEqual(t, launch.Key, next.Key, "each run has its own key")

	_, granted, err = w.s.AskApproval(ctx, write(next.Key, "rows-2"))
	require.NoError(t, err)
	assert.True(t, granted, "a later run of the routine is not asked, whatever the rows")

	del := write(next.Key, "rows-2")
	del.Action, del.ActionName = "lark.base.delete", "Delete from a Base"

	denied, granted, err := w.s.AskApproval(ctx, del)
	require.NoError(t, err)
	assert.False(t, granted, "another action still asks")

	for _, live := range w.runs(other.ID) {
		w.end(live.ID, succeeded)
	}

	_, err = w.s.RunRoutineNow(ctx, other.ID)
	require.NoError(t, err)

	otherKey, err := w.s.ClaimRoutineScript(ctx, other.ID, DefaultBot)
	require.NoError(t, err)

	_, granted, err = w.s.AskApproval(ctx, write(otherKey.Key, "rows-2"))
	require.NoError(t, err)
	assert.False(t, granted, "another routine is asked")

	grants, err := w.s.RoutineGrants(ctx, sync.ID)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, "lark.base.write", grants[0].Action)
	assert.Equal(t, asked.ID, grants[0].ApprovalID)

	// The owner's no ends the run's wait; the run's end refuses what it still asked.
	_, err = w.s.DecideApproval(ctx, denied.ID, false, "")
	require.NoError(t, err)

	answers, err := w.s.RoutineRunAnswers(ctx, second.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, answers.Rejected)
	assert.Equal(t, denied.ID, answers.Rejected.ID)
	assert.Nil(t, answers.Expired)

	w.end(second.Run.ID, RunResult{State: RunStopped, Error: "the owner rejected it"})

	// A change of where the result goes keeps the grant; a change of the script ends it.
	quiet := hourly("sync")
	quiet.Announce, quiet.Summary = false, "the same work"
	assert.Equal(t, 1, w.routine(quiet).Revision)

	grants, err = w.s.RoutineGrants(ctx, sync.ID)
	require.NoError(t, err)
	assert.Len(t, grants, 1)

	edited := quiet
	edited.ScriptText = syncScript + "echo and more\n"
	assert.Equal(t, 2, w.routine(edited).Revision)

	grants, err = w.s.RoutineGrants(ctx, sync.ID)
	require.NoError(t, err)
	assert.Empty(t, grants, "the script changed: what it was allowed always is over")

	third, err := w.s.RunRoutineNow(ctx, sync.ID)
	require.NoError(t, err)

	key, err := w.s.ClaimRoutineScript(ctx, sync.ID, DefaultBot)
	require.NoError(t, err)

	asked, granted, err = w.s.AskApproval(ctx, write(key.Key, "rows-3"))
	require.NoError(t, err)
	require.False(t, granted, "the changed routine asks again")

	_, err = w.s.DecideApproval(ctx, asked.ID, true, ScopeRoutine)
	require.NoError(t, err)
	require.NoError(t, w.s.RevokeRoutineGrant(ctx, sync.ID, "lark", "work", "lark.base.write"))
	require.ErrorIs(t, w.s.RevokeRoutineGrant(ctx, sync.ID, "lark", "work", "lark.base.write"), ErrNotFound)

	_, granted, err = w.s.AskApproval(ctx, write(key.Key, "rows-4"))
	require.NoError(t, err)
	assert.False(t, granted, "taken back, the action asks again")

	// Removing the Routine removes its runs and its grants, and names the run to stop.
	live, err := w.s.DeleteRoutine(ctx, sync.ID)
	require.NoError(t, err)
	require.NotNil(t, live)
	assert.Equal(t, third.Run.ID, live.ID)

	_, err = w.s.GetRoutineRun(ctx, third.Run.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

// A request of a run that nobody answered in time is refused for the owner:
// the run goes on to its end, and its end knows. What a run still asked when
// it ended is refused too.
func TestAnApprovalNobodyAnsweredIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	brief := w.routine(daily("brief"))

	run, err := w.s.RunRoutineNow(ctx, brief.ID)
	require.NoError(t, err)

	send := func(digest string) NewApproval {
		return NewApproval{
			Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send mail",
			ChatID: run.Run.ChatID, AskedBy: "pi", Title: "Send mail: the brief", MatchHash: digest,
		}
	}

	asked, _, err := w.s.AskApproval(ctx, send("mail-1"))
	require.NoError(t, err)
	assert.Equal(t, brief.ID, asked.RoutineID, "a prompt run is known by its chat")
	assert.Equal(t, RunWaiting, w.runs(brief.ID)[0].State)

	expired, err := w.s.ExpireApproval(ctx, asked.ID)
	require.NoError(t, err)
	assert.True(t, expired.Expired)
	assert.Equal(t, ApprovalRejected, expired.Status)
	assert.Equal(t, RunRunning, w.runs(brief.ID)[0].State)

	_, err = w.s.DecideApproval(ctx, asked.ID, true, ScopeOnce)
	require.ErrorIs(t, err, ErrConflict, "it can no longer be answered")

	answers, err := w.s.RoutineRunAnswers(ctx, run.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, answers.Expired)
	assert.Nil(t, answers.Rejected)

	open, _, err := w.s.AskApproval(ctx, send("mail-2"))
	require.NoError(t, err)

	w.end(run.Run.ID, failed("nobody answered"))

	left, err := w.s.GetApproval(ctx, open.ID)
	require.NoError(t, err)
	assert.True(t, left.Expired, "what a run still asked when it ended is refused")

	_, granted, err := w.s.AskApproval(ctx, send("mail-3"))
	require.NoError(t, err)
	assert.False(t, granted)

	stray, err := w.s.ListApprovals(ctx, ApprovalFilter{Status: ApprovalPending})
	require.NoError(t, err)
	require.Len(t, stray, 1)
	assert.Empty(t, stray[0].RoutineID, "the chat of a run that ended is no run's")
}

// What is kept of a Routine's runs: those that ended within seven days. A
// run still going is kept however old, and a long output keeps its end.
func TestOldRunsOfARoutineAreRemoved(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	sync := w.routine(hourly("sync"))

	w.after(time.Hour)
	old := w.tick()[0].Run

	long := strings.Repeat("row\n", MaxRunOutput)
	settled := w.end(old.ID, RunResult{State: RunSucceeded, Output: long + "the end"})
	assert.LessOrEqual(t, len(settled.Run.Output), MaxRunOutput+len("…"))
	assert.True(t, strings.HasSuffix(settled.Run.Output, "the end"), "the end of the output is what is kept")

	w.after(time.Hour)
	stuck := w.tick()[0].Run

	w.after(RoutineRunsKeptFor - time.Hour)
	require.NoError(t, w.s.PruneRoutineRuns(t.Context()))
	assert.Len(t, w.runs(sync.ID), 2, "seven days are kept")

	w.after(2 * time.Minute)
	require.NoError(t, w.s.PruneRoutineRuns(t.Context()))

	left := w.runs(sync.ID)
	require.Len(t, left, 1)
	assert.Equal(t, stuck.ID, left[0].ID, "a run still going is never removed")
}
