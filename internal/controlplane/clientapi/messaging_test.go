package clientapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// keptMessages stands for the messaging accounts' kept messages: it answers the message a query names by its id.
type keptMessages struct {
	Messaging

	kept []messaging.Message
}

func (k keptMessages) Messages(_ context.Context, _, _ string, q messaging.Query) ([]messaging.Message, error) {
	var out []messaging.Message

	for _, m := range k.kept {
		if (q.Chat == "" || m.Chat == q.Chat) && (q.ID == "" || m.ID == q.ID) {
			out = append(out, m)
		}
	}

	return out, nil
}

// Scenario: the Inbox shows who sent the message an item came from and when (issue #1110): it reads that one message
// of the chat by its id, among the others kept of the chat.
func TestTheOwnerReadsOneKeptMessageByItsID(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)
	w.srv.WithMessaging(keptMessages{kept: []messaging.Message{
		{ID: "3EB0C0", Chat: "kai@s.whatsapp.net", SenderName: "Kai Yong", Text: "Hello", At: at.Add(-time.Hour)},
		{ID: "3EB0C1", Chat: "kai@s.whatsapp.net", SenderName: "Kai Yong", Text: "Any news?", At: at},
	}})
	owner, _ := w.token("web", bearertoken.Owner)

	chat, id := "kai@s.whatsapp.net", "3EB0C1"
	resp, err := owner.ListMessagingMessagesWithResponse(t.Context(), "whatsapp", "main",
		&openapi.ListMessagingMessagesParams{Chat: &chat, Id: &id})
	require.NoError(t, err)
	require.NotNil(t, resp.JSON200, "messages: %s", resp.Body)
	require.Len(t, resp.JSON200.Data, 1)
	assert.Equal(t, "Any news?", resp.JSON200.Data[0].Text)
	assert.Equal(t, "Kai Yong", resp.JSON200.Data[0].SenderName)
	assert.True(t, at.Equal(resp.JSON200.Data[0].At))
}
