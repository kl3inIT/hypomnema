package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
)

// The notes of a memory (issue #589): every token reads them, a Bot's token only its own Bot's, as it reads its
// memory (issue #260); only the owner writes or removes one by hand, as he replaces a whole memory. An agent writes
// its own with the memory's tool, through the agent API.

// memoryDir is the memory a request names: a Bot's, by its id, or a profile's, by its name. agent is profile under
// its name until 2026-11-28 (issue #1336), read for one release.
func memoryDir(ctx context.Context, bot, profile, agent *string) (memorystore.Dir, error) {
	p := val(profile)
	if a := val(agent); a != "" {
		if p != "" && p != a {
			return memorystore.Dir{}, fmt.Errorf("%w: name the profile once, with profile", orchestration.ErrInvalid)
		}

		p = a
	}

	switch b := val(bot); {
	case (b == "") == (p == ""):
		return memorystore.Dir{}, fmt.Errorf("%w: name one memory, with bot or profile", orchestration.ErrInvalid)
	case b != "":
		if err := botAllowed(principal(ctx), b); err != nil {
			return memorystore.Dir{}, err
		}

		return memorystore.BotDir(b), nil
	default:
		return memorystore.AgentDir(p), nil
	}
}

// ListMemoryNotes lists a memory's notes.
func (s *Server) ListMemoryNotes(ctx context.Context, req openapi.ListMemoryNotesRequestObject,
) (openapi.ListMemoryNotesResponseObject, error) {
	dir, err := memoryDir(ctx, req.Params.Bot, req.Params.Profile, req.Params.Agent)
	if err != nil {
		return nil, err
	}

	notes, err := s.svc.Memories().Notes(ctx, dir)
	if err != nil {
		return nil, err
	}

	out := openapi.MemoryNoteList{
		Data: make([]openapi.MemoryNoteInfo, 0, len(notes)), MaxNotes: memorystore.MaxNotes,
		MaxNoteBytes: memorystore.MaxNoteBytes,
	}

	for _, n := range notes {
		out.Data = append(out.Data, openapi.MemoryNoteInfo{Path: n.Path, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt})
	}

	return openapi.ListMemoryNotes200JSONResponse(out), nil
}

// GetMemoryNote returns one note with its text.
func (s *Server) GetMemoryNote(ctx context.Context, req openapi.GetMemoryNoteRequestObject,
) (openapi.GetMemoryNoteResponseObject, error) {
	dir, err := memoryDir(ctx, req.Params.Bot, req.Params.Profile, req.Params.Agent)
	if err != nil {
		return nil, err
	}

	n, err := s.svc.Memories().ReadNote(ctx, dir, req.Params.Path)
	if err != nil {
		return nil, err
	}

	return openapi.GetMemoryNote200JSONResponse(toMemoryNote(n)), nil
}

// PutMemoryNote writes one note with the owner's text.
func (s *Server) PutMemoryNote(ctx context.Context, req openapi.PutMemoryNoteRequestObject,
) (openapi.PutMemoryNoteResponseObject, error) {
	dir, err := memoryDir(ctx, req.Params.Bot, req.Params.Profile, req.Params.Agent)
	if err != nil {
		return nil, err
	}

	n, err := s.svc.Memories().WriteNote(ctx, dir, req.Params.Path, req.Body.Text)
	if err != nil {
		return nil, err
	}

	return openapi.PutMemoryNote200JSONResponse(toMemoryNote(n)), nil
}

// DeleteMemoryNote removes one note for the owner.
func (s *Server) DeleteMemoryNote(ctx context.Context, req openapi.DeleteMemoryNoteRequestObject,
) (openapi.DeleteMemoryNoteResponseObject, error) {
	dir, err := memoryDir(ctx, req.Params.Bot, req.Params.Profile, req.Params.Agent)
	if err != nil {
		return nil, err
	}

	if err := s.svc.Memories().DeleteNote(ctx, dir, req.Params.Path); err != nil {
		return nil, err
	}

	return openapi.DeleteMemoryNote204Response{}, nil
}

func toMemoryNote(n memorystore.NoteText) openapi.MemoryNote {
	return openapi.MemoryNote{Path: n.Path, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt, Text: n.Text}
}
