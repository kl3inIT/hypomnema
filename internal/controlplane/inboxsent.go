package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// inboxSent reads a message the owner sent in a chat of his own account, which a Bot gives as evidence that he
// answered an Inbox item (issue #1004): a Zalo or WhatsApp message from what the server keeps of that account (its
// messaging store, by the Connection's label, the chat and the id); a Lark message from Lark's API with the owner's
// own sign-in, since the server keeps none of his Lark messages (checked on zm on 2026-11-20: no table holds them;
// channel_messages keeps only the ids of the events a Lark app's bot took). It reads too who asked and who is in a
// chat now, for his answer in another chat where the asker is (issue #1163): from the service, through the session
// the server holds of a Zalo or WhatsApp account, and from Lark's API.
type inboxSent struct {
	messages func(ctx context.Context, integration, label string, q messaging.Query) ([]messaging.Message, error)
	members  func(ctx context.Context, integration, label, chat string) (messaging.Members, error)
	// larkToken is the access token of the owner's Lark Connection label, his own sign-in.
	larkToken func(ctx context.Context, label string) (string, error)
	// larkURL is Lark's API, https://open.larksuite.com.
	larkURL string
	client  *http.Client
}

// SentMessage reads one message of a chat of the owner's account at service.
func (s inboxSent) SentMessage(ctx context.Context, service, account, chat, id string,
) (inboxstore.SentMessage, error) {
	switch service {
	case inboxstore.FromZalo, inboxstore.FromWhatsApp:
		m, err := s.kept(ctx, service, account, chat, id)
		if err != nil {
			return inboxstore.SentMessage{}, err
		}

		return inboxstore.SentMessage{Chat: m.Chat, At: m.At, FromMe: m.FromMe}, nil
	case inboxstore.FromLark:
		token, err := s.larkToken(ctx, account)
		if err != nil {
			return inboxstore.SentMessage{}, err
		}

		me, err := s.larkMe(ctx, token)
		if err != nil {
			return inboxstore.SentMessage{}, err
		}

		m, err := s.larkMessage(ctx, token, id)
		if err != nil {
			return inboxstore.SentMessage{}, err
		}

		return inboxstore.SentMessage{Chat: m.chat, At: m.at, FromMe: me != "" && m.from == me}, nil
	}

	return inboxstore.SentMessage{}, fmt.Errorf("%w: %s keeps no messages of the owner", storeerr.ErrInvalid, service)
}

// Asker is who asked by a message of a chat of the owner's account (issue #1163): its sender when someone else sent
// it; else, the owner's own message in a direct chat (his promise there), that chat's other party.
func (s inboxSent) Asker(ctx context.Context, service, account, chat, id string) ([]string, error) {
	var (
		sender, me string
		fromMe     bool
	)

	switch service {
	case inboxstore.FromZalo, inboxstore.FromWhatsApp:
		m, err := s.kept(ctx, service, account, chat, id)
		if err != nil {
			return nil, err
		}

		sender, fromMe = m.Sender, m.FromMe
	case inboxstore.FromLark:
		token, err := s.larkToken(ctx, account)
		if err != nil {
			return nil, err
		}

		if me, err = s.larkMe(ctx, token); err != nil {
			return nil, err
		}

		if me == "" {
			return nil, fmt.Errorf("%w: the owner's Lark sign-in does not say who he is", storeerr.ErrNotFound)
		}

		m, err := s.larkMessage(ctx, token, id)
		if err != nil {
			return nil, err
		}

		if m.chat != chat {
			return nil, fmt.Errorf("%w: Lark message %s of chat %s", storeerr.ErrNotFound, id, chat)
		}

		sender, fromMe = m.from, m.from == me
	default:
		return nil, fmt.Errorf("%w: %s keeps no messages of the owner", storeerr.ErrInvalid, service)
	}

	if !fromMe && sender != "" {
		return []string{sender}, nil
	}

	in, err := s.chatMembers(ctx, service, account, chat)
	if err != nil {
		return nil, err
	}

	if in.direct {
		others := slices.DeleteFunc(in.IDs, func(id string) bool { return id == me })
		if len(others) > 0 {
			return others, nil
		}
	}

	return nil, fmt.Errorf("%w: no one but the owner asked in chat %s", storeerr.ErrNotFound, chat)
}

// Members reads who is in a chat of the owner's account now (issue #1163): of a Zalo or WhatsApp account through the
// session the server holds of it, of Lark with his own sign-in.
func (s inboxSent) Members(ctx context.Context, service, account, chat string) (inboxstore.ChatMembers, error) {
	in, err := s.chatMembers(ctx, service, account, chat)

	return in.ChatMembers, err
}

// chatMembers are a chat's members and whether it is a direct chat.
type chatMembers struct {
	inboxstore.ChatMembers

	direct bool
	// people are the members person by person, each by his ids and the name the service lists him by, Lark's
	// (issue #1200).
	people []chatPerson
}

// chatPerson is a member of a chat: every id the service knows him by and the name it lists him by, when it does.
type chatPerson struct {
	ids  []string
	name string
}

func (s inboxSent) chatMembers(ctx context.Context, service, account, chat string) (chatMembers, error) {
	switch service {
	case inboxstore.FromZalo, inboxstore.FromWhatsApp:
		m, err := s.members(ctx, service, account, chat)
		if err != nil {
			return chatMembers{}, err
		}

		in := chatMembers{ChatMembers: inboxstore.ChatMembers{Name: m.Name, IDs: m.IDs}, direct: m.Direct}

		for _, ids := range m.People {
			in.people = append(in.people, chatPerson{ids: ids})
		}

		if m.People == nil {
			for _, id := range m.IDs {
				in.people = append(in.people, chatPerson{ids: []string{id}})
			}
		}

		return in, nil
	case inboxstore.FromLark:
		token, err := s.larkToken(ctx, account)
		if err != nil {
			return chatMembers{}, err
		}

		return s.larkMembers(ctx, token, chat)
	}

	return chatMembers{}, fmt.Errorf("%w: %s lists no members", storeerr.ErrInvalid, service)
}

// kept is one message of a chat that the server keeps of a Zalo or WhatsApp account.
func (s inboxSent) kept(ctx context.Context, service, account, chat, id string) (messaging.Message, error) {
	msgs, err := s.messages(ctx, service, account, messaging.Query{Chat: chat, ID: id, Limit: 1})
	if err != nil {
		return messaging.Message{}, err
	}

	if len(msgs) == 0 || msgs[0].ID != id {
		return messaging.Message{}, fmt.Errorf("%w: message %s of chat %s", storeerr.ErrNotFound, id, chat)
	}

	return msgs[0], nil
}

// larkMe is who the owner is at Lark, his open_id (GET /open-apis/authen/v1/user_info); empty when it does not say,
// and then no message is known to be his.
func (s inboxSent) larkMe(ctx context.Context, token string) (string, error) {
	var me struct {
		Data struct {
			OpenID string `json:"open_id"`
		} `json:"data"`
	}

	if err := s.larkGet(ctx, token, "/open-apis/authen/v1/user_info", &me); err != nil {
		return "", err
	}

	return me.Data.OpenID, nil
}

// larkMessage is a Lark message as the owner's sign-in reads it (GET /open-apis/im/v1/messages/:message_id): its
// chat, when it was sent and its sender's open_id, a person's only.
type larkMessage struct {
	chat, from string
	at         time.Time
}

func (s inboxSent) larkMessage(ctx context.Context, token, id string) (larkMessage, error) {
	var got struct {
		Data struct {
			Items []struct {
				MessageID  string `json:"message_id"`
				ChatID     string `json:"chat_id"`
				CreateTime string `json:"create_time"`
				Sender     struct {
					ID         string `json:"id"`
					SenderType string `json:"sender_type"`
				} `json:"sender"`
			} `json:"items"`
		} `json:"data"`
	}

	if err := s.larkGet(ctx, token, "/open-apis/im/v1/messages/"+url.PathEscape(id), &got); err != nil {
		return larkMessage{}, err
	}

	for _, m := range got.Data.Items {
		if m.MessageID != id {
			continue
		}

		ms, err := strconv.ParseInt(m.CreateTime, 10, 64)
		if err != nil {
			return larkMessage{}, fmt.Errorf("lark: message %s has no time: %w", id, err)
		}

		out := larkMessage{chat: m.ChatID, at: time.UnixMilli(ms).UTC()}
		if m.Sender.SenderType == "user" {
			out.from = m.Sender.ID
		}

		return out, nil
	}

	return larkMessage{}, fmt.Errorf("%w: Lark message %s", storeerr.ErrNotFound, id)
}

// larkMemberPages bounds the pages of a Lark chat's members read, a hundred each: a group larger is not read whole,
// and its members are not known.
const larkMemberPages = 100

// larkMembers are a Lark chat's name, whether it is a direct chat (GET /open-apis/im/v1/chats/:chat_id, chat_mode
// p2p) and its members by their open_id with their names (GET /open-apis/im/v1/chats/:chat_id/members, page after
// page; its items' member_id and name), read with the owner's own sign-in.
func (s inboxSent) larkMembers(ctx context.Context, token, chat string) (chatMembers, error) {
	var info struct {
		Data struct {
			Name     string `json:"name"`
			ChatMode string `json:"chat_mode"`
		} `json:"data"`
	}

	path := "/open-apis/im/v1/chats/" + url.PathEscape(chat)
	if err := s.larkGet(ctx, token, path, &info); err != nil {
		return chatMembers{}, err
	}

	out := chatMembers{ChatMembers: inboxstore.ChatMembers{Name: info.Data.Name, IDs: []string{}},
		direct: info.Data.ChatMode == "p2p"}
	page := ""

	for range larkMemberPages {
		q := url.Values{"member_id_type": {"open_id"}, "page_size": {"100"}}
		if page != "" {
			q.Set("page_token", page)
		}

		var got struct {
			Data struct {
				Items []struct {
					MemberID string `json:"member_id"`
					Name     string `json:"name"`
				} `json:"items"`
				HasMore   bool   `json:"has_more"`
				PageToken string `json:"page_token"`
			} `json:"data"`
		}

		if err := s.larkGet(ctx, token, path+"/members?"+q.Encode(), &got); err != nil {
			return chatMembers{}, err
		}

		for _, m := range got.Data.Items {
			if m.MemberID != "" {
				out.IDs = append(out.IDs, m.MemberID)
				out.people = append(out.people, chatPerson{ids: []string{m.MemberID}, name: m.Name})
			}
		}

		if !got.Data.HasMore || got.Data.PageToken == "" {
			return out, nil
		}

		page = got.Data.PageToken
	}

	return chatMembers{}, fmt.Errorf("lark: chat %s has more than %d members to read", chat, larkMemberPages*100)
}

// larkAnswer is the envelope of every Lark answer: code 0 when it succeeded.
type larkAnswer struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// larkGet reads one of Lark's answers. A message that is not there, or not the owner's to read, is not found.
func (s inboxSent) larkGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.larkURL+path, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("lark: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // Read-only.

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("lark: reading %s: %w", path, err)
	}

	var envelope larkAnswer
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("lark: %s answered %d, not JSON", path, resp.StatusCode)
	}

	switch {
	case envelope.Code == 0 && resp.StatusCode == http.StatusOK:
		return json.Unmarshal(body, out)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: lark: %s: %s", storeerr.ErrNotFound, path, envelope.Msg)
	}

	return fmt.Errorf("lark: %s answered %d, code %d: %s", path, resp.StatusCode, envelope.Code, envelope.Msg)
}

// larkIntegration is the Integration of the owner's Lark sign-in.
const larkIntegration = "lark"
