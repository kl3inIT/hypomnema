package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Scenario: issue #1004, section 4. The owner's sent message a Bot gives as evidence is read where it is: a Zalo or
// WhatsApp one from what the server keeps of that account, by its chat and id; a Lark one from Lark's API with his
// own sign-in, his when its sender is the user he is. A message that is not there is not found.
func TestTheOwnersSentMessageIsReadWhereItIs(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 21, 2, 0, 0, 0, time.UTC)

	var asked messaging.Query

	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer u-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":99991663,"msg":"invalid token"}`))

			return
		}

		switch r.URL.Path {
		case "/open-apis/authen/v1/user_info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"open_id":"ou_dat"}}`))
		case "/open-apis/im/v1/messages/om_mine":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_mine","chat_id":"oc_lan",` +
				`"create_time":"1795312800000","sender":{"id":"ou_dat","sender_type":"user"}}]}}`))
		case "/open-apis/im/v1/messages/om_hers":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_hers","chat_id":"oc_lan",` +
				`"create_time":"1795312800000","sender":{"id":"ou_lan","sender_type":"user"}}]}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":230001,"msg":"message not found"}`))
		}
	}))
	t.Cleanup(lark.Close)

	sent := inboxSent{
		messages: func(_ context.Context, integration, label string, q messaging.Query) ([]messaging.Message, error) {
			asked = q
			if integration != "zalo" || label != "main" || q.ID != "z1" {
				return []messaging.Message{}, nil
			}

			return []messaging.Message{{ID: "z1", Chat: q.Chat, At: at, FromMe: true}}, nil
		},
		larkToken: func(_ context.Context, label string) (string, error) {
			require.Equal(t, "me", label)

			return "u-token", nil
		},
		larkURL: lark.URL, client: lark.Client(),
	}

	ctx := t.Context()

	got, err := sent.SentMessage(ctx, inboxstore.FromZalo, "main", "lan", "z1")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.SentMessage{Chat: "lan", At: at, FromMe: true}, got)
	assert.Equal(t, messaging.Query{Chat: "lan", ID: "z1", Limit: 1}, asked)

	_, err = sent.SentMessage(ctx, inboxstore.FromWhatsApp, "main", "lan", "z1")
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	got, err = sent.SentMessage(ctx, inboxstore.FromLark, "me", "oc_lan", "om_mine")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.SentMessage{Chat: "oc_lan", At: time.UnixMilli(1795312800000).UTC(), FromMe: true}, got)

	got, err = sent.SentMessage(ctx, inboxstore.FromLark, "me", "oc_lan", "om_hers")
	require.NoError(t, err)
	assert.False(t, got.FromMe, "Lan's message is not his")

	_, err = sent.SentMessage(ctx, inboxstore.FromLark, "me", "oc_lan", "om_gone")
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
