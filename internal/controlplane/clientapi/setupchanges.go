package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Managing Bots and profiles (issue #169, part 3). The owner makes, changes
// and removes them; besides him only the Bot coordinator does, with its pi's
// own token (issue #260), from its chat. Every other token is refused: another
// Bot's, a Routine's script's, and the coordinator token outside every box.
// A fixed rule of the role, as OpenMausBot gives the tools that make bots to
// its Chief only (server/drivers/agents-catalog.ts L907-909) and checks it
// again on the server (server/index.ts L14147, L14183).

// setupChat says from which chat a token makes a change of Bots and
// profiles: none for the owner, its own for the Bot coordinator's pi. Any
// other token is refused.
func setupChat(tok orchestration.Token) (string, error) {
	switch {
	case tok.Kind == bearertoken.Owner:
		return "", nil
	case tok.Kind == bearertoken.Bot && tok.Bot == orchestration.DefaultBot && tok.Routine == "" && tok.Chat != "":
		return tok.Chat, nil
	default:
		return "", fmt.Errorf("%w: only the owner and the bot %s, from its chat, make, change and remove bots "+
			"and profiles", errForbidden, orchestration.DefaultBot)
	}
}

// ListSetupChanges lists what the Bot coordinator changed from a chat.
func (s *Server) ListSetupChanges(ctx context.Context, req openapi.ListSetupChangesRequestObject,
) (openapi.ListSetupChangesResponseObject, error) {
	changes, err := s.svc.ListSetupChanges(ctx, req.Params.ChatId)
	if err != nil {
		return nil, err
	}

	out := openapi.ListSetupChanges200JSONResponse{Data: make([]openapi.SetupChange, 0, len(changes))}
	for _, c := range changes {
		out.Data = append(out.Data, toSetupChange(c))
	}

	return out, nil
}

// UndoSetupChange undoes a change of the Bot coordinator.
func (s *Server) UndoSetupChange(ctx context.Context, req openapi.UndoSetupChangeRequestObject,
) (openapi.UndoSetupChangeResponseObject, error) {
	c, err := s.svc.UndoSetupChange(ctx, req.ChangeId)
	if err != nil {
		return nil, err
	}

	return openapi.UndoSetupChange200JSONResponse(toSetupChange(c)), nil
}

func toSetupChange(c orchestration.SetupChange) openapi.SetupChange {
	return openapi.SetupChange{
		Id: c.ID, ChatId: c.ChatID, Kind: openapi.SetupChangeKind(c.Kind), Name: c.Name,
		Action: openapi.SetupChangeAction(c.Action), CanUndo: c.CanUndo, Problem: opt(c.Problem),
		CreatedAt: c.CreatedAt, UndoneAt: c.UndoneAt,
	}
}
