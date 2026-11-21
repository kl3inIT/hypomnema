package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// channel_messages keeps only the ids of the events a Lark app's bot took).
type inboxSent struct {
	messages func(ctx context.Context, integration, label string, q messaging.Query) ([]messaging.Message, error)
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
		msgs, err := s.messages(ctx, service, account, messaging.Query{Chat: chat, ID: id, Limit: 1})
		if err != nil {
			return inboxstore.SentMessage{}, err
		}

		if len(msgs) == 0 || msgs[0].ID != id {
			return inboxstore.SentMessage{}, fmt.Errorf("%w: message %s of chat %s", storeerr.ErrNotFound, id, chat)
		}

		return inboxstore.SentMessage{Chat: msgs[0].Chat, At: msgs[0].At, FromMe: msgs[0].FromMe}, nil
	case inboxstore.FromLark:
		return s.lark(ctx, account, id)
	}

	return inboxstore.SentMessage{}, fmt.Errorf("%w: %s keeps no messages of the owner", storeerr.ErrInvalid, service)
}

// lark reads a message with the owner's own Lark sign-in (GET /open-apis/im/v1/messages/:message_id) and who he is
// (GET /open-apis/authen/v1/user_info): his message is one whose sender is that user.
func (s inboxSent) lark(ctx context.Context, account, id string) (inboxstore.SentMessage, error) {
	token, err := s.larkToken(ctx, account)
	if err != nil {
		return inboxstore.SentMessage{}, err
	}

	var me struct {
		Data struct {
			OpenID string `json:"open_id"`
		} `json:"data"`
	}

	if err := s.larkGet(ctx, token, "/open-apis/authen/v1/user_info", &me); err != nil {
		return inboxstore.SentMessage{}, err
	}

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
		return inboxstore.SentMessage{}, err
	}

	for _, m := range got.Data.Items {
		if m.MessageID != id {
			continue
		}

		ms, err := strconv.ParseInt(m.CreateTime, 10, 64)
		if err != nil {
			return inboxstore.SentMessage{}, fmt.Errorf("lark: message %s has no time: %w", id, err)
		}

		return inboxstore.SentMessage{
			Chat: m.ChatID, At: time.UnixMilli(ms).UTC(),
			FromMe: me.Data.OpenID != "" && m.Sender.SenderType == "user" && m.Sender.ID == me.Data.OpenID,
		}, nil
	}

	return inboxstore.SentMessage{}, fmt.Errorf("%w: Lark message %s", storeerr.ErrNotFound, id)
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
