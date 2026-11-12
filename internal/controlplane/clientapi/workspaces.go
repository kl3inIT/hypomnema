package clientapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// A chat's working folders, for the owner (issue #255): its Bot's own folder
// on the control plane's machine, and the worktree of each Task of its Runs
// on that Task's node. As Omnigent's resource routes, the control plane only
// passes a question to the machine that holds the folder and its answer back;
// what may be read is decided there (workspacefs).

// botWorkspace is the id of a chat's Bot's folder.
const botWorkspace = "bot"

// NodeFiles asks a node about one of its worktrees.
type NodeFiles func(ctx context.Context, nodeID, worktree string, req workspacefs.Request) (workspacefs.Result, error)

// BotFiles asks about a Bot's folder; Root says where the folders are.
type BotFiles interface {
	Ask(ctx context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error)
	Root(bot string) string
}

// WithWorkspaces sets how a chat's folders are read. A nil BotFiles leaves
// the Bot's folder out.
func (s *Server) WithWorkspaces(nodes NodeFiles, bots BotFiles) *Server {
	s.nodeFiles, s.botFiles = nodes, bots

	return s
}

var errNoWorkspaces = fmt.Errorf("%w: this control plane reads no folders", orchestration.ErrUnavailable)

// ListChatWorkspaces lists a chat's folders: the Bot's first, then its Tasks'.
func (s *Server) ListChatWorkspaces(ctx context.Context, req openapi.ListChatWorkspacesRequestObject,
) (openapi.ListChatWorkspacesResponseObject, error) {
	chat, err := s.svc.GetChat(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}

	out := openapi.WorkspaceList{Data: []openapi.Workspace{}}

	if s.botFiles != nil {
		out.Data = append(out.Data, openapi.Workspace{
			Id: botWorkspace, Kind: openapi.WorkspaceKindBot, Name: chat.Bot, Root: s.botFiles.Root(chat.Bot),
			Online: true,
		})
	}

	if s.nodeFiles != nil {
		trees, err := s.svc.ChatWorktrees(ctx, chat.ID)
		if err != nil {
			return nil, err
		}

		online := map[string]bool{}

		for _, t := range trees {
			if _, seen := online[t.NodeID]; !seen {
				n, err := s.svc.GetNode(ctx, t.NodeID)
				online[t.NodeID] = err == nil && n.Connected
			}

			out.Data = append(out.Data, openapi.Workspace{
				Id: t.TaskID, Kind: openapi.WorkspaceKindTask, Name: firstLine(t.Spec), Root: t.Worktree,
				Online: online[t.NodeID], NodeId: &t.NodeID, NodeName: &t.NodeName, TaskId: &t.TaskID,
				RunId: &t.RunID, TaskStatus: ptr(string(t.Status)),
			})
		}
	}

	return openapi.ListChatWorkspaces200JSONResponse(out), nil
}

// firstLine is a Task's spec as a name: its first line, at most 80 characters.
func firstLine(spec string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(spec), "\n")
	if runes := []rune(line); len(runes) > 80 {
		return string(runes[:79]) + "…"
	}

	return line
}

// ask puts a question to the machine that holds one of a chat's folders, and
// turns its refusal into this API's errors.
func (s *Server) ask(ctx context.Context, chatID, workspaceID string, req workspacefs.Request,
) (workspacefs.Result, error) {
	chat, err := s.svc.GetChat(ctx, chatID)
	if err != nil {
		return workspacefs.Result{}, err
	}

	var res workspacefs.Result

	switch {
	case workspaceID == botWorkspace && s.botFiles != nil:
		res, err = s.botFiles.Ask(ctx, chat.Bot, req)
	case workspaceID != botWorkspace && s.nodeFiles != nil:
		tree, found, lerr := s.worktreeOf(ctx, chat.ID, workspaceID)
		if lerr != nil {
			return workspacefs.Result{}, lerr
		}

		if !found {
			return workspacefs.Result{}, fmt.Errorf("%w: chat %s has no folder %s", orchestration.ErrNotFound,
				chat.ID, workspaceID)
		}

		res, err = s.nodeFiles(ctx, tree.NodeID, tree.Worktree, req)
	default:
		return workspacefs.Result{}, errNoWorkspaces
	}

	if err != nil {
		return workspacefs.Result{}, err
	}

	if res.Error != nil {
		return workspacefs.Result{}, refusal(res.Error)
	}

	return res, nil
}

func (s *Server) worktreeOf(ctx context.Context, chatID, taskID string) (orchestration.ChatWorktree, bool, error) {
	trees, err := s.svc.ChatWorktrees(ctx, chatID)
	if err != nil {
		return orchestration.ChatWorktree{}, false, err
	}

	for _, t := range trees {
		if t.TaskID == taskID {
			return t, true, nil
		}
	}

	return orchestration.ChatWorktree{}, false, nil
}

// refusal is a folder's machine's refusal as one of this API's errors, its
// message kept.
func refusal(e *workspacefs.Error) error {
	kind := errors.New("failed")

	switch e.Code {
	case workspacefs.CodeInvalidPath, workspacefs.CodeTooLarge:
		kind = orchestration.ErrInvalid
	case workspacefs.CodeNotFound, workspacefs.CodeGone:
		kind = orchestration.ErrNotFound
	case workspacefs.CodeNotGit:
		kind = orchestration.ErrConflict
	case workspacefs.CodeGitFailed:
		kind = orchestration.ErrUnavailable
	case workspacefs.CodeFailed:
	}

	return fmt.Errorf("%w: %s", kind, e.Message)
}

func (s *Server) ListWorkspaceDirectory(ctx context.Context, req openapi.ListWorkspaceDirectoryRequestObject,
) (openapi.ListWorkspaceDirectoryResponseObject, error) {
	res, err := s.ask(ctx, req.ChatId, req.WorkspaceId, workspacefs.Request{
		Op: workspacefs.OpList, Path: deref(req.Params.Path), After: deref(req.Params.After),
	})
	if err != nil {
		return nil, err
	}

	if res.Listing == nil {
		return nil, errors.New("the machine answered without a listing")
	}

	out := openapi.WorkspaceListing{Entries: make([]openapi.WorkspaceEntry, 0, len(res.Listing.Entries)),
		HasMore: res.Listing.HasMore}
	for _, e := range res.Listing.Entries {
		out.Entries = append(out.Entries, openapi.WorkspaceEntry{
			Name: e.Name, Path: e.Path, Type: openapi.WorkspaceEntryType(e.Type), Bytes: e.Bytes,
			ModifiedAt: e.ModifiedAt,
		})
	}

	return openapi.ListWorkspaceDirectory200JSONResponse(out), nil
}

func (s *Server) ReadWorkspaceFile(ctx context.Context, req openapi.ReadWorkspaceFileRequestObject,
) (openapi.ReadWorkspaceFileResponseObject, error) {
	res, err := s.ask(ctx, req.ChatId, req.WorkspaceId, workspacefs.Request{
		Op: workspacefs.OpRead, Path: deref(req.Params.Path),
	})
	if err != nil {
		return nil, err
	}

	if res.File == nil {
		return nil, errors.New("the machine answered without a file")
	}

	f := res.File

	return openapi.ReadWorkspaceFile200JSONResponse(openapi.WorkspaceFile{
		Path: f.Path, Bytes: f.Bytes, Binary: f.Binary, Truncated: f.Truncated, Content: f.Content,
	}), nil
}

func (s *Server) ListWorkspaceChanges(ctx context.Context, req openapi.ListWorkspaceChangesRequestObject,
) (openapi.ListWorkspaceChangesResponseObject, error) {
	res, err := s.ask(ctx, req.ChatId, req.WorkspaceId, workspacefs.Request{Op: workspacefs.OpChanges})
	if err != nil {
		return nil, err
	}

	if res.Changes == nil {
		return nil, errors.New("the machine answered without changes")
	}

	out := openapi.WorkspaceChanges{Git: res.Changes.Git, Truncated: res.Changes.Truncated,
		Changes: make([]openapi.WorkspaceChange, 0, len(res.Changes.Changes))}
	for _, c := range res.Changes.Changes {
		out.Changes = append(out.Changes, openapi.WorkspaceChange{
			Path: c.Path, Status: openapi.WorkspaceChangeStatus(c.Status), LinesAdded: c.LinesAdded,
			LinesRemoved: c.LinesRemoved, ModifiedAt: c.ModifiedAt,
		})
	}

	return openapi.ListWorkspaceChanges200JSONResponse(out), nil
}

func (s *Server) DiffWorkspaceFile(ctx context.Context, req openapi.DiffWorkspaceFileRequestObject,
) (openapi.DiffWorkspaceFileResponseObject, error) {
	res, err := s.ask(ctx, req.ChatId, req.WorkspaceId, workspacefs.Request{
		Op: workspacefs.OpDiff, Path: deref(req.Params.Path),
	})
	if err != nil {
		return nil, err
	}

	if res.Diff == nil {
		return nil, errors.New("the machine answered without a diff")
	}

	d := res.Diff

	return openapi.DiffWorkspaceFile200JSONResponse(openapi.WorkspaceDiff{
		Path: d.Path, Before: d.Before, After: d.After, Binary: d.Binary,
	}), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
