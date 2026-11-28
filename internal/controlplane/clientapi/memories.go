package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
)

// GetProfileMemory returns a profile's memory (issue #21); GetAgentMemory until 2026-11-28 (issue #1336).
func (s *Server) GetProfileMemory(ctx context.Context, req openapi.GetProfileMemoryRequestObject,
) (openapi.GetProfileMemoryResponseObject, error) {
	m, err := s.svc.Memories().AgentMemory(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	return openapi.GetProfileMemory200JSONResponse(toMemory(m)), nil
}

// SetProfileMemory replaces a profile's memory with the owner's text.
func (s *Server) SetProfileMemory(ctx context.Context, req openapi.SetProfileMemoryRequestObject,
) (openapi.SetProfileMemoryResponseObject, error) {
	m, err := s.svc.Memories().SetAgentMemory(ctx, req.Name, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetProfileMemory200JSONResponse(toMemory(m)), nil
}

// ChangeProfileMemory makes one change to a profile's memory for the owner.
func (s *Server) ChangeProfileMemory(ctx context.Context, req openapi.ChangeProfileMemoryRequestObject,
) (openapi.ChangeProfileMemoryResponseObject, error) {
	m, err := s.svc.Memories().ChangeAgentMemory(ctx, req.Name, memoryOp(*req.Body))
	if err != nil {
		return nil, err
	}

	return openapi.ChangeProfileMemory200JSONResponse(toMemory(m)), nil
}

// GetOwnerMemory returns the memory about the owner.
func (s *Server) GetOwnerMemory(ctx context.Context, _ openapi.GetOwnerMemoryRequestObject,
) (openapi.GetOwnerMemoryResponseObject, error) {
	m, err := s.svc.Memories().OwnerMemory(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetOwnerMemory200JSONResponse(toMemory(m)), nil
}

// SetOwnerMemory replaces the memory about the owner with his text.
func (s *Server) SetOwnerMemory(ctx context.Context, req openapi.SetOwnerMemoryRequestObject,
) (openapi.SetOwnerMemoryResponseObject, error) {
	m, err := s.svc.Memories().SetOwnerMemory(ctx, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetOwnerMemory200JSONResponse(toMemory(m)), nil
}

// ChangeOwnerMemory makes one change to the memory about the owner: a Bot
// keeps there what every agent should know of him.
func (s *Server) ChangeOwnerMemory(ctx context.Context, req openapi.ChangeOwnerMemoryRequestObject,
) (openapi.ChangeOwnerMemoryResponseObject, error) {
	m, err := s.svc.Memories().ChangeOwnerMemory(ctx, memoryOp(*req.Body))
	if err != nil {
		return nil, err
	}

	return openapi.ChangeOwnerMemory200JSONResponse(toMemory(m)), nil
}

func memoryOp(c openapi.MemoryChange) memorystore.Op {
	return memorystore.Op{Kind: string(c.Kind), Text: val(c.Text), Match: val(c.Match)}
}

// toMemory is a profile's memory, or the one about the owner. It names the profile as profile and, for one release,
// as agent, its name until 2026-11-28 (issue #1336).
func toMemory(m memorystore.Memory) openapi.Memory {
	out := openapi.Memory{Profile: ptr(m.Agent), Agent: ptr(m.Agent), Memory: m.Text, MemoryLimit: m.Limit}

	if !m.UpdatedAt.IsZero() {
		out.UpdatedAt = &m.UpdatedAt
	}

	return out
}
