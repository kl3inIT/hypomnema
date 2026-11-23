package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Scenario: issue #1111, section 3. The owner's reply goes through his own account to the item's own chat or
// thread, and what was sent is the evidence: a Zalo chat of the Connection the item names; a Lark chat with his own
// sign-in (lark:me when the item names none); and a reply in the item's Gmail thread, to the Reply-To of the mail
// answered, "Re:" its subject and threaded under it. An item from nowhere to send to is refused.
func TestTheOwnersReplyGoesToTheItemsOwnChatOrThread(t *testing.T) {
	t.Parallel()

	var (
		zalo      messaging.Outgoing
		zaloLabel string
		larkBody  map[string]string
		larkQuery string
		gmailRaw  string
		gmailTo   string
	)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		switch {
		case r.URL.Path == "/open-apis/im/v1/messages" && r.Header.Get("Authorization") == "Bearer u-lark":
			larkQuery = r.URL.RawQuery
			_ = json.Unmarshal(body, &larkBody)
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_sent"}}`))
		case r.URL.Path == "/gmail/v1/users/me/messages/m1" && r.Header.Get("Authorization") == "Bearer g-work":
			_, _ = w.Write([]byte(`{"id":"m1","threadId":"t1","payload":{"headers":[` +
				`{"name":"From","value":"Kai Yong <kai@genai.fund>"},` +
				`{"name":"Reply-To","value":"deals@genai.fund"},` +
				`{"name":"Subject","value":"Pilot\r\nBcc: someone@else"},` +
				`{"name":"Message-ID","value":"<abc@genai.fund>"},` +
				`{"name":"References","value":"<first@genai.fund>"}]}}`))
		case r.URL.Path == "/gmail/v1/users/me/messages/send" && r.Header.Get("Authorization") == "Bearer g-work":
			var sent struct{ Raw, ThreadID string }
			_ = json.Unmarshal(body, &sent)
			raw, _ := base64.URLEncoding.DecodeString(sent.Raw)
			gmailRaw, gmailTo = string(raw), sent.ThreadID
			_, _ = w.Write([]byte(`{"id":"m2","threadId":"t1"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":99991663,"msg":"bad request"}`))
		}
	}))
	t.Cleanup(api.Close)

	s := inboxSender{
		send: func(_ context.Context, integration, label string, out messaging.Outgoing) (messaging.Message, error) {
			require.Equal(t, "zalo", integration)
			zaloLabel, zalo = label, out

			return messaging.Message{ID: "z-sent", Chat: out.Chat, FromMe: true}, nil
		},
		larkToken: func(_ context.Context, label string) (string, error) {
			if label != inboxLarkMe {
				return "", storeerr.ErrNotFound
			}

			return "u-lark", nil
		},
		googleToken: func(_ context.Context, label string) (string, error) {
			require.Equal(t, "work", label)

			return "g-work", nil
		},
		larkURL: api.URL, gmailURL: api.URL, client: api.Client(),
	}
	ctx := t.Context()

	got, err := s.SendAsOwner(ctx, inboxstore.Todo{Source: inboxstore.Source{
		Kind: inboxstore.FromZalo, ID: "lan", Account: "main",
		Ref: &inboxstore.Link{Kind: inboxstore.LinkZaloMessage, Ref: "z1"},
	}}, "Dạ em gửi ạ")
	require.NoError(t, err)
	assert.Equal(t, "main", zaloLabel)
	assert.Equal(t, messaging.Outgoing{Chat: "lan", Text: "Dạ em gửi ạ"}, zalo)
	assert.Equal(t, inboxstore.Link{Kind: inboxstore.LinkZaloSent, Ref: "z-sent", Account: "main", Chat: "lan",
		Label: sentLabel}, got)

	got, err = s.SendAsOwner(ctx, inboxstore.Todo{Source: inboxstore.Source{
		Kind: inboxstore.FromLark, ID: "oc_laura", Ref: &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_1"},
	}}, "Em gửi cô báo cáo ạ")
	require.NoError(t, err)
	assert.Equal(t, "receive_id_type=chat_id", larkQuery)
	assert.Equal(t, map[string]string{"receive_id": "oc_laura", "msg_type": "text",
		"content": `{"text":"Em gửi cô báo cáo ạ"}`}, larkBody)
	assert.Equal(t, inboxstore.Link{Kind: inboxstore.LinkLarkSent, Ref: "om_sent", Account: inboxLarkMe,
		Chat: "oc_laura", Label: sentLabel}, got, "a Lark item with no account is answered with lark:me")

	got, err = s.SendAsOwner(ctx, inboxstore.Todo{Source: inboxstore.Source{
		Kind: inboxstore.FromGmail, ID: "t1", Account: "work", Ref: &inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "m1"},
	}}, "Hi Kai, Monday works.")
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Link{Kind: inboxstore.LinkMail, Ref: "m2", Label: sentLabel}, got)
	assert.Equal(t, "t1", gmailTo, "the reply is in the item's thread")

	head, body, ok := strings.Cut(gmailRaw, "\r\n\r\n")
	require.True(t, ok, gmailRaw)
	assert.Equal(t, []string{
		"To: deals@genai.fund", "Subject: Re: Pilot Bcc: someone@else", "In-Reply-To: <abc@genai.fund>",
		"References: <first@genai.fund> <abc@genai.fund>", "MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8", "Content-Transfer-Encoding: base64",
	}, strings.Split(head, "\r\n"), "a line break read from the mail starts no header of its own")

	text, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\r\n", ""))
	require.NoError(t, err)
	assert.Equal(t, "Hi Kai, Monday works.", string(text))

	_, err = s.SendAsOwner(ctx, inboxstore.Todo{ID: "tdo_1", Source: inboxstore.Source{Kind: inboxstore.ByHand}}, "x")
	require.ErrorIs(t, err, storeerr.ErrInvalid)
}
