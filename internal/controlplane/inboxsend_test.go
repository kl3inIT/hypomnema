package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
// answered, "Re:" its subject and threaded under it. An item from nowhere to send to is refused. A Zalo item's
// message is quoted (issue #1200).
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
		messages: func(_ context.Context, _, _ string, q messaging.Query) ([]messaging.Message, error) {
			return []messaging.Message{{ID: q.ID, Chat: q.Chat}}, nil
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
	assert.Equal(t, messaging.Outgoing{Chat: "lan", Text: "Dạ em gửi ạ", ReplyTo: "z1"}, zalo,
		"it quotes the item's message (issue #1200)")
	assert.Equal(t, inboxstore.Link{Kind: inboxstore.LinkZaloSent, Ref: "z-sent", Account: "main", Chat: "lan",
		Label: sentLabel}, got)

	got, err = s.SendAsOwner(ctx, inboxstore.Todo{Source: inboxstore.Source{Kind: inboxstore.FromLark, ID: "oc_laura"}},
		"Em gửi cô báo cáo ạ")
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

// Scenario: issue #1200. The owner's draft for a group tags the members it names by a name the chat gives them,
// each as the item's service tags, and answers the item's own message: Zalo, the tag at its place in the text with
// the quote of the message; WhatsApp, "@" and the member's number with his id among the mentions, the person known
// by his number and his LID being one; Lark, <at user_id> in a reply to the message. "@Anh", which two members'
// names begin with, tags nobody; a direct chat tags nobody; a message the server does not keep goes unquoted; and a
// chat whose members cannot be read is sent as written.
func TestTheOwnersDraftTagsWhomItNamesAndAnswersTheItemsMessage(t *testing.T) {
	t.Parallel()

	var (
		lark       map[string]string
		larkPath   string
		sent       []messaging.Outgoing
		unreadable bool
	)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		switch r.URL.Path {
		case "/open-apis/im/v1/chats/oc_g":
			_, _ = w.Write([]byte(`{"code":0,"data":{"name":"Dự án","chat_mode":"group"}}`))
		case "/open-apis/im/v1/chats/oc_g/members":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"member_id":"ou_lan","name":"Lan Anh"},` +
				`{"member_id":"ou_tom","name":"Tom"}],"has_more":false}}`))
		case "/open-apis/im/v1/messages/om_1":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_1","chat_id":"oc_g","create_time":"1"}]}}`))
		case "/open-apis/im/v1/messages/om_1/reply":
			larkPath = r.URL.Path
			_ = json.Unmarshal(body, &lark)
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_reply"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":230001,"msg":"no"}`))
		}
	}))
	t.Cleanup(api.Close)

	lanPN, lanLID := "84911111111@s.whatsapp.net", "168453096898812@lid"
	kept := map[string][]messaging.Message{
		"zalo/g1": {
			{ID: "z1", Sender: "u-lan", SenderName: "Lan Anh"},
			{ID: "z0", Sender: "u-tuan", SenderName: "Anh Tuấn"},
			{ID: "z-1", Sender: "u-minh", SenderName: "Anh Minh"},
			{ID: "z-2", Sender: "u-left", SenderName: "Lan Anh"},
		},
		"zalo/u9":     {{ID: "d1", Sender: "u9", SenderName: "Lan Anh"}},
		"whatsapp/wg": {{ID: "w1", Sender: lanPN, SenderName: "Lan"}, {ID: "w0", Sender: lanLID, SenderName: "Lan"}},
	}

	s := inboxSender{
		send: func(_ context.Context, _, _ string, out messaging.Outgoing) (messaging.Message, error) {
			sent = append(sent, out)

			return messaging.Message{ID: "sent"}, nil
		},
		messages: func(_ context.Context, service, _ string, q messaging.Query) ([]messaging.Message, error) {
			var out []messaging.Message

			for _, m := range kept[service+"/"+q.Chat] {
				if q.ID == "" || m.ID == q.ID {
					out = append(out, m)
				}
			}

			return out, nil
		},
		members: func(_ context.Context, service, _, chat string) (messaging.Members, error) {
			switch {
			case unreadable:
				return messaging.Members{}, errors.New("zalo: not connected")
			case chat == "u9":
				return messaging.Members{Direct: true, IDs: []string{"u9"}}, nil
			case service == "whatsapp":
				return messaging.Members{IDs: []string{lanLID, lanPN}, People: [][]string{{lanLID, lanPN}}}, nil
			}

			return messaging.Members{IDs: []string{"u-lan", "u-tuan", "u-minh", "u-me"}}, nil
		},
		larkToken: func(context.Context, string) (string, error) { return "u-lark", nil },
		larkURL:   api.URL, client: api.Client(),
	}
	ctx := t.Context()

	send := func(kind, chat, ref, text string) messaging.Outgoing {
		t.Helper()

		sent = nil
		_, err := s.SendAsOwner(ctx, inboxstore.Todo{Source: inboxstore.Source{
			Kind: kind, ID: chat, Account: "main", Ref: &inboxstore.Link{Kind: itemMessage[kind], Ref: ref},
		}}, text)
		require.NoError(t, err)
		require.Len(t, sent, 1)

		return sent[0]
	}

	assert.Equal(t, messaging.Outgoing{Chat: "g1", Text: "@Lan Anh dạ vâng chị", ReplyTo: "z1",
		Tags: []messaging.Tag{{ID: "u-lan", At: 0, Len: 8, Name: "Lan Anh"}}},
		send(inboxstore.FromZalo, "g1", "z1", "@Lan Anh dạ vâng chị"),
		"the member named Lan Anh is tagged, the one who left is not a member, and the message is quoted")
	assert.Equal(t, messaging.Outgoing{Chat: "g1", Text: "@Anh dạ", ReplyTo: "z1"},
		send(inboxstore.FromZalo, "g1", "z1", "@Anh dạ"), "two names begin with Anh: nobody is tagged")
	assert.Equal(t, messaging.Outgoing{Chat: "u9", Text: "@Lan Anh dạ"},
		send(inboxstore.FromZalo, "u9", "gone", "@Lan Anh dạ"), "a direct chat tags nobody; a message not kept is not quoted")

	assert.Equal(t, messaging.Outgoing{Chat: "wg", Text: "@84911111111 ơi, em gửi", ReplyTo: "w1",
		Mentions: []string{lanPN}}, send(inboxstore.FromWhatsApp, "wg", "w1", "@Lan ơi, em gửi"),
		"Lan by her number and her LID is one person, tagged by the id she wrote last by")

	link, err := s.SendAsOwner(ctx, inboxstore.Todo{Source: inboxstore.Source{
		Kind: inboxstore.FromLark, ID: "oc_g", Ref: &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_1"},
	}}, "@Lan Anh dạ, @Tom em gửi")
	require.NoError(t, err)
	assert.Equal(t, "om_reply", link.Ref)
	assert.Equal(t, "/open-apis/im/v1/messages/om_1/reply", larkPath, "a reply to the item's message")
	assert.Equal(t, "text", lark["msg_type"])

	var content struct{ Text string }
	require.NoError(t, json.Unmarshal([]byte(lark["content"]), &content))
	assert.Equal(t, `<at user_id="ou_lan">Lan Anh</at> dạ, <at user_id="ou_tom">Tom</at> em gửi`, content.Text)

	unreadable = true
	assert.Equal(t, messaging.Outgoing{Chat: "g1", Text: "@Lan Anh dạ", ReplyTo: "z1"},
		send(inboxstore.FromZalo, "g1", "z1", "@Lan Anh dạ"), "members unread: sent as written")
}

func TestLarkSendsWithoutAQuoteWhenTheMessageCannotBeRepliedTo(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		quoteChat  string
		replyError string
		plainError string
	}{
		{name: "another chat", quoteChat: "oc_other"},
		{
			name: "recalled after lookup", quoteChat: "oc_source",
			replyError: `{"code":230011,"msg":"The message is recalled."}`,
		},
		{
			name: "invisible after lookup", quoteChat: "oc_source",
			replyError: `{"code":230050,"msg":"The message is invisible to the operator."}`,
		},
		{
			name: "scheduled for deletion", quoteChat: "oc_source",
			replyError: `{"code":230111,"msg":"Action unavailable as the message will self-destruct soon."}`,
		},
		{
			name: "plain send reports its own error", quoteChat: "oc_source",
			replyError: `{"code":230011,"msg":"The message is recalled."}`,
			plainError: `{"code":230110,"msg":"Action unavailable as the message has been deleted."}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			const messages = "/open-apis/im/v1/messages"

			var posts []string

			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET " + messages + "/om_quote":
					_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"message_id":"om_quote","chat_id":"` +
						test.quoteChat + `","create_time":"1"}]}}`))
				case "POST " + messages + "/om_quote/reply":
					posts = append(posts, "reply")

					if test.replyError != "" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(test.replyError))

						return
					}

					_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_wrong_chat"}}`))
				case "POST " + messages:
					posts = append(posts, "plain")

					var body map[string]string
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, "chat_id", r.URL.Query().Get("receive_id_type"))
					assert.Equal(t, map[string]string{
						"receive_id": "oc_source", "msg_type": "text", "content": `{"text":"Dạ em gửi ạ"}`,
					}, body)

					if test.plainError != "" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(test.plainError))

						return
					}

					_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_plain"}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(api.Close)

			s := inboxSender{
				larkToken: func(context.Context, string) (string, error) { return "u-lark", nil },
				larkURL:   api.URL, client: api.Client(),
			}
			link, err := s.SendAsOwner(t.Context(), inboxstore.Todo{Source: inboxstore.Source{
				Kind: inboxstore.FromLark, ID: "oc_source",
				Ref: &inboxstore.Link{Kind: inboxstore.LinkLarkMessage, Ref: "om_quote"},
			}}, "Dạ em gửi ạ")
			api.Close()

			wantPosts := []string{"plain"}
			if test.replyError != "" {
				wantPosts = []string{"reply", "plain"}
			}

			assert.Equal(t, wantPosts, posts)

			if test.plainError != "" {
				require.EqualError(t, err,
					"lark: sending to oc_source: answered 400, code 230110: Action unavailable as the message has been deleted.")
				assert.Empty(t, link)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, inboxstore.Link{Kind: inboxstore.LinkLarkSent, Ref: "om_plain", Account: inboxLarkMe,
				Chat: "oc_source", Label: sentLabel}, link)
		})
	}
}
