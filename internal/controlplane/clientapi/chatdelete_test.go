package clientapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// piFiles stands for hypomnema-chat-rm: it keeps the chats whose files it was asked to remove, whether each was
// still in the database then, and fails with fail when it is set.
type piFiles struct {
	svc  *orchestration.Service
	fail error

	mu      sync.Mutex
	removed []string
	present []bool
}

func (p *piFiles) Remove(ctx context.Context, chatID string) error {
	_, err := p.svc.GetChat(ctx, chatID)

	p.mu.Lock()
	p.removed = append(p.removed, chatID)
	p.present = append(p.present, err == nil)
	p.mu.Unlock()

	return p.fail
}

// Scenario: the owner deletes a chat (issue #484). While pi works in it the deletion is refused and nothing is
// touched. Once pi is idle, the chat goes: then its pi is stopped, its attachments are deleted in its Bot's folder
// and pi's run folder and sessions are removed, each only after the chat is gone from the database. A pi's token
// cannot delete a chat.
func TestTheOwnerDeletesAChatAndItsFiles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)
	ctx := t.Context()

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	var stillThere []bool
	f := &folders{answer: func(_ string, _ workspacefs.Request) workspacefs.Result {
		_, err := w.svc.GetChat(ctx, chat.ID)
		stillThere = append(stillThere, err == nil)

		return workspacefs.Result{Done: &workspacefs.Done{Path: "attachments/" + chat.ID}}
	}}
	files := &piFiles{svc: w.svc}
	w.srv.WithWorkspaces(f.node, f).WithChatFiles(files)

	refused, err := pi.DeleteChatWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	w.pi.working.Store(chat.ID)

	working, err := owner.DeleteChatWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	errorCode(t, working.Body, working.StatusCode(), http.StatusConflict)
	assert.Contains(t, string(working.Body), "stop it first")
	assert.Empty(t, f.asked, "nothing is touched")
	assert.Empty(t, files.removed)
	assert.Nil(t, w.pi.stopped.Load())

	w.pi.working.Store("")

	deleted, err := owner.DeleteChatWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	require.NotNil(t, deleted.JSON200, "%s", deleted.Body)
	assert.Empty(t, deleted.JSON200.FilesLeft)

	_, err = w.svc.GetChat(ctx, chat.ID)
	require.ErrorIs(t, err, orchestration.ErrNotFound)
	assert.Equal(t, chat.ID, w.pi.stopped.Load(), "its pi is stopped")
	assert.Equal(t, []string{"bot " + orchestration.DefaultBotID + " delete attachments/" + chat.ID}, f.asked)
	assert.Equal(t, []bool{false}, stillThere, "the attachments go after the commit")
	assert.Equal(t, []string{chat.ID}, files.removed)
	assert.Equal(t, []bool{false}, files.present, "pi's files go after the commit")

	again, err := owner.DeleteChatWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	errorCode(t, again.Body, again.StatusCode(), http.StatusNotFound)
}

// Scenario: a deleted chat's files could not all be removed: a chat without attachments is no failure, but pi's
// sessions stayed. The chat is deleted all the same, and the answer names what stayed and why.
func TestADeletedChatsFilesThatStayAreReported(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	ctx := t.Context()

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	f := &folders{answer: func(_ string, _ workspacefs.Request) workspacefs.Result {
		return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeNotFound, Message: "nothing"}}
	}}
	files := &piFiles{svc: w.svc, fail: errors.New("sudo: a password is required")}
	w.srv.WithWorkspaces(f.node, f).WithChatFiles(files)

	deleted, err := owner.DeleteChatWithResponse(ctx, chat.ID)
	require.NoError(t, err)
	require.NotNil(t, deleted.JSON200, "%s", deleted.Body)
	require.Len(t, deleted.JSON200.FilesLeft, 1)
	assert.Contains(t, deleted.JSON200.FilesLeft[0], "sessions/"+chat.ID)
	assert.Contains(t, deleted.JSON200.FilesLeft[0], "sudo: a password is required")

	_, err = w.svc.GetChat(ctx, chat.ID)
	require.ErrorIs(t, err, orchestration.ErrNotFound, "the deletion stands")

	other, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	w.srv.WithChatFiles(nil)

	unset, err := owner.DeleteChatWithResponse(ctx, other.ID)
	require.NoError(t, err)
	require.NotNil(t, unset.JSON200, "%s", unset.Body)
	require.Len(t, unset.JSON200.FilesLeft, 1)
	assert.Contains(t, unset.JSON200.FilesLeft[0], "HYPOMNEMA_CHAT_RM_COMMAND is not set")
}

// Scenario: the owner removes a Bot with its two chats (issue #503). While pi works in one of them the removal is
// refused and nothing goes. Once pi is idle, each chat goes as one deleted alone does: its pi is stopped, its
// attachments are deleted in the Bot's folder and pi's run folder and sessions are removed, after the commit.
func TestRemovingABotWithItsChatsRemovesTheirFiles(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	ctx := t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "trial"})
	require.NoError(t, err)
	first, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	second, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)

	f := &folders{answer: func(_ string, req workspacefs.Request) workspacefs.Result {
		return workspacefs.Result{Done: &workspacefs.Done{Path: req.Path}}
	}}
	files := &piFiles{svc: w.svc}
	w.srv.WithWorkspaces(f.node, f).WithChatFiles(files)
	force := true

	w.pi.working.Store(second.ID)

	refused, err := owner.DeleteBotWithResponse(ctx, bot.ID, &openapi.DeleteBotParams{Force: &force})
	require.NoError(t, err)
	errorCode(t, refused.Body, refused.StatusCode(), http.StatusConflict)
	assert.Contains(t, string(refused.Body), "stop it first")
	_, err = w.svc.GetChat(ctx, first.ID)
	require.NoError(t, err, "nothing goes")
	assert.Empty(t, files.removed)

	w.pi.working.Store("")

	removed, err := owner.DeleteBotWithResponse(ctx, bot.ID, &openapi.DeleteBotParams{Force: &force})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, removed.StatusCode(), "%s", removed.Body)

	chats := []string{first.ID, second.ID}
	assert.ElementsMatch(t, chats, w.pi.stops, "each chat's pi is stopped")
	assert.ElementsMatch(t, []string{
		"bot " + bot.ID + " delete attachments/" + first.ID, "bot " + bot.ID + " delete attachments/" + second.ID,
	}, f.asked)
	assert.ElementsMatch(t, chats, files.removed)
	assert.Equal(t, []bool{false, false}, files.present, "pi's files go after the commit")
}
