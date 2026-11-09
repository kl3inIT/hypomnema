package clientapi

import (
	"context"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Chats with the coordinator on the server (issue #41, PR 3). Only the
// owner's tokens use them (policy.go). Messages are read with a cursor and
// an optional wait, as events are.

// ListChats returns the chats, the most recently active first.
func (s *Server) ListChats(ctx context.Context, req openapi.ListChatsRequestObject,
) (openapi.ListChatsResponseObject, error) {
	chats, err := s.svc.ListChats(ctx, val(req.Params.Limit))
	if err != nil {
		return nil, err
	}

	out := openapi.ListChats200JSONResponse{Data: make([]openapi.Chat, 0, len(chats))}
	for _, c := range chats {
		out.Data = append(out.Data, s.toChat(c))
	}

	return out, nil
}

// CreateChat starts a chat.
func (s *Server) CreateChat(ctx context.Context, req openapi.CreateChatRequestObject,
) (openapi.CreateChatResponseObject, error) {
	chat, err := s.svc.CreateChat(ctx, val(req.Body.Title))
	if err != nil {
		return nil, err
	}

	return openapi.CreateChat201JSONResponse(s.toChat(chat)), nil
}

// GetChat returns a chat and whether pi works on it.
func (s *Server) GetChat(ctx context.Context, req openapi.GetChatRequestObject,
) (openapi.GetChatResponseObject, error) {
	chat, err := s.svc.GetChat(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}

	return openapi.GetChat200JSONResponse(s.toChat(chat)), nil
}

// SendChatMessage keeps the owner's message; the supervisor gives it to pi.
func (s *Server) SendChatMessage(ctx context.Context, req openapi.SendChatMessageRequestObject,
) (openapi.SendChatMessageResponseObject, error) {
	m, err := s.svc.AddChatMessage(ctx, req.ChatId, orchestration.ChatOwner, req.Body.Text)
	if err != nil {
		return nil, err
	}

	return openapi.SendChatMessage201JSONResponse(toChatMessage(m)), nil
}

// ListChatMessages returns the messages after the cursor. With wait, an
// empty page waits up to that many seconds for a message.
func (s *Server) ListChatMessages(ctx context.Context, req openapi.ListChatMessagesRequestObject,
) (openapi.ListChatMessagesResponseObject, error) {
	p := req.Params

	var (
		wake     <-chan struct{}
		deadline <-chan time.Time
	)

	if wait := val(p.Wait); wait > 0 {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Subscribe before the first read, so a message committed in between still wakes the wait.
		wake = s.svc.Subscribe(ctx)
		timer := time.NewTimer(time.Duration(wait) * time.Second)

		defer timer.Stop()

		deadline = timer.C
	}

	for {
		msgs, err := s.svc.ListChatMessages(ctx, req.ChatId, val(p.After), val(p.Limit))
		if err != nil {
			return nil, err
		}

		if len(msgs) > 0 || wake == nil {
			return chatMessageList(msgs, val(p.After)), nil
		}

		select {
		case <-wake:
		case <-deadline:
			return chatMessageList(msgs, val(p.After)), nil
		case <-s.stop:
			return chatMessageList(msgs, val(p.After)), nil
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for messages: %w", ctx.Err())
		}
	}
}

func chatMessageList(msgs []orchestration.ChatMessage, after int64) openapi.ListChatMessages200JSONResponse {
	out := openapi.ListChatMessages200JSONResponse{Data: make([]openapi.ChatMessage, 0, len(msgs)), NextAfter: after}

	for _, m := range msgs {
		out.Data = append(out.Data, toChatMessage(m))
		out.NextAfter = m.Seq
	}

	return out
}

func (s *Server) toChat(c orchestration.Chat) openapi.Chat {
	out := openapi.Chat{Id: c.ID, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}

	if s.coordinator != nil {
		out.Coordinator = s.coordinator.Enabled()
		out.Working = s.coordinator.Working(c.ID)
	}

	return out
}

func toChatMessage(m orchestration.ChatMessage) openapi.ChatMessage {
	out := openapi.ChatMessage{
		Seq: m.Seq, ChatId: m.ChatID, Role: openapi.ChatRole(m.Role), Text: m.Text, DeliveredAt: m.DeliveredAt,
		CreatedAt: m.CreatedAt,
	}

	if m.EventSeq != 0 {
		out.EventSeq = &m.EventSeq
	}

	return out
}
