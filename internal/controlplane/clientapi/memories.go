package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// GetAgentMemory returns an agent's memory (issue #21).
func (s *Server) GetAgentMemory(ctx context.Context, req openapi.GetAgentMemoryRequestObject,
) (openapi.GetAgentMemoryResponseObject, error) {
	m, err := s.svc.AgentMemory(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	return openapi.GetAgentMemory200JSONResponse(toMemory(m)), nil
}

// SetAgentMemory replaces an agent's memory with the owner's text.
func (s *Server) SetAgentMemory(ctx context.Context, req openapi.SetAgentMemoryRequestObject,
) (openapi.SetAgentMemoryResponseObject, error) {
	m, err := s.svc.SetAgentMemory(ctx, req.Name, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetAgentMemory200JSONResponse(toMemory(m)), nil
}

// ChangeAgentMemory makes one change to an agent's memory for the owner.
func (s *Server) ChangeAgentMemory(ctx context.Context, req openapi.ChangeAgentMemoryRequestObject,
) (openapi.ChangeAgentMemoryResponseObject, error) {
	m, err := s.svc.ChangeAgentMemory(ctx, req.Name, memoryOp(*req.Body))
	if err != nil {
		return nil, err
	}

	return openapi.ChangeAgentMemory200JSONResponse(toMemory(m)), nil
}

// GetOwnerMemory returns the memory about the owner.
func (s *Server) GetOwnerMemory(ctx context.Context, _ openapi.GetOwnerMemoryRequestObject,
) (openapi.GetOwnerMemoryResponseObject, error) {
	m, err := s.svc.OwnerMemory(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetOwnerMemory200JSONResponse(toMemory(m)), nil
}

// SetOwnerMemory replaces the memory about the owner with his text.
func (s *Server) SetOwnerMemory(ctx context.Context, req openapi.SetOwnerMemoryRequestObject,
) (openapi.SetOwnerMemoryResponseObject, error) {
	m, err := s.svc.SetOwnerMemory(ctx, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetOwnerMemory200JSONResponse(toMemory(m)), nil
}

// ChangeOwnerMemory makes one change to the memory about the owner: a Bot
// keeps there what every agent should know of him.
func (s *Server) ChangeOwnerMemory(ctx context.Context, req openapi.ChangeOwnerMemoryRequestObject,
) (openapi.ChangeOwnerMemoryResponseObject, error) {
	m, err := s.svc.ChangeOwnerMemory(ctx, memoryOp(*req.Body))
	if err != nil {
		return nil, err
	}

	return openapi.ChangeOwnerMemory200JSONResponse(toMemory(m)), nil
}

func memoryOp(c openapi.MemoryChange) orchestration.MemoryOp {
	return orchestration.MemoryOp{Kind: string(c.Kind), Text: val(c.Text), Match: val(c.Match)}
}

func toMemory(m orchestration.Memory) openapi.Memory {
	out := openapi.Memory{Agent: ptr(m.Agent), Memory: m.Text, MemoryLimit: m.Limit}

	if !m.UpdatedAt.IsZero() {
		out.UpdatedAt = &m.UpdatedAt
	}

	return out
}
