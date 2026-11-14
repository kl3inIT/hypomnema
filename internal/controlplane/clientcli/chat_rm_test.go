package clientcli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Scenario: the owner deletes a chat from the terminal (issue #484), as `opencode session delete` does; a chat
// whose pi files stayed says which, and the chat is deleted all the same. A chat that is not there is refused.
func TestTheOwnerDeletesAChatFromTheTerminal(t *testing.T) {
	t.Parallel()

	w := newWorldWith(t, &replier{})
	w.login(t, bearertoken.Owner)
	ctx := t.Context()

	kept, err := w.svc.CreateChat(ctx, "", "keep me")
	require.NoError(t, err)
	gone, err := w.svc.CreateChat(ctx, "", "probe")
	require.NoError(t, err)

	out, err := w.cmd(t, "chats", "rm", gone.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "chat "+gone.ID+" is deleted\n")
	assert.Contains(t, out, "a file of it stayed: pi's run/"+gone.ID, "this control plane removes no pi files")

	var chats openapi.ChatList
	w.jsonCmd(t, &chats, "", "chats")
	require.Len(t, chats.Data, 1)
	assert.Equal(t, kept.ID, chats.Data[0].Id)

	_, err = w.cmd(t, "chats", "rm", gone.ID)
	require.ErrorContains(t, err, "not found")
}
