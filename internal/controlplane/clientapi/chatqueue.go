package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The chat's queue (issue #497): owner messages written while pi works wait off the transcript until the turn
// settles; the owner may remove one, which gives its text back for the composer (Omnigent's Edit), or send it now,
// which steers the turn.

// ListChatQueue returns the chat's queued messages, oldest first.
func (s *Server) ListChatQueue(ctx context.Context, req openapi.ListChatQueueRequestObject,
) (openapi.ListChatQueueResponseObject, error) {
	q, err := s.svc.ChatQueue(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}

	return openapi.ListChatQueue200JSONResponse(toQueueList(q)), nil
}

// RemoveQueuedChatMessage takes a message out of the chat's queue.
func (s *Server) RemoveQueuedChatMessage(ctx context.Context, req openapi.RemoveQueuedChatMessageRequestObject,
) (openapi.RemoveQueuedChatMessageResponseObject, error) {
	q, err := s.svc.RemoveQueued(ctx, req.ChatId, req.QueueId)
	if err != nil {
		return nil, err
	}

	return openapi.RemoveQueuedChatMessage200JSONResponse(toQueued(q)), nil
}

// SendQueuedChatMessage moves a queued message into the chat, where the supervisor steers pi with it.
func (s *Server) SendQueuedChatMessage(ctx context.Context, req openapi.SendQueuedChatMessageRequestObject,
) (openapi.SendQueuedChatMessageResponseObject, error) {
	m, err := s.svc.SendQueuedNow(ctx, req.ChatId, req.QueueId)
	if err != nil {
		return nil, err
	}

	return openapi.SendQueuedChatMessage201JSONResponse(toChatMessage(m)), nil
}

func toQueueList(q []orchestration.QueuedMessage) openapi.QueuedChatMessageList {
	items := make([]openapi.QueuedChatMessage, 0, len(q))
	for _, m := range q {
		items = append(items, toQueued(m))
	}

	return openapi.QueuedChatMessageList{Items: items}
}

func toQueued(m orchestration.QueuedMessage) openapi.QueuedChatMessage {
	return openapi.QueuedChatMessage{
		Id: m.ID, ChatId: m.ChatID, Text: m.Text, Attachments: toAttachments(m.Attachments), CreatedAt: m.CreatedAt,
	}
}

func toAttachments(files []orchestration.ChatAttachment) *[]openapi.ChatAttachment {
	if len(files) == 0 {
		return nil
	}

	out := make([]openapi.ChatAttachment, 0, len(files))
	for _, a := range files {
		out = append(out, openapi.ChatAttachment{Name: a.Name, Path: a.Path, Size: a.Size, Type: opt(a.Type)})
	}

	return &out
}
