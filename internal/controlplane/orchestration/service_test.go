package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newTestService(t *testing.T) (*Service, *sql.DB, *fakeClock) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, Migrate(t.Context(), db))

	clock := &fakeClock{now: t0}
	n := 0
	ids := func(prefix string) string {
		n++

		return fmt.Sprintf("%s_%04d", prefix, n)
	}

	return New(db, clock, ids), db, clock
}

func newRun(t *testing.T, s *Service) Run {
	t.Helper()

	run, err := s.CreateRun(t.Context(), "add Google login to second-bite", "owner")
	require.NoError(t, err)

	return run
}

// start puts a ready Task into running, as creating its Dispatch will (#19, PR 2).
func start(t *testing.T, s *Service, id string) {
	t.Helper()
	require.NoError(t, s.write(t.Context(), func(q *store.Queries, now int64) error {
		return transitionTask(t.Context(), q, now, id, TaskRunning, BlockedNone, "")
	}))
}

func statuses(t *testing.T, s *Service, runID string) map[string]string {
	t.Helper()

	tasks, err := s.ListTasks(t.Context(), runID)
	require.NoError(t, err)

	out := map[string]string{}
	for _, task := range tasks {
		out[task.Spec] = string(task.Status) + ":" + string(task.BlockedReason)
	}

	return out
}

// The transition table is the specification: this matrix restates it pair by
// pair, and the guarded UPDATE must accept exactly the allowed pairs.
func TestEveryTaskTransitionPair(t *testing.T) {
	t.Parallel()

	allowed := map[[2]TaskStatus]bool{}
	for _, p := range [][2]TaskStatus{
		{TaskPending, TaskReady}, {TaskPending, TaskBlocked}, {TaskPending, TaskCanceled},
		{TaskReady, TaskRunning}, {TaskReady, TaskBlocked}, {TaskReady, TaskCanceled},
		{TaskRunning, TaskSucceeded}, {TaskRunning, TaskFailed}, {TaskRunning, TaskReady},
		{TaskRunning, TaskBlocked}, {TaskRunning, TaskCanceled},
		{TaskBlocked, TaskReady}, {TaskBlocked, TaskFailed}, {TaskBlocked, TaskCanceled},
	} {
		allowed[p] = true
	}

	s, db, _ := newTestService(t)
	run := newRun(t, s)

	for _, from := range taskOrder {
		for _, to := range taskOrder {
			t.Run(fmt.Sprintf("%s to %s", from, to), func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, allowed[[2]TaskStatus{from, to}], canTask(from, to))

				tasks, err := s.CreateTasks(t.Context(), run.ID, []NewTask{{Ref: "x", Role: "implement", Spec: "x"}})
				require.NoError(t, err)

				id := tasks[0].ID
				reason := sql.NullString{String: string(BlockedGate), Valid: from == TaskBlocked}
				_, err = db.ExecContext(t.Context(), "UPDATE tasks SET status = ?, blocked_reason = ? WHERE id = ?",
					string(from), reason, id)
				require.NoError(t, err)

				toReason := BlockedNone
				if to == TaskBlocked {
					toReason = BlockedGate
				}

				err = s.write(t.Context(), func(q *store.Queries, now int64) error {
					return transitionTask(t.Context(), q, now, id, to, toReason, "")
				})
				if allowed[[2]TaskStatus{from, to}] {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrConflict)
				}
			})
		}
	}
}

// A Task becomes ready only once every Task it depends on has succeeded.
func TestTaskBecomesReadyWhenAllDependenciesSucceed(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	run := newRun(t, s)

	tasks, err := s.CreateTasks(t.Context(), run.ID, []NewTask{
		{Ref: "api", Role: "implement", Spec: "api"},
		{Ref: "ui", Role: "implement", Spec: "ui"},
		{Ref: "review", Role: "review", Spec: "review", DependsOn: []string{"api", "ui"}},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"api": "ready:", "ui": "ready:", "review": "pending:"}, statuses(t, s, run.ID))

	start(t, s, tasks[0].ID)
	require.NoError(t, s.CompleteTask(t.Context(), tasks[0].ID, "PR #1"))
	assert.Equal(t, "pending:", statuses(t, s, run.ID)["review"])

	start(t, s, tasks[1].ID)
	require.NoError(t, s.CompleteTask(t.Context(), tasks[1].ID, "PR #2"))
	assert.Equal(t, "ready:", statuses(t, s, run.ID)["review"])

	api, err := s.GetTask(t.Context(), tasks[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "PR #1", api.Result)
	require.NotNil(t, api.FinishedAt)
	assert.Equal(t, t0, *api.FinishedAt)
}

// A failure blocks every Task downstream of it, so the Run can converge
// instead of leaving dependents pending forever (Orca's gap).
func TestFailureBlocksEveryDependentTransitively(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	run := newRun(t, s)

	tasks, err := s.CreateTasks(t.Context(), run.ID, []NewTask{
		{Ref: "a", Role: "implement", Spec: "a"},
		{Ref: "b", Role: "implement", Spec: "b", DependsOn: []string{"a"}},
		{Ref: "c", Role: "review", Spec: "c", DependsOn: []string{"b"}},
		{Ref: "d", Role: "implement", Spec: "d"},
	})
	require.NoError(t, err)

	start(t, s, tasks[0].ID)
	require.NoError(t, s.FailTask(t.Context(), tasks[0].ID, "tests failed"))

	assert.Equal(t, map[string]string{
		"a": "failed:", "b": "blocked:dependency_failed", "c": "blocked:dependency_failed", "d": "ready:",
	}, statuses(t, s, run.ID))

	// A Task added later on top of a dead branch starts blocked.
	later, err := s.CreateTasks(t.Context(), run.ID, []NewTask{
		{Ref: "e", Role: "implement", Spec: "e", DependsOn: []string{tasks[2].ID}},
	})
	require.NoError(t, err)
	assert.Equal(t, TaskBlocked, later[0].Status)
}

// Dependencies can only point backwards within one Run, so no cycle can be
// built, and a refused batch leaves nothing behind.
func TestCreateTasksRefusesBadDependenciesAtomically(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	run := newRun(t, s)
	other := newRun(t, s)

	foreign, err := s.CreateTasks(t.Context(), other.ID, []NewTask{{Ref: "f", Role: "implement", Spec: "f"}})
	require.NoError(t, err)

	cases := map[string][]NewTask{
		"forward reference": {
			{Ref: "a", Role: "implement", Spec: "a", DependsOn: []string{"b"}},
			{Ref: "b", Role: "implement", Spec: "b"},
		},
		"another run's task": {{Ref: "a", Role: "implement", Spec: "a", DependsOn: []string{foreign[0].ID}}},
		"duplicate ref":      {{Ref: "a", Role: "implement", Spec: "a"}, {Ref: "a", Role: "implement", Spec: "b"}},
		"missing spec":       {{Ref: "a", Role: "implement"}},
		"self reference":     {{Ref: "a", Role: "implement", Spec: "a", DependsOn: []string{"a"}}},
	}

	for name, batch := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := s.CreateTasks(t.Context(), run.ID, batch)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}

	tasks, err := s.ListTasks(t.Context(), run.ID)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

// Canceling a Run cancels what has not ended and keeps what has; a
// canceled Run takes no new Tasks.
func TestCancelRunCancelsUnfinishedTasks(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	run := newRun(t, s)

	tasks, err := s.CreateTasks(t.Context(), run.ID, []NewTask{
		{Ref: "done", Role: "implement", Spec: "done"},
		{Ref: "next", Role: "review", Spec: "next", DependsOn: []string{"done"}},
		{Ref: "other", Role: "implement", Spec: "other"},
	})
	require.NoError(t, err)

	start(t, s, tasks[0].ID)
	require.NoError(t, s.CompleteTask(t.Context(), tasks[0].ID, "ok"))

	clock.now = t0.Add(time.Minute)
	require.NoError(t, s.CancelRun(t.Context(), run.ID))

	assert.Equal(t, map[string]string{"done": "succeeded:", "next": "canceled:", "other": "canceled:"},
		statuses(t, s, run.ID))

	got, err := s.GetRun(t.Context(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunCanceled, got.Status)
	require.NotNil(t, got.FinishedAt)
	assert.Equal(t, t0.Add(time.Minute), *got.FinishedAt)

	_, err = s.CreateTasks(t.Context(), run.ID, []NewTask{{Ref: "x", Role: "implement", Spec: "x"}})
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, s.CancelRun(t.Context(), run.ID), ErrConflict)
}

// A Run finishes only when every Task in it has ended.
func TestFinishRunNeedsEveryTaskEnded(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	run := newRun(t, s)

	tasks, err := s.CreateTasks(t.Context(), run.ID, []NewTask{{Ref: "a", Role: "implement", Spec: "a"}})
	require.NoError(t, err)
	require.ErrorIs(t, s.FinishRun(t.Context(), run.ID), ErrConflict)

	start(t, s, tasks[0].ID)
	require.NoError(t, s.CompleteTask(t.Context(), tasks[0].ID, "ok"))
	require.NoError(t, s.FinishRun(t.Context(), run.ID))
}

// Operations name what is wrong: a missing Task, or one in the wrong status.
func TestOperationsReportNotFoundAndConflict(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	run := newRun(t, s)

	tasks, err := s.CreateTasks(t.Context(), run.ID, []NewTask{{Ref: "a", Role: "implement", Spec: "a"}})
	require.NoError(t, err)

	require.ErrorIs(t, s.CompleteTask(t.Context(), tasks[0].ID, "ok"), ErrConflict)
	require.ErrorIs(t, s.CompleteTask(t.Context(), "tsk_missing", "ok"), ErrNotFound)
	_, err = s.GetRun(t.Context(), "run_missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.CreateTasks(t.Context(), "run_missing", []NewTask{{Ref: "a", Role: "implement", Spec: "a"}})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.CreateRun(t.Context(), " ", "owner")
	require.ErrorIs(t, err, ErrInvalid)
}

// `serve` refuses a database that was not migrated; migrating twice is harmless.
func TestCheckSchemaNeedsMigrate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.ErrorIs(t, CheckSchema(ctx, db), ErrSchemaOutdated)
	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))
}
