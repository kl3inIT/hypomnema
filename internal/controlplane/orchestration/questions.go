package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Questions (issue #19, PR 3): a worker asks the coordinator or the owner.
// One concept, as Omnara's interactions, with Orca's two behaviors as a mode:
// a wait question keeps the worker alive while it waits; a Gate ends the
// Dispatch and blocks the Task until it is answered. The first answer wins.

// QuestionMode is whether the worker waits for the answer or ends.
type QuestionMode string

// Question modes.
const (
	// QuestionWait: the worker stays alive and waits; its stall and duration timers pause.
	QuestionWait QuestionMode = "wait"
	// QuestionGate: the Dispatch ended gated; the Task waits for the answer.
	QuestionGate QuestionMode = "gate"
)

// Addressee is who a question is for, and who answered it.
type Addressee string

// Addressees.
const (
	AddressCoordinator Addressee = "coordinator"
	AddressOwner       Addressee = "owner"
)

// QuestionStatus is where a question stands.
type QuestionStatus string

// Question statuses.
const (
	QuestionOpen     QuestionStatus = "open"
	QuestionAnswered QuestionStatus = "answered"
	QuestionCanceled QuestionStatus = "canceled"
)

// Option is one choice offered with a question; AllowsText lets the answer
// add free text to it.
type Option struct {
	Label      string `json:"label"`
	AllowsText bool   `json:"allows_text,omitempty"`
}

// Ask is a question as the worker asks it. No options means a free-text answer.
type Ask struct {
	Text    string    `json:"text"`
	Options []Option  `json:"options,omitempty"`
	To      Addressee `json:"to"`
}

// Answer picks one option (by index) and may add text; a question without
// options takes text alone.
type Answer struct {
	Option *int   `json:"option,omitempty"`
	Text   string `json:"text,omitempty"`
}

// Question is a question with its state and answer.
type Question struct {
	ID           string         `json:"id"`
	DispatchID   string         `json:"dispatch_id"`
	TaskID       string         `json:"task_id"`
	RunID        string         `json:"run_id"`
	Mode         QuestionMode   `json:"mode"`
	AddressedTo  Addressee      `json:"addressed_to"`
	Text         string         `json:"text"`
	Options      []Option       `json:"options"`
	Status       QuestionStatus `json:"status"`
	CancelReason string         `json:"cancel_reason,omitempty"`
	Answer       *Answer        `json:"answer,omitempty"`
	AnsweredBy   Addressee      `json:"answered_by,omitempty"`
	AnsweredAt   *time.Time     `json:"answered_at,omitempty"`
	DeliveredAt  *time.Time     `json:"delivered_at,omitempty"`
	RemindAt     *time.Time     `json:"remind_at,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// QuestionFilter selects questions; empty fields match everything.
type QuestionFilter struct {
	RunID       string
	TaskID      string
	DispatchID  string
	AddressedTo Addressee
	Status      QuestionStatus
}

// AskQuestion is the worker asking a question it waits for. The Dispatch
// stays running and its stall and duration timers pause until the answer is
// delivered. A worker waits on one question at a time.
func (s *Service) AskQuestion(ctx context.Context, call NodeCall, a Ask) (string, error) {
	if err := validAsk(a); err != nil {
		return "", err
	}

	return nodeCall(ctx, s, call, "ask", a, func(q *store.Queries, now int64, d store.Dispatch) (string, error) {
		if err := checkEpoch(d, call); err != nil {
			return "", err
		}

		if DispatchStatus(d.Status) != DispatchRunning {
			return "", fmt.Errorf("%w: dispatch %s is %s, not running", ErrConflict, d.ID, d.Status)
		}

		_, err := q.OpenWaitQuestionOfDispatch(ctx, d.ID)
		if err == nil {
			return "", fmt.Errorf("%w: dispatch %s already waits for an answer", ErrConflict, d.ID)
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("reading the open question of %s: %w", d.ID, err)
		}

		task, err := q.GetTask(ctx, d.TaskID)
		if err != nil {
			return "", notFound(err, "task", d.TaskID)
		}

		id, err := s.insertQuestion(ctx, q, now, d, task, QuestionWait, a)
		if err != nil {
			return "", err
		}

		if err := q.StartWaiting(ctx, store.StartWaitingParams{Now: now, ID: d.ID}); err != nil {
			return "", fmt.Errorf("pausing the timers of %s: %w", d.ID, err)
		}

		return id, nil
	})
}

func (s *Service) insertQuestion(ctx context.Context, q *store.Queries, now int64, d store.Dispatch,
	task store.Task, mode QuestionMode, a Ask,
) (string, error) {
	options, err := json.Marshal(nonNilOptions(a.Options))
	if err != nil {
		return "", fmt.Errorf("encoding the options: %w", err)
	}

	remind := sql.NullInt64{}
	if a.To == AddressOwner {
		remind = sql.NullInt64{Int64: now + ReminderFirst.Milliseconds(), Valid: true}
	}

	id := s.newID("qst")

	if err := q.InsertQuestion(ctx, store.InsertQuestionParams{
		ID: id, DispatchID: d.ID, TaskID: task.ID, RunID: task.RunID, Mode: string(mode), AddressedTo: string(a.To),
		Text: strings.TrimSpace(a.Text), Options: string(options), RemindAt: remind, Now: now,
	}); err != nil {
		return "", fmt.Errorf("inserting a question of %s: %w", d.ID, err)
	}

	return id, nil
}

// AckAnswer is the node confirming it handed the answer to the worker. The
// wait is over: the timers start again.
func (s *Service) AckAnswer(ctx context.Context, call NodeCall, questionID string) error {
	return nodeVoid(ctx, s, call, "ack", questionID, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}

		n, err := q.MarkDelivered(ctx, store.MarkDeliveredParams{Now: now, ID: questionID, DispatchID: d.ID})
		if err != nil {
			return fmt.Errorf("recording the delivery of %s: %w", questionID, err)
		}

		if n != 1 {
			row, err := q.GetQuestion(ctx, questionID)
			if err != nil {
				return notFound(err, "question", questionID)
			}

			if row.DispatchID == d.ID && row.DeliveredAt.Valid {
				return nil
			}

			return fmt.Errorf("%w: question %s has no answer to deliver to %s", ErrConflict, questionID, d.ID)
		}

		if err := q.StopWaiting(ctx, store.StopWaitingParams{Now: now, ID: d.ID}); err != nil {
			return fmt.Errorf("resuming the timers of %s: %w", d.ID, err)
		}

		return nil
	})
}

// AnswerQuestion records the answer to an open question. The first answer
// wins: the same answer from the same answerer again returns the stored one,
// anything else is a conflict (Orca's answer_conflict, Omnara's first writer
// wins). The coordinator answers only questions addressed to it; the owner
// answers any. Answering a Gate makes its Task ready.
func (s *Service) AnswerQuestion(ctx context.Context, id string, by Addressee, a Answer) (Question, error) {
	if by != AddressCoordinator && by != AddressOwner {
		return Question{}, fmt.Errorf("%w: an answer comes from the coordinator or the owner", ErrInvalid)
	}

	encoded, err := json.Marshal(a)
	if err != nil {
		return Question{}, fmt.Errorf("encoding the answer: %w", err)
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetQuestion(ctx, id)
		if err != nil {
			return notFound(err, "question", id)
		}

		question, err := questionFromRow(row)
		if err != nil {
			return err
		}

		if err := validAnswer(question.Options, a); err != nil {
			return err
		}

		switch question.Status {
		case QuestionAnswered:
			if row.Answer.String == string(encoded) && question.AnsweredBy == by {
				return nil
			}

			return fmt.Errorf("%w: question %s already has another answer", ErrConflict, id)
		case QuestionCanceled:
			return fmt.Errorf("%w: question %s was canceled (%s)", ErrConflict, id, question.CancelReason)
		case QuestionOpen:
		}

		if by == AddressCoordinator && question.AddressedTo == AddressOwner {
			return fmt.Errorf("%w: question %s is for the owner", ErrConflict, id)
		}

		n, err := q.AnswerQuestion(ctx, store.AnswerQuestionParams{
			Now: now, Answer: nullString(string(encoded)), AnsweredBy: nullString(string(by)), ID: id,
		})
		if err != nil {
			return fmt.Errorf("answering %s: %w", id, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: question %s changed while answering", ErrConflict, id)
		}

		if question.Mode == QuestionGate {
			return transitionTask(ctx, q, now, question.TaskID, TaskReady, BlockedNone, "")
		}

		return nil
	})
	if err != nil {
		return Question{}, err
	}

	return s.GetQuestion(ctx, id)
}

// ForwardQuestion is the coordinator passing a question it was asked to the
// owner (Orca sends questions to the Run, where the coordinator decides).
func (s *Service) ForwardQuestion(ctx context.Context, id string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.ForwardQuestion(ctx, store.ForwardQuestionParams{
			Now: now, RemindAt: sql.NullInt64{Int64: now + ReminderFirst.Milliseconds(), Valid: true}, ID: id,
		})
		if err != nil {
			return fmt.Errorf("forwarding %s: %w", id, err)
		}

		if n == 1 {
			return nil
		}

		row, err := q.GetQuestion(ctx, id)
		if err != nil {
			return notFound(err, "question", id)
		}

		return fmt.Errorf("%w: question %s is %s and addressed to the %s", ErrConflict, id, row.Status, row.AddressedTo)
	})
}

// GetQuestion returns one question.
func (s *Service) GetQuestion(ctx context.Context, id string) (Question, error) {
	row, err := s.read().GetQuestion(ctx, id)
	if err != nil {
		return Question{}, notFound(err, "question", id)
	}

	return questionFromRow(row)
}

// ListQuestions returns questions in the order they were asked. One call
// feeds every place a question appears: the Run's chat with the coordinator,
// the worker's Conversation, and the list of what waits for the owner.
func (s *Service) ListQuestions(ctx context.Context, f QuestionFilter) ([]Question, error) {
	rows, err := s.read().ListQuestions(ctx, store.ListQuestionsParams{
		RunID: f.RunID, TaskID: f.TaskID, DispatchID: f.DispatchID, AddressedTo: string(f.AddressedTo),
		Status: string(f.Status),
	})
	if err != nil {
		return nil, fmt.Errorf("listing questions: %w", err)
	}

	out := make([]Question, 0, len(rows))

	for _, r := range rows {
		question, err := questionFromRow(r)
		if err != nil {
			return nil, err
		}

		out = append(out, question)
	}

	return out, nil
}

func validAsk(a Ask) error {
	text := strings.TrimSpace(a.Text)

	switch {
	case text == "" || utf8.RuneCountInString(text) > MaxQuestionText:
		return fmt.Errorf("%w: a question needs a text of at most %d characters", ErrInvalid, MaxQuestionText)
	case len(a.Options) > MaxOptions:
		return fmt.Errorf("%w: a question offers at most %d options", ErrInvalid, MaxOptions)
	case a.To != AddressCoordinator && a.To != AddressOwner:
		return fmt.Errorf("%w: a question is for the coordinator or the owner", ErrInvalid)
	}

	for _, o := range a.Options {
		if strings.TrimSpace(o.Label) == "" {
			return fmt.Errorf("%w: every option needs a label", ErrInvalid)
		}
	}

	return nil
}

func validAnswer(options []Option, a Answer) error {
	if utf8.RuneCountInString(a.Text) > MaxAnswerText {
		return fmt.Errorf("%w: an answer's text is at most %d characters", ErrInvalid, MaxAnswerText)
	}

	if len(options) == 0 {
		if a.Option != nil || strings.TrimSpace(a.Text) == "" {
			return fmt.Errorf("%w: this question takes a text answer", ErrInvalid)
		}

		return nil
	}

	if a.Option == nil || *a.Option < 0 || *a.Option >= len(options) {
		return fmt.Errorf("%w: pick one of the %d options", ErrInvalid, len(options))
	}

	if a.Text != "" && !options[*a.Option].AllowsText {
		return fmt.Errorf("%w: option %q takes no text", ErrInvalid, options[*a.Option].Label)
	}

	return nil
}

func nonNilOptions(o []Option) []Option {
	if o == nil {
		return []Option{}
	}

	return o
}

func questionFromRow(r store.Question) (Question, error) {
	out := Question{
		ID: r.ID, DispatchID: r.DispatchID, TaskID: r.TaskID, RunID: r.RunID, Mode: QuestionMode(r.Mode),
		AddressedTo: Addressee(r.AddressedTo), Text: r.Text, Status: QuestionStatus(r.Status),
		CancelReason: r.CancelReason.String, AnsweredBy: Addressee(r.AnsweredBy.String),
		AnsweredAt: optionalTime(r.AnsweredAt), DeliveredAt: optionalTime(r.DeliveredAt),
		RemindAt: optionalTime(r.RemindAt), CreatedAt: millis(r.CreatedAt),
	}

	if err := json.Unmarshal([]byte(r.Options), &out.Options); err != nil {
		return Question{}, fmt.Errorf("decoding the options of %s: %w", r.ID, err)
	}

	if r.Answer.Valid {
		var a Answer
		if err := json.Unmarshal([]byte(r.Answer.String), &a); err != nil {
			return Question{}, fmt.Errorf("decoding the answer of %s: %w", r.ID, err)
		}

		out.Answer = &a
	}

	return out, nil
}
