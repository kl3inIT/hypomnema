package clientcli

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/inbox"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Scenario: review of #1270, round 1. deploy/thu-ky/setup.sh runs `inbox chats move` and stops on its failure
// before it sets the check that reads dropChats no more. So the command fails, naming what is left, whenever an id
// it should move is not stored: while the owner's Zalo cannot be listed, and for an id none of his accounts has. It
// succeeds once every id is stored.
func TestInboxChatsMoveFailsUntilEveryIDIsStored(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	closed := &atomic.Bool{}
	closed.Store(true)
	filters := &atomic.Pointer[string]{}
	filters.Store(new(`{"dropChats": ["\\bOA\\b", "^1001$", "^9999$"]}`))

	var srv *clientapi.Server

	w := newWorldWith(t, replier{}, func(s *clientapi.Server) { srv = s })
	srv.WithInboxChats(inbox.NewChats(inbox.Config{
		Chats: func(_ context.Context, _, _ string, _ int) ([]messaging.Chat, error) {
			if closed.Load() {
				return nil, fmt.Errorf("%w: the session of zalo:main is not open", orchestration.ErrUnavailable)
			}

			return []messaging.Chat{{ID: "1001", Kind: "dm", Name: "Lan Anh"}}, nil
		},
		ChatReads: w.svc.Inbox().ChatReads,
	}))
	srv.WithWorkspaces(nil, folderOf{filters})

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "zalo", Label: "main", Secret: "test-zalo",
	})
	require.NoError(t, err)
	w.login(t, bearertoken.Owner)

	_, err = w.cmd(t, "inbox", "chats", "move")
	require.Error(t, err, "zalo:main cannot be listed")
	assert.Contains(t, err.Error(), "zalo:main")

	closed.Store(false)

	out, err := w.cmd(t, "inbox", "chats", "move")
	require.Error(t, err, "9999 is no chat of his")
	assert.Contains(t, err.Error(), "9999")
	assert.Contains(t, out, "switched off: zalo:main chat 1001")

	filters.Store(new(`{"dropChats": ["\\bOA\\b", "^1001$"]}`))

	out, err = w.cmd(t, "inbox", "chats", "move")
	require.NoError(t, err)
	assert.Contains(t, out, "set already, left as it is: zalo:main chat 1001")
}

func TestInboxChatsSetBulkAcceptsFiltersAndIDs(t *testing.T) {
	t.Parallel()

	var srv *clientapi.Server
	w := newWorldWith(t, replier{}, func(s *clientapi.Server) { srv = s })
	srv.WithInboxChats(inbox.NewChats(inbox.Config{
		Chats: func(_ context.Context, _, _ string, _ int) ([]messaging.Chat, error) {
			return []messaging.Chat{
				{ID: "101", Kind: "group", Name: "Alpha"},
				{ID: "102", Kind: "group", Name: "Birch"},
				{ID: "103", Kind: "dm", Name: "Alex"},
			}, nil
		},
		ChatReads: w.svc.Inbox().ChatReads,
	}))

	_, err := w.svc.SaveServiceConnection(t.Context(), orchestration.Connection{
		Integration: "zalo", Label: "demo", Secret: "test-zalo",
	})
	require.NoError(t, err)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "inbox", "chats", "set", "zalo:demo", "off", "--q", "alp", "--kind", "group", "--state", "read")
	require.NoError(t, err)
	assert.Contains(t, out, "Updated 1 chat of zalo:demo.")

	out, err = w.cmd(t, "inbox", "chats", "zalo:demo", "--state", "skipped")
	require.NoError(t, err)
	assert.Contains(t, out, "101\tAlpha\tgroup\tskipped (owner)")
	assert.Contains(t, out, "1 of 3 chats")

	out, err = w.cmd(t, "inbox", "chats", "set", "zalo:demo", "102,103", "off")
	require.NoError(t, err)
	assert.Contains(t, out, "Updated 2 chats of zalo:demo.")

	out, err = w.cmd(t, "inbox", "chats", "zalo:demo", "--state", "skipped")
	require.NoError(t, err)
	assert.Contains(t, out, "102\tBirch\tgroup\tskipped (owner)")
	assert.Contains(t, out, "103\tAlex\tdirect\tskipped (owner)")
}

// folderOf holds the main Bot's thu-ky/filters.json, which the test changes; every id is made up.
type folderOf struct{ filters *atomic.Pointer[string] }

func (f folderOf) Root(bot string) string { return bot }

func (f folderOf) Ask(_ context.Context, bot string, req workspacefs.Request) (workspacefs.Result, error) {
	if bot != orchestration.DefaultBotID || req.Path != "thu-ky/filters.json" {
		return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeNotFound, Message: "no such file"}}, nil
	}

	return workspacefs.Result{File: &workspacefs.File{Path: req.Path, Content: *f.filters.Load()}}, nil
}
