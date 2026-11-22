package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// Chats with the coordinator on the server (issue #41, PR 3). Only the
// owner's tokens use them (policy.go). Messages are read with a cursor and
// an optional wait, as events are.

// ListChats returns the chats, of one Bot when the request names one, the most recently active first.
func (s *Server) ListChats(ctx context.Context, req openapi.ListChatsRequestObject,
) (openapi.ListChatsResponseObject, error) {
	chats, err := s.svc.ListChats(ctx, val(req.Params.Bot), val(req.Params.Limit), val(req.Params.Offset))
	if err != nil {
		return nil, err
	}

	out := openapi.ListChats200JSONResponse{Data: make([]openapi.Chat, 0, len(chats))}
	for _, c := range chats {
		out.Data = append(out.Data, s.toChat(c))
	}

	return out, nil
}

// CreateChat starts a chat, which becomes its Bot's main chat (issue #471): the web's New chat and the CLI's --new.
func (s *Server) CreateChat(ctx context.Context, req openapi.CreateChatRequestObject,
) (openapi.CreateChatResponseObject, error) {
	b := req.Body
	chat, err := s.svc.StartMainChat(ctx, val(b.Id), val(b.Bot), val(b.Title), orchestration.ChatModel{
		Provider: val(b.Provider), Model: val(b.Model), Effort: val(b.Effort),
	})
	if err != nil {
		return nil, err
	}

	return openapi.CreateChat201JSONResponse(s.toChat(chat)), nil
}

// OpenMainChat returns a Bot's main chat, made when the Bot has none (issue #471): what `hypomnema chat` writes to.
func (s *Server) OpenMainChat(ctx context.Context, req openapi.OpenMainChatRequestObject,
) (openapi.OpenMainChatResponseObject, error) {
	b := req.Body
	chat, made, err := s.svc.OpenMainChat(ctx, val(b.Bot), val(b.Id), val(b.Title), orchestration.ChatModel{
		Provider: val(b.Provider), Model: val(b.Model), Effort: val(b.Effort),
	})
	if err != nil {
		return nil, err
	}

	if made {
		return openapi.OpenMainChat201JSONResponse(s.toChat(chat)), nil
	}

	return openapi.OpenMainChat200JSONResponse(s.toChat(chat)), nil
}

// UpdateChat changes the model and effort the chat runs with (issue #369); the supervisor gives pi the change before
// the chat's next message.
func (s *Server) UpdateChat(ctx context.Context, req openapi.UpdateChatRequestObject,
) (openapi.UpdateChatResponseObject, error) {
	chat, err := s.svc.SetChatModel(ctx, req.ChatId, orchestration.ChatModelChange{
		Provider: req.Body.Provider, Model: req.Body.Model, Effort: req.Body.Effort,
	})
	if err != nil {
		return nil, err
	}

	return openapi.UpdateChat200JSONResponse(s.toChat(chat)), nil
}

// ChatFiles removes what pi kept for a deleted chat on the coordinator's machine: its run folder and its sessions.
type ChatFiles interface {
	Remove(ctx context.Context, chatID string) error
}

// WithChatFiles sets how a deleted chat's files of pi are removed. Nil leaves them where they are, and a deletion
// says so.
func (s *Server) WithChatFiles(f ChatFiles) *Server {
	s.chatFiles = f

	return s
}

// DeleteChat deletes a chat for good (issue #484). A chat pi works in is refused, as OpenMausBot refuses to delete
// a working room (409, "stop that turn first"). After the commit the chat's pi is stopped and its files go, as
// t3code cleans up a deleted thread's attachments after its events are stored: its attachments in its Bot's folder
// through the folders' helper, then pi's run folder and sessions. A file that stays is logged and named in the
// answer; the chat stays deleted.
func (s *Server) DeleteChat(ctx context.Context, req openapi.DeleteChatRequestObject,
) (openapi.DeleteChatResponseObject, error) {
	if _, err := s.svc.GetChat(ctx, req.ChatId); err != nil {
		return nil, err
	}

	if s.coordinator != nil && s.coordinator.Working(req.ChatId) {
		return nil, fmt.Errorf("%w: the coordinator is working in chat %s; stop it first", orchestration.ErrConflict,
			req.ChatId)
	}

	chat, err := s.svc.DeleteChat(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}

	// The chat is gone whatever follows, even if the owner's request ends meanwhile.
	ctx = context.WithoutCancel(ctx)

	if s.coordinator != nil {
		s.coordinator.Stop(chat.ID)
	}

	left := []string{}

	for _, err := range s.removeChatFiles(ctx, chat) {
		s.logger.ErrorContext(ctx, "removing a deleted chat's files failed",
			slog.String("event", "clientapi.chat_files_left"), slog.String("chat_id", chat.ID),
			slog.String("error", err.Error()))

		left = append(left, err.Error())
	}

	return openapi.DeleteChat200JSONResponse{FilesLeft: left}, nil
}

// removeChatFiles removes a deleted chat's files and returns what could not be. Without the folders' helper no
// file was attached, as uploads go through it; without the coordinator no pi kept anything.
func (s *Server) removeChatFiles(ctx context.Context, chat orchestration.Chat) []error {
	var failed []error

	if s.botFiles != nil {
		attachments := orchestration.AttachmentsDir(chat.ID)
		res, err := s.botFiles.Ask(ctx, chat.Bot, workspacefs.Request{Op: workspacefs.OpDelete, Path: attachments})

		switch {
		case err != nil:
			failed = append(failed, fmt.Errorf("%s of bot %s: %w", attachments, chat.Bot, err))
		case res.Error != nil && res.Error.Code != workspacefs.CodeNotFound:
			failed = append(failed, fmt.Errorf("%s of bot %s: %s", attachments, chat.Bot, res.Error.Message))
		}
	}

	switch {
	case s.coordinator == nil || !s.coordinator.Enabled():
	case s.chatFiles == nil:
		failed = append(failed, fmt.Errorf("pi's run/%s and sessions/%s: HYPOMNEMA_CHAT_RM_COMMAND is not set",
			chat.ID, chat.ID))
	default:
		if err := s.chatFiles.Remove(ctx, chat.ID); err != nil {
			failed = append(failed, fmt.Errorf("pi's run/%s and sessions/%s: %w", chat.ID, chat.ID, err))
		}
	}

	return failed
}

// GetChat returns a chat and whether pi works on it.
func (s *Server) GetChat(ctx context.Context, req openapi.GetChatRequestObject,
) (openapi.GetChatResponseObject, error) {
	chat, err := s.svc.GetChat(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}

	read, err := s.svc.ReadRun(ctx, chat.ID)
	if err != nil {
		return nil, err
	}
	shown := s.toChat(chat)
	if read {
		shown.ReadRun = &read
	}

	return openapi.GetChat200JSONResponse(shown), nil
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

	// Written while pi works, it waits in the chat's queue until the turn settles (issue #497).
	busy := s.coordinator != nil && s.coordinator.Working(req.ChatId)

	m, queued, err := s.svc.SendOwnerMessage(ctx, req.ChatId, msg, busy)
	if err != nil {
		return nil, err
	}

	if queued != nil {
		return openapi.SendChatMessage202JSONResponse(toQueued(*queued)), nil
	}

	return openapi.SendChatMessage201JSONResponse(toChatMessage(*m)), nil
}

// ListChatMessages returns the messages after the cursor. With wait, an
// empty page waits up to that many seconds for a message. With before, it
// returns the newest page older than that seq (issue #502).
func (s *Server) ListChatMessages(ctx context.Context, req openapi.ListChatMessagesRequestObject,
) (openapi.ListChatMessagesResponseObject, error) {
	p := req.Params
	if p.Before != nil {
		return s.chatHistory(ctx, req.ChatId, *p.Before, val(p.Limit))
	}

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

		// Read at each page: a change of the Bot's setting applies at once (issue #441).
		display, err := s.svc.ChatToolDisplay(ctx, req.ChatId)
		if err != nil {
			return nil, err
		}

		if len(msgs) > 0 || wake == nil {
			return chatMessageList(msgs, val(p.After), display), nil
		}

		select {
		case <-wake:
		case <-deadline:
			return chatMessageList(msgs, val(p.After), display), nil
		case <-s.stop:
			return chatMessageList(msgs, val(p.After), display), nil
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for messages: %w", ctx.Err())
		}
	}
}

// chatHistory is the page of a chat's history older than before, as the Bot's tool display shows it: the cursors
// count the messages it hides, so a page of hidden ones still moves the reader on.
func (s *Server) chatHistory(ctx context.Context, chatID string, before int64, limit int,
) (openapi.ListChatMessagesResponseObject, error) {
	msgs, more, err := s.svc.ListChatMessagesBefore(ctx, chatID, before, limit)
	if err != nil {
		return nil, err
	}

	display, err := s.svc.ChatToolDisplay(ctx, chatID)
	if err != nil {
		return nil, err
	}

	out := chatMessageList(msgs, 0, display)
	out.HasMore = more

	if len(msgs) > 0 {
		out.NextBefore = &msgs[0].Seq
	}

	return out, nil
}

// chatMessageList is a page as the Bot's tool display shows it: a message it hides still moves the cursor on.
func chatMessageList(msgs []orchestration.ChatMessage, after int64, display orchestration.ToolDisplay,
) openapi.ListChatMessages200JSONResponse {
	out := openapi.ListChatMessages200JSONResponse{Data: make([]openapi.ChatMessage, 0, len(msgs)), NextAfter: after}

	for _, m := range msgs {
		if shown, ok := coordinator.ShownMessage(display, m); ok {
			out.Data = append(out.Data, toChatMessage(shown))
		}

		out.NextAfter = m.Seq
	}

	return out
}

func (s *Server) toChat(c orchestration.Chat) openapi.Chat {
	out := openapi.Chat{
		Id: c.ID, Title: c.Title, Bot: c.Bot, Provider: opt(c.Choice.Provider), Model: opt(c.Choice.Model),
		Effort: opt(c.Choice.Effort), LastLine: opt(c.LastLine), Routine: c.Routine, Channel: c.Channel,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}

	if s.coordinator != nil {
		out.Coordinator = s.coordinator.Enabled()
		out.Working = s.coordinator.Working(c.ID)
	}

	if u := c.Usage; u != nil {
		out.ContextUsage = &openapi.ChatContextUsage{
			ContextWindow: u.Window, ReserveTokens: orchestration.CompactReserve(u.Window), Tokens: u.Tokens,
			Percent: u.Percent,
		}
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

	out.Artifact = toArtifactCard(m.Artifact)
	out.ApprovalId = opt(m.ApprovalID)

	if a := m.Approval; a != nil {
		out.Approval = &openapi.ChatApproval{
			Title: a.Title, ActionName: a.ActionName, Status: openapi.ApprovalStatus(a.Status), DecidedAt: a.DecidedAt,
		}
		if a.Scope != "" {
			scope := openapi.ChatApprovalScope(a.Scope)
			out.Approval.Scope = &scope
		}
	}

	out.Attachments = toAttachments(m.Attachments)

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

// CompactChat asks the chat's pi to compact its context now (issue #384); the compaction runs on as a turn of its
// own, as OpenMausBot's POST /api/bots/:id/compact answers 202 and runs it as a turn.
func (s *Server) CompactChat(ctx context.Context, req openapi.CompactChatRequestObject,
) (openapi.CompactChatResponseObject, error) {
	if _, err := s.svc.GetChat(ctx, req.ChatId); err != nil {
		return nil, err
	}

	if s.coordinator == nil || !s.coordinator.Enabled() {
		return nil, fmt.Errorf("%w: %w", orchestration.ErrUnavailable, coordinator.ErrDisabled)
	}

	if err := s.coordinator.Compact(ctx, req.ChatId); err != nil {
		if errors.Is(err, orchestration.ErrConflict) {
			return nil, err
		}

		return nil, fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
	}

	return openapi.CompactChat202Response{}, nil
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

	// The revision of each artifact whose message this stream sent: an artifact replaced since is sent again, as
	// its message is updated in place (issue #402). A stream that resumes sends the earlier ones once, as it cannot
	// know what the client saw.
	shown := map[string]int{}

	working := s.coordinator != nil && s.coordinator.Working(chatID)
	if !writeSSE(w, "", openapi.ChatState{Working: working}, "state") {
		return nil
	}

	flusher.Flush()

	var display orchestration.ToolDisplay

	// The queue last sent, as its ids; nil before the first, so the stream opens with it (issue #497).
	var queued []int64

	for {
		msgs, err := s.svc.ListChatMessages(ctx, chatID, after, orchestration.ChatPageMax)
		if err == nil {
			// Read at each wake-up, so a change of the Bot's setting applies to what is sent next (issue #441).
			display, err = s.svc.ChatToolDisplay(ctx, chatID)
		}

		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "chat stream failed",
					slog.String("event", "clientapi.chat_stream_failed"), slog.String("error", err.Error()))
			}

			return nil
		}

		for _, m := range msgs {
			visible, ok := coordinator.ShownMessage(display, m)
			if !ok {
				after = m.Seq

				continue
			}

			if !writeSSE(w, strconv.FormatInt(m.Seq, 10), toChatMessage(visible), "message") {
				return nil
			}

			if m.Artifact != nil {
				shown[m.Artifact.ID] = m.Artifact.Revision
			}

			after = m.Seq
		}

		if !s.sendRevised(ctx, w, chatID, after, shown) {
			return nil
		}

		if !s.sendQueue(ctx, w, chatID, &queued) {
			return nil
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
				f, ok := coordinator.ShownFrame(display, f)
				if !ok {
					continue
				}

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

// sendRevised sends again the message of each artifact of the chat whose revision the stream has not sent, as a
// frame without an id: its seq is behind the cursor. It reports whether the client is still there.
func (s *Server) sendRevised(ctx context.Context, w io.Writer, chatID string, after int64, shown map[string]int) bool {
	msgs, err := s.svc.ListChatArtifactMessages(ctx, chatID)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "reading a chat's artifacts failed",
				slog.String("event", "clientapi.chat_artifacts_failed"), slog.String("error", err.Error()))
		}

		return true
	}

	for _, m := range msgs {
		if m.Seq > after || m.Artifact == nil || shown[m.Artifact.ID] == m.Artifact.Revision {
			continue
		}

		if !writeSSE(w, "", toChatMessage(m), "message") {
			return false
		}

		shown[m.Artifact.ID] = m.Artifact.Revision
	}

	return true
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

// sendQueue sends the chat's whole queue when it differs from what the stream sent last (issue #497), as pi's
// queue_update carries the whole queue (json.md). It returns false once the client is gone; a queue that cannot be
// read is logged and sent at the next wake-up.
func (s *Server) sendQueue(ctx context.Context, w http.ResponseWriter, chatID string, sent *[]int64) bool {
	q, err := s.svc.ChatQueue(ctx, chatID)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "reading the chat's queue failed",
				slog.String("event", "clientapi.chat_queue_failed"), slog.String("error", err.Error()))
		}

		return true
	}

	ids := make([]int64, 0, len(q))
	for _, m := range q {
		ids = append(ids, m.ID)
	}

	if *sent != nil && slices.Equal(ids, *sent) {
		return true
	}

	*sent = ids

	return writeSSE(w, "", toQueueList(q), "queue")
}
