package orchestration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Question scenarios, in the same style as dispatch_test.go.

func choice(i int) *int { return &i }

func (w *world) ask(c NodeCall, a Ask) string {
	w.t.Helper()

	id, err := w.s.AskQuestion(w.t.Context(), w.again(c), a)
	require.NoError(w.t, err)

	return id
}

func (w *world) question(id string) Question {
	w.t.Helper()

	q, err := w.s.GetQuestion(w.t.Context(), id)
	require.NoError(w.t, err)

	return q
}

func libraryChoice(to Addressee) Ask {
	return Ask{
		Text:    "Use goth or hand-written OAuth for the Google login?",
		Options: []Option{{Label: "goth"}, {Label: "hand-written", AllowsText: true}},
		To:      to,
	}
}

func mergeGate() Ask {
	return Ask{
		Text:    "PR #7 is green. Merge it?",
		Options: []Option{{Label: "merge"}, {Label: "change first", AllowsText: true}},
		To:      AddressOwner,
	}
}

// Scenario: the worker asks and waits. The answer arrives with the next
// heartbeat; once the node acknowledges it, the timers run again and the
// time spent waiting is recorded.
func TestWaitQuestionIsAnsweredThroughTheHeartbeat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)

	id := w.ask(c, libraryChoice(AddressCoordinator))
	assert.Equal(t, t0, *w.dispatchOf(d.ID).WaitingSince)
	_, err := w.s.AskQuestion(t.Context(), w.again(c), libraryChoice(AddressCoordinator))
	require.ErrorIs(t, err, ErrConflict, "one question at a time")
	assert.Empty(t, w.heartbeat(c).Answers)

	w.advance(time.Minute)
	_, err = w.s.AnswerQuestion(t.Context(), id, AddressCoordinator, Answer{Option: choice(1), Text: "keep deps small"})
	require.NoError(t, err)

	beat := w.heartbeat(c)
	require.Len(t, beat.Answers, 1)
	assert.Equal(t, id, beat.Answers[0].ID)
	assert.Equal(t, &Answer{Option: choice(1), Text: "keep deps small"}, beat.Answers[0].Answer)

	w.advance(time.Second)
	require.NoError(t, w.s.AckAnswer(t.Context(), w.again(c), id))
	require.NoError(t, w.s.AckAnswer(t.Context(), w.again(c), id), "a repeat is harmless")

	got := w.dispatchOf(d.ID)
	assert.Nil(t, got.WaitingSince)
	assert.Equal(t, time.Minute+time.Second, got.Waited)
	assert.Equal(t, w.clock.now, *got.ProgressAt)
	assert.Empty(t, w.heartbeat(c).Answers)
	assert.NotNil(t, w.question(id).DeliveredAt)
}

// Scenario: the answer takes hours. The worker is neither stalled nor over
// its time while it waits; the stall timer starts again from the answer.
func TestWaitingPausesTheStallAndDurationTimers(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)
	id := w.ask(c, libraryChoice(AddressCoordinator))

	for range int(MaxDuration/time.Minute) + 30 {
		w.advance(time.Minute)
		w.heartbeat(c)
	}

	assert.Empty(t, w.reap().Escalations)

	_, err := w.s.AnswerQuestion(t.Context(), id, AddressCoordinator, Answer{Option: choice(0)})
	require.NoError(t, err)
	require.NoError(t, w.s.AckAnswer(t.Context(), w.again(c), id))

	for range int(StallAfter / time.Minute) {
		w.advance(time.Minute)
		w.heartbeat(c)
	}

	assert.Equal(t, []Escalation{{Kind: EscalationStalled, DispatchID: d.ID, NodeID: w.node.ID}}, w.reap().Escalations)
}

// Scenario: the node goes silent while its worker waits. The question stays
// open through the unknown outcome; a reattach resumes the wait, and a
// settlement cancels the question.
func TestQuestionSurvivesALostLeaseUntilTheDispatchSettles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"}, NewTask{Ref: "b", Role: "implement", Spec: "b"})
	_, ca := w.start(tasks[0].ID)
	db, cb := w.start(tasks[1].ID)
	qa := w.ask(ca, libraryChoice(AddressCoordinator))
	qb := w.ask(cb, libraryChoice(AddressCoordinator))

	w.loseLease()
	assert.Equal(t, QuestionOpen, w.question(qa).Status)

	acc, err := w.s.ReattachDispatch(t.Context(), w.again(ca), "pid:42@1")
	require.NoError(t, err)
	ca.Epoch = acc.Epoch
	_, err = w.s.AnswerQuestion(t.Context(), qa, AddressCoordinator, Answer{Option: choice(0)})
	require.NoError(t, err)
	assert.Len(t, w.heartbeat(ca).Answers, 1)

	require.NoError(t, w.s.SettleUnknown(t.Context(), db.ID, Settlement{Result: Result{Summary: "gone"}}))
	got := w.question(qb)
	assert.Equal(t, QuestionCanceled, got.Status)
	assert.Equal(t, "dispatch_ended", got.CancelReason)
	assert.Nil(t, w.dispatchOf(db.ID).WaitingSince)

	_, err = w.s.AnswerQuestion(t.Context(), qb, AddressCoordinator, Answer{Option: choice(0)})
	require.ErrorIs(t, err, ErrConflict)
}

// Scenario: the work needs the owner's decision. The worker ends, the Task
// waits, the owner answers, and the next Dispatch continues the same
// Conversation with the question and the answer.
func TestGateEndsTheDispatchAndResumesWithTheAnswer(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "review", Spec: "b", DependsOn: []string{"a"}},
	)
	d, c := w.start(tasks[0].ID)

	gate := mergeGate()
	require.NoError(t, w.finish(c, Outcome{Gate: &gate, Result: Result{Summary: "PR #7 is ready"}}))
	assert.Equal(t, DispatchGated, w.dispatchOf(d.ID).Status)
	assert.Equal(t, map[string]string{"a": "blocked:gate", "b": "pending:"}, w.statuses())
	assert.Equal(t, ConversationOpen, w.conversation(d.ConversationID).Status)

	open, err := w.s.ListQuestions(t.Context(), QuestionFilter{AddressedTo: AddressOwner, Status: QuestionOpen})
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, QuestionGate, open[0].Mode)
	assert.Equal(t, gate.Text, open[0].Text)

	_, err = w.create(tasks[0].ID, w.spec())
	require.ErrorIs(t, err, ErrConflict, "the task waits for the decision")
	_, err = w.s.AnswerQuestion(t.Context(), open[0].ID, AddressCoordinator, Answer{Option: choice(0)})
	require.ErrorIs(t, err, ErrConflict, "the gate is the owner's")

	_, err = w.s.AnswerQuestion(t.Context(), open[0].ID, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)
	assert.Equal(t, TaskReady, w.task(tasks[0].ID).Status)

	next, err := w.create(tasks[0].ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	assert.Equal(t, d.ConversationID, next.ConversationID)
	assert.Equal(t, open[0].ID, next.GateQuestionID)

	acc, err := w.s.AcceptDispatch(t.Context(), w.call(next, 0))
	require.NoError(t, err)
	require.NotNil(t, acc.Gate)
	assert.Equal(t, gate.Text, acc.Gate.Text)
	assert.Equal(t, &Answer{Option: choice(0)}, acc.Gate.Answer)
	assert.Equal(t, "sess_1", acc.CLISessionRef)

	nc := w.call(next, acc.Epoch)
	require.NoError(t, w.s.ReportStarted(t.Context(), nc, "pid:43@2", "sess_1"))
	require.NoError(t, w.finish(nc, success("merged PR #7")))
	assert.Equal(t, map[string]string{"a": "succeeded:", "b": "ready:"}, w.statuses())

	after, err := w.s.CreateTasks(t.Context(), w.run.ID, []NewTask{{Ref: "c", Role: "implement", Spec: "c"}})
	require.NoError(t, err)
	fresh := w.dispatch(after[0].ID)
	assert.Empty(t, fresh.GateQuestionID, "only the Dispatch right after a gate resumes from it")
}

// Scenario: the Run is canceled while a Gate waits. The Gate is closed with
// its Task.
func TestCancelClosesAnOpenGate(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	_, c := w.start(task.ID)
	gate := mergeGate()
	require.NoError(t, w.finish(c, Outcome{Gate: &gate}))

	require.NoError(t, w.s.CancelRun(t.Context(), w.run.ID))
	assert.Equal(t, TaskCanceled, w.task(task.ID).Status)

	qs, err := w.s.ListQuestions(t.Context(), QuestionFilter{TaskID: task.ID})
	require.NoError(t, err)
	require.Len(t, qs, 1)
	assert.Equal(t, QuestionCanceled, qs[0].Status)
	assert.Equal(t, "task_canceled", qs[0].CancelReason)
}

// Scenario: two people, or a retry, answer at once. The first answer wins;
// the same answer again is harmless; anything else is refused. Answers are
// checked against the options.
func TestFirstAnswerWins(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"}, NewTask{Ref: "b", Role: "implement", Spec: "b"})
	_, ca := w.start(tasks[0].ID)
	_, cb := w.start(tasks[1].ID)
	id := w.ask(ca, libraryChoice(AddressOwner))
	free := w.ask(cb, Ask{Text: "Which Google project id?", To: AddressOwner})
	ctx := t.Context()

	for name, a := range map[string]Answer{
		"no option":        {},
		"out of range":     {Option: choice(2)},
		"text not allowed": {Option: choice(0), Text: "why not"},
		"too long":         {Option: choice(1), Text: strings.Repeat("x", MaxAnswerText+1)},
	} {
		_, err := w.s.AnswerQuestion(ctx, id, AddressOwner, a)
		require.ErrorIs(t, err, ErrInvalid, name)
	}

	_, err := w.s.AnswerQuestion(ctx, free, AddressOwner, Answer{Option: choice(0)})
	require.ErrorIs(t, err, ErrInvalid, "a free-text question takes text")
	_, err = w.s.AnswerQuestion(ctx, free, AddressOwner, Answer{Text: "second-bite-prod"})
	require.NoError(t, err)

	first, err := w.s.AnswerQuestion(ctx, id, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)
	again, err := w.s.AnswerQuestion(ctx, id, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)
	assert.Equal(t, first, again)

	_, err = w.s.AnswerQuestion(ctx, id, AddressOwner, Answer{Option: choice(1)})
	require.ErrorIs(t, err, ErrConflict)
	_, err = w.s.AnswerQuestion(ctx, id, AddressCoordinator, Answer{Option: choice(0)})
	require.ErrorIs(t, err, ErrConflict, "the same answer from someone else")
}

// Scenario: the coordinator cannot decide and passes the question on. Once
// forwarded, only the owner answers it, and the owner is reminded of it.
func TestCoordinatorForwardsAQuestionToTheOwner(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	_, c := w.start(task.ID)
	id := w.ask(c, libraryChoice(AddressCoordinator))
	assert.Nil(t, w.question(id).RemindAt)

	require.NoError(t, w.s.ForwardQuestion(t.Context(), id))
	got := w.question(id)
	assert.Equal(t, AddressOwner, got.AddressedTo)
	assert.Equal(t, t0.Add(ReminderFirst), *got.RemindAt)
	require.ErrorIs(t, w.s.ForwardQuestion(t.Context(), id), ErrConflict)

	_, err := w.s.AnswerQuestion(t.Context(), id, AddressCoordinator, Answer{Option: choice(0)})
	require.ErrorIs(t, err, ErrConflict)
	_, err = w.s.AnswerQuestion(t.Context(), id, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)
	assert.Nil(t, w.question(id).RemindAt)
}

// Scenario: a Gate waits for days. It never expires; the owner is reminded
// after an hour, then daily. Questions to the coordinator are not reminded.
func TestOwnerIsRemindedAfterAnHourThenDaily(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"}, NewTask{Ref: "b", Role: "implement", Spec: "b"})
	da, ca := w.start(tasks[0].ID)
	_, cb := w.start(tasks[1].ID)
	gate := mergeGate()
	toCoordinator := mergeGate()
	toCoordinator.To = AddressCoordinator
	require.NoError(t, w.finish(ca, Outcome{Gate: &gate}))
	require.NoError(t, w.finish(cb, Outcome{Gate: &toCoordinator}))

	qs, err := w.s.ListQuestions(t.Context(), QuestionFilter{TaskID: tasks[0].ID})
	require.NoError(t, err)
	reminder := []Escalation{{Kind: EscalationQuestionReminder, DispatchID: da.ID, QuestionID: qs[0].ID}}

	w.advance(ReminderFirst - time.Minute)
	assert.Empty(t, w.reap().Escalations)
	w.advance(time.Minute)
	assert.Equal(t, reminder, w.reap().Escalations)
	assert.Empty(t, w.reap().Escalations, "once")

	w.advance(ReminderEvery - time.Minute)
	assert.Empty(t, w.reap().Escalations)
	w.advance(time.Minute)
	assert.Equal(t, reminder, w.reap().Escalations)

	w.advance(30 * 24 * time.Hour)
	assert.Equal(t, QuestionOpen, w.question(qs[0].ID).Status, "a gate never expires")
}

// Scenario: the network repeats a question. The same request id returns the
// same question and asks nothing twice. Bad questions are refused.
func TestRetriedAskReturnsTheSameQuestion(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	d, c := w.start(task.ID)

	call := w.again(c)
	first, err := w.s.AskQuestion(t.Context(), call, libraryChoice(AddressOwner))
	require.NoError(t, err)
	second, err := w.s.AskQuestion(t.Context(), call, libraryChoice(AddressOwner))
	require.NoError(t, err)
	assert.Equal(t, first, second)

	qs, err := w.s.ListQuestions(t.Context(), QuestionFilter{DispatchID: d.ID})
	require.NoError(t, err)
	assert.Len(t, qs, 1)

	many := make([]Option, MaxOptions+1)
	for i := range many {
		many[i] = Option{Label: "x"}
	}

	for name, a := range map[string]Ask{
		"empty text":    {Text: " ", To: AddressOwner},
		"too long":      {Text: strings.Repeat("x", MaxQuestionText+1), To: AddressOwner},
		"many options":  {Text: "x", Options: many, To: AddressOwner},
		"empty label":   {Text: "x", Options: []Option{{Label: ""}}, To: AddressOwner},
		"no addressee":  {Text: "x"},
		"other address": {Text: "x", To: "node"},
	} {
		_, err := w.s.AskQuestion(t.Context(), w.again(c), a)
		require.ErrorIs(t, err, ErrInvalid, name)
	}

	gate := mergeGate()
	require.ErrorIs(t, w.finish(c, Outcome{Class: FailureWorkerError, Gate: &gate}), ErrInvalid)
}

// Migration 3 rebuilds the dispatches table; Dispatches and their effects and
// resources written under migration 2 survive it.
func TestMigrationKeepsDispatchesFromTheSecondSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 2)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`,
		`INSERT INTO tasks (id, run_id, position, role, spec, status, created_at, updated_at)
		 VALUES ('a', 'r', 1, 'implement', 'a', 'running', 1, 1)`,
		`INSERT INTO conversations (id, task_id, node_id, worktree, runtime, account_ref, status, created_at, updated_at)
		 VALUES ('c', 'a', 'node_a', '/w', 'claude', 'acct', 'open', 1, 1)`,
		`INSERT INTO dispatches (id, task_id, conversation_id, node_id, status, epoch, created_at, updated_at)
		 VALUES ('d', 'a', 'c', 'node_a', 'running', 1, 1, 1)`,
		`INSERT INTO dispatch_effects (dispatch_id, seq, kind, ref, phase, created_at, updated_at)
		 VALUES ('d', 1, 'push', 'feat/x', 'intent', 1, 1)`,
		`INSERT INTO dispatch_resources (dispatch_id, kind, ref, custody, created_at)
		 VALUES ('d', 'worktree', '/w', 'owned', 1)`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" })
	d, err := s.GetDispatch(ctx, "d")
	require.NoError(t, err)
	assert.Equal(t, DispatchRunning, d.Status)
	assert.Zero(t, d.Waited)

	effects, err := s.ListEffects(ctx, "d")
	require.NoError(t, err)
	assert.Equal(t, []Effect{{Seq: 1, Kind: "push", Ref: "feat/x", Phase: EffectIntent}}, effects)

	resources, err := s.ListResources(ctx, "d")
	require.NoError(t, err)
	assert.Len(t, resources, 1)

	var violations int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations))
	assert.Zero(t, violations)
}
