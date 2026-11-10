package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The event log (issue #19, PR 4): every change writes its events in the
// same transaction. Readers follow it with a cursor (Omnara's after_sequence)
// and are woken by a signal that carries no data (Coder's pubsub rule: re-read
// from the cursor, tolerate spurious wake-ups).

// EventKind names what changed.
type EventKind string

// Event kinds.
const (
	EventRunCreated         EventKind = "run.created"
	EventRunFinished        EventKind = "run.finished"
	EventRunCanceled        EventKind = "run.canceled"
	EventTaskCreated        EventKind = "task.created"
	EventTaskStatus         EventKind = "task.status"
	EventDispatchCreated    EventKind = "dispatch.created"
	EventDispatchStatus     EventKind = "dispatch.status"
	EventConversationClosed EventKind = "conversation.closed"
	EventQuestionOpened     EventKind = "question.opened"
	EventQuestionAnswered   EventKind = "question.answered"
	EventQuestionForwarded  EventKind = "question.forwarded"
	EventQuestionCanceled   EventKind = "question.canceled"
	EventEscalation         EventKind = "escalation"
)

// Event is one entry of the log. Payload is a small JSON object of ids,
// statuses and reasons; content is read from its own record.
type Event struct {
	Seq        int64
	Kind       EventKind
	RunID      string
	TaskID     string
	DispatchID string
	QuestionID string
	NodeID     string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

// EventFilter selects events; empty fields match everything.
type EventFilter struct {
	RunID  string
	TaskID string
}

// EventPage is one page of the log. NextAfter is the cursor for the next
// page; HasMore says whether it already holds more events.
type EventPage struct {
	Events    []Event
	NextAfter int64
	HasMore   bool
}

// LastEventSeq returns the newest event's seq, 0 when there is none. A reader
// that takes it before a snapshot and follows the events after it misses no
// change.
func (s *Service) LastEventSeq(ctx context.Context) (int64, error) {
	seq, err := s.read().LastEventSeq(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the last event: %w", err)
	}

	return seq, nil
}

// ListEvents returns the events after a sequence number, oldest first. The
// limit defaults to EventPageDefault and is capped at EventPageMax (Omnara).
func (s *Service) ListEvents(ctx context.Context, after int64, f EventFilter, limit int) (EventPage, error) {
	if limit <= 0 {
		limit = EventPageDefault
	}

	limit = min(limit, EventPageMax)

	rows, err := s.read().ListEvents(ctx, store.ListEventsParams{
		After: after, RunID: f.RunID, TaskID: f.TaskID, Lim: int64(limit) + 1,
	})
	if err != nil {
		return EventPage{}, fmt.Errorf("listing events: %w", err)
	}

	page := EventPage{Events: make([]Event, 0, min(len(rows), limit)), NextAfter: after, HasMore: len(rows) > limit}

	for _, r := range rows[:min(len(rows), limit)] {
		page.Events = append(page.Events, Event{
			Seq: r.Seq, Kind: EventKind(r.Kind), RunID: r.RunID.String, TaskID: r.TaskID.String,
			DispatchID: r.DispatchID.String, QuestionID: r.QuestionID.String, NodeID: r.NodeID.String,
			Payload: json.RawMessage(r.Payload), CreatedAt: millis(r.CreatedAt),
		})
		page.NextAfter = r.Seq
	}

	return page, nil
}

// Subscribe returns a channel that is signaled after commits, until ctx ends.
// A signal carries no data and may stand for several commits, or for a commit
// that wrote no event; the reader re-reads from its cursor. A slow reader
// never blocks a writer.
func (s *Service) Subscribe(ctx context.Context) <-chan struct{} {
	return s.notify.subscribe(ctx)
}

// notifier fans a wake-up signal out to subscribers.
type notifier struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newNotifier() *notifier { return &notifier{subs: map[chan struct{}]struct{}{}} }

func (n *notifier) subscribe(ctx context.Context) <-chan struct{} {
	c := make(chan struct{}, 1)

	n.mu.Lock()
	n.subs[c] = struct{}{}
	n.mu.Unlock()

	go func() {
		<-ctx.Done()
		n.mu.Lock()
		delete(n.subs, c)
		close(c)
		n.mu.Unlock()
	}()

	return c
}

func (n *notifier) signal() {
	n.mu.Lock()
	defer n.mu.Unlock()

	for c := range n.subs {
		select {
		case c <- struct{}{}:
		default:
			// A signal is already pending; the reader will see this commit too.
		}
	}
}

// event is one entry to write.
type event struct {
	kind                                EventKind
	run, task, dispatch, question, node string
	payload                             map[string]any
}

func emit(ctx context.Context, q *store.Queries, now int64, e event) error {
	payload := e.payload
	if payload == nil {
		payload = map[string]any{}
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding the %s event: %w", e.kind, err)
	}

	if err := q.InsertEvent(ctx, store.InsertEventParams{
		Kind: string(e.kind), RunID: nullString(e.run), TaskID: nullString(e.task),
		DispatchID: nullString(e.dispatch), QuestionID: nullString(e.question), NodeID: nullString(e.node),
		Payload: string(b), Now: now,
	}); err != nil {
		return fmt.Errorf("writing the %s event: %w", e.kind, err)
	}

	return nil
}

// emitDispatchStatus writes a Dispatch's status change. extra adds the
// failure class, the unknown cause or the new epoch.
func emitDispatchStatus(ctx context.Context, q *store.Queries, now int64, d store.Dispatch,
	to DispatchStatus, extra map[string]any,
) error {
	task, err := q.GetTask(ctx, d.TaskID)
	if err != nil {
		return notFound(err, "task", d.TaskID)
	}

	payload := map[string]any{"from": d.Status, "to": string(to), "epoch": d.Epoch}
	for k, v := range extra {
		payload[k] = v
	}

	return emit(ctx, q, now, event{
		kind: EventDispatchStatus, run: task.RunID, task: task.ID, dispatch: d.ID, node: d.NodeID, payload: payload,
	})
}

// emitQuestion writes a question event.
func emitQuestion(ctx context.Context, q *store.Queries, now int64, kind EventKind,
	r questionRef, payload map[string]any,
) error {
	return emit(ctx, q, now, event{
		kind: kind, run: r.run, task: r.task, dispatch: r.dispatch, question: r.id, payload: payload,
	})
}

// questionRef is what an event needs to know about a question.
type questionRef struct{ id, task, run, dispatch string }

// emitEscalation writes an escalation found by the reaper, with the Run and
// Task it concerns.
func emitEscalation(ctx context.Context, q *store.Queries, now int64, e Escalation) error {
	ev := event{
		kind: EventEscalation, dispatch: e.DispatchID, question: e.QuestionID, node: e.NodeID,
		task: e.TaskID, payload: map[string]any{"kind": string(e.Kind)},
	}

	if e.Reason != "" {
		ev.payload["reason"] = e.Reason
	}

	if e.AccountRef != "" {
		ev.payload["account_ref"] = e.AccountRef
	}

	if e.TaskID != "" && e.DispatchID == "" {
		task, err := q.GetTask(ctx, e.TaskID)
		if err != nil {
			return notFound(err, "task", e.TaskID)
		}

		ev.run = task.RunID
	}

	if e.DispatchID != "" {
		d, err := q.GetDispatch(ctx, e.DispatchID)
		if err != nil {
			return notFound(err, "dispatch", e.DispatchID)
		}

		task, err := q.GetTask(ctx, d.TaskID)
		if err != nil {
			return notFound(err, "task", d.TaskID)
		}

		ev.task, ev.run = task.ID, task.RunID
		if ev.node == "" {
			ev.node = d.NodeID
		}
	}

	return emit(ctx, q, now, ev)
}
