package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// events renders the log after a cursor as short lines, naming Tasks by spec.
func (w *world) events(after int64, f EventFilter) []string {
	w.t.Helper()

	page, err := w.s.ListEvents(w.t.Context(), after, f, EventPageMax)
	require.NoError(w.t, err)
	require.False(w.t, page.HasMore)

	tasks, err := w.s.ListTasks(w.t.Context(), w.run.ID)
	require.NoError(w.t, err)

	spec := map[string]string{}
	for _, t := range tasks {
		spec[t.ID] = t.Spec
	}

	out := make([]string, 0, len(page.Events))

	for _, e := range page.Events {
		var p map[string]any
		require.NoError(w.t, json.Unmarshal(e.Payload, &p))

		line := string(e.Kind)
		if e.TaskID != "" {
			line += " " + spec[e.TaskID]
		}

		switch e.Kind {
		case EventTaskCreated:
			line += fmt.Sprintf(" %v", p["status"])
		case EventTaskStatus, EventDispatchStatus:
			line += fmt.Sprintf(" %v>%v", p["from"], p["to"])
		case EventConversationClosed, EventQuestionCanceled:
			line += fmt.Sprintf(" %v", p["reason"])
		case EventQuestionOpened, EventQuestionAnswered, EventQuestionForwarded:
			line += fmt.Sprintf(" %v", p["mode"])
		case EventEscalation:
			line += fmt.Sprintf(" %v", p["kind"])
		case EventRunCreated, EventRunFinished, EventRunCanceled, EventDispatchCreated:
		}

		out = append(out, line)
	}

	return out
}

func (w *world) lastSeq() int64 {
	w.t.Helper()

	var seq int64
	require.NoError(w.t, w.db.QueryRowContext(w.t.Context(), "SELECT coalesce(max(seq), 0) FROM events").Scan(&seq))

	return seq
}

// Scenario: a Run from its creation through a Gate to success. Every change
// is in the log, in order, with the status it left.
func TestEventsFollowARunThroughAGate(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "review", Spec: "b", DependsOn: []string{"a"}},
	)
	_, c := w.start(tasks[0].ID)
	gate := mergeGate()
	require.NoError(t, w.finish(c, Outcome{Gate: &gate}))

	open, err := w.s.ListQuestions(t.Context(), QuestionFilter{TaskID: tasks[0].ID})
	require.NoError(t, err)
	_, err = w.s.AnswerQuestion(t.Context(), open[0].ID, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)

	next, err := w.create(tasks[0].ID, DispatchSpec{Continue: true})
	require.NoError(t, err)
	nc := w.accept(next)
	require.NoError(t, w.s.ReportStarted(t.Context(), w.again(nc), "pid:43@2", "sess_1"))
	require.NoError(t, w.finish(nc, success("merged")))

	assert.Equal(t, []string{
		"run.created",
		"task.created a ready",
		"task.created b pending",
		"dispatch.created a",
		"task.status a ready>running",
		"dispatch.status a queued>starting",
		"dispatch.status a starting>running",
		"dispatch.status a running>gated",
		"question.opened a gate",
		"task.status a running>blocked",
		"question.answered a gate",
		"task.status a blocked>ready",
		"dispatch.created a",
		"task.status a ready>running",
		"dispatch.status a queued>starting",
		"dispatch.status a starting>running",
		"dispatch.status a running>succeeded",
		"task.status a running>succeeded",
		"task.status b pending>ready",
	}, w.events(0, EventFilter{RunID: w.run.ID}))
}

// Scenario: a node goes silent and comes back; a cancel waits for the stop.
func TestEventsRecordUnknownReattachAndCancel(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	_, c := w.start(task.ID)
	from := w.lastSeq()

	w.loseLease()
	acc, err := w.s.ReattachDispatch(t.Context(), w.again(c), "pid:42@1")
	require.NoError(t, err)
	c.Epoch = acc.Epoch
	require.NoError(t, w.s.CancelTask(t.Context(), task.ID))
	require.NoError(t, w.s.ReportStopped(t.Context(), w.again(c)))

	assert.Equal(t, []string{
		"dispatch.status a running>unknown",
		"task.status a running>blocked",
		"dispatch.status a unknown>running",
		"task.status a blocked>running",
		"dispatch.status a running>stopping",
		"dispatch.status a stopping>canceled",
		"task.status a running>canceled",
	}, w.events(from, EventFilter{TaskID: task.ID}))
}

// Scenario: an operation is refused. It leaves nothing in the log.
func TestRefusedOperationWritesNoEvent(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "implement", Spec: "b", DependsOn: []string{"a"}},
	)
	from := w.lastSeq()

	_, err := w.create(tasks[1].ID, w.spec())
	require.ErrorIs(t, err, ErrConflict)
	_, err = w.s.CreateTasks(t.Context(), w.run.ID, []NewTask{
		{Ref: "c", Role: "implement", Spec: "c"},
		{Ref: "d", Role: "implement", Spec: "d", DependsOn: []string{"missing"}},
	})
	require.ErrorIs(t, err, ErrInvalid)

	assert.Equal(t, from, w.lastSeq())
}

// Paging follows Omnara: a cursor, a limit and has_more, with filters.
func TestListEventsPages(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "implement", Spec: "b"},
		NewTask{Ref: "c", Role: "implement", Spec: "c"},
	)
	other := newRun(t, w.s)
	ctx := t.Context()

	first, err := w.s.ListEvents(ctx, 0, EventFilter{RunID: w.run.ID}, 3)
	require.NoError(t, err)
	assert.Len(t, first.Events, 3)
	assert.True(t, first.HasMore)
	assert.Equal(t, first.Events[2].Seq, first.NextAfter)

	second, err := w.s.ListEvents(ctx, first.NextAfter, EventFilter{RunID: w.run.ID}, 3)
	require.NoError(t, err)
	assert.Len(t, second.Events, 1)
	assert.False(t, second.HasMore)

	empty, err := w.s.ListEvents(ctx, second.NextAfter, EventFilter{RunID: w.run.ID}, 3)
	require.NoError(t, err)
	assert.Empty(t, empty.Events)
	assert.Equal(t, second.NextAfter, empty.NextAfter, "the cursor stays where it was")

	all, err := w.s.ListEvents(ctx, 0, EventFilter{}, 0)
	require.NoError(t, err)
	assert.Len(t, all.Events, 5, "both runs, with the default limit")
	assert.Equal(t, other.ID, all.Events[4].RunID)

	byTask, err := w.s.ListEvents(ctx, 0, EventFilter{TaskID: tasks[1].ID}, 0)
	require.NoError(t, err)
	require.Len(t, byTask.Events, 1)
	assert.Equal(t, EventTaskCreated, byTask.Events[0].Kind)
}

// Escalations are written once, with the change that found them.
func TestEscalationsAreWrittenOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})[0]
	_, c := w.start(task.ID)
	from := w.lastSeq()

	for range int(StallAfter / time.Minute) {
		w.advance(time.Minute)
		w.heartbeat(c)
	}

	w.reap()
	w.reap()
	assert.Equal(t, []string{"escalation a stalled"}, w.events(from, EventFilter{TaskID: task.ID}))
}

// Subscribers are woken after a commit, never after a refused operation, and
// a slow subscriber only ever holds one pending signal.
func TestSubscribeSignalsAfterCommit(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	signals := w.s.Subscribe(ctx)

	pending := func() bool {
		select {
		case <-signals:
			return true
		default:
			return false
		}
	}

	assert.False(t, pending())
	w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})
	assert.True(t, pending())

	_, err := w.s.CreateDispatch(t.Context(), "tsk_missing", w.spec())
	require.ErrorIs(t, err, ErrNotFound)
	assert.False(t, pending(), "a refused operation commits nothing")

	w.tasks(NewTask{Ref: "b", Role: "implement", Spec: "b"})
	w.tasks(NewTask{Ref: "c", Role: "implement", Spec: "c"})
	assert.True(t, pending())
	assert.False(t, pending(), "two commits, one signal")

	cancel()
	require.Eventually(t, func() bool {
		_, open := <-signals

		return !open
	}, time.Second, time.Millisecond)
}

// Migration 4 only adds the log; data written under migration 3 is untouched.
func TestMigrationAddsTheEventLog(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 3)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" })
	run, err := s.GetRun(ctx, "r")
	require.NoError(t, err)
	assert.Equal(t, RunOpen, run.Status)

	page, err := s.ListEvents(ctx, 0, EventFilter{}, 0)
	require.NoError(t, err)
	assert.Empty(t, page.Events)
}
