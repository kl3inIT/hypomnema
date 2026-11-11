package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Approvals (issue #154): a request to a service that a Policy answered ASK
// waits for the owner. An Approval is his answer to one such request; it is
// not a Gate, which is a decision a Task waits on. Onyx keeps the same record
// (its approval row: who asked, the actions, the decoded request, the
// decision and how far it reaches).

// ApprovalStatus is where an Approval stands.
type ApprovalStatus string

// The statuses of an Approval.
const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
)

// ApprovalScope is how far an approval reaches.
type ApprovalScope string

// The scopes of an approval: this one request, or the action for the rest of
// the Run or the chat that asked (Onyx's "approve for the session").
const (
	ScopeOnce    ApprovalScope = "once"
	ScopeSession ApprovalScope = "session"
)

// The answers an action of a Connection may have.
const (
	PolicyAllow = "allow"
	PolicyAsk   = "ask"
	PolicyNever = "never"
)

// maxApprovals bounds one listing.
const maxApprovals = 200

// Field is one line of a decoded request, as the owner reads it: "To", and
// who.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Approval is one request waiting for, or answered by, the owner.
type Approval struct {
	ID          string
	Integration string
	Label       string
	// Action is the action's id in its Integration's catalog; ActionName
	// what the owner reads.
	Action     string
	ActionName string
	// DispatchID, TaskID and RunID are set when a worker asked; ChatID when
	// a chat's coordinator did.
	DispatchID string
	TaskID     string
	RunID      string
	ChatID     string
	// AskedBy names the agent in words: "pi" or "worker".
	AskedBy string
	Title   string
	Payload []Field
	Status  ApprovalStatus
	Scope   ApprovalScope
	// Used says an approval for one request has run.
	Used      bool
	CreatedAt time.Time
	DecidedAt *time.Time
}

// NewApproval is a held request to record.
type NewApproval struct {
	Integration string
	Label       string
	Action      string
	ActionName  string
	DispatchID  string
	ChatID      string
	AskedBy     string
	Title       string
	Payload     []Field
	// MatchHash is a digest of the decoded request: a repeat of the same
	// request has the same one, whatever its bytes.
	MatchHash string
}

// ApprovalFilter selects Approvals; empty fields match everything.
type ApprovalFilter struct {
	Status ApprovalStatus
	ChatID string
	TaskID string
}

// AskApproval answers a held request from what the owner already said, or
// records it for him to answer. granted is true when the request may run
// now: the owner approved the action for the rest of this Run or chat, or
// approved this very request before and it has not run since. Otherwise the
// Approval returned is the one to wait on: the pending one of the same
// request when there is one, a new one when there is not.
func (s *Service) AskApproval(ctx context.Context, n NewApproval) (approval Approval, granted bool, err error) {
	if n.Integration == "" || n.Label == "" || n.Action == "" || n.MatchHash == "" || n.Title == "" {
		return Approval{}, false, fmt.Errorf("%w: an approval needs its connection, action, title and digest", ErrInvalid)
	}

	payload, err := json.Marshal(n.Payload)
	if err != nil {
		return Approval{}, false, fmt.Errorf("encoding the request shown to the owner: %w", err)
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		taskID, runID, err := runOfDispatch(ctx, q, n.DispatchID)
		if err != nil {
			return err
		}

		if runID != "" || n.ChatID != "" {
			sessions, err := q.CountSessionApprovals(ctx, store.CountSessionApprovalsParams{
				Integration: n.Integration, Label: n.Label, Action: n.Action, RunID: runID, ChatID: n.ChatID,
			})
			if err != nil {
				return fmt.Errorf("reading the approvals of the session: %w", err)
			}

			if sessions > 0 {
				granted = true

				return nil
			}
		}

		same, err := q.ListApprovalsByMatch(ctx, store.ListApprovalsByMatchParams{
			MatchHash: n.MatchHash, Integration: n.Integration, Label: n.Label, Action: n.Action,
		})
		if err != nil {
			return fmt.Errorf("reading the approvals of the same request: %w", err)
		}

		for _, row := range same {
			// Only what the same Run or the same chat asked: an approval never crosses to another.
			if row.RunID.String != runID || row.ChatID.String != n.ChatID {
				continue
			}

			switch {
			case row.Status == string(ApprovalApproved) && row.Scope.String == string(ScopeOnce) && !row.UsedAt.Valid:
				used, err := q.UseApproval(ctx, store.UseApprovalParams{ID: row.ID, Now: sql.NullInt64{Int64: now, Valid: true}})
				if err != nil {
					return fmt.Errorf("using approval %s: %w", row.ID, err)
				}

				if used == 1 {
					granted = true
					approval, err = approvalFromRow(row)

					return err
				}
			case row.Status == string(ApprovalPending):
				approval, err = approvalFromRow(row)

				return err
			}
		}

		id := s.newID("apr")
		if err := q.InsertApproval(ctx, store.InsertApprovalParams{
			ID: id, Integration: n.Integration, Label: n.Label, Action: n.Action, ActionName: n.ActionName,
			DispatchID: nullString(n.DispatchID), TaskID: nullString(taskID), RunID: nullString(runID),
			ChatID: nullString(n.ChatID), AskedBy: n.AskedBy, Title: n.Title, Payload: string(payload),
			MatchHash: n.MatchHash, Now: now,
		}); err != nil {
			return fmt.Errorf("recording the approval: %w", err)
		}

		row, err := q.GetApproval(ctx, id)
		if err != nil {
			return fmt.Errorf("reading approval %s: %w", id, err)
		}

		approval, err = approvalFromRow(row)

		return err
	})
	if err != nil {
		return Approval{}, false, err
	}

	return approval, granted, nil
}

// runOfDispatch returns a Dispatch's Task and Run; both empty for no
// Dispatch, or one that is gone.
func runOfDispatch(ctx context.Context, q *store.Queries, dispatchID string) (taskID, runID string, err error) {
	if dispatchID == "" {
		return "", "", nil
	}

	dispatch, err := q.GetDispatch(ctx, dispatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}

	if err != nil {
		return "", "", fmt.Errorf("reading dispatch %s: %w", dispatchID, err)
	}

	task, err := q.GetTask(ctx, dispatch.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatch.TaskID, "", nil
	}

	if err != nil {
		return "", "", fmt.Errorf("reading task %s: %w", dispatch.TaskID, err)
	}

	return task.ID, task.RunID, nil
}

// GetApproval returns one Approval.
func (s *Service) GetApproval(ctx context.Context, id string) (Approval, error) {
	row, err := s.read().GetApproval(ctx, id)
	if err != nil {
		return Approval{}, notFound(err, "approval", id)
	}

	return approvalFromRow(row)
}

// ListApprovals lists Approvals, the newest first.
func (s *Service) ListApprovals(ctx context.Context, f ApprovalFilter) ([]Approval, error) {
	rows, err := s.read().ListApprovals(ctx, store.ListApprovalsParams{
		Status: string(f.Status), ChatID: f.ChatID, TaskID: f.TaskID, MaxRows: maxApprovals,
	})
	if err != nil {
		return nil, fmt.Errorf("listing approvals: %w", err)
	}

	out := make([]Approval, 0, len(rows))

	for _, row := range rows {
		a, err := approvalFromRow(row)
		if err != nil {
			return nil, err
		}

		out = append(out, a)
	}

	return out, nil
}

// DecideApproval records the owner's answer. The first answer wins: the same
// answer again returns the stored Approval, another is a conflict, as a
// question's answer.
func (s *Service) DecideApproval(ctx context.Context, id string, approve bool, scope ApprovalScope) (Approval, error) {
	status := ApprovalRejected
	stored := sql.NullString{}

	if approve {
		if scope != ScopeOnce && scope != ScopeSession {
			return Approval{}, fmt.Errorf("%w: an approval is for this once or for the session", ErrInvalid)
		}

		status, stored = ApprovalApproved, nullString(string(scope))
	}

	var out Approval

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetApproval(ctx, id)
		if err != nil {
			return notFound(err, "approval", id)
		}

		if row.Status != string(ApprovalPending) {
			if row.Status == string(status) && row.Scope == stored {
				out, err = approvalFromRow(row)

				return err
			}

			return fmt.Errorf("%w: approval %s was already answered", ErrConflict, id)
		}

		if stored.String == string(ScopeSession) && !row.RunID.Valid && !row.ChatID.Valid {
			return fmt.Errorf("%w: approval %s has no Run or chat to approve for", ErrInvalid, id)
		}

		if _, err := q.DecideApproval(ctx, store.DecideApprovalParams{
			ID: id, Status: string(status), Scope: stored, Now: sql.NullInt64{Int64: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("answering approval %s: %w", id, err)
		}

		row, err = q.GetApproval(ctx, id)
		if err != nil {
			return fmt.Errorf("reading approval %s: %w", id, err)
		}

		out, err = approvalFromRow(row)

		return err
	})
	if err != nil {
		return Approval{}, err
	}

	return out, nil
}

// UseApproval marks an approval for one request as run. It reports whether
// this call was the one that used it: an approval for one request runs once.
func (s *Service) UseApproval(ctx context.Context, id string) (bool, error) {
	used := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.UseApproval(ctx, store.UseApprovalParams{ID: id, Now: sql.NullInt64{Int64: now, Valid: true}})
		used = n == 1

		return err
	})
	if err != nil {
		return false, fmt.Errorf("using approval %s: %w", id, err)
	}

	return used, nil
}

// TellAgentOfApproval lets the agent that asked know the owner answered
// after its request had ended (owner, 2026-11-11). A chat's coordinator gets
// a wake-up in its chat. A worker gets the answer to the question it waits
// on; a worker that asked none is not waiting and is told nothing. told says
// whether anyone was.
func (s *Service) TellAgentOfApproval(ctx context.Context, a Approval) (told bool, err error) {
	said := fmt.Sprintf("The owner rejected %q (%s). Do not run it; say so in your report.", a.Title, a.ID)
	if a.Status == ApprovalApproved {
		said = fmt.Sprintf("The owner approved %q (%s). Run the same command again, unchanged: it will pass once.",
			a.Title, a.ID)

		if a.Scope == ScopeSession {
			said = fmt.Sprintf("The owner approved %q (%s), and %s for the rest of this session. Run the same "+
				"command again.", a.Title, a.ID, a.ActionName)
		}
	}

	if a.ChatID != "" {
		err := s.write(ctx, func(q *store.Queries, now int64) error {
			if err := q.InsertChatNotice(ctx, store.InsertChatNoticeParams{ChatID: a.ChatID, Text: said, Now: now}); err != nil {
				return err
			}

			return q.TouchChat(ctx, store.TouchChatParams{ID: a.ChatID, Now: now})
		})
		if err != nil {
			return false, fmt.Errorf("telling chat %s of approval %s: %w", a.ChatID, a.ID, err)
		}

		return true, nil
	}

	if a.DispatchID == "" {
		return false, nil
	}

	question, err := s.read().OpenQuestionOfDispatch(ctx, a.DispatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading the question dispatch %s waits on: %w", a.DispatchID, err)
	}

	if _, err := s.AnswerQuestion(ctx, question, AddressOwner, Answer{Text: said}); err != nil {
		return false, fmt.Errorf("answering the question dispatch %s waits on: %w", a.DispatchID, err)
	}

	return true, nil
}

// ActionPolicies returns the owner's answers for a Connection's actions,
// by action id; an action that is not there has its default.
func (s *Service) ActionPolicies(ctx context.Context, integration, label string) (map[string]string, error) {
	rows, err := s.read().ListActionPolicies(ctx, store.ListActionPoliciesParams{Integration: integration, Label: label})
	if err != nil {
		return nil, fmt.Errorf("reading the action policies of %s: %w", Use(integration, label), err)
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Action] = row.Policy
	}

	return out, nil
}

// SetActionPolicy keeps the owner's answer for one action of a Connection;
// an empty policy returns the action to its default.
func (s *Service) SetActionPolicy(ctx context.Context, integration, label, action, policy string) error {
	if integration == "" || label == "" || action == "" {
		return fmt.Errorf("%w: name the connection and the action", ErrInvalid)
	}

	if policy != "" && policy != PolicyAllow && policy != PolicyAsk && policy != PolicyNever {
		return fmt.Errorf("%w: an action is allowed, asked about or never done", ErrInvalid)
	}

	all, err := s.ConnectionsOf(ctx, integration)
	if err != nil {
		return err
	}

	if !slices.ContainsFunc(all, func(c Connection) bool { return c.Label == label }) {
		return fmt.Errorf("%w: connection %s", ErrNotFound, Use(integration, label))
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if policy == "" {
			return q.DeleteActionPolicy(ctx, store.DeleteActionPolicyParams{
				Integration: integration, Label: label, Action: action,
			})
		}

		return q.UpsertActionPolicy(ctx, store.UpsertActionPolicyParams{
			Integration: integration, Label: label, Action: action, Policy: policy, Now: now,
		})
	})
	if err != nil {
		return fmt.Errorf("keeping the policy of %s for %s: %w", action, Use(integration, label), err)
	}

	return nil
}

func approvalFromRow(row store.Approval) (Approval, error) {
	var payload []Field
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return Approval{}, fmt.Errorf("reading the request of approval %s: %w", row.ID, err)
	}

	return Approval{
		ID: row.ID, Integration: row.Integration, Label: row.Label, Action: row.Action, ActionName: row.ActionName,
		DispatchID: row.DispatchID.String, TaskID: row.TaskID.String, RunID: row.RunID.String,
		ChatID: row.ChatID.String, AskedBy: row.AskedBy, Title: row.Title, Payload: payload,
		Status: ApprovalStatus(row.Status), Scope: ApprovalScope(row.Scope.String), Used: row.UsedAt.Valid,
		CreatedAt: millis(row.CreatedAt), DecidedAt: optionalTime(row.DecidedAt),
	}, nil
}
