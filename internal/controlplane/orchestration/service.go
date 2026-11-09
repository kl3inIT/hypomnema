package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
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
	// random returns a number in [0, 1) for the back-off jitter.
	random func() float64
	// notify wakes event readers after each commit.
	notify *notifier
}

// New returns a Service over an opened, migrated database. The clock and id
// generator are passed in so tests control time and ids.
func New(db *sql.DB, clock platform.Clock, newID func(prefix string) string, opts ...ServiceOption) *Service {
	s := &Service{db: db, clock: clock, newID: newID, random: rand.Float64, notify: newNotifier()}
	for _, o := range opts {
		o(s)
	}

	return s
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

	s.notify.signal()

	return nil
}

func (s *Service) read() *store.Queries {
	return store.New(s.db)
}

// CreateRun records a new objective from the owner or a coordinator.
func (s *Service) CreateRun(ctx context.Context, nr NewRun) (Run, error) {
	return s.CreateRunOnce(ctx, Once{}, nr)
}

// CreateRunOnce is CreateRun with an idempotency key.
func (s *Service) CreateRunOnce(ctx context.Context, o Once, nr NewRun) (Run, error) {
	objective, createdBy := nr.Objective, nr.CreatedBy
	if strings.TrimSpace(objective) == "" || createdBy == "" {
		return Run{}, fmt.Errorf("%w: a run needs an objective and its creator", ErrInvalid)
	}

	if nr.Repo != "" && !validRepo(nr.Repo) {
		return Run{}, fmt.Errorf("%w: repository %q is not owner/name", ErrInvalid, nr.Repo)
	}

	base := ""
	if nr.Repo != "" {
		base = or(nr.Base, DefaultBase)
	}

	var id string

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		id, err = once(ctx, q, now, o, "runs.create", nr, func() (string, error) {
			if nr.ChatID != "" {
				if _, err := q.GetChat(ctx, nr.ChatID); err != nil {
					return "", notFound(err, "chat", nr.ChatID)
				}
			}

			id := s.newID("run")

			if err := q.InsertRun(ctx, store.InsertRunParams{
				ID: id, Objective: objective, CreatedBy: createdBy, Repo: nullString(nr.Repo), Base: nullString(base),
				CoordinatorTokenID: nullString(nr.CoordinatorTokenID), ChatID: nullString(nr.ChatID), Now: now,
			}); err != nil {
				return "", fmt.Errorf("inserting run: %w", err)
			}

			return id, emit(ctx, q, now, event{
				kind: EventRunCreated, run: id, payload: map[string]any{"created_by": createdBy},
			})
		})

		return err
	})
	if err != nil {
		return Run{}, fmt.Errorf("creating a run: %w", err)
	}

	return s.GetRun(ctx, id)
}

// ListRuns returns Runs newest first, a page at a time. The limit defaults
// to RunPageDefault and is capped at RunPageMax.
func (s *Service) ListRuns(ctx context.Context, f RunFilter, after *RunCursor, limit int) (RunPage, error) {
	if limit <= 0 {
		limit = RunPageDefault
	}

	limit = min(limit, RunPageMax)

	p := store.ListRunsParams{Status: string(f.Status), Lim: int64(limit) + 1}
	if after != nil {
		p.HasCursor, p.CursorAt, p.CursorID = 1, after.CreatedAt.UnixMilli(), after.ID
	}

	rows, err := s.read().ListRuns(ctx, p)
	if err != nil {
		return RunPage{}, fmt.Errorf("listing runs: %w", err)
	}

	page := RunPage{Runs: make([]Run, 0, min(len(rows), limit))}
	for _, r := range rows[:min(len(rows), limit)] {
		page.Runs = append(page.Runs, runFromRow(r))
	}

	if len(rows) > limit {
		last := page.Runs[len(page.Runs)-1]
		page.Next = &RunCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	return page, nil
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

// CancelRun cancels a Run and every Task in it that has not ended. A Task
// whose Dispatch may still be running is canceled once that Dispatch stops
// (Omnigent's _best_effort_stop walks every running worker the same way).
func (s *Service) CancelRun(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := transitionRun(ctx, q, now, id, RunOpen, RunCanceled); err != nil {
			return err
		}

		ids, err := q.ListUnfinishedTaskIDs(ctx, store.ListUnfinishedTaskIDsParams{RunID: id, Statuses: unfinished()})
		if err != nil {
			return fmt.Errorf("listing the run's tasks: %w", err)
		}

		for _, t := range ids {
			// An earlier Task's cancel may have ended this one already.
			task, err := q.GetTask(ctx, t)
			if err != nil {
				return notFound(err, "task", t)
			}

			if TaskStatus(task.Status).terminal() {
				continue
			}

			if err := s.cancelTask(ctx, q, now, t); err != nil {
				return err
			}
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
	return s.CreateTasksOnce(ctx, Once{}, runID, tasks)
}

// CreateTasksOnce is CreateTasks with an idempotency key.
func (s *Service) CreateTasksOnce(ctx context.Context, o Once, runID string, tasks []NewTask) ([]Task, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: no tasks", ErrInvalid)
	}

	var ids []string

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		ids, err = once(ctx, q, now, o, "tasks.create", struct {
			RunID string
			Tasks []NewTask
		}{runID, tasks}, func() ([]string, error) {
			return s.insertTasks(ctx, q, now, runID, tasks)
		})

		return err
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

func (s *Service) insertTasks(ctx context.Context, q *store.Queries, now int64, runID string, tasks []NewTask,
) ([]string, error) {
	run, err := q.GetRun(ctx, runID)
	if err != nil {
		return nil, notFound(err, "run", runID)
	}

	if RunStatus(run.Status) != RunOpen {
		return nil, fmt.Errorf("%w: run %s is %s", ErrConflict, runID, run.Status)
	}

	position, err := q.NextTaskPosition(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("reading the next task position: %w", err)
	}

	ids := make([]string, 0, len(tasks))
	byRef := map[string]string{}
	status := map[string]depState{}

	for i, nt := range tasks {
		if nt.Profile == "" {
			nt.Profile = DefaultProfile
		}

		if err := validNewTask(nt, byRef); err != nil {
			return nil, err
		}

		if err := checkTaskProfile(ctx, q, nt); err != nil {
			return nil, err
		}

		deps, err := resolveDeps(ctx, q, runID, nt.DependsOn, byRef, status)
		if err != nil {
			return nil, err
		}

		id := s.newID("tsk")
		st, reason := initialStatus(deps, status)

		if err := q.InsertTask(ctx, store.InsertTaskParams{
			ID: id, RunID: runID, Position: position + int64(i), Profile: nt.Profile, Spec: nt.Spec,
			Runtime: nullString(nt.Runtime), Model: nullString(nt.Model), Effort: nullString(nt.Effort),
			Status: string(st), BlockedReason: nullString(string(reason)), Now: now,
		}); err != nil {
			return nil, fmt.Errorf("inserting task %q: %w", nt.Ref, err)
		}

		for _, dep := range deps {
			if err := q.InsertTaskDep(ctx, store.InsertTaskDepParams{TaskID: id, DependsOn: dep}); err != nil {
				return nil, fmt.Errorf("inserting a dependency of %q: %w", nt.Ref, err)
			}
		}

		if err := emit(ctx, q, now, event{
			kind: EventTaskCreated, run: runID, task: id,
			payload: map[string]any{"status": string(st), "reason": string(reason)},
		}); err != nil {
			return nil, err
		}

		byRef[nt.Ref] = id
		status[id] = depState{st, reason}
		ids = append(ids, id)
	}

	return ids, nil
}

func validNewTask(nt NewTask, byRef map[string]string) error {
	switch {
	case nt.Ref == "":
		return fmt.Errorf("%w: a task needs a ref", ErrInvalid)
	case byRef[nt.Ref] != "":
		return fmt.Errorf("%w: ref %q is used twice", ErrInvalid, nt.Ref)
	case strings.TrimSpace(nt.Spec) == "":
		return fmt.Errorf("%w: task %q needs a spec", ErrInvalid, nt.Ref)
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

// completeTask records a Task's success and makes ready every dependent whose
// dependencies have now all succeeded, in the same transaction (Orca's
// promoteReadyTasks).
func completeTask(ctx context.Context, q *store.Queries, now int64, id, result string) error {
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
}

// failTask records that a Task failed for good, and blocks every Task that
// depends on it, directly or not. Orca leaves such dependents pending, so a
// run never converges; this departs from Orca on purpose.
func failTask(ctx context.Context, q *store.Queries, now int64, id, reason string) error {
	if err := transitionTask(ctx, q, now, id, TaskFailed, BlockedNone, reason); err != nil {
		return err
	}

	return blockDependents(ctx, q, now, id)
}

// cancelNow cancels a Task that has no Dispatch left running, closes its open
// Gate, and blocks its dependents.
func cancelNow(ctx context.Context, q *store.Queries, now int64, id string) error {
	if err := transitionTask(ctx, q, now, id, TaskCanceled, BlockedNone, ""); err != nil {
		return err
	}

	gates, err := q.CancelOpenGatesOfTask(ctx, store.CancelOpenGatesOfTaskParams{Now: now, TaskID: id})
	if err != nil {
		return fmt.Errorf("closing the gate of task %s: %w", id, err)
	}

	for _, g := range gates {
		if err := emitQuestion(ctx, q, now, EventQuestionCanceled,
			questionRef{id: g.ID, task: g.TaskID, run: g.RunID, dispatch: g.DispatchID},
			map[string]any{"mode": g.Mode, "reason": "task_canceled"}); err != nil {
			return err
		}
	}

	return blockDependents(ctx, q, now, id)
}

// CancelTask cancels a Task that has not ended. If its Dispatch may still be
// running, the Dispatch is asked to stop and the Task is canceled only once
// the stop is confirmed or the Dispatch otherwise settles, so no second worker
// starts while the first may still push (Orca's beginWorkerStop).
func (s *Service) CancelTask(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return s.cancelTask(ctx, q, now, id)
	})
}

func (s *Service) cancelTask(ctx context.Context, q *store.Queries, now int64, id string) error {
	task, err := q.GetTask(ctx, id)
	if err != nil {
		return notFound(err, "task", id)
	}

	if TaskStatus(task.Status).terminal() {
		return fmt.Errorf("%w: task %s is %s", ErrConflict, id, task.Status)
	}

	d, err := q.ActiveDispatchOfTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cancelNow(ctx, q, now, id)
	}

	if err != nil {
		return fmt.Errorf("reading the dispatch of task %s: %w", id, err)
	}

	if err := q.RequestTaskCancel(ctx, store.RequestTaskCancelParams{Now: now, ID: id}); err != nil {
		return fmt.Errorf("requesting the cancel of task %s: %w", id, err)
	}

	switch DispatchStatus(d.Status) {
	case DispatchQueued:
		// No node has it yet, so nothing can be running.
		return s.settle(ctx, q, now, d, DispatchCanceled, FailureNone, nil, nil, true)
	case DispatchStarting, DispatchRunning:
		return requestStop(ctx, q, now, d)
	default:
		// stopping, unknown, stop_unknown: the cancel applies when the Dispatch settles.
		return nil
	}
}

// ReopenTask gives a failed Task, or one blocked because its account needed a
// login, a new start: its failure count is reset, and the Tasks blocked only
// because of it go back to waiting (Orca resets its breaker the same way,
// failed-start-circuit-breaker.test.ts).
func (s *Service) ReopenTask(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		task, err := q.GetTask(ctx, id)
		if err != nil {
			return notFound(err, "task", id)
		}

		run, err := q.GetRun(ctx, task.RunID)
		if err != nil {
			return notFound(err, "run", task.RunID)
		}

		reopenable := TaskStatus(task.Status) == TaskFailed ||
			(TaskStatus(task.Status) == TaskBlocked && BlockedReason(task.BlockedReason.String) == BlockedNeedsAuth)

		switch {
		case RunStatus(run.Status) != RunOpen:
			return fmt.Errorf("%w: run %s is %s", ErrConflict, run.ID, run.Status)
		case !reopenable:
			return fmt.Errorf("%w: task %s is %s; only a failed task or one that needs a login reopens",
				ErrConflict, id, task.Status)
		}

		if err := q.ResetTaskFailures(ctx, store.ResetTaskFailuresParams{Now: now, ID: id}); err != nil {
			return fmt.Errorf("resetting task %s: %w", id, err)
		}

		if err := transitionTask(ctx, q, now, id, TaskReady, BlockedNone, ""); err != nil {
			return err
		}

		return unblockDependents(ctx, q, now, id)
	})
}

// unblockDependents walks the dependents of a reopened Task and puts back to
// pending each one that was blocked only by dependencies that can now succeed.
func unblockDependents(ctx context.Context, q *store.Queries, now int64, id string) error {
	queue := []string{id}

	for len(queue) > 0 {
		deps, err := q.ListDependents(ctx, queue[0])
		if err != nil {
			return fmt.Errorf("listing dependents: %w", err)
		}

		queue = queue[1:]

		for _, d := range deps {
			t, err := q.GetTask(ctx, d.ID)
			if err != nil {
				return notFound(err, "task", d.ID)
			}

			if TaskStatus(t.Status) != TaskBlocked || BlockedReason(t.BlockedReason.String) != BlockedDependencyFailed {
				continue
			}

			states, err := q.ListDependencyStates(ctx, d.ID)
			if err != nil {
				return fmt.Errorf("reading the dependencies of %s: %w", d.ID, err)
			}

			if slices.ContainsFunc(states, func(st store.ListDependencyStatesRow) bool {
				return depState{TaskStatus(st.Status), BlockedReason(st.BlockedReason.String)}.neverSucceeds()
			}) {
				continue
			}

			if err := transitionTask(ctx, q, now, d.ID, TaskPending, BlockedNone, ""); err != nil {
				return err
			}

			queue = append(queue, d.ID)
		}
	}

	return nil
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
			st := TaskStatus(d.Status)
			if st != TaskPending && st != TaskReady {
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
	current, err := q.GetTask(ctx, id)
	if err != nil {
		return notFound(err, "task", id)
	}

	from := TaskStatus(current.Status)
	if !canTask(from, to) {
		return fmt.Errorf("%w: task %s is %s, cannot become %s", ErrConflict, id, from, to)
	}

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

	if n != 1 {
		return fmt.Errorf("%w: task %s changed while becoming %s", ErrConflict, id, to)
	}

	return emit(ctx, q, now, event{
		kind: EventTaskStatus, run: current.RunID, task: id,
		payload: map[string]any{"from": string(from), "to": string(to), "reason": string(reason)},
	})
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
		kind := EventRunFinished
		if to == RunCanceled {
			kind = EventRunCanceled
		}

		return emit(ctx, q, now, event{kind: kind, run: id})
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

func nullMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}

	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
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
		Repo: r.Repo.String, Base: r.Base.String, CoordinatorTokenID: r.CoordinatorTokenID.String, ChatID: r.ChatID.String,
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt), FinishedAt: optionalTime(r.FinishedAt),
	}
}

func taskFromRow(t store.Task) Task {
	return Task{
		ID: t.ID, RunID: t.RunID, Position: int(t.Position), Profile: t.Profile, Spec: t.Spec,
		Runtime: t.Runtime.String, Model: t.Model.String, Effort: t.Effort.String,
		Status: TaskStatus(t.Status), BlockedReason: BlockedReason(t.BlockedReason.String),
		FailureCount: int(t.FailureCount), Result: t.Result.String,
		CreatedAt: millis(t.CreatedAt), UpdatedAt: millis(t.UpdatedAt), FinishedAt: optionalTime(t.FinishedAt),
		NotBefore: optionalTime(t.NotBefore), CancelRequestedAt: optionalTime(t.CancelRequestedAt),
	}
}

// validRepo reports whether s is owner/name with GitHub's characters.
func validRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false
	}

	for _, r := range owner + name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}

	return name != "." && name != ".."
}
