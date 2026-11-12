package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Chats with the coordinator on the server (issue #41, PR 3), after Omnara
// (docs/research/2026-11-09-chat-storage.md): every message is kept here;
// owner messages and wake-ups wait for pi and are marked delivered one by
// one; a wake-up is a system message naming its event, so one event wakes a
// chat once. Whether pi is working is the supervisor's to know, not stored.

// ChatRole says who wrote a chat message.
type ChatRole string

// Chat roles.
const (
	ChatOwner       ChatRole = "owner"
	ChatCoordinator ChatRole = "coordinator"
	ChatTool        ChatRole = "tool"
	ChatSystem      ChatRole = "system"
)

// Limits on chats.
const (
	// MaxChatText bounds one message.
	MaxChatText = 100_000
	// ChatPageDefault and ChatPageMax bound a page of messages or chats.
	ChatPageDefault = 100
	ChatPageMax     = 500
	// wakeScan bounds the events read for one chat in one pass.
	wakeScan = 500
)

// Chat is one conversation between the owner and the coordinator.
type Chat struct {
	ID    string
	Title string
	// Bot is the Bot the chat talks with.
	Bot       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChatMessage is one message of a chat.
type ChatMessage struct {
	Seq    int64
	ChatID string
	Role   ChatRole
	Text   string
	// Parts is a JSON array of a reply's or a tool's parts (text, thinking,
	// tool_call, tool_result); nil for other messages.
	Parts json.RawMessage
	// EventSeq is the event a wake-up reports; 0 for other messages.
	EventSeq int64
	// DeliveredAt is when pi got an owner message or a wake-up.
	DeliveredAt *time.Time
	CreatedAt   time.Time
}

// untitled is a new chat's title until its first owner message names it
// (queries/chats.sql TitleUntitledChat, migration 00012).
const untitled = "Chat"

// titleOf is a message's first line, at most 80 characters.
func titleOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 80 {
		line = string(r[:80])
	}

	return strings.TrimSpace(line)
}

// CreateChat starts a chat with a Bot, the first one when none is named.
// Without a title, its first owner message names it.
func (s *Service) CreateChat(ctx context.Context, bot, title string) (Chat, error) {
	if bot == "" {
		bot = DefaultBot
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = untitled
	}

	if len(title) > 200 {
		return Chat{}, fmt.Errorf("%w: a chat title is at most 200 characters", ErrInvalid)
	}

	id := s.newID("cht")

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetBot(ctx, bot); err != nil {
			return notFound(err, "bot", bot)
		}

		return q.InsertChat(ctx, store.InsertChatParams{ID: id, Title: title, Bot: bot, Now: now})
	}); err != nil {
		return Chat{}, fmt.Errorf("creating a chat: %w", err)
	}

	return s.GetChat(ctx, id)
}

// GetChat returns one chat.
func (s *Service) GetChat(ctx context.Context, id string) (Chat, error) {
	row, err := s.read().GetChat(ctx, id)
	if err != nil {
		return Chat{}, notFound(err, "chat", id)
	}

	return chatFromRow(row), nil
}

// ListChats returns the chats, the most recently active first.
func (s *Service) ListChats(ctx context.Context, limit int) ([]Chat, error) {
	rows, err := s.read().ListChats(ctx, int64(pageLimit(limit)))
	if err != nil {
		return nil, fmt.Errorf("listing chats: %w", err)
	}

	out := make([]Chat, 0, len(rows))
	for _, r := range rows {
		out = append(out, chatFromRow(r))
	}

	return out, nil
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return ChatPageDefault
	}

	return min(limit, ChatPageMax)
}

func chatFromRow(r store.Chat) Chat {
	return Chat{ID: r.ID, Title: r.Title, Bot: r.Bot, CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt)}
}

// AddChatMessage appends a message. An owner message waits for pi until it
// is delivered; coordinator, tool and system messages are records only.
func (s *Service) AddChatMessage(ctx context.Context, chatID string, role ChatRole, text string) (ChatMessage, error) {
	return s.AddChatMessageParts(ctx, chatID, role, text, nil)
}

// AddChatMessageParts appends a message with its parts, a JSON array.
func (s *Service) AddChatMessageParts(ctx context.Context, chatID string, role ChatRole, text string,
	parts json.RawMessage,
) (ChatMessage, error) {
	switch {
	case role != ChatOwner && role != ChatCoordinator && role != ChatTool && role != ChatSystem:
		return ChatMessage{}, fmt.Errorf("%w: unknown chat role %q", ErrInvalid, role)
	case strings.TrimSpace(text) == "" && (parts == nil || role == ChatOwner):
		return ChatMessage{}, fmt.Errorf("%w: a message needs text", ErrInvalid)
	case len(text) > MaxChatText:
		return ChatMessage{}, fmt.Errorf("%w: a message is at most %d bytes", ErrInvalid, MaxChatText)
	case parts != nil && !json.Valid(parts):
		return ChatMessage{}, fmt.Errorf("%w: message parts are not JSON", ErrInvalid)
	}

	var stored sql.NullString
	if parts != nil {
		stored = sql.NullString{String: string(parts), Valid: true}
	}

	var seq int64

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetChat(ctx, chatID); err != nil {
			return notFound(err, "chat", chatID)
		}

		var err error

		seq, err = q.InsertChatMessage(ctx, store.InsertChatMessageParams{
			ChatID: chatID, Role: string(role), Text: text, Parts: stored, Now: now,
		})
		if err != nil {
			return fmt.Errorf("inserting a message: %w", err)
		}

		if role == ChatOwner {
			if err := q.TitleUntitledChat(ctx, store.TitleUntitledChatParams{ID: chatID, Title: titleOf(text)}); err != nil {
				return fmt.Errorf("titling the chat: %w", err)
			}
		}

		return q.TouchChat(ctx, store.TouchChatParams{ID: chatID, Now: now})
	})
	if err != nil {
		return ChatMessage{}, fmt.Errorf("adding a message to chat %s: %w", chatID, err)
	}

	msgs, err := s.ListChatMessages(ctx, chatID, seq-1, 1)
	if err != nil || len(msgs) == 0 {
		return ChatMessage{}, errors.Join(err, fmt.Errorf("reading message %d", seq))
	}

	return msgs[0], nil
}

// ListChatMessages returns a chat's messages after a seq, oldest first.
func (s *Service) ListChatMessages(ctx context.Context, chatID string, after int64, limit int) ([]ChatMessage, error) {
	q := s.read()
	if _, err := q.GetChat(ctx, chatID); err != nil {
		return nil, notFound(err, "chat", chatID)
	}

	rows, err := q.ListChatMessages(ctx, store.ListChatMessagesParams{
		ChatID: chatID, After: after, Lim: int64(pageLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing the messages of chat %s: %w", chatID, err)
	}

	return chatMessages(rows), nil
}

func chatMessages(rows []store.ChatMessage) []ChatMessage {
	out := make([]ChatMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChatMessage{
			Seq: r.Seq, ChatID: r.ChatID, Role: ChatRole(r.Role), Text: r.Text, Parts: rawParts(r.Parts),
			EventSeq:    r.EventSeq.Int64,
			DeliveredAt: optionalTime(r.DeliveredAt), CreatedAt: millis(r.CreatedAt),
		})
	}

	return out
}

func rawParts(p sql.NullString) json.RawMessage {
	if !p.Valid {
		return nil
	}

	return json.RawMessage(p.String)
}

// Undelivered returns the owner messages and wake-ups pi has not had yet,
// oldest first.
func (s *Service) Undelivered(ctx context.Context, chatID string) ([]ChatMessage, error) {
	rows, err := s.read().UndeliveredChatMessages(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("reading what chat %s has for pi: %w", chatID, err)
	}

	return chatMessages(rows), nil
}

// ChatsWithUndelivered lists the chats that have something for pi.
func (s *Service) ChatsWithUndelivered(ctx context.Context) ([]string, error) {
	ids, err := s.read().ChatsWithUndelivered(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing chats with messages for pi: %w", err)
	}

	return ids, nil
}

// MarkDelivered records that pi got a chat's owner messages and wake-ups up
// to seq.
func (s *Service) MarkDelivered(ctx context.Context, chatID string, upto int64) error {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.MarkChatMessagesDelivered(ctx, store.MarkChatMessagesDeliveredParams{
			ChatID: chatID, Upto: upto, Now: sql.NullInt64{Int64: now, Valid: true},
		})
	}); err != nil {
		return fmt.Errorf("marking the messages of chat %s delivered: %w", chatID, err)
	}

	return nil
}

// WakeChats adds a wake-up to each chat for every new event of its Runs
// that needs a coordinator: a Task ended or was blocked, a question was
// asked of the coordinator, an escalation came, or a Run ended. It returns
// how many it added. An event wakes a chat once, whoever calls.
func (s *Service) WakeChats(ctx context.Context) (int, error) {
	chats, err := s.read().ChatsWithRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing chats with runs: %w", err)
	}

	added := 0

	for _, chatID := range chats {
		n, err := s.wakeChat(ctx, chatID.String)
		if err != nil {
			return added, err
		}

		added += n
	}

	return added, nil
}

func (s *Service) wakeChat(ctx context.Context, chatID string) (int, error) {
	added := 0

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		after, err := q.ChatWokenAfter(ctx, chatID)
		if err != nil {
			return fmt.Errorf("reading the wake-ups of chat %s: %w", chatID, err)
		}

		rows, err := q.ChatRunEvents(ctx, store.ChatRunEventsParams{ChatID: nullString(chatID), After: after, Lim: wakeScan})
		if err != nil {
			return fmt.Errorf("reading the events of chat %s: %w", chatID, err)
		}

		for _, r := range rows {
			text := wakeText(r)
			if text == "" {
				continue
			}

			if _, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{
				ChatID: chatID, Role: string(ChatSystem), Text: text,
				EventSeq: sql.NullInt64{Int64: r.Seq, Valid: true}, Now: now,
			}); err != nil {
				return fmt.Errorf("adding a wake-up to chat %s: %w", chatID, err)
			}

			added++
		}

		if added > 0 {
			return q.TouchChat(ctx, store.TouchChatParams{ID: chatID, Now: now})
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return added, nil
}

// wakeText says what an event asks of the coordinator and the command to
// read it, or "" for an event that asks nothing.
func wakeText(e store.Event) string {
	var p map[string]any
	_ = json.Unmarshal([]byte(e.Payload), &p)

	str := func(k string) string { v, _ := p[k].(string); return v }

	switch EventKind(e.Kind) {
	case EventTaskStatus:
		switch to := str("to"); to {
		case "succeeded", "failed", "blocked", "canceled":
			if to == "blocked" && str("reason") != "" {
				to += " (" + str("reason") + ")"
			}

			return fmt.Sprintf("Task %s of run %s is %s: hypomnema tasks show %s --json", e.TaskID.String,
				e.RunID.String, to, e.TaskID.String)
		}
	case EventQuestionOpened:
		if str("addressed_to") == string(AddressCoordinator) {
			return fmt.Sprintf("A worker on task %s asks you question %s: hypomnema questions show %s --json",
				e.TaskID.String, e.QuestionID.String, e.QuestionID.String)
		}
	case EventEscalation:
		return fmt.Sprintf("Escalation (%s) on task %s of run %s: hypomnema tasks show %s --json", str("kind"),
			e.TaskID.String, e.RunID.String, e.TaskID.String)
	case EventRunFinished, EventRunCanceled:
		return fmt.Sprintf("Run %s is %s: hypomnema runs show %s --json", e.RunID.String,
			strings.TrimPrefix(e.Kind, "run."), e.RunID.String)
	default:
		// Other events ask nothing of the coordinator.
	}

	return ""
}
