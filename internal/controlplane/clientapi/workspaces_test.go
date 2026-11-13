package clientapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// folders stands for the machines that hold a chat's folders: it keeps the
// questions it is asked and answers each with what `answer` gives.
type folders struct {
	mu     sync.Mutex
	asked  []string
	answer func(where string, req workspacefs.Request) workspacefs.Result
}

func (f *folders) node(_ context.Context, nodeID, worktree string, req workspacefs.Request,
) (workspacefs.Result, error) {
	return f.ask("node "+nodeID+" "+worktree, req), nil
}

func (f *folders) Ask(_ context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error) {
	return f.ask("bot "+bot, req), nil
}

func (f *folders) Root(bot string) string { return "/home/coordinator/bots/" + bot }

func (f *folders) ask(where string, req workspacefs.Request) workspacefs.Result {
	f.mu.Lock()
	f.asked = append(f.asked, where+" "+string(req.Op)+" "+req.Path)
	f.mu.Unlock()

	return f.answer(where, req)
}

// chatWithATask is a chat whose coordinator made a Run with one Task, which
// was given to a node.
func (w *world) chatWithATask(c *openapi.ClientWithResponses) (orchestration.Chat, orchestration.ChatWorktree) {
	w.t.Helper()
	ctx := w.t.Context()

	chat, err := w.svc.CreateChat(ctx, "", "health page")
	require.NoError(w.t, err)

	run, err := w.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "add a health page", CreatedBy: "pi", Repo: "kl3inIT/demo", ChatID: chat.ID,
	})
	require.NoError(w.t, err)

	_, err = w.svc.AddAccount(ctx, "claude", "main")
	require.NoError(w.t, err)

	node, _, err := w.svc.AddNode(ctx, "box", 0)
	require.NoError(w.t, err)
	require.NoError(w.t, w.svc.NodeHello(ctx, node.ID, orchestration.Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude"}},
	}))

	tasks, err := c.CreateTasksWithResponse(ctx, run.ID, nil, openapi.NewTasks{Tasks: []openapi.NewTask{
		{Ref: "a", Spec: "add GET /health\nreturning ok"},
	}})
	require.NoError(w.t, err)
	require.Equal(w.t, http.StatusCreated, tasks.StatusCode(), string(tasks.Body))

	_, err = w.svc.Schedule(ctx)
	require.NoError(w.t, err)

	trees, err := w.svc.ChatWorktrees(ctx, chat.ID)
	require.NoError(w.t, err)
	require.Len(w.t, trees, 1)

	return chat, trees[0]
}

// Scenario: the owner opens the Files panel of a chat. It lists the Bot's
// folder and the worktree of the chat's Task, on its machine; a directory, a
// file, the changes and a diff of each are asked of the machine that holds it,
// and what that machine refuses is refused in its words.
func TestAChatsFoldersAreReadOnTheirMachines(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)
	ctx := t.Context()
	chat, tree := w.chatWithATask(owner)

	none, err := owner.ListChatWorkspacesWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	require.NotNil(t, none.JSON200, "%s", none.Body)
	assert.Empty(t, none.JSON200.Data, "a control plane that reads no folders lists none")

	f := &folders{answer: func(_ string, req workspacefs.Request) workspacefs.Result {
		switch {
		case req.Path == "../x":
			return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeInvalidPath, Message: "no"}}
		case req.Path == "gone.go":
			return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeNotFound, Message: "gone"}}
		case req.Op == workspacefs.OpList:
			return workspacefs.Result{Listing: &workspacefs.Listing{Entries: []workspacefs.Entry{
				{Name: "main.go", Path: req.Path + "/main.go", Type: "file", Bytes: 12},
			}}}
		case req.Op == workspacefs.OpRead:
			return workspacefs.Result{File: &workspacefs.File{Path: req.Path, Bytes: 2, Content: "ok"}}
		case req.Op == workspacefs.OpChanges:
			added := 3

			return workspacefs.Result{Changes: &workspacefs.Changes{Git: true, Changes: []workspacefs.Change{
				{Path: "main.go", Status: "created", LinesAdded: &added},
			}}}
		default:
			after := "new"

			return workspacefs.Result{Diff: &workspacefs.Diff{Path: req.Path, After: &after}}
		}
	}}
	w.srv.WithWorkspaces(f.node, f)

	list, err := owner.ListChatWorkspacesWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	require.NotNil(t, list.JSON200, "%s", list.Body)
	require.Len(t, list.JSON200.Data, 2)

	bot, task := list.JSON200.Data[0], list.JSON200.Data[1]
	assert.Equal(t, openapi.WorkspaceKindBot, bot.Kind)
	assert.Equal(t, "coordinator", bot.Name)
	assert.Equal(t, "/home/coordinator/bots/coordinator", bot.Root)
	assert.Equal(t, tree.TaskID, task.Id)
	assert.Equal(t, "add GET /health", task.Name, "a Task is named by its spec's first line")
	assert.Equal(t, tree.Worktree, task.Root)
	assert.True(t, task.Online)
	assert.Equal(t, "box", *task.NodeName)

	dir, err := owner.ListWorkspaceDirectoryWithResponse(ctx, chat.ID, task.Id,
		&openapi.ListWorkspaceDirectoryParams{Path: ptr("src")})
	require.NoError(t, err)
	require.NotNil(t, dir.JSON200, "%s", dir.Body)
	assert.Equal(t, "src/main.go", dir.JSON200.Entries[0].Path)

	file, err := owner.ReadWorkspaceFileWithResponse(ctx, chat.ID, "bot", &openapi.ReadWorkspaceFileParams{
		Path: ptr("notes.md"),
	})
	require.NoError(t, err)
	require.NotNil(t, file.JSON200, "%s", file.Body)
	assert.Equal(t, "ok", file.JSON200.Content)

	changes, err := owner.ListWorkspaceChangesWithResponse(ctx, chat.ID, task.Id)
	require.NoError(t, err)
	require.NotNil(t, changes.JSON200, "%s", changes.Body)
	assert.Equal(t, 3, *changes.JSON200.Changes[0].LinesAdded)

	diff, err := owner.DiffWorkspaceFileWithResponse(ctx, chat.ID, task.Id, &openapi.DiffWorkspaceFileParams{
		Path: ptr("main.go"),
	})
	require.NoError(t, err)
	require.NotNil(t, diff.JSON200, "%s", diff.Body)
	assert.Nil(t, diff.JSON200.Before)
	assert.Equal(t, "new", *diff.JSON200.After)

	assert.Equal(t, []string{
		"node " + tree.NodeID + " " + tree.Worktree + " list src",
		"bot coordinator read notes.md",
		"node " + tree.NodeID + " " + tree.Worktree + " changes ",
		"node " + tree.NodeID + " " + tree.Worktree + " diff main.go",
	}, f.asked)

	refused, err := owner.ReadWorkspaceFileWithResponse(ctx, chat.ID, task.Id,
		&openapi.ReadWorkspaceFileParams{Path: ptr("../x")})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, refused.Body, refused.StatusCode(), http.StatusBadRequest))
	assert.Contains(t, string(refused.Body), "no", "the machine's words are kept")

	gone, err := owner.ReadWorkspaceFileWithResponse(ctx, chat.ID, task.Id,
		&openapi.ReadWorkspaceFileParams{Path: ptr("gone.go")})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, gone.StatusCode())

	other, err := owner.ListWorkspaceChangesWithResponse(ctx, chat.ID, "tsk_other")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, other.StatusCode(), "a folder of another chat's Task is not this chat's")

	forbidden, err := pi.ListChatWorkspacesWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, forbidden.StatusCode(), "the folders are the owner's to read")

	assert.Equal(t, "a", firstLine("a\nb"))
	assert.Equal(t, strings.Repeat("x", 79)+"…", firstLine(strings.Repeat("x", 100)))
}

// Scenario: the owner changes a chat's folders (issue #297). A save names the text it was opened as and is asked
// of the machine; the machine's "changed" is a conflict, "missing" not found. A folder is made, a file moved and
// deleted on the machine that holds them. A file's bytes come as an opaque download that is never a page of this
// origin. A coordinator changes nothing.
func TestAChatsFoldersAreChangedOnTheirMachines(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)
	ctx := t.Context()
	chat, tree := w.chatWithATask(owner)

	var writes []workspacefs.Request

	f := &folders{answer: func(_ string, req workspacefs.Request) workspacefs.Result {
		switch {
		case req.Op == workspacefs.OpWrite && req.Expect == "old":
			return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeChanged, Message: "it changed"}}
		case req.Op == workspacefs.OpWrite:
			writes = append(writes, req)

			return workspacefs.Result{Done: &workspacefs.Done{Path: req.Path, SHA256: "new"}}
		case req.Op == workspacefs.OpRead && req.Raw:
			return workspacefs.Result{File: &workspacefs.File{Path: req.Path, Binary: true, Data: []byte("<svg/>")}}
		case req.Op == workspacefs.OpRename:
			return workspacefs.Result{Done: &workspacefs.Done{Path: req.To}}
		default:
			return workspacefs.Result{Done: &workspacefs.Done{Path: req.Path}}
		}
	}}
	w.srv.WithWorkspaces(f.node, f)

	saved, err := owner.WriteWorkspaceFileWithResponse(ctx, chat.ID, tree.TaskID, openapi.WorkspaceWrite{
		Path: "src/app.ts", Content: "two\n", ExpectSha256: ptr("current"),
	})
	require.NoError(t, err)
	require.NotNil(t, saved.JSON200, "%s", saved.Body)
	assert.Equal(t, "new", *saved.JSON200.Sha256)
	require.Len(t, writes, 1)
	assert.Equal(t, workspacefs.Request{Op: workspacefs.OpWrite, Path: "src/app.ts", Content: "two\n",
		Expect: "current"}, writes[0])

	stale, err := owner.WriteWorkspaceFileWithResponse(ctx, chat.ID, tree.TaskID, openapi.WorkspaceWrite{
		Path: "src/app.ts", Content: "three\n", ExpectSha256: ptr("old"),
	})
	require.NoError(t, err)
	assert.Equal(t, openapi.Conflict, errorCode(t, stale.Body, stale.StatusCode(), http.StatusConflict))

	folder, err := owner.MakeWorkspaceFolderWithResponse(ctx, chat.ID, "bot",
		openapi.WorkspacePathBody{Path: "notes"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, folder.StatusCode(), "%s", folder.Body)

	moved, err := owner.MoveWorkspaceEntryWithResponse(ctx, chat.ID, tree.TaskID,
		openapi.WorkspaceMove{Path: "a.md", To: "docs/a.md"})
	require.NoError(t, err)
	require.NotNil(t, moved.JSON200, "%s", moved.Body)
	assert.Equal(t, "docs/a.md", moved.JSON200.Path)

	deleted, err := owner.DeleteWorkspaceEntryWithResponse(ctx, chat.ID, tree.TaskID,
		&openapi.DeleteWorkspaceEntryParams{Path: ptr("docs")})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, deleted.StatusCode())

	raw, err := owner.ReadWorkspaceRawWithResponse(ctx, chat.ID, tree.TaskID,
		&openapi.ReadWorkspaceRawParams{Path: ptr("img/logo.svg")})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, raw.StatusCode())
	assert.Equal(t, "<svg/>", string(raw.Body))
	assert.Equal(t, "application/octet-stream", raw.HTTPResponse.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", raw.HTTPResponse.Header.Get("X-Content-Type-Options"))
	assert.Contains(t, raw.HTTPResponse.Header.Get("Content-Security-Policy"), "sandbox")
	assert.Equal(t, "attachment; filename=logo.svg", raw.HTTPResponse.Header.Get("Content-Disposition"))

	node := "node " + tree.NodeID + " " + tree.Worktree
	assert.Equal(t, []string{
		node + " write src/app.ts", node + " write src/app.ts", "bot coordinator mkdir notes",
		node + " rename a.md", node + " delete docs", node + " read img/logo.svg",
	}, f.asked)

	forbidden, err := pi.DeleteWorkspaceEntryWithResponse(ctx, chat.ID, tree.TaskID,
		&openapi.DeleteWorkspaceEntryParams{Path: ptr("src")})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, forbidden.StatusCode(), "the folders are the owner's to change")
}

// Scenario: the owner uploads a file to a chat's folder (issue #315): it is begun, sent in pieces and committed on
// the machine that holds the folder, each step naming the upload the machine began; a coordinator uploads nothing.
func TestAnUploadGoesToTheFoldersMachineInPieces(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)
	ctx := t.Context()
	chat, tree := w.chatWithATask(owner)

	var steps []workspacefs.Request

	f := &folders{answer: func(_ string, req workspacefs.Request) workspacefs.Result {
		steps = append(steps, req)

		switch req.Op {
		case workspacefs.OpUploadBegin:
			return workspacefs.Result{Done: &workspacefs.Done{Path: req.Path, Upload: "u1"}}
		case workspacefs.OpUploadCommit:
			return workspacefs.Result{Done: &workspacefs.Done{Path: req.Path, SHA256: "s"}}
		default:
			return workspacefs.Result{Done: &workspacefs.Done{Path: req.Path}}
		}
	}}
	w.srv.WithWorkspaces(f.node, f)

	begun, err := owner.BeginWorkspaceUploadWithResponse(ctx, chat.ID, tree.TaskID,
		openapi.WorkspacePathBody{Path: "img/logo.png"})
	require.NoError(t, err)
	require.NotNil(t, begun.JSON201, "%s", begun.Body)
	require.Equal(t, "u1", *begun.JSON201.Upload)

	piece, err := owner.SendWorkspaceUploadChunkWithResponse(ctx, chat.ID, tree.TaskID, openapi.WorkspaceUploadChunk{
		Path: "img/logo.png", Upload: "u1", Offset: 0, Data: []byte("png"),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, piece.StatusCode(), "%s", piece.Body)

	done, err := owner.CommitWorkspaceUploadWithResponse(ctx, chat.ID, tree.TaskID, openapi.WorkspaceUploadCommit{
		Path: "img/logo.png", Upload: "u1", ExpectSha256: ptr("old"),
	})
	require.NoError(t, err)
	require.NotNil(t, done.JSON200, "%s", done.Body)

	aborted, err := owner.AbortWorkspaceUploadWithResponse(ctx, chat.ID, tree.TaskID,
		openapi.WorkspaceUploadRef{Path: "img/logo.png", Upload: "u2"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, aborted.StatusCode())

	assert.Equal(t, []workspacefs.Request{
		{Op: workspacefs.OpUploadBegin, Path: "img/logo.png"},
		{Op: workspacefs.OpUploadChunk, Path: "img/logo.png", Upload: "u1", Data: []byte("png")},
		{Op: workspacefs.OpUploadCommit, Path: "img/logo.png", Upload: "u1", Expect: "old"},
		{Op: workspacefs.OpUploadAbort, Path: "img/logo.png", Upload: "u2"},
	}, steps)

	forbidden, err := pi.BeginWorkspaceUploadWithResponse(ctx, chat.ID, tree.TaskID,
		openapi.WorkspacePathBody{Path: "x"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, forbidden.StatusCode())
}
