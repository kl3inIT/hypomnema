package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Service performs the named operations on the orchestration state.
type Service struct {
	db    *sql.DB
	clock platform.Clock
	newID func(prefix string) string
}

// New returns a Service over an opened, migrated database. The clock and id
// generator are passed in so tests control time and ids.
func New(db *sql.DB, clock platform.Clock, newID func(prefix string) string) *Service {
	return &Service{db: db, clock: clock, newID: newID}
}

// write runs fn in one transaction. The connection opens every transaction
// with BEGIN IMMEDIATE (internal/shared/sqlite), so fn reads the latest state.
func (s *Service) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(store.New(tx), s.clock.Now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	return nil
}

func (s *Service) read() *store.Queries {
	return store.New(s.db)
}

// CreateRun records a new objective from the owner.
func (s *Service) CreateRun(ctx context.Context, objective, createdBy string) (Run, error) {
	if strings.TrimSpace(objective) == "" || createdBy == "" {
		return Run{}, fmt.Errorf("%w: a run needs an objective and its creator", ErrInvalid)
	}

	id := s.newID("run")

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertRun(ctx, store.InsertRunParams{ID: id, Objective: objective, CreatedBy: createdBy, Now: now})
	})
	if err != nil {
		return Run{}, fmt.Errorf("creating a run: %w", err)
	}

	return s.GetRun(ctx, id)
}

// GetRun returns one Run.
func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	row, err := s.read().GetRun(ctx, id)
	if err != nil {
		return Run{}, notFound(err, "run", id)
	}

	return runFromRow(row), nil
}

// FinishRun closes a Run whose Tasks have all ended.
func (s *Service) FinishRun(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		open, err := q.CountUnfinishedTasks(ctx, store.CountUnfinishedTasksParams{RunID: id, Statuses: unfinished()})
		if err != nil {
			return fmt.Errorf("counting unfinished tasks: %w", err)
		}

		if open > 0 {
			return fmt.Errorf("%w: run %s has %d unfinished tasks", ErrConflict, id, open)
		}

		return transitionRun(ctx, q, now, id, RunOpen, RunFinished)
	})
}

// CancelRun cancels a Run and every Task in it that has not ended.
func (s *Service) CancelRun(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := transitionRun(ctx, q, now, id, RunOpen, RunCanceled); err != nil {
			return err
		}

		_, err := q.CancelOpenTasksOfRun(ctx, store.CancelOpenTasksOfRunParams{
			Now: now, RunID: id, FromStatuses: taskFrom(TaskCanceled),
		})
		if err != nil {
			return fmt.Errorf("canceling the run's tasks: %w", err)
		}

		return nil
	})
}

// CreateTasks adds Tasks to an open Run in one transaction. A dependency must
// be an earlier Task of the same call or a Task already in the Run, so no
// cycle can be built (as Orca's task-store.ts). A Task starts ready when every
// dependency succeeded, blocked when one failed or was canceled, and pending
// otherwise.
func (s *Service) CreateTasks(ctx context.Context, runID string, tasks []NewTask) ([]Task, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: no tasks", ErrInvalid)
	}

	ids := make([]string, 0, len(tasks))

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		run, err := q.GetRun(ctx, runID)
		if err != nil {
			return notFound(err, "run", runID)
		}

		if RunStatus(run.Status) != RunOpen {
			return fmt.Errorf("%w: run %s is %s", ErrConflict, runID, run.Status)
		}

		position, err := q.NextTaskPosition(ctx, runID)
		if err != nil {
			return fmt.Errorf("reading the next task position: %w", err)
		}

		byRef := map[string]string{}
		status := map[string]depState{}

		for i, nt := range tasks {
			if err := validNewTask(nt, byRef); err != nil {
				return err
			}

			deps, err := resolveDeps(ctx, q, runID, nt.DependsOn, byRef, status)
			if err != nil {
				return err
			}

			id := s.newID("tsk")
			st, reason := initialStatus(deps, status)

			if err := q.InsertTask(ctx, store.InsertTaskParams{
				ID: id, RunID: runID, Position: position + int64(i), Role: nt.Role, Spec: nt.Spec,
				Runtime: nullString(nt.Runtime), Model: nullString(nt.Model), Effort: nullString(nt.Effort),
				Status: string(st), BlockedReason: nullString(string(reason)), Now: now,
			}); err != nil {
				return fmt.Errorf("inserting task %q: %w", nt.Ref, err)
			}

			for _, dep := range deps {
				if err := q.InsertTaskDep(ctx, store.InsertTaskDepParams{TaskID: id, DependsOn: dep}); err != nil {
					return fmt.Errorf("inserting a dependency of %q: %w", nt.Ref, err)
				}
			}

			byRef[nt.Ref] = id
			status[id] = depState{st, reason}
			ids = append(ids, id)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("creating tasks: %w", err)
	}

	out := make([]Task, 0, len(ids))

	for _, id := range ids {
		t, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}

		out = append(out, t)
	}

	return out, nil
}

func validNewTask(nt NewTask, byRef map[string]string) error {
	switch {
	case nt.Ref == "":
		return fmt.Errorf("%w: a task needs a ref", ErrInvalid)
	case byRef[nt.Ref] != "":
		return fmt.Errorf("%w: ref %q is used twice", ErrInvalid, nt.Ref)
	case strings.TrimSpace(nt.Role) == "" || strings.TrimSpace(nt.Spec) == "":
		return fmt.Errorf("%w: task %q needs a role and a spec", ErrInvalid, nt.Ref)
	default:
		return nil
	}
}

// resolveDeps turns refs and ids into Task ids of the same Run, recording the
// status of Tasks already stored.
func resolveDeps(ctx context.Context, q *store.Queries, runID string, refs []string,
	byRef map[string]string, status map[string]depState,
) ([]string, error) {
	seen := map[string]bool{}

	var deps []string

	for _, ref := range refs {
		id, ok := byRef[ref]
		if !ok {
			t, err := q.GetTask(ctx, ref)
			if err != nil || t.RunID != runID {
				return nil, fmt.Errorf("%w: dependency %q is not an earlier task of this call or of run %s",
					ErrInvalid, ref, runID)
			}

			id = t.ID
			status[id] = depState{TaskStatus(t.Status), BlockedReason(t.BlockedReason.String)}
		}

		if !seen[id] {
			seen[id] = true
			deps = append(deps, id)
		}
	}

	return deps, nil
}

// depState is what CreateTasks knows about a dependency.
type depState struct {
	status TaskStatus
	reason BlockedReason
}

// neverSucceeds reports whether a dependency can no longer succeed.
func (d depState) neverSucceeds() bool {
	return d.status == TaskFailed || d.status == TaskCanceled ||
		(d.status == TaskBlocked && d.reason == BlockedDependencyFailed)
}

func initialStatus(deps []string, status map[string]depState) (TaskStatus, BlockedReason) {
	ready := true

	for _, d := range deps {
		switch {
		case status[d].neverSucceeds():
			return TaskBlocked, BlockedDependencyFailed
		case status[d].status != TaskSucceeded:
			// Pending, ready, running, or blocked by a Gate or an unknown Dispatch: it may still succeed.
			ready = false
		}
	}

	if ready {
		return TaskReady, BlockedNone
	}

	return TaskPending, BlockedNone
}

// GetTask returns one Task.
func (s *Service) GetTask(ctx context.Context, id string) (Task, error) {
	row, err := s.read().GetTask(ctx, id)
	if err != nil {
		return Task{}, notFound(err, "task", id)
	}

	return taskFromRow(row), nil
}

// ListTasks returns a Run's Tasks in creation order.
func (s *Service) ListTasks(ctx context.Context, runID string) ([]Task, error) {
	rows, err := s.read().ListTasksByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		out = append(out, taskFromRow(r))
	}

	return out, nil
}

// CompleteTask records a running Task's success and makes ready every
// dependent whose dependencies have now all succeeded, in the same
// transaction (Orca's promoteReadyTasks).
func (s *Service) CompleteTask(ctx context.Context, id, result string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := transitionTask(ctx, q, now, id, TaskSucceeded, BlockedNone, result); err != nil {
			return err
		}

		ready, err := q.ListUnblockedDependents(ctx, id)
		if err != nil {
			return fmt.Errorf("listing dependents: %w", err)
		}

		for _, dep := range ready {
			if err := transitionTask(ctx, q, now, dep, TaskReady, BlockedNone, ""); err != nil {
				return err
			}
		}

		return nil
	})
}

// FailTask records that a Task failed for good, and blocks every Task that
// depends on it, directly or not. Orca leaves such dependents pending, so a
// run never converges; this departs from Orca on purpose.
func (s *Service) FailTask(ctx context.Context, id, reason string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := transitionTask(ctx, q, now, id, TaskFailed, BlockedNone, reason); err != nil {
			return err
		}

		return blockDependents(ctx, q, now, id)
	})
}

// CancelTask cancels a Task that has not ended and blocks its dependents.
func (s *Service) CancelTask(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := transitionTask(ctx, q, now, id, TaskCanceled, BlockedNone, ""); err != nil {
			return err
		}

		return blockDependents(ctx, q, now, id)
	})
}

// blockDependents walks the dependents of a Task that will never succeed and
// blocks each pending or ready one, then its own dependents.
func blockDependents(ctx context.Context, q *store.Queries, now int64, id string) error {
	queue := []string{id}

	for len(queue) > 0 {
		deps, err := q.ListDependents(ctx, queue[0])
		if err != nil {
			return fmt.Errorf("listing dependents: %w", err)
		}

		queue = queue[1:]

		for _, d := range deps {
			if !canTask(TaskStatus(d.Status), TaskBlocked) {
				continue
			}

			if err := transitionTask(ctx, q, now, d.ID, TaskBlocked, BlockedDependencyFailed, ""); err != nil {
				return err
			}

			queue = append(queue, d.ID)
		}
	}

	return nil
}

// transitionTask moves a Task to "to" if the transition table allows it from
// the Task's current status; otherwise it reports ErrNotFound or ErrConflict.
func transitionTask(ctx context.Context, q *store.Queries, now int64, id string,
	to TaskStatus, reason BlockedReason, result string,
) error {
	finished := sql.NullInt64{}
	if to.terminal() {
		finished = sql.NullInt64{Int64: now, Valid: true}
	}

	n, err := q.TransitionTask(ctx, store.TransitionTaskParams{
		ToStatus: string(to), BlockedReason: nullString(string(reason)), Result: nullString(result),
		Now: now, FinishedAt: finished, ID: id, FromStatuses: taskFrom(to),
	})
	if err != nil {
		return fmt.Errorf("moving task %s to %s: %w", id, to, err)
	}

	if n == 1 {
		return nil
	}

	t, err := q.GetTask(ctx, id)
	if err != nil {
		return notFound(err, "task", id)
	}

	return fmt.Errorf("%w: task %s is %s, cannot become %s", ErrConflict, id, t.Status, to)
}

func transitionRun(ctx context.Context, q *store.Queries, now int64, id string, from, to RunStatus) error {
	if !canRun(from, to) {
		return fmt.Errorf("%w: a run cannot go from %s to %s", ErrConflict, from, to)
	}

	n, err := q.TransitionRun(ctx, store.TransitionRunParams{
		ToStatus: string(to), Now: now, ID: id, FromStatus: string(from),
	})
	if err != nil {
		return fmt.Errorf("moving run %s to %s: %w", id, to, err)
	}

	if n == 1 {
		return nil
	}

	r, err := q.GetRun(ctx, id)
	if err != nil {
		return notFound(err, "run", id)
	}

	return fmt.Errorf("%w: run %s is %s, cannot become %s", ErrConflict, id, r.Status, to)
}

func notFound(err error, kind, id string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, kind, id)
	}

	return fmt.Errorf("reading %s %s: %w", kind, id, err)
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func millis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func optionalTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}

	t := millis(v.Int64)

	return &t
}

func runFromRow(r store.Run) Run {
	return Run{
		ID: r.ID, Objective: r.Objective, Status: RunStatus(r.Status), CreatedBy: r.CreatedBy,
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt), FinishedAt: optionalTime(r.FinishedAt),
	}
}

func taskFromRow(t store.Task) Task {
	return Task{
		ID: t.ID, RunID: t.RunID, Position: int(t.Position), Role: t.Role, Spec: t.Spec,
		Runtime: t.Runtime.String, Model: t.Model.String, Effort: t.Effort.String,
		Status: TaskStatus(t.Status), BlockedReason: BlockedReason(t.BlockedReason.String),
		FailureCount: int(t.FailureCount), Result: t.Result.String,
		CreatedAt: millis(t.CreatedAt), UpdatedAt: millis(t.UpdatedAt), FinishedAt: optionalTime(t.FinishedAt),
	}
}
