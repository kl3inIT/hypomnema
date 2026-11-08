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

	s := New(db, clock, ids)
	// No jitter: the back-off is exactly BackoffBase × 2^(n−1).
	s.random = func() float64 { return 0.5 }

	return s, db, clock
}

func newRun(t *testing.T, s *Service) Run {
	t.Helper()

	run, err := s.CreateRun(t.Context(), NewRun{Objective: "add Google login to second-bite", CreatedBy: "owner"})
	require.NoError(t, err)

	return run
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
		{TaskBlocked, TaskPending}, {TaskBlocked, TaskReady}, {TaskBlocked, TaskRunning},
		{TaskBlocked, TaskSucceeded}, {TaskBlocked, TaskFailed}, {TaskBlocked, TaskCanceled},
		{TaskFailed, TaskReady},
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

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "api", Role: "implement", Spec: "api"},
		NewTask{Ref: "ui", Role: "implement", Spec: "ui"},
		NewTask{Ref: "review", Role: "review", Spec: "review", DependsOn: []string{"api", "ui"}},
	)
	assert.Equal(t, map[string]string{"api": "ready:", "ui": "ready:", "review": "pending:"}, w.statuses())

	w.succeed(tasks[0].ID, "PR #1")
	assert.Equal(t, "pending:", w.statuses()["review"])

	w.succeed(tasks[1].ID, "PR #2")
	assert.Equal(t, "ready:", w.statuses()["review"])

	api := w.task(tasks[0].ID)
	assert.Equal(t, "PR #1", api.Result)
	require.NotNil(t, api.FinishedAt)
	assert.Equal(t, t0, *api.FinishedAt)
}

// A failure blocks every Task downstream of it, so the Run can converge
// instead of leaving dependents pending forever (Orca's gap).
func TestFailureBlocksEveryDependentTransitively(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "implement", Spec: "b", DependsOn: []string{"a"}},
		NewTask{Ref: "c", Role: "review", Spec: "c", DependsOn: []string{"b"}},
		NewTask{Ref: "d", Role: "implement", Spec: "d"},
	)

	w.failForGood(tasks[0].ID)

	assert.Equal(t, map[string]string{
		"a": "failed:", "b": "blocked:dependency_failed", "c": "blocked:dependency_failed", "d": "ready:",
	}, w.statuses())

	// A Task added later on top of a dead branch starts blocked.
	later, err := w.s.CreateTasks(t.Context(), w.run.ID, []NewTask{
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

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "done", Role: "implement", Spec: "done"},
		NewTask{Ref: "next", Role: "review", Spec: "next", DependsOn: []string{"done"}},
		NewTask{Ref: "other", Role: "implement", Spec: "other"},
	)

	w.succeed(tasks[0].ID, "ok")

	w.advance(time.Minute)
	require.NoError(t, w.s.CancelRun(t.Context(), w.run.ID))

	assert.Equal(t, map[string]string{"done": "succeeded:", "next": "canceled:", "other": "canceled:"}, w.statuses())

	got, err := w.s.GetRun(t.Context(), w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, RunCanceled, got.Status)
	require.NotNil(t, got.FinishedAt)
	assert.Equal(t, t0.Add(time.Minute), *got.FinishedAt)

	_, err = w.s.CreateTasks(t.Context(), w.run.ID, []NewTask{{Ref: "x", Role: "implement", Spec: "x"}})
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, w.s.CancelRun(t.Context(), w.run.ID), ErrConflict)
}

// A Run finishes only when every Task in it has ended.
func TestFinishRunNeedsEveryTaskEnded(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(NewTask{Ref: "a", Role: "implement", Spec: "a"})
	require.ErrorIs(t, w.s.FinishRun(t.Context(), w.run.ID), ErrConflict)

	w.succeed(tasks[0].ID, "ok")
	require.NoError(t, w.s.FinishRun(t.Context(), w.run.ID))
}

// Operations name what is wrong: a missing record, or one in the wrong status.
func TestOperationsReportNotFoundAndConflict(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	tasks := w.tasks(
		NewTask{Ref: "a", Role: "implement", Spec: "a"},
		NewTask{Ref: "b", Role: "implement", Spec: "b", DependsOn: []string{"a"}},
	)

	_, err := w.create(tasks[1].ID, w.spec())
	require.ErrorIs(t, err, ErrConflict)
	_, err = w.create("tsk_missing", w.spec())
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.GetRun(t.Context(), "run_missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.CreateTasks(t.Context(), "run_missing", []NewTask{{Ref: "a", Role: "implement", Spec: "a"}})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = w.s.CreateRun(t.Context(), NewRun{Objective: " ", CreatedBy: "owner"})
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorIs(t, w.s.ReopenTask(t.Context(), tasks[0].ID), ErrConflict)
	_, err = w.s.Heartbeat(t.Context(), NodeCall{NodeID: "node_a", DispatchID: "dsp_missing", RequestID: "r"})
	require.ErrorIs(t, err, ErrNotFound)
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

// Migration 2 rebuilds the tasks table; Runs, Tasks and dependencies written
// under migration 1 survive it, and foreign keys still hold.
func TestMigrationKeepsDataFromTheFirstSchema(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 1)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO runs (id, objective, status, created_by, created_at, updated_at) VALUES ('r', 'o', 'open', 'owner', 1, 1)`,
		`INSERT INTO tasks (id, run_id, position, role, spec, status, blocked_reason, created_at, updated_at)
		 VALUES ('a', 'r', 1, 'implement', 'a', 'failed', NULL, 1, 1),
		        ('b', 'r', 2, 'implement', 'b', 'blocked', 'dependency_failed', 1, 1)`,
		`INSERT INTO task_deps (task_id, depends_on) VALUES ('b', 'a')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(p string) string { return p + "_x" })
	assert.Equal(t, map[string]string{"a": "failed:", "b": "blocked:dependency_failed"}, statuses(t, s, "r"))

	var violations int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations))
	assert.Zero(t, violations)

	// The rebuilt table takes the new reason, and the dependency still links the two.
	require.NoError(t, s.ReopenTask(ctx, "a"))
	assert.Equal(t, map[string]string{"a": "ready:", "b": "pending:"}, statuses(t, s, "r"))
}
