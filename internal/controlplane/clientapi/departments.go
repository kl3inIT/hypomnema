package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The departments the sidebar lists Bots in (issue #471); the owner's alone to change, as the Bots' order.

// ListDepartments lists every department in the owner's order.
func (s *Server) ListDepartments(ctx context.Context, _ openapi.ListDepartmentsRequestObject,
) (openapi.ListDepartmentsResponseObject, error) {
	deps, err := s.svc.ListDepartments(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListDepartments200JSONResponse{Data: make([]openapi.Department, 0, len(deps))}
	for _, d := range deps {
		out.Data = append(out.Data, toDepartment(d))
	}

	return out, nil
}

// CreateDepartment makes a department with the Bots named in it.
func (s *Server) CreateDepartment(ctx context.Context, req openapi.CreateDepartmentRequestObject,
) (openapi.CreateDepartmentResponseObject, error) {
	var bots []string
	if req.Body.BotIds != nil {
		bots = *req.Body.BotIds
	}

	d, err := s.svc.CreateDepartment(ctx, req.Body.Name, bots)
	if err != nil {
		return nil, err
	}

	return openapi.CreateDepartment201JSONResponse(toDepartment(d)), nil
}

// ReorderDepartments moves the departments named.
func (s *Server) ReorderDepartments(ctx context.Context, req openapi.ReorderDepartmentsRequestObject,
) (openapi.ReorderDepartmentsResponseObject, error) {
	order, err := s.svc.ReorderDepartments(ctx, req.Body.DepartmentIds)
	if err != nil {
		return nil, err
	}

	return openapi.ReorderDepartments200JSONResponse{DepartmentIds: order}, nil
}

// RenameDepartment gives a department another name.
func (s *Server) RenameDepartment(ctx context.Context, req openapi.RenameDepartmentRequestObject,
) (openapi.RenameDepartmentResponseObject, error) {
	d, err := s.svc.RenameDepartment(ctx, req.DepartmentId, req.Body.Name)
	if err != nil {
		return nil, err
	}

	return openapi.RenameDepartment200JSONResponse(toDepartment(d)), nil
}

// DeleteDepartment removes a department; its Bots go back to the Bots in none.
func (s *Server) DeleteDepartment(ctx context.Context, req openapi.DeleteDepartmentRequestObject,
) (openapi.DeleteDepartmentResponseObject, error) {
	if err := s.svc.DeleteDepartment(ctx, req.DepartmentId); err != nil {
		return nil, err
	}

	return openapi.DeleteDepartment204Response{}, nil
}

func toDepartment(d orchestration.Department) openapi.Department {
	return openapi.Department{Id: d.ID, Name: d.Name, Position: d.Position}
}
