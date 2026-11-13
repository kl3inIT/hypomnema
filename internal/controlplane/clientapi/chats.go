package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
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
	chat, err := s.svc.CreateChat(ctx, val(req.Body.Bot), val(req.Body.Title))
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

// SendChatMessage keeps the owner's message with the files attached to it (issue #343), which the web uploaded into
// the Bot's folder first; the supervisor gives it to pi.
func (s *Server) SendChatMessage(ctx context.Context, req openapi.SendChatMessageRequestObject,
) (openapi.SendChatMessageResponseObject, error) {
	msg := orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: req.Body.Text}

	for _, a := range val(req.Body.Attachments) {
		msg.Attachments = append(msg.Attachments, orchestration.ChatAttachment{
			Name: a.Name, Path: a.Path, Size: a.Size, Type: val(a.Type),
		})
	}

	m, err := s.svc.AddChatMessages(ctx, req.ChatId, msg)
	if err != nil {
		return nil, err
	}

	return openapi.SendChatMessage201JSONResponse(toChatMessage(m[0])), nil
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
	out := openapi.Chat{Id: c.ID, Title: c.Title, Bot: c.Bot, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}

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

	if m.Kind != "" {
		kind := openapi.ChatKind(m.Kind)
		out.Kind = &kind
	}

	if w := m.Wake; w != nil {
		out.Wake = &openapi.ChatWake{
			Event: openapi.ChatWakeEvent(w.Event), RunId: opt(w.RunID), TaskId: opt(w.TaskID),
			QuestionId: opt(w.QuestionID), Status: opt(w.Status), Reason: opt(w.Reason), Escalation: opt(w.Escalation),
		}
	}

	if len(m.Attachments) > 0 {
		files := make([]openapi.ChatAttachment, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			files = append(files, openapi.ChatAttachment{Name: a.Name, Path: a.Path, Size: a.Size, Type: opt(a.Type)})
		}

		out.Attachments = &files
	}

	if m.Parts != nil {
		var parts []openapi.ChatPart
		if json.Unmarshal(m.Parts, &parts) == nil {
			out.Parts = &parts
		}
	}

	return out
}

// AbortChat asks pi to stop its current turn.
func (s *Server) AbortChat(ctx context.Context, req openapi.AbortChatRequestObject,
) (openapi.AbortChatResponseObject, error) {
	if _, err := s.svc.GetChat(ctx, req.ChatId); err != nil {
		return nil, err
	}

	if s.coordinator != nil {
		if err := s.coordinator.Abort(ctx, req.ChatId); err != nil {
			return nil, fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
		}
	}

	return openapi.AbortChat204Response{}, nil
}

// GetChatGatewayKey returns the key the chat's pi calls the model gateway
// with (issue #204). The supervisor holds it for as long as that pi runs;
// Omnigent's pi asks the process that started it in the same way.
//
// Only the token of that chat's pi gets it (issue #260). A chat's id is no
// secret: a Run records the chat it was made in, and every token reads Runs,
// so a coordinator or another chat's pi could otherwise take this pi's key.
// The owner is refused too: the owner makes keys of their own.
func (s *Server) GetChatGatewayKey(ctx context.Context, req openapi.GetChatGatewayKeyRequestObject,
) (openapi.GetChatGatewayKeyResponseObject, error) {
	tok := principal(ctx)
	if tok.Kind != bearertoken.Bot || tok.Chat != req.ChatId {
		return nil, fmt.Errorf("%w: only the token of the chat's own pi reads its gateway key", errForbidden)
	}

	if s.coordinator == nil {
		return nil, fmt.Errorf("%w: chat %s has no running pi", orchestration.ErrNotFound, req.ChatId)
	}

	key, ok := s.coordinator.Key(req.ChatId)
	if !ok {
		return nil, fmt.Errorf("%w: chat %s has no running pi", orchestration.ErrNotFound, req.ChatId)
	}

	noStore := "no-store"

	return openapi.GetChatGatewayKey200JSONResponse{
		Body:    openapi.ChatGatewayKey{Key: key},
		Headers: openapi.GetChatGatewayKey200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// StreamChat sends the kept messages after the cursor, then each new one as
// it is kept, with pi's live frames in between (after Omnara's delta
// previews: what is live is shown until the kept message replaces it).
func (s *Server) StreamChat(ctx context.Context, req openapi.StreamChatRequestObject,
) (openapi.StreamChatResponseObject, error) {
	if _, err := s.svc.GetChat(ctx, req.ChatId); err != nil {
		return nil, err
	}

	after := val(req.Params.After)
	if req.Params.LastEventID != nil {
		after = *req.Params.LastEventID
	}

	return chatStream(func(w http.ResponseWriter) error {
		return s.streamChat(ctx, w, req.ChatId, after)
	}), nil
}

type chatStream func(w http.ResponseWriter) error

func (f chatStream) VisitStreamChatResponse(w http.ResponseWriter) error { return f(w) }

func (s *Server) streamChat(ctx context.Context, w http.ResponseWriter, chatID string, after int64) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("%w: streaming is not supported here", orchestration.ErrUnavailable)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Subscribe and watch before the first read, so nothing kept or live in between is missed.
	wake := s.svc.Subscribe(ctx)

	var live <-chan coordinator.Frame

	if s.coordinator != nil {
		frames, stop := s.coordinator.Watch(chatID)
		defer stop()

		live = frames
	}

	heartbeat := time.NewTicker(s.heartbeat)
	defer heartbeat.Stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	working := s.coordinator != nil && s.coordinator.Working(chatID)
	if !writeSSE(w, "", openapi.ChatState{Working: working}, "state") {
		return nil
	}

	flusher.Flush()

	for {
		msgs, err := s.svc.ListChatMessages(ctx, chatID, after, orchestration.ChatPageMax)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "chat stream failed",
					slog.String("event", "clientapi.chat_stream_failed"), slog.String("error", err.Error()))
			}

			return nil
		}

		for _, m := range msgs {
			if !writeSSE(w, strconv.FormatInt(m.Seq, 10), toChatMessage(m), "message") {
				return nil
			}

			after = m.Seq
		}

		flusher.Flush()

		if len(msgs) == orchestration.ChatPageMax {
			continue
		}

	wait:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-s.stop:
				return nil
			case <-wake:
				break wait
			case f := <-live:
				if !writeSSE(w, "", f.Data, f.Event) {
					return nil
				}

				flusher.Flush()
			case <-heartbeat.C:
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return nil
				}

				flusher.Flush()
			}
		}
	}
}

// writeSSE writes one frame; id is empty for frames that are not kept. It
// reports whether the client is still there.
func writeSSE(w io.Writer, id string, data any, event string) bool {
	body, err := json.Marshal(data)
	if err != nil {
		return false
	}

	if data == nil {
		body = []byte("{}")
	}

	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return false
		}
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)

	return err == nil
}
