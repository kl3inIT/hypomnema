package clientapi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Conversions between the orchestration core's types and the API's.

func ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}

	return &v
}

func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}

	return *p
}

func toRun(r orchestration.Run) openapi.Run {
	return openapi.Run{
		Id: r.ID, Objective: r.Objective, Status: openapi.RunStatus(r.Status), CreatedBy: r.CreatedBy,
		Repo: ptr(r.Repo), Base: ptr(r.Base), CoordinatorTokenId: ptr(r.CoordinatorTokenID), ChatId: ptr(r.ChatID),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
	}
}

func toTask(t orchestration.Task) openapi.Task {
	var reason *openapi.BlockedReason
	if t.BlockedReason != orchestration.BlockedNone {
		reason = ptr(openapi.BlockedReason(t.BlockedReason))
	}

	return openapi.Task{
		Id: t.ID, RunId: t.RunID, Position: t.Position, Profile: t.Profile, Spec: t.Spec,
		Runtime: ptr(t.Runtime), Model: ptr(t.Model), Effort: ptr(t.Effort),
		Status: openapi.TaskStatus(t.Status), BlockedReason: reason, FailureCount: t.FailureCount,
		Result: ptr(t.Result), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, FinishedAt: t.FinishedAt,
		NotBefore: t.NotBefore, CancelRequestedAt: t.CancelRequestedAt,
	}
}

func toTaskDetail(t orchestration.Task, d *orchestration.Dispatch) openapi.TaskDetail {
	b := toTask(t)
	out := openapi.TaskDetail{
		Id: b.Id, RunId: b.RunId, Position: b.Position, Profile: b.Profile, Spec: b.Spec, Runtime: b.Runtime,
		Model: b.Model, Effort: b.Effort, Status: b.Status, BlockedReason: b.BlockedReason,
		FailureCount: b.FailureCount, Result: b.Result, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
		FinishedAt: b.FinishedAt, NotBefore: b.NotBefore, CancelRequestedAt: b.CancelRequestedAt,
	}

	if d != nil {
		dispatch := toDispatch(*d)
		out.LatestDispatch = &dispatch
	}

	return out
}

func toDispatch(d orchestration.Dispatch) openapi.Dispatch {
	out := openapi.Dispatch{
		Id: d.ID, TaskId: d.TaskID, NodeId: d.NodeID, Status: openapi.DispatchStatus(d.Status), Epoch: d.Epoch,
		FailureClass: ptr(string(d.FailureClass)), UnknownCause: ptr(string(d.UnknownCause)),
		Model: ptr(d.Model), Effort: ptr(d.Effort), ProfileVersionId: ptr(d.ProfileVersionID),
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		StartedAt: d.StartedAt, ProgressAt: d.ProgressAt, SettledAt: d.SettledAt, WaitingSince: d.WaitingSince,
	}

	if d.Result != nil {
		evidence := make([]openapi.Evidence, 0, len(d.Result.Evidence))
		for _, e := range d.Result.Evidence {
			evidence = append(evidence, openapi.Evidence{Kind: e.Kind, Ref: e.Ref})
		}

		out.Result = &openapi.DispatchResult{Summary: d.Result.Summary, Evidence: evidence}
	}

	return out
}

func toQuestion(q orchestration.Question) openapi.Question {
	options := make([]openapi.Option, 0, len(q.Options))
	for _, o := range q.Options {
		options = append(options, openapi.Option{Label: o.Label, AllowsText: ptr(o.AllowsText)})
	}

	out := openapi.Question{
		Id: q.ID, DispatchId: q.DispatchID, TaskId: q.TaskID, RunId: q.RunID, Mode: openapi.QuestionMode(q.Mode),
		AddressedTo: openapi.Addressee(q.AddressedTo), Text: q.Text, Options: options,
		Status: openapi.QuestionStatus(q.Status), CancelReason: ptr(q.CancelReason), AnsweredAt: q.AnsweredAt,
		CreatedAt: q.CreatedAt,
	}

	if q.AnsweredBy != "" {
		out.AnsweredBy = ptr(openapi.Addressee(q.AnsweredBy))
	}

	if q.Answer != nil {
		out.Answer = &openapi.Answer{Option: q.Answer.Option, Text: ptr(q.Answer.Text)}
	}

	return out
}

func toEvent(e orchestration.Event) (openapi.Event, error) {
	payload := map[string]any{}
	if len(e.Payload) > 0 {
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return openapi.Event{}, fmt.Errorf("decoding the payload of event %d: %w", e.Seq, err)
		}
	}

	return openapi.Event{
		Seq: e.Seq, Kind: string(e.Kind), RunId: ptr(e.RunID), TaskId: ptr(e.TaskID),
		DispatchId: ptr(e.DispatchID), QuestionId: ptr(e.QuestionID), NodeId: ptr(e.NodeID),
		Payload: payload, CreatedAt: e.CreatedAt,
	}, nil
}

func toNode(n orchestration.Node) openapi.Node {
	runtimes := make([]openapi.Runtime, 0, len(n.Runtimes))
	for _, r := range n.Runtimes {
		runtimes = append(runtimes, openapi.Runtime{Runtime: r.Runtime, Path: r.Path, Version: r.Version})
	}

	return openapi.Node{
		Id: n.ID, Name: n.Name, Status: openapi.NodeStatus(n.Status), MaxDispatches: n.MaxDispatches,
		WorktreeRoot: ptr(n.WorktreeRoot), Runtimes: runtimes, LastSeenAt: n.LastSeenAt,
		UnavailableAt: n.UnavailableAt, Connected: n.Connected, Active: n.Active, CreatedAt: n.CreatedAt,
	}
}

func toAccount(a orchestration.Account) openapi.Account {
	return openapi.Account{
		Id: a.ID, Runtime: a.Runtime, Label: a.Label, Status: openapi.AccountStatus(a.Status),
		Position: a.Position, CoolingUntil: a.CoolingUntil,
	}
}

func fromNewTask(t openapi.NewTask) orchestration.NewTask {
	return orchestration.NewTask{
		Ref: t.Ref, Profile: val(t.Profile), Spec: t.Spec, Runtime: val(t.Runtime), Model: val(t.Model),
		Effort:    val(t.Effort),
		DependsOn: val(t.DependsOn),
	}
}

// A Run cursor is opaque to clients: the last Run's creation time in Unix
// milliseconds and its id.
func encodeCursor(c *orchestration.RunCursor) *string {
	if c == nil {
		return nil
	}

	s := strconv.FormatInt(c.CreatedAt.UnixMilli(), 10) + "." + c.ID

	return &s
}

func decodeCursor(s *string) (*orchestration.RunCursor, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // No cursor is the first page, not an error.
	}

	ms, id, ok := strings.Cut(*s, ".")

	at, err := strconv.ParseInt(ms, 10, 64)
	if !ok || err != nil || id == "" {
		return nil, fmt.Errorf("%w: cursor %q is not one this API gave", orchestration.ErrInvalid, *s)
	}

	return &orchestration.RunCursor{CreatedAt: time.UnixMilli(at).UTC(), ID: id}, nil
}
