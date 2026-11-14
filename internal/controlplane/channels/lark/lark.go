// Package lark is LarkChannel: the Channels of kind lark (issues #136, #169
// and #404), a Lark app each. It is Lark's side of the runtime in package
// channels, as MemoryOS's LarkChannelAdapter is of its ChannelSupervisor:
// the persistent connection and its events, CardKit's cards that fill as a
// reply is written, the emoji marks, the mention tags, the file types, and
// how an app and a person are named at Lark.
//
// It follows the Feishu channels of OpenClaw (extensions/feishu) and Hermes
// (plugins/platforms/feishu) and the owner's MemoryOS (mem-246-lark-bot):
// events come over a persistent connection, so no public address is opened.
package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	larksdk "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
)

// LarkChannel is the Channel of kind lark: a Lark app.
type LarkChannel struct{}

var _ channels.Channel = LarkChannel{}

// Kind is lark.
func (LarkChannel) Kind() channels.Kind { return channelstore.KindLark }

// Capabilities are a Lark card's.
func (LarkChannel) Capabilities() channels.Capabilities {
	return channels.Capabilities{MaxText: cardLimit, Platform: "Lark"}
}

// Open connects to Lark as the Channel's app.
func (LarkChannel) Open(_ context.Context, channel channelstore.Channel, secret string) (channels.Conn, error) {
	return Connect(channel.AppID, secret), nil
}

// Lark through its own Go SDK (github.com/larksuite/oapi-sdk-go, MIT), which
// lark-cli, Lark's own Go program, uses for the same things: the persistent
// connection (ws), the event dispatcher, and the typed calls. Tried against
// the owner's app on 2026-11-11 (issue #136): a direct message arrives with
// its ids and the sender's tenant, and a CardKit card is created, sent by its
// id, filled by element with a rising sequence and closed.

const (
	// connectWait is how long the persistent connection gets to open.
	connectWait = 20 * time.Second
	sendTimeout = 30 * time.Second
	// mediaTimeout is how long a file's upload may take (OpenClaw
	// extensions/feishu/src/media.ts, FEISHU_MEDIA_HTTP_TIMEOUT_MS).
	mediaTimeout = 2 * time.Minute
	// cardLimit is the most characters one card shows: a card holds 30 KB and
	// a character may take three bytes. A longer reply goes on in further
	// cards (channels' split).
	cardLimit = 8000
	// openIDPrefix starts a person's open_id, which names their direct chat
	// with the bot until Lark gives the chat's own id.
	openIDPrefix = "ou_"
)

// The emoji a Lark Channel puts on a person's message, as OpenClaw and
// Hermes do: Typing while the Bot works on it, and a cross when it failed.
var emoji = map[channels.Mark]string{channels.MarkWorking: "Typing", channels.MarkFailed: "CrossMark"}

// answerElement is the id of the card's one element, which the text fills.
const answerElement = "answer"

// sdk is Lark through the SDK.
type sdk struct {
	appID  string
	secret string
	api    *larksdk.Client
	// media uploads files, with a longer wait than api's (OpenClaw's media client).
	media *larksdk.Client

	mu sync.Mutex
	// seqs is the last sequence each card being written was updated with: Lark takes an update only with a
	// higher one, and reopening a card's writing spends sequences its caller does not count.
	seqs map[string]int
}

// next is the sequence of a card's next update: at least the caller's, and
// above every one used.
func (s *sdk) next(cardID string, seq int) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if last := s.seqs[cardID]; seq <= last {
		seq = last + 1
	}

	s.seqs[cardID] = seq

	return seq
}

// Connect returns Lark for an app. Only Lark international is served
// (open.larksuite.com), as the Integration is.
func Connect(appID, secret string) channels.Conn {
	return &sdk{
		appID: appID, secret: secret, seqs: map[string]int{},
		// The SDK writes the address of a connection, which holds its ticket, at its info level.
		api: larksdk.NewClient(appID, secret, larksdk.WithOpenBaseUrl(larksdk.LarkBaseUrl),
			larksdk.WithLogLevel(larkcore.LogLevelError), larksdk.WithReqTimeout(sendTimeout)),
		media: larksdk.NewClient(appID, secret, larksdk.WithOpenBaseUrl(larksdk.LarkBaseUrl),
			larksdk.WithLogLevel(larkcore.LogLevelError), larksdk.WithReqTimeout(mediaTimeout)),
	}
}

func (s *sdk) Listen(ctx context.Context, on channels.Events) error {
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, e *larkim.P2MessageReceiveV1) error {
			on.Message(messageOf(e))

			return nil
		}).
		// The bot was added to a group or left one (im.chat.member.bot.added_v1 and deleted_v1): the roster
		// changes at once, as MemoryOS's LarkChannelAdapter does. An app that does not subscribe them still finds
		// its groups in the listing every ten minutes.
		OnP2ChatMemberBotAddedV1(func(_ context.Context, e *larkim.P2ChatMemberBotAddedV1) error {
			if e != nil && e.Event != nil && deref(e.Event.ChatId) != "" {
				on.Joined(channels.Group{ChatID: deref(e.Event.ChatId), Name: deref(e.Event.Name)})
			}

			return nil
		}).
		OnP2ChatMemberBotDeletedV1(func(_ context.Context, e *larkim.P2ChatMemberBotDeletedV1) error {
			if e != nil && e.Event != nil && deref(e.Event.ChatId) != "" {
				on.Left(deref(e.Event.ChatId))
			}

			return nil
		}).
		// A press of a card's button (the callback card.action.trigger): what press answers is shown to who
		// pressed, as a toast.
		OnP2CardActionTrigger(func(_ context.Context, e *callback.CardActionTriggerEvent) (
			*callback.CardActionTriggerResponse, error,
		) {
			said := on.Press(pressOf(e))
			if said == "" {
				return &callback.CardActionTriggerResponse{}, nil
			}

			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: said}}, nil
		}).
		// A press of an item of the bot's menu (application.bot.menu_v6), which Lark's developer console sets: it
		// becomes a message from who pressed (OpenClaw extensions/feishu/src/monitor.account.ts, handleBotMenu).
		OnP2BotMenuV6(func(_ context.Context, e *larkapplication.P2BotMenuV6) error {
			if on.Menu != nil {
				on.Menu(menuOf(e))
			}

			return nil
		}).
		// Lark tells an app when its messages were read; nothing is done with it, and the SDK logs an event
		// nobody handles as an error.
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil })

	client := larkws.NewClient(s.appID, s.secret, larkws.WithEventHandler(handler),
		larkws.WithDomain(larksdk.LarkBaseUrl), larkws.WithLogLevel(larkcore.LogLevelError))

	// Start returns only when the connection could not be opened: once open it blocks for good, and the SDK
	// has no way to close it. So a failure shows within the wait, and no answer by then is a connection.
	failed := make(chan error, 1)

	go func() { failed <- client.Start(ctx) }()

	select {
	case err := <-failed:
		if err == nil {
			err = errors.New("the connection ended")
		}

		return fmt.Errorf("opening the persistent connection to Lark: %w", err)
	case <-time.After(connectWait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// messageOf reads what the bot needs of a received message.
func messageOf(e *larkim.P2MessageReceiveV1) channels.Message {
	var m channels.Message

	if e == nil || e.Event == nil || e.Event.Message == nil || e.Event.Sender == nil {
		return m
	}

	msg, sender := e.Event.Message, e.Event.Sender
	m.ID, m.ChatID = deref(msg.MessageId), deref(msg.ChatId)
	m.Direct = deref(msg.ChatType) == "p2p"
	m.IsText = deref(msg.MessageType) == "text"

	if m.IsText {
		m.Text = textOf(deref(msg.Content))
	}

	for _, mention := range msg.Mentions {
		if mention == nil {
			continue
		}

		at := channels.Mention{Key: deref(mention.Key), Name: deref(mention.Name)}
		if mention.Id != nil {
			at.PersonID = deref(mention.Id.OpenId)
		}

		m.Mentions = append(m.Mentions, at)
	}

	m.ReplyTo = deref(msg.ParentId)
	m.FromPerson = deref(sender.SenderType) == "user"
	m.SenderOrg = deref(sender.TenantKey)

	if sender.SenderId != nil {
		m.Sender = deref(sender.SenderId.OpenId)
	}

	if e.EventV2Base != nil && e.EventV2Base.Header != nil {
		m.Org = e.EventV2Base.Header.TenantKey
	}

	return m
}

// menuOf reads who pressed an item of the bot's menu, and the item's key.
func menuOf(e *larkapplication.P2BotMenuV6) channels.MenuPress {
	var p channels.MenuPress

	if e == nil || e.Event == nil {
		return p
	}

	p.Key = deref(e.Event.EventKey)
	if e.Event.Operator != nil && e.Event.Operator.OperatorId != nil {
		p.Operator = deref(e.Event.Operator.OperatorId.OpenId)
	}

	if e.Event.Timestamp != nil {
		p.At = fmt.Sprint(*e.Event.Timestamp)
	}

	if e.EventV2Base != nil && e.EventV2Base.Header != nil {
		p.Org = e.EventV2Base.Header.TenantKey
		if p.At == "" {
			p.At = e.EventV2Base.Header.EventID
		}
	}

	return p
}

// pressOf reads who pressed a card's button and what the button carries.
func pressOf(e *callback.CardActionTriggerEvent) channels.Press {
	p := channels.Press{Value: map[string]string{}}

	if e == nil || e.Event == nil {
		return p
	}

	if e.Event.Operator != nil {
		p.Operator = e.Event.Operator.OpenID
	}

	if e.Event.Action != nil {
		for k, v := range e.Event.Action.Value {
			if text, ok := v.(string); ok {
				p.Value[k] = text
			}
		}
	}

	return p
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// textOf is the text of a Lark text message's content, which is JSON.
func textOf(content string) string {
	var parsed struct {
		Text string `json:"text"`
	}

	if json.Unmarshal([]byte(content), &parsed) != nil {
		return ""
	}

	return parsed.Text
}

// inert writes Lark's mention tags out instead of letting them act: an <at>
// in a reply, or in a document the reply quotes, would notify whoever it
// names, a whole group for <at id=all> (MemoryOS, LarkReplyCard.inert).
func inert(text string) string {
	return mentionTag.ReplaceAllString(text, "&lt;$1$2")
}

var mentionTag = regexp.MustCompile(`(?i)<(/?)(at|person)\b`)

// Notice sends a text message.
func (s *sdk) Notice(ctx context.Context, chatID, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	_, err = s.send(ctx, chatID, "text", string(content))

	return err
}

// The first bytes of the files Lark files under a type of its own.
var (
	pdfMagic  = []byte("%PDF-")
	zipMagic  = []byte("PK\x03\x04")
	oleMagic  = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	fileTypes = map[string]struct {
		kind  string
		magic []byte
	}{
		".pdf": {"pdf", pdfMagic},
		".doc": {"doc", oleMagic}, ".docx": {"doc", zipMagic},
		".xls": {"xls", oleMagic}, ".xlsx": {"xls", zipMagic},
		".ppt": {"ppt", oleMagic}, ".pptx": {"ppt", zipMagic},
	}
)

// fileType is the file_type Lark files an upload under: pdf, doc, xls or ppt
// by the name's extension (OpenClaw media.ts, detectFileType), when the
// file's bytes are of that kind, and stream for anything else. A name alone
// does not make a file what it says (OpenClaw: never pass a filename to
// signature detection).
func fileType(name string, data []byte) string {
	t, ok := fileTypes[strings.ToLower(path.Ext(name))]
	if !ok || !bytes.HasPrefix(data, t.magic) {
		return "stream"
	}

	return t.kind
}

// SendFile uploads a file (POST /open-apis/im/v1/files) and sends it as a file
// message in the chat, as OpenClaw (extensions/feishu/src/media.ts) and Hermes
// (plugins/platforms/feishu/adapter.py, _send_uploaded_file_message) upload
// and then send the file_key, filed under the type its bytes are. It needs
// the scope im:resource.
func (s *sdk) SendFile(ctx context.Context, chatID string, a channels.Attachment) error {
	resp, err := s.media.Im.V1.File.Create(ctx, larkim.NewCreateFileReqBuilder().Body(
		larkim.NewCreateFileReqBodyBuilder().FileType(fileType(a.Name, a.Data)).FileName(a.Name).
			File(bytes.NewReader(a.Data)).Build(),
	).Build())
	if err != nil {
		return fmt.Errorf("uploading a file to Lark: %w", err)
	}

	if err := refused("uploading a file to Lark", resp.Success(), resp.Code, resp.Msg); err != nil {
		return err
	}

	if resp.Data == nil || deref(resp.Data.FileKey) == "" {
		return errors.New("uploading a file to Lark: Lark gave no file_key")
	}

	content, err := json.Marshal(map[string]string{"file_key": *resp.Data.FileKey})
	if err != nil {
		return err
	}

	_, err = s.send(ctx, chatID, "file", string(content))

	return err
}

// send sends a message to a chat, or to a person's direct chat with the bot
// by their open_id (receive_id_type open_id, as OpenClaw sends to
// user:<open_id>), and returns the id of the chat Lark put it in.
func (s *sdk) send(ctx context.Context, chatID, kind, content string) (string, error) {
	idType := "chat_id"
	if strings.HasPrefix(chatID, openIDPrefix) {
		idType = "open_id"
	}

	resp, err := s.api.Im.V1.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(idType).
		Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType(kind).Content(content).Build()).Build())
	if err != nil {
		return "", fmt.Errorf("sending to Lark: %w", err)
	}

	if err := refused("sending to Lark", resp.Success(), resp.Code, resp.Msg); err != nil {
		return "", err
	}

	if resp.Data != nil && deref(resp.Data.ChatId) != "" {
		return *resp.Data.ChatId, nil
	}

	return chatID, nil
}

// streamingClosed is the code Lark answers a card's update with once its
// writing has ended, as it does by itself after ten minutes (MemoryOS,
// LarkReplyCard.STREAMING_CLOSED).
const streamingClosed = 300309

// reply is a card being written, which Lark updates by element with a rising
// sequence (CardKit).
type reply struct {
	s    *sdk
	card string
	seq  int
}

// OpenReply opens a card that is still being written.
func (s *sdk) OpenReply(ctx context.Context, chatID, replyTo, text string) (channels.Reply, string, error) {
	cardID, larkChatID, err := s.openCard(ctx, chatID, replyTo, inert(text))
	if err != nil {
		return nil, "", err
	}

	return &reply{s: s, card: cardID, seq: 1}, larkChatID, nil
}

// Update replaces the card's text.
func (r *reply) Update(ctx context.Context, text string) error {
	err := r.s.updateCard(ctx, r.card, inert(text), r.seq)
	r.seq++

	return err
}

// Close sets the card's final text and ends its writing.
func (r *reply) Close(ctx context.Context, text string) error {
	return r.s.closeCard(ctx, r.card, inert(text), r.seq)
}

// Mark puts an emoji on a message (a reaction) and returns what takes it off.
func (s *sdk) Mark(ctx context.Context, messageID string, mark channels.Mark) (func(context.Context) error, error) {
	reaction, err := s.react(ctx, messageID, emoji[mark])
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) error { return s.unreact(ctx, messageID, reaction) }, nil
}

func (s *sdk) openCard(ctx context.Context, chatID, replyTo, text string) (cardID, larkChatID string, err error) {
	card, err := json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": true, "update_multi": true,
			"summary": map[string]string{"content": summaryOf(text)},
			// How fast Lark types a change out: MemoryOS's setting, quicker than Lark's own.
			"streaming_config": map[string]any{
				"print_frequency_ms": map[string]int{"default": 30}, "print_step": map[string]int{"default": 2},
				"print_strategy": "fast",
			},
		},
		"body": map[string]any{"elements": []any{
			map[string]string{"tag": "markdown", "element_id": answerElement, "content": text},
		}},
	})
	if err != nil {
		return "", "", err
	}

	created, err := s.api.Cardkit.V1.Card.Create(ctx, larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().Type("card_json").Data(string(card)).Build()).Build())
	if err != nil {
		return "", "", fmt.Errorf("creating a card: %w", err)
	}

	if err := refused("creating a card", created.Success(), created.Code, created.Msg); err != nil {
		return "", "", err
	}

	if created.Data == nil || created.Data.CardId == nil {
		return "", "", errors.New("creating a card: Lark gave no card id")
	}

	cardID = *created.Data.CardId

	content, err := json.Marshal(map[string]any{"type": "card", "data": map[string]string{"card_id": cardID}})
	if err != nil {
		return "", "", err
	}

	if replyTo == "" {
		larkChatID, err = s.send(ctx, chatID, "interactive", string(content))

		return cardID, larkChatID, err
	}

	// A reply quotes the person's message, as OpenClaw, Hermes and MemoryOS answer. When that message was
	// withdrawn the card is sent on its own (Hermes).
	resp, err := s.api.Im.V1.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(replyTo).
		Body(larkim.NewReplyMessageReqBodyBuilder().MsgType("interactive").Content(string(content)).
			ReplyInThread(false).Build()).Build())
	if err != nil || !resp.Success() {
		larkChatID, err = s.send(ctx, chatID, "interactive", string(content))

		return cardID, larkChatID, err
	}

	if resp.Data != nil && deref(resp.Data.ChatId) != "" {
		return cardID, *resp.Data.ChatId, nil
	}

	return cardID, chatID, nil
}

func (s *sdk) react(ctx context.Context, messageID, emoji string) (string, error) {
	resp, err := s.api.Im.V1.MessageReaction.Create(ctx, larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).Body(larkim.NewCreateMessageReactionReqBodyBuilder().
		ReactionType(larkim.NewEmojiBuilder().EmojiType(emoji).Build()).Build()).Build())
	if err != nil {
		return "", fmt.Errorf("reacting to a message: %w", err)
	}

	if err := refused("reacting to a message", resp.Success(), resp.Code, resp.Msg); err != nil {
		return "", err
	}

	if resp.Data == nil {
		return "", nil
	}

	return deref(resp.Data.ReactionId), nil
}

func (s *sdk) unreact(ctx context.Context, messageID, reactionID string) error {
	resp, err := s.api.Im.V1.MessageReaction.Delete(ctx, larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).ReactionId(reactionID).Build())
	if err != nil {
		return fmt.Errorf("taking a reaction off a message: %w", err)
	}

	return refused("taking a reaction off a message", resp.Success(), resp.Code, resp.Msg)
}

func (s *sdk) updateCard(ctx context.Context, cardID, text string, seq int) error {
	code, msg, err := s.content(ctx, cardID, text, s.next(cardID, seq))
	if err != nil {
		return err
	}

	// Lark ended the card's writing by itself: it is switched on again and the update is sent once more
	// (MemoryOS).
	if code == streamingClosed {
		if err := s.streaming(ctx, cardID, true, "", s.next(cardID, 0)); err != nil {
			return err
		}

		code, msg, err = s.content(ctx, cardID, text, s.next(cardID, 0))
		if err != nil {
			return err
		}
	}

	return refused("updating a card", code == 0, code, msg)
}

// content replaces the text of the card's one element.
func (s *sdk) content(ctx context.Context, cardID, text string, seq int) (code int, msg string, err error) {
	resp, err := s.api.Cardkit.V1.CardElement.Content(ctx, larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).ElementId(answerElement).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().Content(text).Sequence(seq).Build()).Build())
	if err != nil {
		return 0, "", fmt.Errorf("updating a card: %w", err)
	}

	return resp.Code, resp.Msg, nil
}

func (s *sdk) closeCard(ctx context.Context, cardID, text string, seq int) error {
	if err := s.updateCard(ctx, cardID, text, seq); err != nil {
		return err
	}

	err := s.streaming(ctx, cardID, false, summaryOf(text), s.next(cardID, 0))

	s.mu.Lock()
	delete(s.seqs, cardID)
	s.mu.Unlock()

	return err
}

// streaming switches a card's writing on or off; off, it also sets what a
// chat list shows for the card.
func (s *sdk) streaming(ctx context.Context, cardID string, on bool, summary string, seq int) error {
	config := map[string]any{"streaming_mode": on, "update_multi": true}
	if summary != "" {
		config["summary"] = map[string]string{"content": summary}
	}

	settings, err := json.Marshal(map[string]any{"config": config})
	if err != nil {
		return err
	}

	resp, err := s.api.Cardkit.V1.Card.Settings(ctx, larkcardkit.NewSettingsCardReqBuilder().CardId(cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().Settings(string(settings)).Sequence(seq).Build()).Build())
	if err != nil {
		return fmt.Errorf("changing a card's settings: %w", err)
	}

	return refused("changing a card's settings", resp.Success(), resp.Code, resp.Msg)
}

// cardJSON is a card that is not being written: a title, the text, a button
// a choice and small print. Its text and small print mention nobody.
func cardJSON(card channels.Card) (string, error) {
	elements := []any{map[string]string{"tag": "markdown", "content": inert(card.Text)}}

	for _, b := range card.Buttons {
		elements = append(elements, map[string]any{
			"tag": "button", "text": map[string]string{"tag": "plain_text", "content": b.Label},
			"behaviors": []any{map[string]any{"type": "callback", "value": b.Value}},
		})
	}

	if card.Note != "" {
		elements = append(elements, map[string]any{
			"tag": "markdown", "content": inert(card.Note), "text_size": "notation",
		})
	}

	raw, err := json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{"summary": map[string]string{"content": summaryOf(inert(card.Text))}},
		"header": map[string]any{
			"title": map[string]string{"tag": "plain_text", "content": card.Title}, "template": colorOf(card.Tone),
		},
		"body": map[string]any{"elements": elements},
	})

	return string(raw), err
}

// SendQuestion sends a card to a person by their open_id.
func (s *sdk) SendQuestion(ctx context.Context, openID string, card channels.Card) (channels.QuestionRef, error) {
	cardID, messageID, err := s.sendCard(ctx, openID, card)
	if err != nil {
		return channels.QuestionRef{}, err
	}

	return channels.QuestionRef{ID: cardID, MessageID: messageID}, nil
}

// ReplaceQuestion replaces a card SendQuestion sent, with the next sequence.
func (s *sdk) ReplaceQuestion(ctx context.Context, ref channels.QuestionRef, card channels.Card,
) (channels.QuestionRef, error) {
	ref.Seq++

	if err := s.replaceCard(ctx, ref.ID, card, ref.Seq); err != nil {
		return channels.QuestionRef{}, err
	}

	return ref, nil
}

func (s *sdk) sendCard(ctx context.Context, openID string, card channels.Card) (cardID, messageID string, err error) {
	data, err := cardJSON(card)
	if err != nil {
		return "", "", err
	}

	created, err := s.api.Cardkit.V1.Card.Create(ctx, larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().Type("card_json").Data(data).Build()).Build())
	if err != nil {
		return "", "", fmt.Errorf("creating a card: %w", err)
	}

	if err := refused("creating a card", created.Success(), created.Code, created.Msg); err != nil {
		return "", "", err
	}

	if created.Data == nil || created.Data.CardId == nil {
		return "", "", errors.New("creating a card: Lark gave no card id")
	}

	cardID = *created.Data.CardId

	content, err := json.Marshal(map[string]any{"type": "card", "data": map[string]string{"card_id": cardID}})
	if err != nil {
		return "", "", err
	}

	sent, err := s.api.Im.V1.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(openID).MsgType("interactive").
			Content(string(content)).Build()).Build())
	if err != nil {
		return "", "", fmt.Errorf("sending a card: %w", err)
	}

	if err := refused("sending a card", sent.Success(), sent.Code, sent.Msg); err != nil {
		return "", "", err
	}

	if sent.Data == nil || sent.Data.MessageId == nil {
		return "", "", errors.New("sending a card: Lark gave no message id")
	}

	return cardID, *sent.Data.MessageId, nil
}

func (s *sdk) replaceCard(ctx context.Context, cardID string, card channels.Card, seq int) error {
	data, err := cardJSON(card)
	if err != nil {
		return err
	}

	resp, err := s.api.Cardkit.V1.Card.Update(ctx, larkcardkit.NewUpdateCardReqBuilder().CardId(cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Card(larkcardkit.NewCardBuilder().Type("card_json").Data(data).Build()).Sequence(seq).Build()).Build())
	if err != nil {
		return fmt.Errorf("replacing a card: %w", err)
	}

	return refused("replacing a card", resp.Success(), resp.Code, resp.Msg)
}

// colorOf is a header's color among the ones Lark has: orange for a question
// that waits, green for one that was answered, red for one that was canceled
// or something gone wrong, grey for one that ended otherwise; blue for none.
func colorOf(tone channels.Tone) string {
	switch tone {
	case channels.ToneAsking:
		return "orange"
	case channels.ToneAnswered:
		return "green"
	case channels.ToneAlert:
		return "red"
	case channels.ToneClosed:
		return "grey"
	}

	return "blue"
}

// summaryOf is what a chat list shows for a card: its first line without its
// Markdown, cut short (MemoryOS, LarkReplyCard.summary).
func summaryOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(markdownMarks.Replace(strings.TrimPrefix(line, "> ")))
	if r := []rune(line); len(r) > 60 {
		line = string(r[:60]) + "…"
	}

	if line == "" {
		return "Hypomnema"
	}

	return line
}

// PersonByEmail finds the open_id of a member of the app's organization.
func (s *sdk) PersonByEmail(ctx context.Context, email string) (string, error) {
	resp, err := s.api.Contact.V3.User.BatchGetId(ctx, larkcontact.NewBatchGetIdUserReqBuilder().UserIdType("open_id").
		Body(larkcontact.NewBatchGetIdUserReqBodyBuilder().Emails([]string{email}).Build()).Build())
	if err != nil {
		return "", fmt.Errorf("looking %s up at Lark: %w", email, err)
	}

	if err := refused("looking "+email+" up at Lark", resp.Success(), resp.Code, resp.Msg); err != nil {
		return "", err
	}

	if resp.Data != nil {
		for _, u := range resp.Data.UserList {
			if u != nil && deref(u.UserId) != "" {
				return *u.UserId, nil
			}
		}
	}

	return "", fmt.Errorf("no member of the app's Lark organization has the email %s, or the app may not see them", email)
}

func (s *sdk) NameOf(ctx context.Context, openID string) (string, error) {
	resp, err := s.api.Contact.V3.User.Get(ctx, larkcontact.NewGetUserReqBuilder().UserId(openID).
		UserIdType("open_id").Build())
	if err != nil {
		return "", fmt.Errorf("reading %s at Lark: %w", openID, err)
	}

	if err := refused("reading "+openID+" at Lark", resp.Success(), resp.Code, resp.Msg); err != nil {
		return "", err
	}

	if resp.Data == nil || resp.Data.User == nil {
		return "", nil
	}

	return deref(resp.Data.User.Name), nil
}

// Self reads the bot's own open_id (GET /open-apis/bot/v3/info), which a
// mention of the bot carries. The SDK has no typed call for it, so it is
// asked as a raw request with the app's token, as MemoryOS's checkBot asks.
func (s *sdk) Self(ctx context.Context) (string, error) {
	resp, err := s.api.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", fmt.Errorf("reading the bot at Lark: %w", err)
	}

	var info struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}

	if err := json.Unmarshal(resp.RawBody, &info); err != nil {
		return "", fmt.Errorf("reading the bot at Lark: %w", err)
	}

	if err := refused("reading the bot at Lark", info.Code == 0, info.Code, info.Msg); err != nil {
		return "", err
	}

	if info.Bot.OpenID == "" {
		return "", errors.New("reading the bot at Lark: Lark gave no open_id")
	}

	return info.Bot.OpenID, nil
}

// The groups are listed a hundred a page, at most fifty pages (MemoryOS,
// LarkChannelAdapter.CHAT_PAGE and CHAT_PAGES).
const (
	chatPage  = 100
	chatPages = 50
)

// Groups lists the groups the bot is in (GET /open-apis/im/v1/chats), which
// lists no direct chat. A group no longer in use is left out, as MemoryOS
// leaves it out. A page that cannot be read, or more pages than are read,
// gives no list, so no group is removed for a list that is not whole.
func (s *sdk) Groups(ctx context.Context) ([]channels.Group, error) {
	return groupsOf(func(token string) (*larkim.ListChatResp, error) {
		req := larkim.NewListChatReqBuilder().PageSize(chatPage)
		if token != "" {
			req.PageToken(token)
		}

		return s.api.Im.V1.Chat.List(ctx, req.Build())
	})
}

// groupsOf reads the bot's groups page by page, each page after the token of
// the one before (MemoryOS, LarkChannelAdapter.groups). An answer without its
// data, or one that says more follow without saying where, is not a whole
// list either.
func groupsOf(page func(token string) (*larkim.ListChatResp, error)) ([]channels.Group, error) {
	var (
		out   []channels.Group
		token string
	)

	for range chatPages {
		resp, err := page(token)
		if err != nil {
			return nil, fmt.Errorf("listing the bot's groups: %w", err)
		}

		if err := refused("listing the bot's groups", resp.Success(), resp.Code, resp.Msg); err != nil {
			return nil, err
		}

		if resp.Data == nil {
			return nil, errors.New("listing the bot's groups: Lark gave no list")
		}

		for _, chat := range resp.Data.Items {
			if chat == nil || deref(chat.ChatId) == "" {
				continue
			}

			if status := deref(chat.ChatStatus); status != "" && status != "normal" {
				continue
			}

			out = append(out, channels.Group{ChatID: deref(chat.ChatId), Name: strings.TrimSpace(deref(chat.Name))})
		}

		if resp.Data.HasMore == nil || !*resp.Data.HasMore {
			return out, nil
		}

		token = deref(resp.Data.PageToken)
		if token == "" {
			return nil, errors.New("listing the bot's groups: Lark said more follow and gave no page token")
		}
	}

	return nil, fmt.Errorf("listing the bot's groups: more than %d groups", chatPage*chatPages)
}

var markdownMarks = strings.NewReplacer("*", "", "_", "", "`", "", "#", "", "[", "", "]", "")

// refused turns an answer Lark gave with a code into an error that holds the
// code and Lark's own short message, which names a missing scope.
func refused(doing string, ok bool, code int, msg string) error {
	if ok {
		return nil
	}

	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200])
	}

	return fmt.Errorf("%s: Lark refused it with code %d: %s", doing, code, msg)
}
