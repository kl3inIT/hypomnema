package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// Approvals and the answers an action of a Connection has (issue #154). Only
// the owner reads and answers them: an agent that could approve its own
// request would not be asking.

// Approvals records the owner's answer to a held request and lets the
// request, or its agent, know: the control plane's service proxy.
type Approvals interface {
	Decide(ctx context.Context, id string, approve bool, scope orchestration.ApprovalScope) (orchestration.Approval, error)
}

// WithApprovals sets the service proxy that holds requests for the owner.
func (s *Server) WithApprovals(a Approvals) *Server {
	s.approvals = a

	return s
}

var errNoApprovals = fmt.Errorf("%w: this control plane has no service proxy", orchestration.ErrUnavailable)

func toApproval(a orchestration.Approval) openapi.Approval {
	out := openapi.Approval{
		Id: a.ID, Integration: a.Integration, Label: a.Label, Action: a.Action, ActionName: a.ActionName,
		AskedBy: a.AskedBy, Title: a.Title, Payload: make([]openapi.ApprovalField, 0, len(a.Payload)),
		Status: openapi.ApprovalStatus(a.Status), Used: a.Used, CreatedAt: a.CreatedAt, DecidedAt: a.DecidedAt,
		DispatchId: opt(a.DispatchID), TaskId: opt(a.TaskID), RunId: opt(a.RunID), ChatId: opt(a.ChatID),
	}

	for _, f := range a.Payload {
		out.Payload = append(out.Payload, openapi.ApprovalField{Label: f.Label, Value: f.Value})
	}

	if a.Scope != "" {
		scope := openapi.ApprovalScope(a.Scope)
		out.Scope = &scope
	}

	return out
}

// opt is a string as an optional field: absent when empty.
func opt(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// ListApprovals lists the requests that wait for, or had, the owner's answer.
func (s *Server) ListApprovals(ctx context.Context, req openapi.ListApprovalsRequestObject,
) (openapi.ListApprovalsResponseObject, error) {
	status := orchestration.ApprovalStatus("")
	if req.Params.Status != nil {
		status = orchestration.ApprovalStatus(*req.Params.Status)
	}

	all, err := s.svc.ListApprovals(ctx, orchestration.ApprovalFilter{
		Status: status, ChatID: val(req.Params.ChatId), TaskID: val(req.Params.TaskId),
	})
	if err != nil {
		return nil, err
	}

	out := openapi.ListApprovals200JSONResponse{Data: make([]openapi.Approval, 0, len(all))}
	for _, a := range all {
		out.Data = append(out.Data, toApproval(a))
	}

	return out, nil
}

// DecideApproval records the owner's answer to one request.
func (s *Server) DecideApproval(ctx context.Context, req openapi.DecideApprovalRequestObject,
) (openapi.DecideApprovalResponseObject, error) {
	if s.approvals == nil {
		return nil, errNoApprovals
	}

	approve, scope := true, orchestration.ScopeOnce

	switch req.Body.Decision {
	case openapi.Approve:
	case openapi.ApproveSession:
		scope = orchestration.ScopeSession
	case openapi.Reject:
		approve = false
	default:
		return nil, fmt.Errorf("%w: answer approve, approve_session or reject", orchestration.ErrInvalid)
	}

	decided, err := s.approvals.Decide(ctx, req.ApprovalId, approve, scope)
	if err != nil {
		return nil, err
	}

	return openapi.DecideApproval200JSONResponse(toApproval(decided)), nil
}

func toConnectionAction(a serviceproxy.Action, set map[string]string) openapi.ConnectionAction {
	policy := a.Default()
	if answer, ok := set[a.ID]; ok {
		policy = answer
	}

	return openapi.ConnectionAction{
		Id: a.ID, Service: a.Service, Name: a.Name, Text: a.Text, Kind: openapi.ConnectionActionKind(a.Kind),
		Policy: openapi.ActionPolicy(policy), Default: openapi.ActionPolicy(a.Default()),
	}
}

// ListConnectionActions lists what agents may do with a Connection.
func (s *Server) ListConnectionActions(ctx context.Context, req openapi.ListConnectionActionsRequestObject,
) (openapi.ListConnectionActionsResponseObject, error) {
	out := openapi.ListConnectionActions200JSONResponse{Data: []openapi.ConnectionAction{}}

	catalog, ok := serviceproxy.CatalogOf(req.Integration)
	if !ok {
		// An Integration that does not go through the proxy has no actions to answer for.
		return out, nil
	}

	set, err := s.svc.ActionPolicies(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	for _, a := range catalog.Listed() {
		out.Data = append(out.Data, toConnectionAction(a, set))
	}

	return out, nil
}

// SetConnectionAction sets the answer of one action of a Connection.
func (s *Server) SetConnectionAction(ctx context.Context, req openapi.SetConnectionActionRequestObject,
) (openapi.SetConnectionActionResponseObject, error) {
	catalog, ok := serviceproxy.CatalogOf(req.Integration)
	if !ok {
		return nil, fmt.Errorf("%w: %s does not go through the service proxy", orchestration.ErrInvalid, req.Integration)
	}

	action, ok := catalog.Action(req.Action)
	if !ok {
		return nil, fmt.Errorf("%w: %s has no action %s", orchestration.ErrNotFound, req.Integration, req.Action)
	}

	policy := string(req.Body.Policy)
	if policy == action.Default() {
		// The default is kept by keeping nothing: an action follows its default when that changes.
		policy = ""
	}

	if err := s.svc.SetActionPolicy(ctx, req.Integration, req.Label, action.ID, policy); err != nil {
		return nil, err
	}

	set, err := s.svc.ActionPolicies(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	return openapi.SetConnectionAction200JSONResponse(toConnectionAction(action, set)), nil
}
