package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Connection requests (issue #471, part D, the grant card): a Bot's ask, from its pi in its main chat, for a
// Connection outside its list. Only the owner lists and answers them (policy.go), never a Bot's token, PutBotFromChat
// or a Lark sender: a Bot that could grant its own request would not be asking, as only the owner answers an
// Approval (approvals.go).

func toConnectionRequest(r orchestration.ConnectionRequest) openapi.ConnectionRequest {
	out := openapi.ConnectionRequest{
		Id: r.ID, Bot: r.Bot, ChatId: r.Chat, Integration: r.Integration, Label: r.Label, Reason: r.Reason,
		State: openapi.ConnectionRequestState(r.State), Why: opt(r.Why), CreatedAt: r.CreatedAt,
	}

	if !r.DecidedAt.IsZero() {
		decided := r.DecidedAt
		out.DecidedAt = &decided
	}

	return out
}

// ListConnectionRequests lists the Bots' requests for a Connection, the newest first.
func (s *Server) ListConnectionRequests(ctx context.Context, req openapi.ListConnectionRequestsRequestObject,
) (openapi.ListConnectionRequestsResponseObject, error) {
	all, err := s.svc.ListConnectionRequests(ctx, orchestration.ConnectionRequestFilter{
		State: orchestration.ConnectionRequestState(val(req.Params.State)), Chat: val(req.Params.ChatId),
	})
	if err != nil {
		return nil, err
	}

	out := openapi.ListConnectionRequests200JSONResponse{Data: make([]openapi.ConnectionRequest, 0, len(all))}
	for _, r := range all {
		out.Data = append(out.Data, toConnectionRequest(r))
	}

	return out, nil
}

// DecideConnectionRequest records the owner's answer to one request: Always for this Bot, or Decline.
func (s *Server) DecideConnectionRequest(ctx context.Context, req openapi.DecideConnectionRequestRequestObject,
) (openapi.DecideConnectionRequestResponseObject, error) {
	var grant bool

	switch req.Body.Decision {
	case openapi.Grant:
		grant = true
	case openapi.Decline:
	default:
		return nil, fmt.Errorf("%w: answer grant or decline", orchestration.ErrInvalid)
	}

	decided, err := s.svc.DecideConnectionRequest(ctx, req.RequestId, grant)
	if err != nil {
		return nil, err
	}

	return openapi.DecideConnectionRequest200JSONResponse(toConnectionRequest(decided)), nil
}
