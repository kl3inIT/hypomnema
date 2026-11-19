package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
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
	// ChatApproval is the owner's answer to an Approval asked in the chat (issue #465), given to the coordinator
	// when its request had ended.
	ChatApproval ChatKind = "approval"
	// ChatArtifact shows an artifact a Bot made (issue #402, artifacts.go).
	ChatArtifact ChatKind = "artifact"
	// ChatContinued says the control plane restarted and woke the Bot to continue the turn the restart cut (issue
	// #716, cutturns.go); pi is given it.
	ChatContinued ChatKind = "continued"
	// ChatCut says a restart cut the turn and it is not continued.
	ChatCut ChatKind = "cut"
)

// Limits on chats.
const (
	// MaxChatText bounds one message.
	MaxChatText = storeutil.MaxChatText
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
	Choice ChatModel
	// Usage is how full its context window is, nil before pi first reported it (issue #384).
	Usage     *ContextUsage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ContextUsage is pi's report of how full a chat's context window is (issue #384; pi docs/rpc-commands.md,
// get_session_stats): the tokens pi counts in its context, the model's window, and the share used. Tokens and Percent
// are nil right after a compaction, until pi's next answer measures them again (pi agent-session.ts
// getContextUsage).
type ContextUsage struct {
	Tokens  *int64
	Window  int64
	Percent *float64
}

// SetChatContextUsage keeps pi's last report of the chat's context window (issue #384). The chat's updated_at stays:
// it orders the chats by what was said in them.
func (s *Service) SetChatContextUsage(ctx context.Context, id string, u ContextUsage) error {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		p := store.SetChatContextUsageParams{ID: id, ContextWindow: sql.NullInt64{Int64: u.Window, Valid: u.Window > 0}}
		if u.Tokens != nil {
			p.Tokens = sql.NullInt64{Int64: *u.Tokens, Valid: true}
		}

		if u.Percent != nil {
			p.Percent = sql.NullFloat64{Float64: *u.Percent, Valid: true}
		}

		return q.SetChatContextUsage(ctx, p)
	}); err != nil {
		return fmt.Errorf("keeping the context usage of chat %s: %w", id, err)
	}

	return nil
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
	// Artifact is what an artifact's message shows (issue #402); nil for other messages.
	Artifact *ArtifactCard
	// ApprovalID is the Approval an approval row answers (issue #465); empty for other messages and older rows.
	ApprovalID string
	// Approval is that Approval as it was answered, read with the message; nil where the message is read for pi.
	Approval *ChatApprovalAnswer
	// Sender is who wrote a person's message when it was not the owner (issue #468); nil for the owner's and for
	// every message that is not a person's.
	Sender *ChatSender
	// DeliveredAt is when pi got an owner message or a wake-up.
	DeliveredAt *time.Time
	CreatedAt   time.Time
}

// ChatSender is a person other than the owner who wrote a chat message (issue #468): a person of a Channel, by
// their id at the service and their name, as OpenMausBot keeps a message's sender {name, id} (server/store.ts).
// A person message with no sender is the owner's. ID is empty for a message written before senders were kept,
// whose writer is not known.
type ChatSender struct {
	ID   string
	Name string
}

// senderColumns are the columns a sender is kept in: NULL for the owner.
func senderColumns(s *ChatSender) (id, name sql.NullString) {
	if s == nil {
		return sql.NullString{}, sql.NullString{}
	}

	return sql.NullString{String: s.ID, Valid: true}, sql.NullString{String: s.Name, Valid: s.Name != ""}
}

// senderOf reads the sender kept in a row's columns.
func senderOf(id, name sql.NullString) *ChatSender {
	if !id.Valid {
		return nil
	}

	return &ChatSender{ID: id.String, Name: name.String}
}

// ChatApprovalAnswer is what an approval row shows of its Approval (issue #465): what was asked, the answer and
// how far it reaches, and when it was given. An answered Approval no longer changes, as a wake-up's event does not.
type ChatApprovalAnswer struct {
	Title      string
	ActionName string
	Status     ApprovalStatus
	// Scope is how far an approval reaches; empty for a rejection.
	Scope     ApprovalScope
	DecidedAt *time.Time
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
		bot = DefaultBotID
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

// DeleteChat deletes a chat for good (issue #484), as OpenMausBot deletes a room, Hermes a session and opencode a
// session, with no archive: its messages, setup changes, artifacts and Lark chat go; Runs, their Routines' runs and
// approvals made in it stay and lose the chat; its gateway keys are revoked and kept. A chat a Routine tells its runs
// in, or whose Routine's run is live, is refused, as OpenMausBot refuses a working room (409): the Routine is removed
// or stopped first. Whether pi works in it is the supervisor's to know; the caller refuses that first. The chat is
// returned: its files, outside the database, are the caller's to remove after the commit.
func (s *Service) DeleteChat(ctx context.Context, id string) (Chat, error) {
	var chat Chat

	err := s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
		row, err := q.GetChat(ctx, id)
		if err != nil {
			return notFound(err, "chat", id)
		}

		chat = chatFromRow(row)

		if err := chatLive(ctx, q, id); err != nil {
			return err
		}

		return s.deleteChatTx(ctx, tx, q, id, now)
	})
	if err != nil {
		return Chat{}, fmt.Errorf("deleting chat %s: %w", id, err)
	}

	return chat, nil
}

// chatLive refuses to delete a chat a Routine posts into.
func chatLive(ctx context.Context, q *store.Queries, id string) error {
	routines, err := routinestore.CountRoutinesOfChatTx(ctx, q, id)
	if err != nil {
		return err
	}

	if routines > 0 {
		return fmt.Errorf("%w: %d routines tell their runs in chat %s; remove them first", ErrConflict, routines, id)
	}

	run, err := routinestore.LiveRunOfChatTx(ctx, q, id)
	if err != nil {
		return err
	}

	if run != nil {
		return fmt.Errorf("%w: run %s of a routine is live in chat %s; stop it first", ErrConflict, run.ID, id)
	}

	return nil
}

// deleteChatTx deletes one chat with the caller's transaction and queries: what refers to it is let go of or removed
// first (queries/chats.sql), then the bytes of its artifacts that no other artifact keeps. Its gateway keys are
// revoked by the gateway's own function in the same transaction (issue #405), so the chat and its live keys never
// outlast one another. A chat and a Bot removed with its chats (ForceDeleteBot) take the same steps, as
// ForceDeleteBot's own did (issue #330).
func (s *Service) deleteChatTx(ctx context.Context, tx *sql.Tx, q *store.Queries, id string, now int64) error {
	blobs, err := q.ListArtifactBlobsOfChat(ctx, id)
	if err != nil {
		return err
	}

	steps := []func(context.Context, string) error{
		q.DetachRunsFromChat,
		func(ctx context.Context, id string) error { return routinestore.DetachFromChatTx(ctx, q, id) },
		q.DetachApprovalsFromChat,
		func(ctx context.Context, id string) error {
			if s.chatKeys == nil {
				return nil
			}

			return s.chatKeys(ctx, tx, id, now)
		},
		q.DeleteSetupChangesOfChat,
		func(ctx context.Context, id string) error { return channelstore.DeleteChannelChatOfChatTx(ctx, q, id) },
		q.DeleteArtifactsOfChat, q.DeleteChatQueue, q.DeleteChatMessages, q.DeleteChat,
	}
	for _, step := range steps {
		if err := step(ctx, id); err != nil {
			return err
		}
	}

	if len(blobs) == 0 {
		return nil
	}

	candidates, err := json.Marshal(blobs)
	if err != nil {
		return err
	}

	return q.DeleteUnusedArtifactBlobs(ctx, string(candidates))
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return ChatPageDefault
	}

	return min(limit, ChatPageMax)
}

func chatFromRow(r store.Chat) Chat {
	return Chat{
		ID: r.ID, Title: r.Title, Bot: r.BotID,
		Choice:    ChatModel{Provider: r.Provider.String, Model: r.Model.String, Effort: r.Effort.String},
		Usage:     usageFromRow(r),
		CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
	}
}

func usageFromRow(r store.Chat) *ContextUsage {
	if !r.ContextWindow.Valid || r.ContextWindow.Int64 <= 0 {
		return nil
	}

	u := &ContextUsage{Window: r.ContextWindow.Int64}
	if r.ContextTokens.Valid {
		u.Tokens = &r.ContextTokens.Int64
	}

	if r.ContextPercent.Valid {
		u.Percent = &r.ContextPercent.Float64
	}

	return u
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
	// Sender is who wrote a person's message when it is not the owner (issue #468), and only a person's: a role
	// owner message is a person's, the owner's or a Channel person's.
	Sender *ChatSender
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
	case m.Sender != nil && m.Role != ChatOwner:
		return fmt.Errorf("%w: only a person's message has a sender", ErrInvalid)
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

			files, err := keptAttachments(m.Attachments)
			if err != nil {
				return err
			}

			senderID, senderName := senderColumns(m.Sender)

			seq, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{
				ChatID: chatID, Role: string(m.Role), Kind: string(m.Kind), Text: m.Text, Parts: stored,
				Attachments: files, SenderID: senderID, SenderName: senderName, Now: now,
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

	return listChatMessages(ctx, q, chatID, after, math.MaxInt64, pageLimit(limit))
}

// ListChatMessagesBefore returns the newest messages with a seq below before, oldest first, and whether older ones
// are left: a page of history read from its end, as ListItems reads a transcript (issue #502).
func (s *Service) ListChatMessagesBefore(ctx context.Context, chatID string, before int64, limit int,
) ([]ChatMessage, bool, error) {
	q := s.read()
	if _, err := q.GetChat(ctx, chatID); err != nil {
		return nil, false, notFound(err, "chat", chatID)
	}

	limit = pageLimit(limit)

	start, err := q.ChatPageStart(ctx, store.ChatPageStartParams{ChatID: chatID, Before: before, Skip: int64(limit)})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		start = 0
	case err != nil:
		return nil, false, fmt.Errorf("finding the start of a page of chat %s: %w", chatID, err)
	}

	msgs, err := listChatMessages(ctx, q, chatID, start, before, limit)

	return msgs, start > 0, err
}

func listChatMessages(ctx context.Context, q *store.Queries, chatID string, after, before int64, limit int,
) ([]ChatMessage, error) {
	rows, err := q.ListChatMessages(ctx, store.ListChatMessagesParams{
		ChatID: chatID, After: after, Before: before, Lim: int64(limit),
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

		if r.ArtifactID != "" {
			m.Artifact = &ArtifactCard{
				ID: r.ArtifactID, Revision: int(r.ArtifactRevision), Title: r.ArtifactTitle,
				Type: artifactpack.Type(r.ArtifactType), UpdatedAt: millis(r.ArtifactUpdatedAt),
			}
		}

		if r.ApprovalStatus != "" {
			m.Approval = chatApproval(r)
		}

		out = append(out, m)
	}

	return out, nil
}

func chatApproval(r store.ListChatMessagesRow) *ChatApprovalAnswer {
	scope := ApprovalScope(r.ApprovalScope)
	if r.ApprovalStanding == 1 {
		scope = ScopeRoutine
	}

	a := &ChatApprovalAnswer{
		Title: r.ApprovalTitle, ActionName: r.ApprovalActionName, Status: ApprovalStatus(r.ApprovalStatus), Scope: scope,
	}
	if r.ApprovalDecidedAt != 0 {
		at := millis(r.ApprovalDecidedAt)
		a.DecidedAt = &at
	}

	return a
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
		ApprovalID:  r.ApprovalID.String,
		Sender:      senderOf(r.SenderID, r.SenderName),
		DeliveredAt: optionalTime(r.DeliveredAt), CreatedAt: millis(r.CreatedAt),
	}
}

// ChatTurn names the turn a chat's pi works on now: its turn mark's pi and
// start (issue #716), which agent_start sets and agent_settled clears, so a
// next turn has another name. Empty between turns. A take a Bot films ends
// with the turn that started it (issue #817).
func (s *Service) ChatTurn(ctx context.Context, chatID string) (string, error) {
	row, err := s.read().GetChat(ctx, chatID)
	if err != nil {
		return "", notFound(err, "chat", chatID)
	}

	if !row.TurnToken.Valid || !row.TurnStartedAt.Valid {
		return "", nil
	}

	return fmt.Sprintf("%s@%d", row.TurnToken.String, row.TurnStartedAt.Int64), nil
}

// OwnerTurn says whether what a chat's pi works on now is the owner's own request (issue #468): the last of the
// chat's messages pi is given as a turn is a message the owner wrote. A message of a Channel person who is not
// the owner, a wake-up (a Run's events, a Routine's run, an Approval answered after its request ended) and a
// message whose writer is not known each end his turn, whether pi has it yet or not, as OpenMausBot lets the last
// sender decide (server/host/outside-lineage.ts) and OpenClaw never counts a wake as an owner turn
// (docs/plugins/tool-plugins.md). A chat with no such message is no turn of his.
func (s *Service) OwnerTurn(ctx context.Context, chatID string) (bool, error) {
	last, err := s.read().LastTurnInput(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading whose turn chat %s is: %w", chatID, err)
	}

	return ChatRole(last.Role) == ChatOwner && !last.SenderID.Valid, nil
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
			if review, ok := p["review"].(map[string]any); ok {
				return reviewWakeText(e, review)
			}

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

func reviewWakeText(e store.Event, review map[string]any) string {
	action := "Run stalled after review; follow the coordinator's review reference."
	if review["waiting_for_owner"] == true {
		action = "Review limit reached. Leave the Run waiting for the owner; add no Task and report the verdict and findings."
	}

	return fmt.Sprintf("Review Task %s of run %s: %s on %s (mergeable: %s, merge state: %s, required checks: %s). "+
		"%s Read the findings: hypomnema tasks show %s --json",
		e.TaskID.String, e.RunID.String, review["verdict"], review["pull_request"], review["mergeable"],
		review["merge_state"], review["checks"], action, e.TaskID.String)
}
