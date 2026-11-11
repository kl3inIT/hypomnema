package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Bots (issue #169). Every token reads them and changes a memory: pi, which
// holds a coordinator's token, reads its Bot's persona when it starts and
// writes its memory. Only the owner makes, changes and removes a Bot
// (policy.go).

// ListBots returns every Bot.
func (s *Server) ListBots(ctx context.Context, _ openapi.ListBotsRequestObject,
) (openapi.ListBotsResponseObject, error) {
	bots, err := s.svc.ListBots(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListBots200JSONResponse{Data: make([]openapi.Bot, 0, len(bots))}
	for _, b := range bots {
		out.Data = append(out.Data, toBot(b))
	}

	return out, nil
}

// GetBot returns a Bot.
func (s *Server) GetBot(ctx context.Context, req openapi.GetBotRequestObject) (openapi.GetBotResponseObject, error) {
	b, err := s.svc.GetBot(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	return openapi.GetBot200JSONResponse(toBot(b)), nil
}

// PutBot makes a Bot or changes its model and persona.
func (s *Server) PutBot(ctx context.Context, req openapi.PutBotRequestObject) (openapi.PutBotResponseObject, error) {
	b, err := s.svc.PutBot(ctx, orchestration.Bot{
		Name: req.Name, Provider: val(req.Body.Provider), Model: val(req.Body.Model), Effort: val(req.Body.Effort),
		Persona: val(req.Body.Persona),
	})
	if err != nil {
		return nil, err
	}

	return openapi.PutBot200JSONResponse(toBot(b)), nil
}

// DeleteBot removes a Bot nothing depends on.
func (s *Server) DeleteBot(ctx context.Context, req openapi.DeleteBotRequestObject,
) (openapi.DeleteBotResponseObject, error) {
	if err := s.svc.DeleteBot(ctx, req.Name); err != nil {
		return nil, err
	}

	return openapi.DeleteBot204Response{}, nil
}

// ChangeBotMemory applies one change to a Bot's memory.
func (s *Server) ChangeBotMemory(ctx context.Context, req openapi.ChangeBotMemoryRequestObject,
) (openapi.ChangeBotMemoryResponseObject, error) {
	b, err := s.svc.ChangeBotMemory(ctx, req.Name, orchestration.MemoryOp{
		Kind: string(req.Body.Kind), Text: val(req.Body.Text), Match: val(req.Body.Match),
	})
	if err != nil {
		return nil, err
	}

	return openapi.ChangeBotMemory200JSONResponse(toBot(b)), nil
}

func toBot(b orchestration.Bot) openapi.Bot {
	return openapi.Bot{
		Name: b.Name, Provider: ptr(b.Provider), Model: ptr(b.Model), Effort: ptr(b.Effort),
		Persona: b.Persona, Memory: b.Memory, MemoryLimit: orchestration.MaxMemory,
		CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
}
