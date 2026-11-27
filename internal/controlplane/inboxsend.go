package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// inboxSender sends the owner's reply from the Inbox (issue #1111) to the item's own chat or thread, through his own
// account, and gives the message sent as the evidence that closes the item: a Zalo or WhatsApp chat through the
// Connection the item names (the messaging account's Send, as zalo_send and whatsapp_send send), a Lark chat with his
// own Lark sign-in (lark:me when the item names none) by Lark's send-message API, and a reply in the item's Gmail
// thread by the Gmail API, threaded as inbox-zero's replyToEmail threads one (apps/web/utils/gmail/mail.ts: to the
// Reply-To or From of the mail answered, "Re:" its subject, In-Reply-To and References its Message-ID, and the
// thread's id). Only the owner's Send calls it; no Bot's tool reaches it.
//
// A draft for a group tags the people it names as "@" and their names (issue #1200), each found among the chat's
// members by a name the chat gives him (messaging.FindTags): the names his kept messages there give him (Zalo,
// WhatsApp) or Lark's list of the chat's members. A Zalo tag goes to zca-js at its place in the text; a WhatsApp
// one is written "@" and his number with his id in the message's mentions (issue #852); a Lark one is written
// <at user_id="ou_…">Name</at> in the text message (Lark's "Sent message content", text). A name nobody, or two
// people, go by stays text, and so does every name when the members cannot be read. The reply quotes the item's own
// message where it is known: Zalo's quote and WhatsApp's context of a kept message, Lark's reply to it (POST
// /open-apis/im/v1/messages/:message_id/reply).
type inboxSender struct {
	send func(ctx context.Context, integration, label string, out messaging.Outgoing) (messaging.Message, error)
	// messages and members read what the server keeps of a Zalo or WhatsApp chat and who is in it, as inboxSent's.
	messages func(ctx context.Context, integration, label string, q messaging.Query) ([]messaging.Message, error)
	members  func(ctx context.Context, integration, label, chat string) (messaging.Members, error)
	// larkToken and googleToken are the access tokens of the owner's Lark and Google Connections, by label.
	larkToken   func(ctx context.Context, label string) (string, error)
	googleToken func(ctx context.Context, label string) (string, error)
	// larkURL is Lark's API, https://open.larksuite.com; gmailURL Gmail's, https://gmail.googleapis.com.
	larkURL, gmailURL string
	client            *http.Client
	log               *slog.Logger
}

// reader reads chats as inboxSent reads them, with the sender's own accounts.
func (s inboxSender) reader() inboxSent {
	return inboxSent{messages: s.messages, members: s.members, larkToken: s.larkToken, larkURL: s.larkURL,
		client: s.client}
}

// inboxLarkMe is the owner's own Lark sign-in, which a Lark item without an account is answered with.
const inboxLarkMe = "me"

// SendAsOwner sends text to the item's own chat or thread.
func (s inboxSender) SendAsOwner(ctx context.Context, t inboxstore.Todo, text string) (inboxstore.Link, error) {
	src := t.Source

	switch src.Kind {
	case inboxstore.FromZalo, inboxstore.FromWhatsApp:
		replyTo, err := s.keptReply(ctx, t)
		if err != nil {
			return inboxstore.Link{}, err
		}

		out := messaging.Outgoing{Chat: src.ID, Text: text, ReplyTo: replyTo}

		tags := s.tags(ctx, src.Kind, src.Account, src.ID, text)
		if src.Kind == inboxstore.FromZalo {
			out.Tags = tags
		} else {
			out.Text, out.Mentions = writeTags(text, tags, func(tag messaging.Tag) string {
				return "@" + messaging.MentionUser(tag.ID)
			})
		}

		msg, err := s.send(ctx, src.Kind, src.Account, out)
		if err != nil {
			return inboxstore.Link{}, err
		}

		kind := inboxstore.LinkZaloSent
		if src.Kind == inboxstore.FromWhatsApp {
			kind = inboxstore.LinkWhatsAppSent
		}

		return inboxstore.Link{Kind: kind, Ref: msg.ID, Account: src.Account, Chat: src.ID, Label: sentLabel}, nil
	case inboxstore.FromLark:
		return s.lark(ctx, t, text)
	case inboxstore.FromGmail:
		return s.gmail(ctx, t, text)
	}

	return inboxstore.Link{}, fmt.Errorf("%w: to-do %s came from %s, which has no chat of the owner's to send to",
		orchestration.ErrInvalid, t.ID, src.Kind)
}

// sentLabel names the evidence of a reply sent from the Inbox.
const sentLabel = "Sent from the Inbox"

// itemMessage is the kind of Link that names an item's own message at each messaging service.
var itemMessage = map[string]string{
	inboxstore.FromZalo: inboxstore.LinkZaloMessage, inboxstore.FromWhatsApp: inboxstore.LinkWhatsAppMessage,
	inboxstore.FromLark: inboxstore.LinkLarkMessage,
}

// replyRef is the id of the item's own message, which the reply quotes; empty when it names none.
func replyRef(t inboxstore.Todo) string {
	if t.Source.Ref == nil || t.Source.Ref.Kind != itemMessage[t.Source.Kind] {
		return ""
	}

	return t.Source.Ref.Ref
}

// keptReply is the item's own Zalo or WhatsApp message when the server keeps it, to be quoted: one it does not keep
// cannot be quoted, and the reply goes without, as the Hermes Zalo plugin sends a reply whose quote it lacks
// (adapter.py, send: body["quote"] only when its payload is known).
func (s inboxSender) keptReply(ctx context.Context, t inboxstore.Todo) (string, error) {
	id := replyRef(t)
	if id == "" {
		return "", nil
	}

	_, err := s.reader().kept(ctx, t.Source.Kind, t.Source.Account, t.Source.ID, id)
	if errors.Is(err, storeerr.ErrNotFound) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	return id, nil
}

// tagNamesRead is how many of a chat's latest messages kept are read for the names its members go by: the most a
// read of the store gives.
const tagNamesRead = 500

// tags are the members of a group a text names "@" and a name they go by in that chat (messaging.FindTags). None
// in a direct chat, and none when the members cannot be read: the text then goes as written, as zaloclaw's
// resolveOutboundMentions and the Hermes Zalo plugin send it when their lookup fails.
func (s inboxSender) tags(ctx context.Context, service, account, chat, text string) []messaging.Tag {
	if !strings.Contains(text, "@") {
		return nil
	}

	people, err := s.named(ctx, service, account, chat)
	if err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "inbox: a draft's tags stay text: the chat's members could not be read",
				slog.String("service", service), slog.String("chat", chat), slog.String("error", err.Error()))
		}

		return nil
	}

	return messaging.FindTags(text, people)
}

// named are the members of a group with the names the chat gives them: Lark's list of its members, and the names
// the latest messages kept of a Zalo or WhatsApp chat give their senders, each tagged by the id he sent the latest
// of them by, as mautrix-whatsapp tags the person by the id the chat knows him by (convertPill). A direct chat has
// none.
func (s inboxSender) named(ctx context.Context, service, account, chat string) ([]messaging.Named, error) {
	in, err := s.reader().chatMembers(ctx, service, account, chat)
	if err != nil || in.direct {
		return nil, err
	}

	people := make([]messaging.Named, len(in.people))
	who := map[string]int{}

	for i, p := range in.people {
		for _, id := range p.ids {
			who[id] = i
		}

		if p.name != "" && len(p.ids) > 0 {
			people[i] = messaging.Named{ID: p.ids[0], Names: []string{p.name}}
		}
	}

	if service == inboxstore.FromZalo || service == inboxstore.FromWhatsApp {
		msgs, err := s.messages(ctx, service, account, messaging.Query{Chat: chat, Limit: tagNamesRead})
		if err != nil {
			return nil, err
		}

		for _, m := range msgs {
			i, ok := who[m.Sender]
			if !ok || strings.TrimSpace(m.SenderName) == "" {
				continue
			}

			if people[i].ID == "" {
				people[i].ID = m.Sender
			}

			if !slices.Contains(people[i].Names, m.SenderName) {
				people[i].Names = append(people[i].Names, m.SenderName)
			}
		}
	}

	return people, nil
}

// writeTags writes each tag of a text as the service writes one, and gives the ids it tags, once each.
func writeTags(text string, tags []messaging.Tag, write func(messaging.Tag) string) (string, []string) {
	if len(tags) == 0 {
		return text, nil
	}

	var (
		b   strings.Builder
		ids []string
	)

	at := 0
	for _, tag := range tags {
		b.WriteString(text[at:tag.At])
		b.WriteString(write(tag))
		at = tag.At + tag.Len

		if !slices.Contains(ids, tag.ID) {
			ids = append(ids, tag.ID)
		}
	}

	b.WriteString(text[at:])

	return b.String(), ids
}

// lark sends a text message to the item's Lark chat as the owner, with his user access token: a reply to the item's
// own message when it names one (POST /open-apis/im/v1/messages/:message_id/reply), else a message to the chat
// (POST /open-apis/im/v1/messages, receive_id_type chat_id). The members it tags are written <at user_id="ou_…">
// and the name as the text writes it </at> (Lark's text content).
func (s inboxSender) lark(ctx context.Context, t inboxstore.Todo, text string) (inboxstore.Link, error) {
	account := t.Source.Account
	if account == "" {
		account = inboxLarkMe
	}

	token, err := s.larkToken(ctx, account)
	if err != nil {
		return inboxstore.Link{}, err
	}

	text, _ = writeTags(text, s.tags(ctx, inboxstore.FromLark, account, t.Source.ID, text),
		func(tag messaging.Tag) string {
			return `<at user_id="` + tag.ID + `">` + tag.Name + `</at>`
		})

	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return inboxstore.Link{}, fmt.Errorf("lark: writing the message: %w", err)
	}

	message := map[string]string{"receive_id": t.Source.ID, "msg_type": "text", "content": string(content)}
	target := s.larkURL + "/open-apis/im/v1/messages?receive_id_type=chat_id"

	if id := replyRef(t); id != "" {
		message = map[string]string{"msg_type": "text", "content": string(content)}
		target = s.larkURL + "/open-apis/im/v1/messages/" + url.PathEscape(id) + "/reply"
	}

	body, err := json.Marshal(message)
	if err != nil {
		return inboxstore.Link{}, fmt.Errorf("lark: writing the message: %w", err)
	}

	var got struct {
		larkAnswer
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}

	answer, err := s.call(ctx, http.MethodPost, target, token, body, &got)
	if err != nil {
		return inboxstore.Link{}, fmt.Errorf("lark: sending to %s: %w", t.Source.ID, err)
	}

	if answer != http.StatusOK || got.Code != 0 || got.Data.MessageID == "" {
		return inboxstore.Link{}, fmt.Errorf("lark: sending to %s answered %d, code %d: %s", t.Source.ID, answer,
			got.Code, got.Msg)
	}

	return inboxstore.Link{
		Kind: inboxstore.LinkLarkSent, Ref: got.Data.MessageID, Account: account, Chat: t.Source.ID, Label: sentLabel,
	}, nil
}

// gmailHeaders are the headers of the mail answered that a reply is threaded by.
var gmailHeaders = []string{"From", "Reply-To", "Subject", "Message-ID", "References"}

// gmail replies in the item's thread to the mail it came from, as the owner, with his Google Connection.
func (s inboxSender) gmail(ctx context.Context, t inboxstore.Todo, text string) (inboxstore.Link, error) {
	if t.Source.Ref == nil || t.Source.Ref.Kind != inboxstore.LinkMail {
		return inboxstore.Link{}, fmt.Errorf("%w: to-do %s names no mail to answer", orchestration.ErrInvalid, t.ID)
	}

	token, err := s.googleToken(ctx, t.Source.Account)
	if err != nil {
		return inboxstore.Link{}, err
	}

	q := url.Values{"format": {"metadata"}, "metadataHeaders": gmailHeaders}

	var answered struct {
		ThreadID string `json:"threadId"`
		Payload  struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"payload"`
	}

	status, err := s.call(ctx, http.MethodGet, s.gmailURL+"/gmail/v1/users/me/messages/"+
		url.PathEscape(t.Source.Ref.Ref)+"?"+q.Encode(), token, nil, &answered)
	if err != nil {
		return inboxstore.Link{}, fmt.Errorf("gmail: reading the mail answered: %w", err)
	}

	if status != http.StatusOK {
		return inboxstore.Link{}, fmt.Errorf("gmail: reading the mail answered answered %d", status)
	}

	h := map[string]string{}
	for _, header := range answered.Payload.Headers {
		h[strings.ToLower(header.Name)] = header.Value
	}

	to := h["reply-to"]
	if to == "" {
		to = h["from"]
	}

	if to == "" {
		return inboxstore.Link{}, fmt.Errorf("%w: the mail answered names nobody to reply to", orchestration.ErrInvalid)
	}

	thread := answered.ThreadID
	if thread == "" {
		thread = t.Source.ID
	}

	raw := gmailReply(to, h["subject"], h["message-id"], h["references"], text)

	body, err := json.Marshal(map[string]string{"raw": base64.URLEncoding.EncodeToString(raw), "threadId": thread})
	if err != nil {
		return inboxstore.Link{}, fmt.Errorf("gmail: writing the reply: %w", err)
	}

	var sent struct {
		ID string `json:"id"`
	}

	status, err = s.call(ctx, http.MethodPost, s.gmailURL+"/gmail/v1/users/me/messages/send", token, body, &sent)
	if err != nil {
		return inboxstore.Link{}, fmt.Errorf("gmail: sending the reply: %w", err)
	}

	if status != http.StatusOK || sent.ID == "" {
		return inboxstore.Link{}, fmt.Errorf("gmail: sending the reply answered %d", status)
	}

	return inboxstore.Link{Kind: inboxstore.LinkMail, Ref: sent.ID, Label: sentLabel}, nil
}

// gmailReply is the reply as a plain text mail (RFC 5322): to whom, "Re:" the subject unless it has it, threaded
// under the mail answered by In-Reply-To and References (RFC 5322, 3.6.4), the text in base64 UTF-8.
func gmailReply(to, subject, messageID, references, text string) []byte {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
		subject = "Re: " + subject
	}

	var b bytes.Buffer

	fmt.Fprintf(&b, "To: %s\r\n", oneLine(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", oneLine(subject)))

	if messageID != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", oneLine(messageID))
		fmt.Fprintf(&b, "References: %s\r\n", strings.TrimSpace(oneLine(references)+" "+oneLine(messageID)))
	}

	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n")

	enc := base64.StdEncoding.EncodeToString([]byte(text))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}

	b.WriteString(enc + "\r\n")

	return b.Bytes()
}

// oneLine keeps a header's value on its line: a line break in a value read from a mail would start a header of
// its own.
func oneLine(v string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(v)), " ")
}

// call sends one request with a bearer token and reads its JSON answer into out, whatever its status.
func (s inboxSender) call(ctx context.Context, method, target, token string, body []byte, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Authorization", "Bearer "+token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck // Read whole below.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}

	if err := json.Unmarshal(raw, out); err != nil && resp.StatusCode == http.StatusOK {
		return 0, fmt.Errorf("answered %d, not JSON", resp.StatusCode)
	}

	return resp.StatusCode, nil
}
