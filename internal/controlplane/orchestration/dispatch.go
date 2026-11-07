package orchestration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The Dispatch lifecycle (issue #19, PR 2): the coordinator creates a
// Dispatch for a ready Task, a node accepts it, reports on it under a lease,
// and its final report, a cancel, the reaper, a settlement or an abandon ends
// it. Every change is one transaction that also moves the Task.

// CreateDispatch starts an attempt at a ready Task: it continues the Task's
// open Conversation when spec.Continue is set, or opens a new one on the given
// node, worktree, runtime and account. A Conversation is continued only when
// its last Dispatch ended with a first-hand report; one that was interrupted
// is closed, and the next Dispatch carries the recovery note.
func (s *Service) CreateDispatch(ctx context.Context, taskID string, spec DispatchSpec) (Dispatch, error) {
	id := s.newID("dsp")

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		task, err := q.GetTask(ctx, taskID)
		if err != nil {
			return notFound(err, "task", taskID)
		}

		run, err := q.GetRun(ctx, task.RunID)
		if err != nil {
			return notFound(err, "run", task.RunID)
		}

		switch {
		case RunStatus(run.Status) != RunOpen:
			return fmt.Errorf("%w: run %s is %s", ErrConflict, run.ID, run.Status)
		case TaskStatus(task.Status) != TaskReady:
			return fmt.Errorf("%w: task %s is %s, not ready", ErrConflict, taskID, task.Status)
		case task.NotBefore.Valid && task.NotBefore.Int64 > now:
			return fmt.Errorf("%w: task %s waits until %s", ErrConflict, taskID, millis(task.NotBefore.Int64))
		}

		conv, node, recovery, err := s.conversationFor(ctx, q, now, taskID, spec)
		if err != nil {
			return err
		}

		gate, err := answeredGateBefore(ctx, q, taskID)
		if err != nil {
			return err
		}

		if err := q.InsertDispatch(ctx, store.InsertDispatchParams{
			ID: id, TaskID: taskID, ConversationID: conv, NodeID: node, Recovery: boolInt(recovery),
			GateQuestionID: nullString(gate), Now: now,
		}); err != nil {
			return fmt.Errorf("inserting dispatch: %w", err)
		}

		if err := emit(ctx, q, now, event{
			kind: EventDispatchCreated, run: task.RunID, task: taskID, dispatch: id, node: node,
			payload: map[string]any{"conversation_id": conv, "recovery": recovery, "gate_question_id": gate},
		}); err != nil {
			return err
		}

		return transitionTask(ctx, q, now, taskID, TaskRunning, BlockedNone, "")
	})
	if err != nil {
		return Dispatch{}, fmt.Errorf("creating a dispatch: %w", err)
	}

	return s.GetDispatch(ctx, id)
}

// conversationFor returns the Conversation a new Dispatch runs in, its node,
// and whether the worker must be told the previous attempt was interrupted.
func (s *Service) conversationFor(ctx context.Context, q *store.Queries, now int64, taskID string,
	spec DispatchSpec,
) (string, string, bool, error) {
	latest, err := q.LatestConversationOfTask(ctx, taskID)
	hasLatest := err == nil

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("reading the conversation of task %s: %w", taskID, err)
	}

	open := hasLatest && ConversationStatus(latest.Status) == ConversationOpen

	if spec.Continue {
		if !open {
			return "", "", false, fmt.Errorf("%w: task %s has no open conversation to continue", ErrConflict, taskID)
		}

		if spec.NodeID != "" && spec.NodeID != latest.NodeID {
			return "", "", false, fmt.Errorf("%w: conversation %s is on node %s", ErrInvalid, latest.ID, latest.NodeID)
		}

		if err := checkCooldown(ctx, q, now, latest.NodeID, latest.Runtime, latest.AccountRef); err != nil {
			return "", "", false, err
		}

		return latest.ID, latest.NodeID, false, nil
	}

	if spec.NodeID == "" || spec.Worktree == "" || spec.Runtime == "" || spec.AccountRef == "" {
		return "", "", false, fmt.Errorf("%w: a new conversation needs a node, worktree, runtime and account", ErrInvalid)
	}

	if err := checkCooldown(ctx, q, now, spec.NodeID, spec.Runtime, spec.AccountRef); err != nil {
		return "", "", false, err
	}

	if open {
		if err := closeConversation(ctx, q, now, latest.ID, CloseSuperseded); err != nil {
			return "", "", false, err
		}
	}

	id := s.newID("cnv")
	if err := q.InsertConversation(ctx, store.InsertConversationParams{
		ID: id, TaskID: taskID, NodeID: spec.NodeID, Worktree: spec.Worktree, Runtime: spec.Runtime,
		AccountRef: spec.AccountRef, Now: now,
	}); err != nil {
		return "", "", false, fmt.Errorf("inserting conversation: %w", err)
	}

	recovery := hasLatest && CloseReason(latest.CloseReason.String) == CloseInterrupted

	return id, spec.NodeID, recovery, nil
}

func checkCooldown(ctx context.Context, q *store.Queries, now int64, node, runtime, account string) error {
	c, err := q.GetCooldown(ctx, store.GetCooldownParams{NodeID: node, Runtime: runtime, AccountRef: account, Now: now})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("reading the cooldown of %s/%s/%s: %w", node, runtime, account, err)
	}

	return fmt.Errorf("%w: %s/%s/%s until %s (%s)", ErrCoolingDown, node, runtime, account, millis(c.Until), c.Reason)
}

func closeConversation(ctx context.Context, q *store.Queries, now int64, id string, reason CloseReason) error {
	n, err := q.CloseConversation(ctx, store.CloseConversationParams{
		CloseReason: nullString(string(reason)), Now: now, ID: id,
	})
	if err != nil {
		return fmt.Errorf("closing conversation %s: %w", id, err)
	}

	if n != 1 {
		return nil
	}

	conv, err := q.GetConversation(ctx, id)
	if err != nil {
		return notFound(err, "conversation", id)
	}

	task, err := q.GetTask(ctx, conv.TaskID)
	if err != nil {
		return notFound(err, "task", conv.TaskID)
	}

	return emit(ctx, q, now, event{
		kind: EventConversationClosed, run: task.RunID, task: task.ID, node: conv.NodeID,
		payload: map[string]any{"conversation_id": id, "reason": string(reason)},
	})
}

// AcceptDispatch is a node taking a queued Dispatch. It succeeds only before
// QueueTimeout has passed; each accept starts a new epoch and a lease.
func (s *Service) AcceptDispatch(ctx context.Context, call NodeCall) (Accepted, error) {
	return nodeCall(ctx, s, call, "accept", nil, func(q *store.Queries, now int64, d store.Dispatch) (Accepted, error) {
		lease := now + LeaseDuration.Milliseconds()

		n, err := q.AcceptDispatch(ctx, store.AcceptDispatchParams{
			Now: now, LeaseExpiresAt: lease, ID: d.ID, NodeID: call.NodeID,
			QueuedAfter: now - QueueTimeout.Milliseconds(),
		})
		if err != nil {
			return Accepted{}, fmt.Errorf("accepting dispatch %s: %w", d.ID, err)
		}

		if n != 1 {
			return Accepted{}, fmt.Errorf("%w: dispatch %s is %s or expired", ErrConflict, d.ID, d.Status)
		}

		if err := emitDispatchStatus(ctx, q, now, d, DispatchStarting, map[string]any{"epoch": d.Epoch + 1}); err != nil {
			return Accepted{}, err
		}

		return accepted(ctx, q, d, lease)
	})
}

// accepted is what a node learns when it takes a Dispatch: the new epoch and
// lease, the Conversation's CLI session, whether the worker gets the recovery
// note, and the answered Gate it resumes from.
func accepted(ctx context.Context, q *store.Queries, d store.Dispatch, lease int64) (Accepted, error) {
	conv, err := q.GetConversation(ctx, d.ConversationID)
	if err != nil {
		return Accepted{}, notFound(err, "conversation", d.ConversationID)
	}

	out := Accepted{
		Epoch: int(d.Epoch) + 1, LeaseExpiresAt: millis(lease), Recovery: d.Recovery == 1,
		CLISessionRef: conv.CliSessionRef.String,
	}

	if !d.GateQuestionID.Valid {
		return out, nil
	}

	row, err := q.GetQuestion(ctx, d.GateQuestionID.String)
	if err != nil {
		return Accepted{}, notFound(err, "question", d.GateQuestionID.String)
	}

	gate, err := questionFromRow(row)
	if err != nil {
		return Accepted{}, err
	}

	out.Gate = &gate

	return out, nil
}

// answeredGateBefore returns the answered Gate a new Dispatch of the Task
// resumes from: the Gate raised by the Task's last Dispatch, when that one
// ended gated (Orca puts the resolved gate in the next Dispatch's preamble).
func answeredGateBefore(ctx context.Context, q *store.Queries, taskID string) (string, error) {
	last, err := q.LastDispatchOfTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading the last dispatch of %s: %w", taskID, err)
	}

	if DispatchStatus(last.Status) != DispatchGated {
		return "", nil
	}

	gate, err := q.GateOfDispatch(ctx, last.ID)
	if err != nil {
		return "", notFound(err, "gate of dispatch", last.ID)
	}

	if QuestionStatus(gate.Status) != QuestionAnswered {
		return "", nil
	}

	return gate.ID, nil
}

// Heartbeat renews the lease of a live Dispatch. A lease that has run out is
// never renewed: the Dispatch is, or is about to be, unknown. The reply tells
// the node when a stop was requested; that first reply acknowledges the stop.
func (s *Service) Heartbeat(ctx context.Context, call NodeCall) (Beat, error) {
	return nodeCall(ctx, s, call, "heartbeat", nil, func(q *store.Queries, now int64, d store.Dispatch) (Beat, error) {
		if err := checkEpoch(d, call); err != nil {
			return Beat{}, err
		}

		lease := now + LeaseDuration.Milliseconds()

		n, err := q.RenewLease(ctx, store.RenewLeaseParams{Now: now, LeaseExpiresAt: lease, ID: d.ID, Epoch: d.Epoch})
		if err != nil {
			return Beat{}, fmt.Errorf("renewing the lease of %s: %w", d.ID, err)
		}

		if n != 1 {
			return Beat{}, fmt.Errorf("%w: dispatch %s is %s or its lease ran out", ErrConflict, d.ID, d.Status)
		}

		rows, err := q.ListUndeliveredAnswers(ctx, d.ID)
		if err != nil {
			return Beat{}, fmt.Errorf("reading the answers for %s: %w", d.ID, err)
		}

		answers := make([]Question, 0, len(rows))

		for _, r := range rows {
			question, err := questionFromRow(r)
			if err != nil {
				return Beat{}, err
			}

			answers = append(answers, question)
		}

		return Beat{
			LeaseExpiresAt: millis(lease), StopRequested: DispatchStatus(d.Status) == DispatchStopping, Answers: answers,
		}, nil
	})
}

// ReportStarted is the node saying the worker's CLI is running. processRef
// identifies the process (pid and start time) for a later reattach;
// cliSessionRef is the CLI's own session id, kept on the Conversation.
func (s *Service) ReportStarted(ctx context.Context, call NodeCall, processRef, cliSessionRef string) error {
	if processRef == "" {
		return fmt.Errorf("%w: a started report needs the process identity", ErrInvalid)
	}

	payload := []string{processRef, cliSessionRef}

	return nodeVoid(ctx, s, call, "started", payload, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		n, err := q.MarkStarted(ctx, store.MarkStartedParams{
			Now: now, ProcessRef: nullString(processRef), ID: d.ID, Epoch: d.Epoch,
		})
		if err != nil {
			return fmt.Errorf("marking %s started: %w", d.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: dispatch %s is %s, not starting", ErrConflict, d.ID, d.Status)
		}

		if err := emitDispatchStatus(ctx, q, now, d, DispatchRunning, nil); err != nil {
			return err
		}

		if cliSessionRef == "" {
			return nil
		}

		if err := q.SetCLISessionRef(ctx, store.SetCLISessionRefParams{
			CliSessionRef: nullString(cliSessionRef), Now: now, ID: d.ConversationID,
		}); err != nil {
			return fmt.Errorf("recording the CLI session of %s: %w", d.ID, err)
		}

		return nil
	})
}

// ReportProgress records that the worker did something. It is separate from
// the heartbeat: a live process that does nothing is stalled, not lost.
func (s *Service) ReportProgress(ctx context.Context, call NodeCall) error {
	return nodeVoid(ctx, s, call, "progress", nil, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		n, err := q.MarkProgress(ctx, store.MarkProgressParams{Now: now, ID: d.ID, Epoch: d.Epoch})
		if err != nil {
			return fmt.Errorf("recording progress of %s: %w", d.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: dispatch %s is %s, not running", ErrConflict, d.ID, d.Status)
		}

		return nil
	})
}

// RecordEffect records an outside effect: its intent before it happens, done
// after. A done completes the intent with the same Seq, or stands alone.
func (s *Service) RecordEffect(ctx context.Context, call NodeCall, e Effect) error {
	if e.Seq <= 0 || e.Kind == "" || (e.Phase != EffectIntent && e.Phase != EffectDone) {
		return fmt.Errorf("%w: an effect needs a positive seq, a kind and a phase", ErrInvalid)
	}

	return nodeVoid(ctx, s, call, "effect", e, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		if DispatchStatus(d.Status).settled() {
			return fmt.Errorf("%w: dispatch %s is %s", ErrConflict, d.ID, d.Status)
		}

		n, err := q.RecordEffect(ctx, store.RecordEffectParams{
			DispatchID: d.ID, Seq: int64(e.Seq), Kind: e.Kind, Ref: e.Ref, Phase: string(e.Phase),
			Payload: nullString(e.Payload), Now: now,
		})
		if err != nil {
			return fmt.Errorf("recording an effect of %s: %w", d.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: effect %d of %s is already recorded as %s", ErrConflict, e.Seq, d.ID, e.Phase)
		}

		return nil
	})
}

// resourceKinds lists what a Dispatch can hold on its node.
var resourceKinds = []string{"worktree", "vault_session", "process"}

// RecordResource records something the Dispatch holds on its node. owned
// resources are released by the node after the Dispatch settles, unless its
// Conversation continues; an abandoned Dispatch lists what was not released.
func (s *Service) RecordResource(ctx context.Context, call NodeCall, r Resource) error {
	if !slices.Contains(resourceKinds, r.Kind) || r.Ref == "" || (r.Custody != "owned" && r.Custody != "external") {
		return fmt.Errorf("%w: a resource needs a known kind, a ref and a custody", ErrInvalid)
	}

	payload := []string{r.Kind, r.Ref, r.Custody}

	return nodeVoid(ctx, s, call, "resource", payload, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		if DispatchStatus(d.Status).settled() {
			return fmt.Errorf("%w: dispatch %s is %s", ErrConflict, d.ID, d.Status)
		}

		if err := q.RecordResource(ctx, store.RecordResourceParams{
			DispatchID: d.ID, Kind: r.Kind, Ref: r.Ref, Custody: r.Custody, Now: now,
		}); err != nil {
			return fmt.Errorf("recording a resource of %s: %w", d.ID, err)
		}

		return nil
	})
}

// ReleaseResource records that the node released a resource.
func (s *Service) ReleaseResource(ctx context.Context, call NodeCall, kind, ref string) error {
	payload := []string{kind, ref}

	return nodeVoid(ctx, s, call, "release", payload, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		n, err := q.ReleaseResource(ctx, store.ReleaseResourceParams{Now: now, DispatchID: d.ID, Kind: kind, Ref: ref})
		if err != nil {
			return fmt.Errorf("releasing a resource of %s: %w", d.ID, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: %s %s of %s is not held", ErrNotFound, kind, ref, d.ID)
		}

		return nil
	})
}

// ReportStopped is the node confirming that the worker stopped after a stop
// request. Only a confirmed stop cancels the Task (Omnigent never reports a
// stop it has not confirmed).
func (s *Service) ReportStopped(ctx context.Context, call NodeCall) error {
	return nodeVoid(ctx, s, call, "stopped", nil, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		switch DispatchStatus(d.Status) {
		case DispatchCanceled:
			return nil
		case DispatchStopping, DispatchStopUnknown:
			return s.settle(ctx, q, now, d, DispatchCanceled, FailureNone, nil, nil, true)
		default:
			return fmt.Errorf("%w: dispatch %s is %s; no stop was requested", ErrConflict, d.ID, d.Status)
		}
	})
}

// ReportExited is the node saying the worker process exited without a final
// report. That is never a success (Orca's finished_unverified): the outcome is
// unknown until a report or the coordinator settles it. During a stop, the
// exit is the confirmation of the stop.
func (s *Service) ReportExited(ctx context.Context, call NodeCall) error {
	return nodeVoid(ctx, s, call, "exited", nil, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		switch DispatchStatus(d.Status) {
		case DispatchStopping:
			return s.settle(ctx, q, now, d, DispatchCanceled, FailureNone, nil, nil, true)
		case DispatchStarting, DispatchRunning:
			return markUnknown(ctx, q, now, d, DispatchUnknown, UnknownExitedUnreported)
		default:
			return fmt.Errorf("%w: dispatch %s is %s", ErrConflict, d.ID, d.Status)
		}
	})
}

// reportable lists, per Dispatch status, the outcomes a final report may carry.
// A report from the proven epoch outranks an unknown outcome (Orca's
// worker-report-admission.ts); a report during a stop is refused.
var reportable = map[DispatchStatus][]FailureClass{
	// Never executed: the launch failed, or the account was refused before the CLI ran.
	DispatchStarting: {FailureStartFailed, FailureNeedsAuth, FailureRateLimited},
	DispatchRunning:  {FailureNone, FailureWorkerError, FailureNeedsAuth, FailureRateLimited},
	DispatchUnknown:  {FailureNone, FailureWorkerError, FailureRateLimited},
	// stop_unknown: the worker finished before it saw the stop.
	DispatchStopUnknown: {FailureNone, FailureWorkerError, FailureRateLimited},
}

// ReportFinished is the worker's final report, relayed by the node. With a
// Gate, the worker ended to wait for a decision: the Dispatch ends gated and
// the Task waits for the answer. A settled Dispatch accepts only an identical
// repeat.
func (s *Service) ReportFinished(ctx context.Context, call NodeCall, o Outcome) error {
	if err := validOutcome(o); err != nil {
		return err
	}

	to := DispatchSucceeded

	switch {
	case o.Gate != nil:
		to = DispatchGated
	case o.Class != FailureNone:
		to = DispatchFailed
	}

	result, err := json.Marshal(o.Result)
	if err != nil {
		return fmt.Errorf("encoding the result: %w", err)
	}

	return nodeVoid(ctx, s, call, "finished", o, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		st := DispatchStatus(d.Status)

		if st.settled() {
			if st == to && FailureClass(d.FailureClass.String) == o.Class && d.Result.String == string(result) {
				return nil
			}

			return fmt.Errorf("%w: dispatch %s already ended as %s", ErrConflict, d.ID, d.Status)
		}

		if !slices.Contains(reportable[st], o.Class) {
			return fmt.Errorf("%w: dispatch %s is %s and cannot end as %q", ErrConflict, d.ID, d.Status, o.Class)
		}

		if o.Gate != nil {
			return s.settleGated(ctx, q, now, d, o)
		}

		return s.settle(ctx, q, now, d, to, o.Class, &o.Result, o.ResetsAt, true)
	})
}

func validOutcome(o Outcome) error {
	switch {
	case o.Gate != nil && o.Class != FailureNone:
		return fmt.Errorf("%w: a gate ends a dispatch without a failure", ErrInvalid)
	case o.Gate != nil:
		return validAsk(*o.Gate)
	case o.Class == FailureNone && strings.TrimSpace(o.Result.Summary) == "":
		return fmt.Errorf("%w: a success needs a summary", ErrInvalid)
	case o.Class == FailureLost:
		return fmt.Errorf("%w: only the coordinator settles a dispatch as lost", ErrInvalid)
	default:
		return nil
	}
}

// settleGated ends a Dispatch whose worker stopped to wait for a decision,
// and opens the Gate: the Task is blocked until it is answered, and the next
// Dispatch resumes the Conversation with the answer (Orca's createGate). A
// Task whose cancel was requested is canceled instead.
func (s *Service) settleGated(ctx context.Context, q *store.Queries, now int64, d store.Dispatch, o Outcome) error {
	if err := s.settle(ctx, q, now, d, DispatchGated, FailureNone, &o.Result, nil, true); err != nil {
		return err
	}

	task, err := q.GetTask(ctx, d.TaskID)
	if err != nil {
		return notFound(err, "task", d.TaskID)
	}

	if TaskStatus(task.Status).terminal() {
		return nil
	}

	if _, err := s.insertQuestion(ctx, q, now, d, task, QuestionGate, *o.Gate); err != nil {
		return err
	}

	return transitionTask(ctx, q, now, d.TaskID, TaskBlocked, BlockedGate, "")
}

// ReattachDispatch is a node that came back (a laptop that slept) proving its
// worker still runs: the same node, the same process, the epoch it last held,
// and no replacement started meanwhile (an unknown Dispatch blocks any). It
// gets a new epoch. Omnara and Coder never revive a lost attempt; Omnigent
// holds a sleeping host's turn, which suits long CLI sessions.
func (s *Service) ReattachDispatch(ctx context.Context, call NodeCall, processRef string) (Accepted, error) {
	return nodeCall(ctx, s, call, "reattach", processRef,
		func(q *store.Queries, now int64, d store.Dispatch) (Accepted, error) {
			lease := now + LeaseDuration.Milliseconds()

			n, err := q.Reattach(ctx, store.ReattachParams{
				Now: now, LeaseExpiresAt: lease, ID: d.ID, Epoch: int64(call.Epoch), NodeID: call.NodeID,
				ProcessRef: processRef,
			})
			if err != nil {
				return Accepted{}, fmt.Errorf("reattaching %s: %w", d.ID, err)
			}

			if n != 1 {
				return Accepted{}, fmt.Errorf("%w: dispatch %s cannot be reattached (%s, epoch %d)",
					ErrConflict, d.ID, d.Status, d.Epoch)
			}

			if err := emitDispatchStatus(ctx, q, now, d, DispatchRunning,
				map[string]any{"epoch": d.Epoch + 1, "reattached": true}); err != nil {
				return Accepted{}, err
			}

			task, err := q.GetTask(ctx, d.TaskID)
			if err != nil {
				return Accepted{}, notFound(err, "task", d.TaskID)
			}

			if err := transitionTask(ctx, q, now, d.TaskID, TaskRunning, BlockedNone, ""); err != nil {
				return Accepted{}, err
			}

			if task.CancelRequestedAt.Valid {
				running := d
				running.Status, running.Epoch = string(DispatchRunning), d.Epoch+1

				if err := requestStop(ctx, q, now, running); err != nil {
					return Accepted{}, err
				}
			}

			return accepted(ctx, q, d, lease)
		})
}

// SettleUnknown is the coordinator deciding an unknown Dispatch after checking
// its effects: it succeeded, or it is lost (a counted failure). The decision is
// not first-hand, so the Conversation is closed.
func (s *Service) SettleUnknown(ctx context.Context, id string, st Settlement) error {
	if st.Succeeded && strings.TrimSpace(st.Result.Summary) == "" {
		return fmt.Errorf("%w: a success needs a summary", ErrInvalid)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		d, err := q.GetDispatch(ctx, id)
		if err != nil {
			return notFound(err, "dispatch", id)
		}

		if !slices.Contains([]DispatchStatus{DispatchUnknown, DispatchStopUnknown}, DispatchStatus(d.Status)) {
			return fmt.Errorf("%w: dispatch %s is %s, not unknown", ErrConflict, id, d.Status)
		}

		if st.Succeeded {
			return s.settle(ctx, q, now, d, DispatchSucceeded, FailureNone, &st.Result, nil, false)
		}

		return s.settle(ctx, q, now, d, DispatchFailed, FailureLost, &st.Result, nil, false)
	})
}

// AbandonDispatch gives up finding out what an unknown Dispatch did. The Task
// counts it as lost, and the resources it may still hold are returned.
func (s *Service) AbandonDispatch(ctx context.Context, id string) ([]Resource, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		d, err := q.GetDispatch(ctx, id)
		if err != nil {
			return notFound(err, "dispatch", id)
		}

		return s.settle(ctx, q, now, d, DispatchAbandoned, FailureNone, nil, nil, false)
	})
	if err != nil {
		return nil, err
	}

	all, err := s.ListResources(ctx, id)
	if err != nil {
		return nil, err
	}

	held := []Resource{}

	for _, r := range all {
		if r.ReleasedAt == nil {
			held = append(held, r)
		}
	}

	return held, nil
}

// settle ends a Dispatch and moves its Task in the same transaction. A Task
// whose cancel was requested is canceled whatever the outcome. firstHand is
// false when the coordinator decided instead of the worker; the Conversation
// is then closed, and the next Dispatch starts fresh with the recovery note.
func (s *Service) settle(ctx context.Context, q *store.Queries, now int64, d store.Dispatch,
	to DispatchStatus, class FailureClass, result *Result, resetsAt *time.Time, firstHand bool,
) error {
	encoded := sql.NullString{}

	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encoding the result: %w", err)
		}

		encoded = sql.NullString{String: string(b), Valid: true}
	}

	n, err := q.SettleDispatch(ctx, store.SettleDispatchParams{
		Now: now, ToStatus: string(to), FailureClass: nullString(string(class)), Result: encoded,
		ResetsAt: nullMillis(resetsAt), ID: d.ID, FromStatuses: dispatchFrom(to),
	})
	if err != nil {
		return fmt.Errorf("moving dispatch %s to %s: %w", d.ID, to, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: dispatch %s is %s, cannot become %s", ErrConflict, d.ID, d.Status, to)
	}

	if err := emitDispatchStatus(ctx, q, now, d, to, map[string]any{"class": string(class)}); err != nil {
		return err
	}

	// The worker no longer waits for anything (Orca's closeQuestionsForDispatch on every settlement).
	closed, err := q.CancelOpenWaitQuestionsOfDispatch(ctx, store.CancelOpenWaitQuestionsOfDispatchParams{
		Now: now, DispatchID: d.ID,
	})
	if err != nil {
		return fmt.Errorf("closing the questions of %s: %w", d.ID, err)
	}

	for _, c := range closed {
		if err := emitQuestion(ctx, q, now, EventQuestionCanceled,
			questionRef{id: c.ID, task: c.TaskID, run: c.RunID, dispatch: c.DispatchID},
			map[string]any{"mode": c.Mode, "reason": "dispatch_ended"}); err != nil {
			return err
		}
	}

	conv, err := q.GetConversation(ctx, d.ConversationID)
	if err != nil {
		return notFound(err, "conversation", d.ConversationID)
	}

	switch {
	case !firstHand:
		err = closeConversation(ctx, q, now, conv.ID, CloseInterrupted)
	case to == DispatchExpired && !conv.CliSessionRef.Valid:
		err = closeConversation(ctx, q, now, conv.ID, CloseNeverStarted)
	}

	if err != nil {
		return err
	}

	task, err := q.GetTask(ctx, d.TaskID)
	if err != nil {
		return notFound(err, "task", d.TaskID)
	}

	if task.CancelRequestedAt.Valid {
		return cancelNow(ctx, q, now, d.TaskID)
	}

	return s.afterSettle(ctx, q, now, d, conv, to, class, result, resetsAt)
}

// afterSettle moves the Task once its Dispatch ended, by failure class.
func (s *Service) afterSettle(ctx context.Context, q *store.Queries, now int64, d store.Dispatch,
	conv store.Conversation, to DispatchStatus, class FailureClass, result *Result, resetsAt *time.Time,
) error {
	switch to {
	case DispatchSucceeded:
		return completeTask(ctx, q, now, d.TaskID, result.Summary)
	case DispatchCanceled:
		return cancelNow(ctx, q, now, d.TaskID)
	case DispatchGated:
		// settleGated opens the Gate and blocks the Task.
		return nil
	case DispatchExpired:
		return transitionTask(ctx, q, now, d.TaskID, TaskReady, BlockedNone, "")
	case DispatchAbandoned:
		return s.countFailure(ctx, q, now, d.TaskID, FailureLost)
	case DispatchFailed:
	default:
		return fmt.Errorf("%w: dispatch %s cannot settle as %s", ErrConflict, d.ID, to)
	}

	switch class {
	case FailureNeedsAuth:
		return transitionTask(ctx, q, now, d.TaskID, TaskBlocked, BlockedNeedsAuth, "")
	case FailureRateLimited:
		until := now + RateLimitCooldown.Milliseconds()
		if resetsAt != nil {
			until = max(resetsAt.UnixMilli(), now)
		}

		if err := q.UpsertCooldown(ctx, store.UpsertCooldownParams{
			NodeID: conv.NodeID, Runtime: conv.Runtime, AccountRef: conv.AccountRef, Until: until,
			Reason: string(FailureRateLimited),
		}); err != nil {
			return fmt.Errorf("cooling down %s/%s/%s: %w", conv.NodeID, conv.Runtime, conv.AccountRef, err)
		}

		if err := q.SetTaskNotBefore(ctx, store.SetTaskNotBeforeParams{
			NotBefore: sql.NullInt64{Int64: until, Valid: true}, Now: now, ID: d.TaskID,
		}); err != nil {
			return fmt.Errorf("delaying task %s: %w", d.TaskID, err)
		}

		return transitionTask(ctx, q, now, d.TaskID, TaskReady, BlockedNone, "")
	default:
		return s.countFailure(ctx, q, now, d.TaskID, class)
	}
}

// countFailure records a failure that counts toward the breaker: the Task is
// retried after a back-off, and fails for good at the BreakerFailures-th.
func (s *Service) countFailure(ctx context.Context, q *store.Queries, now int64, taskID string,
	class FailureClass,
) error {
	n, err := q.RecordTaskFailure(ctx, store.RecordTaskFailureParams{Now: now, ID: taskID})
	if err != nil {
		return fmt.Errorf("counting a failure of %s: %w", taskID, err)
	}

	if n >= BreakerFailures {
		return failTask(ctx, q, now, taskID, fmt.Sprintf("%d failed attempts; the last: %s", n, class))
	}

	notBefore := now + backoff(int(n), s.random()).Milliseconds()

	if err := q.SetTaskNotBefore(ctx, store.SetTaskNotBeforeParams{
		NotBefore: sql.NullInt64{Int64: notBefore, Valid: true}, Now: now, ID: taskID,
	}); err != nil {
		return fmt.Errorf("delaying task %s: %w", taskID, err)
	}

	return transitionTask(ctx, q, now, taskID, TaskReady, BlockedNone, "")
}

// markUnknown records that a Dispatch's outcome is unknown. Its Task is
// blocked until the Dispatch is reattached, reported or settled; nothing
// starts a replacement meanwhile.
func markUnknown(ctx context.Context, q *store.Queries, now int64, d store.Dispatch,
	to DispatchStatus, cause UnknownCause,
) error {
	n, err := q.MarkUnknown(ctx, store.MarkUnknownParams{
		ToStatus: string(to), UnknownCause: nullString(string(cause)), Now: now, ID: d.ID,
		FromStatuses: dispatchFrom(to),
	})
	if err != nil {
		return fmt.Errorf("moving dispatch %s to %s: %w", d.ID, to, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: dispatch %s is %s, cannot become %s", ErrConflict, d.ID, d.Status, to)
	}

	if err := emitDispatchStatus(ctx, q, now, d, to, map[string]any{"cause": string(cause)}); err != nil {
		return err
	}

	return transitionTask(ctx, q, now, d.TaskID, TaskBlocked, BlockedDispatchUnknown, "")
}

// requestStop asks a starting or running Dispatch to stop.
func requestStop(ctx context.Context, q *store.Queries, now int64, d store.Dispatch) error {
	n, err := q.RequestStop(ctx, store.RequestStopParams{Now: now, ID: d.ID})
	if err != nil {
		return fmt.Errorf("stopping dispatch %s: %w", d.ID, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: dispatch %s changed while stopping", ErrConflict, d.ID)
	}

	return emitDispatchStatus(ctx, q, now, d, DispatchStopping, nil)
}

// Reap applies the timers: queued Dispatches expire, lost leases make the
// outcome unknown, unconfirmed stops become stop_unknown, and stalled or
// overlong Dispatches and unavailable nodes are escalated, never killed. It
// also drops ended cooldowns and old receipts. One transaction.
func (s *Service) Reap(ctx context.Context) (Reaped, error) {
	out := Reaped{Expired: []string{}, Unknown: []string{}, StopUnknown: []string{}, Escalations: []Escalation{}}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := s.reapQueued(ctx, q, now, &out); err != nil {
			return err
		}

		if err := reapLeases(ctx, q, now, &out); err != nil {
			return err
		}

		if err := reapEscalations(ctx, q, now, &out); err != nil {
			return err
		}

		for _, e := range out.Escalations {
			if err := emitEscalation(ctx, q, now, e); err != nil {
				return err
			}
		}

		return reapHousekeeping(ctx, q, now)
	})
	if err != nil {
		return Reaped{}, fmt.Errorf("reaping: %w", err)
	}

	return out, nil
}

func (s *Service) reapQueued(ctx context.Context, q *store.Queries, now int64, out *Reaped) error {
	ids, err := q.ListQueuedSince(ctx, now-QueueTimeout.Milliseconds())
	if err != nil {
		return fmt.Errorf("listing queued dispatches: %w", err)
	}

	flagged := map[string]bool{}

	for _, id := range ids {
		d, err := q.GetDispatch(ctx, id)
		if err != nil {
			return notFound(err, "dispatch", id)
		}

		if err := s.settle(ctx, q, now, d, DispatchExpired, FailureNone, nil, nil, true); err != nil {
			return err
		}

		out.Expired = append(out.Expired, id)

		recent, err := q.RecentDispatchStatusesOfNode(ctx, store.RecentDispatchStatusesOfNodeParams{
			NodeID: d.NodeID, N: ExpiriesBeforeUnavailable,
		})
		if err != nil {
			return fmt.Errorf("reading recent dispatches of node %s: %w", d.NodeID, err)
		}

		allExpired := len(recent) == ExpiriesBeforeUnavailable &&
			!slices.ContainsFunc(recent, func(st string) bool { return DispatchStatus(st) != DispatchExpired })

		if allExpired && !flagged[d.NodeID] {
			flagged[d.NodeID] = true
			out.Escalations = append(out.Escalations, Escalation{Kind: EscalationNodeUnavailable, NodeID: d.NodeID})
		}
	}

	return nil
}

func reapLeases(ctx context.Context, q *store.Queries, now int64, out *Reaped) error {
	expired, err := q.ListLeaseExpired(ctx, now)
	if err != nil {
		return fmt.Errorf("listing lost leases: %w", err)
	}

	for _, row := range expired {
		d, err := q.GetDispatch(ctx, row.ID)
		if err != nil {
			return notFound(err, "dispatch", row.ID)
		}

		if DispatchStatus(row.Status) == DispatchStopping {
			if err := markUnknown(ctx, q, now, d, DispatchStopUnknown, UnknownLeaseLost); err != nil {
				return err
			}

			out.StopUnknown = append(out.StopUnknown, row.ID)

			continue
		}

		if err := markUnknown(ctx, q, now, d, DispatchUnknown, UnknownLeaseLost); err != nil {
			return err
		}

		out.Unknown = append(out.Unknown, row.ID)
	}

	stops, err := q.ListStopTimedOut(ctx, now-StopTimeout.Milliseconds())
	if err != nil {
		return fmt.Errorf("listing unconfirmed stops: %w", err)
	}

	for _, id := range stops {
		d, err := q.GetDispatch(ctx, id)
		if err != nil {
			return notFound(err, "dispatch", id)
		}

		if err := markUnknown(ctx, q, now, d, DispatchStopUnknown, UnknownStopTimeout); err != nil {
			return err
		}

		out.StopUnknown = append(out.StopUnknown, id)
	}

	return nil
}

func reapEscalations(ctx context.Context, q *store.Queries, now int64, out *Reaped) error {
	stalled, err := q.MarkStalled(ctx, store.MarkStalledParams{Now: now, ProgressBefore: now - StallAfter.Milliseconds()})
	if err != nil {
		return fmt.Errorf("marking stalled dispatches: %w", err)
	}

	for _, r := range stalled {
		out.Escalations = append(out.Escalations, Escalation{Kind: EscalationStalled, DispatchID: r.ID, NodeID: r.NodeID})
	}

	overrun, err := q.MarkOverrun(ctx, store.MarkOverrunParams{Now: now, MaxMs: MaxDuration.Milliseconds()})
	if err != nil {
		return fmt.Errorf("marking overlong dispatches: %w", err)
	}

	for _, r := range overrun {
		out.Escalations = append(out.Escalations, Escalation{Kind: EscalationOverrun, DispatchID: r.ID, NodeID: r.NodeID})
	}

	due, err := q.ListDueReminders(ctx, now)
	if err != nil {
		return fmt.Errorf("listing due reminders: %w", err)
	}

	for _, r := range due {
		if err := q.SetRemindAt(ctx, store.SetRemindAtParams{
			Now: now, RemindAt: sql.NullInt64{Int64: now + ReminderEvery.Milliseconds(), Valid: true}, ID: r.ID,
		}); err != nil {
			return fmt.Errorf("scheduling the next reminder of %s: %w", r.ID, err)
		}

		out.Escalations = append(out.Escalations, Escalation{
			Kind: EscalationQuestionReminder, DispatchID: r.DispatchID, QuestionID: r.ID,
		})
	}

	return nil
}

func reapHousekeeping(ctx context.Context, q *store.Queries, now int64) error {
	if _, err := q.DeleteExpiredCooldowns(ctx, now); err != nil {
		return fmt.Errorf("dropping ended cooldowns: %w", err)
	}

	if _, err := q.DeleteReceiptsBefore(ctx, now-ReceiptRetention.Milliseconds()); err != nil {
		return fmt.Errorf("dropping old receipts: %w", err)
	}

	if _, err := q.TrimReceipts(ctx, ReceiptKeep); err != nil {
		return fmt.Errorf("trimming receipts: %w", err)
	}

	return nil
}

// GetDispatch returns one Dispatch.
func (s *Service) GetDispatch(ctx context.Context, id string) (Dispatch, error) {
	row, err := s.read().GetDispatch(ctx, id)
	if err != nil {
		return Dispatch{}, notFound(err, "dispatch", id)
	}

	return dispatchFromRow(row)
}

// GetConversation returns one Conversation.
func (s *Service) GetConversation(ctx context.Context, id string) (Conversation, error) {
	row, err := s.read().GetConversation(ctx, id)
	if err != nil {
		return Conversation{}, notFound(err, "conversation", id)
	}

	return Conversation{
		ID: row.ID, TaskID: row.TaskID, NodeID: row.NodeID, Worktree: row.Worktree, Runtime: row.Runtime,
		AccountRef: row.AccountRef, CLISessionRef: row.CliSessionRef.String, Status: ConversationStatus(row.Status),
		CloseReason: CloseReason(row.CloseReason.String), CreatedAt: millis(row.CreatedAt),
		UpdatedAt: millis(row.UpdatedAt),
	}, nil
}

// ListEffects returns a Dispatch's outside effects in order.
func (s *Service) ListEffects(ctx context.Context, dispatchID string) ([]Effect, error) {
	rows, err := s.read().ListEffects(ctx, dispatchID)
	if err != nil {
		return nil, fmt.Errorf("listing effects: %w", err)
	}

	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, Effect{
			Seq: int(r.Seq), Kind: r.Kind, Ref: r.Ref, Phase: EffectPhase(r.Phase), Payload: r.Payload.String,
		})
	}

	return out, nil
}

// ListResources returns what a Dispatch holds or held on its node.
func (s *Service) ListResources(ctx context.Context, dispatchID string) ([]Resource, error) {
	rows, err := s.read().ListResources(ctx, dispatchID)
	if err != nil {
		return nil, fmt.Errorf("listing resources: %w", err)
	}

	out := make([]Resource, 0, len(rows))
	for _, r := range rows {
		out = append(out, Resource{Kind: r.Kind, Ref: r.Ref, Custody: r.Custody, ReleasedAt: optionalTime(r.ReleasedAt)})
	}

	return out, nil
}

// nodeCall runs one node call in a transaction, once per request id: a repeat
// with the same payload returns the stored response, another payload is
// refused (Orca's receipts). The Dispatch must belong to the calling node.
func nodeCall[T any](ctx context.Context, s *Service, call NodeCall, method string, payload any,
	fn func(q *store.Queries, now int64, d store.Dispatch) (T, error),
) (T, error) {
	var out T

	if call.NodeID == "" || call.DispatchID == "" || call.RequestID == "" {
		return out, fmt.Errorf("%w: a node call needs the node, the dispatch and a request id", ErrInvalid)
	}

	hash, err := payloadHash(method, call, payload)
	if err != nil {
		return out, err
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		r, err := q.GetReceipt(ctx, store.GetReceiptParams{Caller: call.NodeID, RequestID: call.RequestID})
		if err == nil {
			if r.Method != method || r.PayloadHash != hash {
				return fmt.Errorf("%w: request %s was already used for another call", ErrConflict, call.RequestID)
			}

			if err := json.Unmarshal([]byte(r.Response), &out); err != nil {
				return fmt.Errorf("decoding the stored response of %s: %w", call.RequestID, err)
			}

			return nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading receipt %s: %w", call.RequestID, err)
		}

		d, err := q.GetDispatch(ctx, call.DispatchID)
		if err != nil {
			return notFound(err, "dispatch", call.DispatchID)
		}

		if d.NodeID != call.NodeID {
			return fmt.Errorf("%w: dispatch %s belongs to another node", ErrConflict, d.ID)
		}

		if out, err = fn(q, now, d); err != nil {
			return err
		}

		response, err := json.Marshal(out)
		if err != nil {
			return fmt.Errorf("encoding the response: %w", err)
		}

		if err := q.InsertReceipt(ctx, store.InsertReceiptParams{
			Caller: call.NodeID, RequestID: call.RequestID, Method: method, PayloadHash: hash,
			Response: string(response), Now: now,
		}); err != nil {
			return fmt.Errorf("storing receipt %s: %w", call.RequestID, err)
		}

		return nil
	})

	return out, err
}

// nodeVoid is nodeCall for a call that returns nothing.
func nodeVoid(ctx context.Context, s *Service, call NodeCall, method string, payload any,
	fn func(q *store.Queries, now int64, d store.Dispatch) error,
) error {
	_, err := nodeCall(ctx, s, call, method, payload,
		func(q *store.Queries, now int64, d store.Dispatch) (struct{}, error) {
			return struct{}{}, fn(q, now, d)
		})

	return err
}

func payloadHash(method string, call NodeCall, payload any) (string, error) {
	b, err := json.Marshal(struct {
		Method     string `json:"method"`
		DispatchID string `json:"dispatch_id"`
		Epoch      int    `json:"epoch"`
		Payload    any    `json:"payload"`
	}{method, call.DispatchID, call.Epoch, payload})
	if err != nil {
		return "", fmt.Errorf("encoding the payload: %w", err)
	}

	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:]), nil
}

// checkEpoch refuses a call from an epoch other than the Dispatch's current one.
func checkEpoch(d store.Dispatch, call NodeCall) error {
	if int64(call.Epoch) != d.Epoch {
		return fmt.Errorf("%w: epoch %d of dispatch %s is stale; the current one is %d",
			ErrConflict, call.Epoch, d.ID, d.Epoch)
	}

	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

func dispatchFromRow(d store.Dispatch) (Dispatch, error) {
	out := Dispatch{
		ID: d.ID, TaskID: d.TaskID, ConversationID: d.ConversationID, NodeID: d.NodeID,
		Status: DispatchStatus(d.Status), Epoch: int(d.Epoch), FailureClass: FailureClass(d.FailureClass.String),
		UnknownCause: UnknownCause(d.UnknownCause.String), Recovery: d.Recovery == 1,
		ResetsAt: optionalTime(d.ResetsAt), CreatedAt: millis(d.CreatedAt), UpdatedAt: millis(d.UpdatedAt),
		StartedAt: optionalTime(d.StartedAt), LeaseExpiresAt: optionalTime(d.LeaseExpiresAt),
		ProgressAt: optionalTime(d.ProgressAt), StopRequestedAt: optionalTime(d.StopRequestedAt),
		StalledAt: optionalTime(d.StalledAt), OverrunAt: optionalTime(d.OverrunAt),
		SettledAt: optionalTime(d.SettledAt), WaitingSince: optionalTime(d.WaitingSince),
		Waited: time.Duration(d.WaitedMs) * time.Millisecond, GateQuestionID: d.GateQuestionID.String,
	}

	if d.Result.Valid {
		var r Result
		if err := json.Unmarshal([]byte(d.Result.String), &r); err != nil {
			return Dispatch{}, fmt.Errorf("decoding the result of %s: %w", d.ID, err)
		}

		out.Result = &r
	}

	return out, nil
}
