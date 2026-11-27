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
// own sign-in, his when its sender is the user he is. A person's message says its sender, his own none (issue
// #1204). A message that is not there is not found.
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
			switch {
			case integration != "zalo" || label != "main":
				return []messaging.Message{}, nil
			case q.ID == "z1":
				return []messaging.Message{{ID: "z1", Chat: q.Chat, At: at, FromMe: true, Sender: "84900000000"}}, nil
			case q.ID == "z2":
				return []messaging.Message{{ID: "z2", Chat: q.Chat, At: at, Sender: "84911111111"}}, nil
			}

			return []messaging.Message{}, nil
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

	got, err = sent.SentMessage(ctx, inboxstore.FromZalo, "main", "lan", "z2")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.SentMessage{Chat: "lan", At: at, Sender: "84911111111"}, got, "a person's, by sender")

	_, err = sent.SentMessage(ctx, inboxstore.FromWhatsApp, "main", "lan", "z1")
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	got, err = sent.SentMessage(ctx, inboxstore.FromLark, "me", "oc_lan", "om_mine")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.SentMessage{Chat: "oc_lan", At: time.UnixMilli(1795312800000).UTC(), FromMe: true}, got)

	got, err = sent.SentMessage(ctx, inboxstore.FromLark, "me", "oc_lan", "om_hers")
	require.NoError(t, err)
	assert.False(t, got.FromMe, "Lan's message is not his")
	assert.Equal(t, "ou_lan", got.Sender, "a person's message says who sent it")

	_, err = sent.SentMessage(ctx, inboxstore.FromLark, "me", "oc_lan", "om_gone")
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

// Scenario: issue #1163. For the owner's answer in another chat, who asked and who is in that chat are read where
// they are. Of a WhatsApp or Zalo account: the asker is the sender of the item's message, or, when the owner wrote it
// in a direct chat, its other party; who is in a chat, as the session the server holds lists them. Of Lark, with his
// own sign-in: the sender of the message, and a chat's name and members page after page. An asker who is only the
// owner, in a group, is no one; a chat whose members Lark does not give is not read.
func TestWhoAskedAndWhoIsInAChatAreReadWhereTheyAre(t *testing.T) {
	t.Parallel()

	lark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/authen/v1/user_info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"open_id":"ou_dat"}}`))
		case "/open-apis/im/v1/messages/om_asked":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_asked","chat_id":"oc_pmo",` +
				`"create_time":"1795312800000","sender":{"id":"ou_kai","sender_type":"user"}}]}}`))
		case "/open-apis/im/v1/chats/oc_team":
			_, _ = w.Write([]byte(`{"code":0,"data":{"name":"Team","chat_mode":"group"}}`))
		case "/open-apis/im/v1/chats/oc_team/members":
			if r.URL.Query().Get("member_id_type") != "open_id" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":99992402,"msg":"field validation failed"}`))

				return
			}

			if r.URL.Query().Get("page_token") == "" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"member_id":"ou_dat"}],"has_more":true,` +
					`"page_token":"p2"}}`))

				return
			}

			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"member_id":"ou_kai"}],"has_more":false}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":232011,"msg":"not in the chat"}`))
		}
	}))
	t.Cleanup(lark.Close)

	kept := map[string]messaging.Message{
		"whatsapp/team/q1": {ID: "q1", Chat: "team", Sender: "84911111111@s.whatsapp.net"},
		"zalo/u9/p1":       {ID: "p1", Chat: "u9", Sender: "100", FromMe: true},
		"zalo/g1/p2":       {ID: "p2", Chat: "g1", Sender: "100", FromMe: true},
	}
	sent := inboxSent{
		messages: func(_ context.Context, integration, _ string, q messaging.Query) ([]messaging.Message, error) {
			if m, ok := kept[integration+"/"+q.Chat+"/"+q.ID]; ok {
				return []messaging.Message{m}, nil
			}

			return []messaging.Message{}, nil
		},
		members: func(_ context.Context, integration, label, chat string) (messaging.Members, error) {
			switch integration + "/" + label + "/" + chat {
			case "zalo/main/u9":
				return messaging.Members{Name: "Minh", Direct: true, IDs: []string{"u9"}}, nil
			case "zalo/main/g1":
				return messaging.Members{Name: "Báo giá", IDs: []string{"100", "200"}}, nil
			}

			return messaging.Members{}, storeerr.ErrUnavailable
		},
		larkToken: func(context.Context, string) (string, error) { return "u-token", nil },
		larkURL:   lark.URL, client: lark.Client(),
	}

	ctx := t.Context()

	asker, err := sent.Asker(ctx, inboxstore.FromWhatsApp, "main", "team", "q1")
	require.NoError(t, err)
	assert.Equal(t, []string{"84911111111@s.whatsapp.net"}, asker, "the sender of the message")

	asker, err = sent.Asker(ctx, inboxstore.FromZalo, "main", "u9", "p1")
	require.NoError(t, err)
	assert.Equal(t, []string{"u9"}, asker, "his own promise in a direct chat: its other party")

	_, err = sent.Asker(ctx, inboxstore.FromZalo, "main", "g1", "p2")
	require.ErrorIs(t, err, storeerr.ErrNotFound, "his own message in a group names no one")

	_, err = sent.Asker(ctx, inboxstore.FromZalo, "main", "g1", "gone")
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	in, err := sent.Members(ctx, inboxstore.FromZalo, "main", "g1")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.ChatMembers{Name: "Báo giá", IDs: []string{"100", "200"}}, in)

	_, err = sent.Members(ctx, inboxstore.FromWhatsApp, "main", "team")
	require.ErrorIs(t, err, storeerr.ErrUnavailable, "a session not held lists no one")

	asker, err = sent.Asker(ctx, inboxstore.FromLark, "me", "oc_pmo", "om_asked")
	require.NoError(t, err)
	assert.Equal(t, []string{"ou_kai"}, asker)

	_, err = sent.Asker(ctx, inboxstore.FromLark, "me", "oc_other", "om_asked")
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a message of another chat")

	in, err = sent.Members(ctx, inboxstore.FromLark, "me", "oc_team")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.ChatMembers{Name: "Team", IDs: []string{"ou_dat", "ou_kai"}}, in)

	_, err = sent.Members(ctx, inboxstore.FromLark, "me", "oc_left")
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
