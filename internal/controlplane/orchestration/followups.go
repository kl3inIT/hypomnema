package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// A follow-up (issue #666) is more work asked of a Task that succeeded, sent
// to the worker that did it rather than to a new Task's worker, which would
// start without what the first one read and changed. The references send
// follow-up work to the agent that holds the context:
//
//   - Orca: a finished worker gets a message "as a follow-up Task and reports
//     with worker_done" (skill-guides/orchestration.md:57-61), and "Send fixes
//     to a worker" (:104).
//   - Polly sends a review's fixes "to the SAME implementer conversation ... A
//     new title would spawn a fresh worker with no memory of the task"
//     (omnigent examples/polly/skills/cross-review/SKILL.md:50-56).
//   - omp: "Prefer messaging an existing agent ... over a fresh spawn for
//     follow-up work: it already holds the relevant context"
//     (docs/tools/task.md:232); Codex's send_input and resume_agent.
//
// The Task goes back to ready, and its next Dispatch continues the open
// Conversation, in the same worktree and branch, with the note in the
// worker's instructions, as a resolved Gate's answer is given
// (answeredGateBefore).

// FollowUp is one note asking more of a Task's worker.
type FollowUp struct {
	ID        string
	TaskID    string
	Note      string
	AskedBy   Addressee
	CreatedAt time.Time
}

// FollowUpTask asks more of a Task that succeeded or that waits at a Gate: it
// keeps the note, moves the Task back to ready with its failure count reset
// (Orca makes it a new follow-up Task, with its own count), and its next
// Dispatch continues the worker's Conversation with the note. Only such a
// Task of an open Run whose latest Conversation is still open can be followed
// up: without that Conversation the worker's context is gone, and the work is
// a new Task.
//
// A follow-up to a Task blocked at a Gate replaces the Gate (issue #714): the
// Gate is closed as superseded, its question.canceled event names the
// follow-up, and the worker continues with the note instead of an answer, as
// Omnara cancels the open questions of a session on new input with the reason
// superseded_by_input (commands_interactions.go:340-362).
func (s *Service) FollowUpTask(ctx context.Context, id string, by Addressee, note string) (FollowUp, error) {
	if by != AddressCoordinator && by != AddressOwner {
		return FollowUp{}, fmt.Errorf("%w: a follow-up comes from the coordinator or the owner", ErrInvalid)
	}

	note = strings.TrimSpace(note)

	switch {
	case note == "":
		return FollowUp{}, fmt.Errorf("%w: a follow-up needs a note", ErrInvalid)
	case utf8.RuneCountInString(note) > MaxFollowUpText:
		return FollowUp{}, fmt.Errorf("%w: a follow-up's note is at most %d characters", ErrInvalid, MaxFollowUpText)
	}

	fid := s.newID("fup")

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		task, err := q.GetTask(ctx, id)
		if err != nil {
			return notFound(err, "task", id)
		}

		run, err := q.GetRun(ctx, task.RunID)
		if err != nil {
			return notFound(err, "run", task.RunID)
		}

		_, open, err := openConversation(ctx, q, id)
		if err != nil {
			return err
		}

		gated := TaskStatus(task.Status) == TaskBlocked && BlockedReason(task.BlockedReason.String) == BlockedGate

		switch {
		case RunStatus(run.Status) != RunOpen:
			return fmt.Errorf("%w: run %s is %s", ErrConflict, run.ID, run.Status)
		case TaskStatus(task.Status) != TaskSucceeded && !gated:
			return fmt.Errorf("%w: task %s is %s; only a task that succeeded or waits at a gate takes a follow-up",
				ErrConflict, id, task.Status)
		case !open:
			return fmt.Errorf("%w: the worker of task %s has no open conversation to continue; "+
				"create a new task whose spec starts from the branch hyp/%s", ErrConflict, id, id)
		}

		if err := q.InsertFollowUp(ctx, store.InsertFollowUpParams{
			ID: fid, TaskID: id, Note: note, AskedBy: string(by), Now: now,
		}); err != nil {
			return fmt.Errorf("inserting the follow-up of task %s: %w", id, err)
		}

		if gated {
			if err := supersedeGates(ctx, q, now, id, fid); err != nil {
				return err
			}
		}

		if err := q.ResetTaskFailures(ctx, store.ResetTaskFailuresParams{Now: now, ID: id}); err != nil {
			return fmt.Errorf("resetting task %s: %w", id, err)
		}

		// Its event is task.status from succeeded (or blocked) to ready; the
		// Dispatch that carries the note names it (dispatch.created).
		return transitionTask(ctx, q, now, id, TaskReady, BlockedNone, "")
	})
	if err != nil {
		return FollowUp{}, err
	}

	row, err := s.read().GetFollowUp(ctx, fid)
	if err != nil {
		return FollowUp{}, notFound(err, "follow-up", fid)
	}

	return followUpFromRow(row), nil
}

// supersedeGates closes the Task's open Gate as replaced by the follow-up
// fid, as cancelNow closes it on a cancel.
func supersedeGates(ctx context.Context, q *store.Queries, now int64, taskID, fid string) error {
	gates, err := q.SupersedeOpenGatesOfTask(ctx, store.SupersedeOpenGatesOfTaskParams{Now: now, TaskID: taskID})
	if err != nil {
		return fmt.Errorf("closing the gate of task %s: %w", taskID, err)
	}

	for _, g := range gates {
		if err := emitQuestion(ctx, q, now, EventQuestionCanceled,
			questionRef{id: g.ID, task: g.TaskID, run: g.RunID, dispatch: g.DispatchID},
			map[string]any{"mode": g.Mode, "reason": "superseded", "follow_up": fid}); err != nil {
			return err
		}
	}

	return nil
}

// ListFollowUps returns a Task's follow-ups, oldest first.
func (s *Service) ListFollowUps(ctx context.Context, taskID string) ([]FollowUp, error) {
	rows, err := s.read().ListFollowUpsOfTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing the follow-ups of task %s: %w", taskID, err)
	}

	out := make([]FollowUp, 0, len(rows))
	for _, r := range rows {
		out = append(out, followUpFromRow(r))
	}

	return out, nil
}

// pendingFollowUp returns the follow-up a new Dispatch of the Task gives its
// worker: the newest one that no Dispatch carrying it has yet done. Every
// Dispatch until then carries it, so a retry in a new Conversation, after a
// lost worker or a handoff, is not left with the first spec only.
func pendingFollowUp(ctx context.Context, q *store.Queries, taskID string) (string, error) {
	f, err := q.PendingFollowUpOfTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading the follow-up of task %s: %w", taskID, err)
	}

	return f.ID, nil
}

func followUpFromRow(r store.TaskFollowUp) FollowUp {
	return FollowUp{
		ID: r.ID, TaskID: r.TaskID, Note: r.Note, AskedBy: Addressee(r.AskedBy), CreatedAt: millis(r.CreatedAt),
	}
}
