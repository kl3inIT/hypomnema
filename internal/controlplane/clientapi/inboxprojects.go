package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// The Inbox's projects (issue #1157): every token reads them, as it reads the Inbox; only the owner adds, renames and
// removes them (policy.go), from the Inbox's project filter, as Linear's workspace keeps its projects.

// ListInboxProjects lists the projects, by name.
func (s *Server) ListInboxProjects(ctx context.Context, _ openapi.ListInboxProjectsRequestObject,
) (openapi.ListInboxProjectsResponseObject, error) {
	all, err := s.svc.Inbox().Projects(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.InboxProjectList{Data: make([]openapi.InboxProject, 0, len(all))}
	for _, p := range all {
		out.Data = append(out.Data, projectOut(p))
	}

	return openapi.ListInboxProjects200JSONResponse(out), nil
}

// CreateInboxProject adds a project.
func (s *Server) CreateInboxProject(ctx context.Context, req openapi.CreateInboxProjectRequestObject,
) (openapi.CreateInboxProjectResponseObject, error) {
	p, err := s.svc.Inbox().CreateProject(ctx, req.Body.Name, req.Body.Color)
	if err != nil {
		return nil, err
	}

	return openapi.CreateInboxProject201JSONResponse(projectOut(p)), nil
}

// EditInboxProject renames a project or changes its color.
func (s *Server) EditInboxProject(ctx context.Context, req openapi.EditInboxProjectRequestObject,
) (openapi.EditInboxProjectResponseObject, error) {
	p, err := s.svc.Inbox().EditProject(ctx, req.ProjectId, req.Body.Name, req.Body.Color)
	if err != nil {
		return nil, err
	}

	return openapi.EditInboxProject200JSONResponse(projectOut(p)), nil
}

// DeleteInboxProject removes a project; its items are left without one.
func (s *Server) DeleteInboxProject(ctx context.Context, req openapi.DeleteInboxProjectRequestObject,
) (openapi.DeleteInboxProjectResponseObject, error) {
	if err := s.svc.Inbox().DeleteProject(ctx, inboxstore.Actor{Kind: inboxstore.Owner}, req.ProjectId); err != nil {
		return nil, err
	}

	return openapi.DeleteInboxProject204Response{}, nil
}

func projectOut(p inboxstore.Project) openapi.InboxProject {
	return openapi.InboxProject{Id: p.ID, Name: p.Name, Color: p.Color, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}
