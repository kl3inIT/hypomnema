package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
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

// ChatKind says what a system message is (issue #267, after t3code's typed
// timeline items and OpenMausBot's named stop rows); owner, coordinator and
// tool messages have none.
type ChatKind string

// Kinds of system messages.
const (
	// ChatWake reports an event of one of the chat's Runs.
	ChatWake ChatKind = "wake"
	// ChatFailed says pi's model call failed.
	ChatFailed ChatKind = "failed"
	// ChatStopped ends a turn the owner stopped.
	ChatStopped ChatKind = "stopped"
	// ChatApproval tells the coordinator the owner answered an Approval.
	ChatApproval ChatKind = "approval"
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
	Bot string
	// Choice is the model and effort the chat runs with, each empty when it is the Bot's (issue #369).
	Choice    ChatModel
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChatModel is a chat's own model and effort (issue #369), as a t3code thread, an Orca session and an Omnigent
// session keep theirs: a chat starts with its Bot's, a change applies to the chat from its next message, and the
// Bot's page keeps the default. An empty field is the Bot's. Provider is where the model is found, an id in pi's
// models.json, as a Bot's is.
type ChatModel struct {
	Provider string
	Model    string
	Effort   string
}

// ChatEfforts are the efforts a chat may name: the model gateway's levels (modelgateway attributes.go), after which
// pi's thinking levels are named, none being pi's off (clientcli/pimodels.go). The composer offers a model's own.
var ChatEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// checkChatModel checks a chat's choice before it is stored and sets the provider its model is found at, as a
// Bot's model is checked: a model pi can reach. A provider without a model is nothing to run with.
func checkChatModel(ctx context.Context, q *store.Queries, m ChatModel) (ChatModel, error) {
	if m.Effort != "" && !slices.Contains(ChatEfforts, m.Effort) {
		return ChatModel{}, fmt.Errorf("%w: unknown effort %q; known: %s", ErrInvalid, m.Effort,
			strings.Join(ChatEfforts, ", "))
	}

	if m.Model == "" {
		m.Provider = ""

		return m, nil
	}

	provider, err := checkModel(ctx, q, botRuntime, m.Provider, m.Model, "")
	if err != nil {
		return ChatModel{}, err
	}

	m.Provider = provider

	return m, nil
}

// ChatModelChange changes a chat's choice: each field given replaces the chat's, and an empty one gives the chat its
// Bot's again. A provider counts only with the model it names.
type ChatModelChange struct {
	Provider *string
	Model    *string
	Effort   *string
}

// SetChatModel changes the model and effort a chat runs with (issue #369). pi takes the change before the chat's
// next message (coordinator), and a pi started later starts with it.
func (s *Service) SetChatModel(ctx context.Context, id string, change ChatModelChange) (Chat, error) {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetChat(ctx, id)
		if err != nil {
			return notFound(err, "chat", id)
		}

		m := chatFromRow(row).Choice
		if change.Model != nil {
			m.Model, m.Provider = *change.Model, ""
			if change.Provider != nil {
				m.Provider = *change.Provider
			}
		}

		if change.Effort != nil {
			m.Effort = *change.Effort
		}

		if m, err = checkChatModel(ctx, q, m); err != nil {
			return err
		}

		return q.SetChatModel(ctx, store.SetChatModelParams{
			ID: id, Provider: nullString(m.Provider), Model: nullString(m.Model), Effort: nullString(m.Effort),
		})
	}); err != nil {
		return Chat{}, fmt.Errorf("choosing the model of chat %s: %w", id, err)
	}

	return s.GetChat(ctx, id)
}

// ChatMessage is one message of a chat.
type ChatMessage struct {
	Seq    int64
	ChatID string
	Role   ChatRole
	// Kind is what a system message is; empty for other messages.
	Kind ChatKind
	Text string
	// Parts is a JSON array of a reply's or a tool's parts (text, thinking,
	// tool_call, tool_result); nil for other messages.
	Parts json.RawMessage
	// EventSeq is the event a wake-up reports; 0 for other messages.
	EventSeq int64
	// Wake is the event a wake-up reports, read with the message; nil for
	// other messages and where the message is read for pi.
	Wake *WakeEvent
	// Attachments are the files the owner attached to an owner message (issue #343); nil for other messages.
	Attachments []ChatAttachment
	// DeliveredAt is when pi got an owner message or a wake-up.
	DeliveredAt *time.Time
	CreatedAt   time.Time
}

// ChatAttachment is a file the owner attached to a message (issue #343), as t3code's chat attachment (name, size,
// type) and OpenMausBot's file by path: the file was uploaded into the chat's Bot's folder, and pi reads it there.
type ChatAttachment struct {
	// Name is the file's name as the owner picked it.
	Name string `json:"name"`
	// Path is where it is in the Bot's folder: attachments/<chat id>/<file>.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Type is its media type as the browser named it; empty when it named none.
	Type string `json:"type,omitempty"`
}

// AttachmentsDir is the folder of a chat's attachments in its Bot's folder (issue #343), kept apart from the work
// as Orca's agent-session-attachments and OpenMausBot's DATA_DIR/attachments are.
func AttachmentsDir(chatID string) string { return "attachments/" + chatID }

// maxAttachmentField bounds an attachment's name and type.
const maxAttachmentField = 255

func (a ChatAttachment) check(chatID string) error {
	dir, file := path.Split(a.Path)

	switch {
	case dir != AttachmentsDir(chatID)+"/" || file == "" || file == "." || file == ".." || !plainLine(file):
		return fmt.Errorf("%w: an attachment is a file of %s, not %q", ErrInvalid, AttachmentsDir(chatID), a.Path)
	case strings.TrimSpace(a.Name) == "":
		return fmt.Errorf("%w: an attachment needs its name", ErrInvalid)
	case len(a.Name) > maxAttachmentField || len(a.Type) > maxAttachmentField:
		return fmt.Errorf("%w: an attachment's name and type are at most %d bytes", ErrInvalid, maxAttachmentField)
	case !plainLine(a.Name) || !plainLine(a.Type):
		return fmt.Errorf("%w: an attachment's name and type are one line of text", ErrInvalid)
	case a.Size < 0 || a.Size > workspacefs.MaxUpload:
		return fmt.Errorf("%w: an attachment is at most %d MiB", ErrInvalid, workspacefs.MaxUpload>>20)
	}

	return nil
}

// plainLine says whether s is UTF-8 without a control character, so that a name is one line of what pi is given.
func plainLine(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}

	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}

	return true
}

// WakeEvent is what a wake-up's event says (issue #303, for the web's
// wake-up cards, after t3code's TimelineSystemDivider): it is read from the
// event, which never changes, so it is never out of step with the row.
type WakeEvent struct {
	Event      EventKind
	RunID      string
	TaskID     string
	QuestionID string
	// Status is what the Task became (task.status) or the Run's end
	// (finished, canceled); empty for a question or an escalation.
	Status string
	// Reason is why a Task is blocked or failed, or an escalation's reason.
	Reason string
	// Escalation is an escalation's kind.
	Escalation string
}

// untitled is a new chat's title until its first owner message names it
// (queries/chats.sql TitleUntitledChat, migration 00012).
const untitled = "Chat"

// titleOf is a message's first line, at most 80 characters; a message of files alone is named by its first file.
func titleOf(text string, files []ChatAttachment) string {
	if strings.TrimSpace(text) == "" && len(files) > 0 {
		text = files[0].Name
	}

	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 80 {
		line = string(r[:80])
	}

	return strings.TrimSpace(line)
}

// CreateChat starts a chat with a Bot, the first one when none is named.
// Without a title, its first owner message names it.
func (s *Service) CreateChat(ctx context.Context, bot, title string) (Chat, error) {
	return s.CreateChatWithID(ctx, "", bot, title, ChatModel{})
}

// ChatIDPrefix starts every chat's id.
const ChatIDPrefix = "cht"

// IsChatID reports whether id has the shape of the chat ids made here.
func IsChatID(id string) bool { return platform.IsID(ChatIDPrefix, id) }

// CreateChatWithID starts a chat under the id the client chose, or a new one
// when id is empty. The web's New chat page chooses its chat's id when it
// opens, so that the files attached before the first message have the chat's
// folder of attachments (issue #357, after t3code's newThreadId() before the
// thread exists). The id must have the shape of the ids made here and not be
// taken. The page's choice of model and effort is the chat's from its start
// (issue #369, as t3code's draft thread keeps its model); an empty one is the
// Bot's.
func (s *Service) CreateChatWithID(ctx context.Context, id, bot, title string, m ChatModel) (Chat, error) {
	if id == "" {
		id = s.newID(ChatIDPrefix)
	} else if !IsChatID(id) {
		return Chat{}, fmt.Errorf("%w: a chat id is %q followed by 26 characters of a-z and 2-7", ErrInvalid,
			ChatIDPrefix+"_")
	}

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

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetBot(ctx, bot); err != nil {
			return notFound(err, "bot", bot)
		}

		if _, err := q.GetChat(ctx, id); err == nil {
			return fmt.Errorf("%w: chat %s exists; open it, or start a new chat", ErrConflict, id)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading chat %s: %w", id, err)
		}

		m, err := checkChatModel(ctx, q, m)
		if err != nil {
			return err
		}

		return q.InsertChat(ctx, store.InsertChatParams{
			ID: id, Title: title, Bot: bot, Provider: nullString(m.Provider), Model: nullString(m.Model),
			Effort: nullString(m.Effort), Now: now,
		})
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
	return Chat{
		ID: r.ID, Title: r.Title, Bot: r.Bot,
		Choice:    ChatModel{Provider: r.Provider.String, Model: r.Model.String, Effort: r.Effort.String},
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
	}
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
	msgs, err := s.AddChatMessages(ctx, chatID, NewChatMessage{Role: role, Text: text, Parts: parts})
	if err != nil {
		return ChatMessage{}, err
	}

	return msgs[0], nil
}

// NewChatMessage is a message to append.
type NewChatMessage struct {
	Role ChatRole
	// Kind is a system message's kind, and only a system message's.
	Kind  ChatKind
	Text  string
	Parts json.RawMessage
	// Attachments are an owner message's files, and only an owner message's.
	Attachments []ChatAttachment
}

func (m NewChatMessage) check(chatID string) error {
	switch {
	case m.Role != ChatOwner && m.Role != ChatCoordinator && m.Role != ChatTool && m.Role != ChatSystem:
		return fmt.Errorf("%w: unknown chat role %q", ErrInvalid, m.Role)
	case m.Kind != "" && m.Role != ChatSystem:
		return fmt.Errorf("%w: only a system message has a kind", ErrInvalid)
	case m.Kind != "" && m.Kind != ChatWake && m.Kind != ChatFailed && m.Kind != ChatStopped && m.Kind != ChatApproval:
		return fmt.Errorf("%w: unknown kind of chat message %q", ErrInvalid, m.Kind)
	case len(m.Attachments) > 0 && m.Role != ChatOwner:
		return fmt.Errorf("%w: only an owner message has attachments", ErrInvalid)
	case strings.TrimSpace(m.Text) == "" && len(m.Attachments) == 0 && (m.Parts == nil || m.Role == ChatOwner):
		return fmt.Errorf("%w: a message needs text", ErrInvalid)
	case len(m.Text) > MaxChatText:
		return fmt.Errorf("%w: a message is at most %d bytes", ErrInvalid, MaxChatText)
	case m.Parts != nil && !json.Valid(m.Parts):
		return fmt.Errorf("%w: message parts are not JSON", ErrInvalid)
	}

	for _, a := range m.Attachments {
		if err := a.check(chatID); err != nil {
			return err
		}
	}

	return nil
}

// AddChatMessages appends messages in one write, so a reader sees all of
// them or none: a stopped turn's last reply and the row that says it was
// stopped come together.
func (s *Service) AddChatMessages(ctx context.Context, chatID string, msgs ...NewChatMessage) ([]ChatMessage, error) {
	if len(msgs) == 0 {
		return nil, fmt.Errorf("%w: no message to add", ErrInvalid)
	}

	for _, m := range msgs {
		if err := m.check(chatID); err != nil {
			return nil, err
		}
	}

	var first int64

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetChat(ctx, chatID); err != nil {
			return notFound(err, "chat", chatID)
		}

		for i, m := range msgs {
			var stored sql.NullString
			if m.Parts != nil {
				stored = sql.NullString{String: string(m.Parts), Valid: true}
			}

			var files sql.NullString

			if len(m.Attachments) > 0 {
				raw, err := json.Marshal(m.Attachments)
				if err != nil {
					return fmt.Errorf("keeping the attachments: %w", err)
				}

				files = sql.NullString{String: string(raw), Valid: true}
			}

			seq, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{
				ChatID: chatID, Role: string(m.Role), Kind: string(m.Kind), Text: m.Text, Parts: stored,
				Attachments: files, Now: now,
			})
			if err != nil {
				return fmt.Errorf("inserting a message: %w", err)
			}

			if i == 0 {
				first = seq
			}

			if m.Role == ChatOwner {
				if err := q.TitleUntitledChat(ctx, store.TitleUntitledChatParams{
					ID: chatID, Title: titleOf(m.Text, m.Attachments),
				}); err != nil {
					return fmt.Errorf("titling the chat: %w", err)
				}
			}
		}

		return q.TouchChat(ctx, store.TouchChatParams{ID: chatID, Now: now})
	})
	if err != nil {
		return nil, fmt.Errorf("adding a message to chat %s: %w", chatID, err)
	}

	out, err := s.ListChatMessages(ctx, chatID, first-1, len(msgs))
	if err != nil || len(out) != len(msgs) {
		return nil, errors.Join(err, fmt.Errorf("reading message %d", first))
	}

	return out, nil
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

	out := make([]ChatMessage, 0, len(rows))
	for _, r := range rows {
		m := chatMessage(r.ChatMessage)
		if r.EventKind != "" {
			m.Wake = chatWake(r)
		}

		out = append(out, m)
	}

	return out, nil
}

func chatWake(r store.ListChatMessagesRow) *WakeEvent {
	var p map[string]any
	_ = json.Unmarshal([]byte(r.EventPayload), &p)

	str := func(k string) string { v, _ := p[k].(string); return v }

	w := &WakeEvent{
		Event: EventKind(r.EventKind), RunID: r.EventRunID, TaskID: r.EventTaskID, QuestionID: r.EventQuestionID,
	}

	switch w.Event {
	case EventTaskStatus:
		w.Status, w.Reason = str("to"), str("reason")
	case EventRunFinished:
		w.Status = string(RunFinished)
	case EventRunCanceled:
		w.Status = string(RunCanceled)
	case EventEscalation:
		w.Escalation, w.Reason = str("kind"), str("reason")
	default:
		// A question names only its ids.
	}

	return w
}

func chatMessages(rows []store.ChatMessage) []ChatMessage {
	out := make([]ChatMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, chatMessage(r))
	}

	return out
}

func chatMessage(r store.ChatMessage) ChatMessage {
	return ChatMessage{
		Seq: r.Seq, ChatID: r.ChatID, Role: ChatRole(r.Role), Kind: ChatKind(r.Kind), Text: r.Text,
		Parts:       rawParts(r.Parts),
		Attachments: attachmentsOf(r.Attachments),
		EventSeq:    r.EventSeq.Int64,
		DeliveredAt: optionalTime(r.DeliveredAt), CreatedAt: millis(r.CreatedAt),
	}
}

// attachmentsOf reads the attachments kept on a row. Their JSON was checked as it was written; a row that cannot be
// read has none.
func attachmentsOf(p sql.NullString) []ChatAttachment {
	if !p.Valid {
		return nil
	}

	var out []ChatAttachment
	if json.Unmarshal([]byte(p.String), &out) != nil {
		return nil
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
				ChatID: chatID, Role: string(ChatSystem), Kind: string(ChatWake), Text: text,
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
