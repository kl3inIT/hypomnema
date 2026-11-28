package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/inbox"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// SetInboxChatsBulk sets the owner's choice on the selected chats of a single source.
func (s *Server) SetInboxChatsBulk(ctx context.Context, req openapi.SetInboxChatsBulkRequestObject,
) (openapi.SetInboxChatsBulkResponseObject, error) {
	if s.inboxChats == nil {
		return nil, errNoInboxChats
	}

	if (req.Body.ChatIds == nil) == (req.Body.Filter == nil) {
		return nil, fmt.Errorf("%w: supply either chat_ids or filter", orchestration.ErrInvalid)
	}

	if err := s.chatSource(ctx, req.Integration, req.Label); err != nil {
		return nil, err
	}

	chats, err := s.inboxChats.List(ctx, req.Integration, req.Label)
	if err != nil {
		return nil, err
	}

	selected, err := selectInboxChats(chats, *req.Body)
	if err != nil {
		return nil, err
	}

	targets := make([]inboxstore.ChatReadTarget, 0, len(selected))
	for _, ch := range selected {
		targets = append(targets, inboxstore.ChatReadTarget{Chat: ch.ID, DefaultReads: inboxstore.DefaultReads(ch.Kind)})
	}

	changes, err := s.svc.Inbox().SetChatReads(ctx, req.Integration, req.Label, targets, req.Body.Reads)
	if err != nil {
		return nil, err
	}

	out := openapi.SetInboxChatsBulk200JSONResponse{Changes: make([]openapi.InboxChatBulkChange, 0, len(changes))}
	for _, change := range changes {
		ch := change.Current
		item := openapi.InboxChatBulkChange{Chat: ch.Chat, Reads: ch.Reads, SetAt: ch.At}

		if prev := change.Previous; prev != nil {
			item.Previous = &openapi.InboxChatPreviousChoice{Reads: prev.Reads, SetAt: prev.At}
		}

		out.Changes = append(out.Changes, item)
	}

	return out, nil
}

func selectInboxChats(chats []inbox.Chat, choice openapi.InboxChatBulkChoice) ([]inbox.Chat, error) {
	if f := choice.Filter; f != nil {
		matches := inbox.Filter(chats, val(f.Q), string(val(f.Kind)), string(val(f.State)))
		return matches[:min(len(matches), defaultInboxChats)], nil
	}

	byID := make(map[string]inbox.Chat, len(chats))
	for _, ch := range chats {
		byID[ch.ID] = ch
	}

	selected := make([]inbox.Chat, 0, len(*choice.ChatIds))
	for _, id := range *choice.ChatIds {
		ch, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: chat %s is missing; refresh the account chat list", orchestration.ErrConflict, id)
		}

		selected = append(selected, ch)
	}

	return selected, nil
}

// UndoInboxChatsBulk restores the previous states of a batch on the source that was changed.
func (s *Server) UndoInboxChatsBulk(ctx context.Context, req openapi.UndoInboxChatsBulkRequestObject,
) (openapi.UndoInboxChatsBulkResponseObject, error) {
	if err := s.chatSource(ctx, req.Integration, req.Label); err != nil {
		return nil, err
	}

	changes := make([]inboxstore.ChatReadChange, 0, len(req.Body.Changes))
	for _, item := range req.Body.Changes {
		change := inboxstore.ChatReadChange{
			Current: inboxstore.ChatRead{Chat: item.Chat, Reads: item.Reads, At: item.SetAt},
		}

		if prev := item.Previous; prev != nil {
			change.Previous = &inboxstore.ChatRead{Reads: prev.Reads, At: prev.SetAt}
		}

		changes = append(changes, change)
	}

	if err := s.svc.Inbox().UndoChatReads(ctx, req.Integration, req.Label, changes); err != nil {
		return nil, err
	}

	return openapi.UndoInboxChatsBulk204Response{}, nil
}
