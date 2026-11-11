package larkbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Lark through its own Go SDK (github.com/larksuite/oapi-sdk-go, MIT), which
// lark-cli, Lark's own Go program, uses for the same things: the persistent
// connection (ws), the event dispatcher, and the typed calls. Tried against
// the owner's app on 2026-11-11 (issue #136): a direct message arrives with
// its ids and the sender's tenant, and a CardKit card is created, sent by its
// id, filled by element with a rising sequence and closed.

// connectWait is how long the persistent connection gets to open.
const connectWait = 20 * time.Second

// answerElement is the id of the card's one element, which the text fills.
const answerElement = "answer"

// sdk is Lark through the SDK.
type sdk struct {
	appID  string
	secret string
	api    *lark.Client
}

// Connect returns Lark for an app. Only Lark international is served
// (open.larksuite.com), as the Integration is.
func Connect(appID, secret string) Lark {
	return &sdk{
		appID: appID, secret: secret,
		// The SDK writes the address of a connection, which holds its ticket, at its info level.
		api: lark.NewClient(appID, secret, lark.WithOpenBaseUrl(lark.LarkBaseUrl),
			lark.WithLogLevel(larkcore.LogLevelError), lark.WithReqTimeout(sendTimeout)),
	}
}

func (s *sdk) Listen(ctx context.Context, take func(Message), press func(Press) string) error {
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, e *larkim.P2MessageReceiveV1) error {
			take(messageOf(e))

			return nil
		}).
		// A press of a card's button (the callback card.action.trigger): what press answers is shown to who
		// pressed, as a toast.
		OnP2CardActionTrigger(func(_ context.Context, e *callback.CardActionTriggerEvent) (
			*callback.CardActionTriggerResponse, error,
		) {
			said := press(pressOf(e))
			if said == "" {
				return &callback.CardActionTriggerResponse{}, nil
			}

			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: said}}, nil
		}).
		// Lark tells an app when its messages were read; nothing is done with it, and the SDK logs an event
		// nobody handles as an error.
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil })

	client := larkws.NewClient(s.appID, s.secret, larkws.WithEventHandler(handler),
		larkws.WithDomain(lark.LarkBaseUrl), larkws.WithLogLevel(larkcore.LogLevelError))

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
func messageOf(e *larkim.P2MessageReceiveV1) Message {
	var m Message

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

// pressOf reads who pressed a card's button and what the button carries.
func pressOf(e *callback.CardActionTriggerEvent) Press {
	p := Press{Value: map[string]string{}}

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

func (s *sdk) SendText(ctx context.Context, chatID, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	return s.send(ctx, chatID, "text", string(content))
}

func (s *sdk) send(ctx context.Context, chatID, kind, content string) error {
	resp, err := s.api.Im.V1.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType(kind).Content(content).Build()).Build())
	if err != nil {
		return fmt.Errorf("sending to Lark: %w", err)
	}

	return refused("sending to Lark", resp.Success(), resp.Code, resp.Msg)
}

func (s *sdk) OpenCard(ctx context.Context, chatID, text string) (string, error) {
	card, err := json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{"streaming_mode": true, "summary": map[string]string{"content": summaryOf(text)}},
		"body": map[string]any{"elements": []any{
			map[string]string{"tag": "markdown", "element_id": answerElement, "content": text},
		}},
	})
	if err != nil {
		return "", err
	}

	created, err := s.api.Cardkit.V1.Card.Create(ctx, larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().Type("card_json").Data(string(card)).Build()).Build())
	if err != nil {
		return "", fmt.Errorf("creating a card: %w", err)
	}

	if err := refused("creating a card", created.Success(), created.Code, created.Msg); err != nil {
		return "", err
	}

	if created.Data == nil || created.Data.CardId == nil {
		return "", errors.New("creating a card: Lark gave no card id")
	}

	id := *created.Data.CardId

	content, err := json.Marshal(map[string]any{"type": "card", "data": map[string]string{"card_id": id}})
	if err != nil {
		return "", err
	}

	return id, s.send(ctx, chatID, "interactive", string(content))
}

func (s *sdk) UpdateCard(ctx context.Context, cardID, text string, seq int) error {
	resp, err := s.api.Cardkit.V1.CardElement.Content(ctx, larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).ElementId(answerElement).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().Content(text).Sequence(seq).Build()).Build())
	if err != nil {
		return fmt.Errorf("updating a card: %w", err)
	}

	return refused("updating a card", resp.Success(), resp.Code, resp.Msg)
}

func (s *sdk) CloseCard(ctx context.Context, cardID, text string, seq int) error {
	if err := s.UpdateCard(ctx, cardID, text, seq); err != nil {
		return err
	}

	settings, err := json.Marshal(map[string]any{
		"config": map[string]any{"streaming_mode": false, "summary": map[string]string{"content": summaryOf(text)}},
	})
	if err != nil {
		return err
	}

	resp, err := s.api.Cardkit.V1.Card.Settings(ctx, larkcardkit.NewSettingsCardReqBuilder().CardId(cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().Settings(string(settings)).Sequence(seq+1).Build()).Build())
	if err != nil {
		return fmt.Errorf("closing a card: %w", err)
	}

	return refused("closing a card", resp.Success(), resp.Code, resp.Msg)
}

// cardJSON is a card that is not being written: a title, the text, a button
// a choice and small print.
func cardJSON(card Card) (string, error) {
	elements := []any{map[string]string{"tag": "markdown", "content": card.Text}}

	for _, b := range card.Buttons {
		elements = append(elements, map[string]any{
			"tag": "button", "text": map[string]string{"tag": "plain_text", "content": b.Label},
			"behaviors": []any{map[string]any{"type": "callback", "value": b.Value}},
		})
	}

	if card.Note != "" {
		elements = append(elements, map[string]any{
			"tag": "markdown", "content": card.Note, "text_size": "notation",
		})
	}

	raw, err := json.Marshal(map[string]any{
		"schema": "2.0",
		"config": map[string]any{"summary": map[string]string{"content": summaryOf(card.Text)}},
		"header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": card.Title}},
		"body":   map[string]any{"elements": elements},
	})

	return string(raw), err
}

func (s *sdk) SendCard(ctx context.Context, openID string, card Card) (cardID, messageID string, err error) {
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

func (s *sdk) ReplaceCard(ctx context.Context, cardID string, card Card, seq int) error {
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

// summaryOf is what a chat list shows for a card: its first line, cut short.
func summaryOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 60 {
		line = string(r[:60]) + "…"
	}

	if line == "" {
		return "Hypomnema"
	}

	return line
}

func (s *sdk) OpenIDOf(ctx context.Context, email string) (string, error) {
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
