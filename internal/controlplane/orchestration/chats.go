package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/chatfile"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
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
	Usage *ContextUsage
	// LastLine is the first line of its latest message of the owner or its Bot, read in a list only (issue #471).
	LastLine string
	// Routine says the chat was opened for a Routine's run, and Channel that it is a Lark chat: a person's or a
	// group's at a Channel, one someone other than the owner wrote in, or another person's from before sender ids,
	// titled "Lark: <name>" with Lark's "[Lark message from <name>, who is not the owner. …]" label in its lines
	// (issue #923). Neither is ever a Bot's main chat, as OpenMausBot's main thread is never a routine's run nor a
	// channel's thread (server/main-thread.ts, server/index.ts mainThreadTasks).
	Routine   bool
	Channel   bool
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

// piReserveShare and piReserveFloor: pi compacts a chat once fewer than max(15% of the model's window, 16,384)
// tokens are free, as omp (packages/agent/src/compaction/compaction.ts) and opencode
// (packages/core/src/session/compaction.ts) reserve (issue #395). pi's own reserve is 16,384 whatever the window.
const (
	piReserveShare = 0.15
	piReserveFloor = 16384
)

// CompactReserve is the reserve each chat's pi is given for a model with this window (pi's
// compaction.modelOverrides reserveTokens), and so what the context meter shows pi compacting at, the window less
// this (issue #1009). It is the one place the reserve is computed: pi's settings and the API both read it.
func CompactReserve(window int64) int64 {
	return max(int64(float64(window)*piReserveShare), piReserveFloor)
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
	// Recall is the owner's words a wake-up carries, which the turn it starts is recalled by (issue #805): his
	// comment on an Inbox item, or its title and his prompt when he handed it over; empty for other messages.
	Recall string
	// DeliveredAt is when pi got an owner message or a wake-up.
	DeliveredAt *time.Time
	CreatedAt   time.Time
}

// ChatSender is a person other than the owner who wrote a chat message (issue #468): a person of a Channel, by
// their id at the service and their name, as OpenMausBot keeps a message's sender {name, id} (server/store.ts).
// A person message with no sender is the owner's. ID is empty when the service did not say who wrote it.
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

// ChatAttachment is a file uploaded to a chat's Bot's folder for a message or an Inbox wake.
type ChatAttachment = chatfile.Attachment

// AttachmentsDir is where files attached in a chat are uploaded in its Bot's folder.
func AttachmentsDir(chatID string) string { return chatfile.Dir(chatID) }

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
	chat, _, err := s.createChat(ctx, id, bot, title, m, notMain)
	return chat, err
}

// StartMainChat is CreateChatWithID for the web's New chat and the CLI's --new or /new (issue #471): the chat made
// becomes its Bot's main chat, the one a click on the Bot opens, as OpenClaw's /new starts a new session under the
// agent's main key (docs/concepts/session.md). The Bot's former main chat stays as it was, with its id and its
// history; it is only no longer the Bot's row, and its open Runs move to the chat made (issue #1169, moveMainRunsTx).
// Lark's chats and a Routine's never take this path.
func (s *Service) StartMainChat(ctx context.Context, id, bot, title string, m ChatModel) (Chat, error) {
	chat, _, err := s.createChat(ctx, id, bot, title, m, moveMain)
	return chat, err
}

// OpenMainChat returns a Bot's main chat, the main Bot's when bot is empty, making it when the Bot has none (issue
// #471): a Bot that never had one, or whose main chat was deleted. The chat made takes the id, title and model given,
// as CreateChatWithID's, so that the web's first message, whose files were put under the id its page chose, makes
// it; a Bot that has a main chat keeps it as it is, and the id, title and model given are not used. It reads and
// makes in one write, so the web and the CLI writing to a Bot at once end in one main chat. made says it was made
// now.
func (s *Service) OpenMainChat(ctx context.Context, bot, id, title string, m ChatModel) (chat Chat, made bool,
	err error,
) {
	return s.createChat(ctx, id, bot, title, m, mainIfNone)
}

// mainMode says what a chat made does to its Bot's main chat.
type mainMode int

const (
	// notMain leaves it: a Lark chat, a test's.
	notMain mainMode = iota
	// moveMain makes the chat the main chat.
	moveMain
	// mainIfNone returns the main chat when the Bot has one, and makes the chat its main chat when not.
	mainIfNone
)

func (s *Service) createChat(ctx context.Context, id, bot, title string, m ChatModel, mode mainMode,
) (Chat, bool, error) {
	if id != "" && !IsChatID(id) {
		return Chat{}, false, fmt.Errorf("%w: a chat id is %q followed by 26 characters of a-z and 2-7", ErrInvalid,
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
		return Chat{}, false, fmt.Errorf("%w: a chat title is at most 200 characters", ErrInvalid)
	}

	var made bool

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error
		id, made, err = s.createChatTx(ctx, q, now, id, bot, title, m, mode)

		return err
	}); err != nil {
		return Chat{}, false, fmt.Errorf("creating a chat: %w", err)
	}

	chat, err := s.GetChat(ctx, id)

	return chat, made, err
}

func (s *Service) createChatTx(ctx context.Context, q *store.Queries, now int64, id, bot, title string, m ChatModel,
	mode mainMode,
) (string, bool, error) {
	row, err := q.GetBot(ctx, bot)
	if err != nil {
		return "", false, notFound(err, "bot", bot)
	}

	if mode == mainIfNone && row.MainChat.Valid {
		return row.MainChat.String, false, nil
	}

	if id == "" {
		id = s.newID(ChatIDPrefix)
	}

	if _, err := q.GetChat(ctx, id); err == nil {
		return "", false, fmt.Errorf("%w: chat %s exists; open it, or start a new chat", ErrConflict, id)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("reading chat %s: %w", id, err)
	}

	m, err = checkChatModel(ctx, q, m)
	if err != nil {
		return "", false, err
	}

	if err := q.InsertChat(ctx, store.InsertChatParams{
		ID: id, Title: title, Bot: bot, Provider: nullString(m.Provider), Model: nullString(m.Model),
		Effort: nullString(m.Effort), Now: now,
	}); err != nil {
		return "", false, err
	}

	if mode != notMain {
		if err := q.SetBotMainChat(ctx, store.SetBotMainChatParams{ChatID: nullString(id), Bot: bot}); err != nil {
			return "", false, err
		}

		// The open Runs of the former main chat move with it (issue #1169).
		if err := moveMainRunsTx(ctx, q, now, row.MainChat.String, id); err != nil {
			return "", false, err
		}
	}

	return id, true, nil
}

// GetChat returns one chat.
func (s *Service) GetChat(ctx context.Context, id string) (Chat, error) {
	q := s.read()

	row, err := q.GetChat(ctx, id)
	if err != nil {
		return Chat{}, notFound(err, "chat", id)
	}

	marks, err := q.GetChatMarks(ctx, id)
	if err != nil {
		return Chat{}, notFound(err, "chat", id)
	}

	c := chatFromRow(row)
	c.Routine, c.Channel = marks.Routine != 0, marks.Channel != 0

	return c, nil
}

// SetMainChat makes one of a Bot's chats its main chat, the one a click on the Bot opens and the web and the CLI
// write to (issue #923): the owner's Set as main chat, as OpenMausBot's "Set as main thread" stores the bot's
// mainThreadChoice (server/routes/bot-patch.ts). A chat that is not the Bot's, a Routine's chat and a Lark chat are
// refused, as OpenMausBot refuses a routine's run, another bot's thread and one its resolver cannot pick, alike
// (server/routes/bot-patch.test.ts). The Bot's former main chat stays as it was, but for its open Runs, which move to
// the chat set (issue #1169, moveMainRunsTx).
func (s *Service) SetMainChat(ctx context.Context, bot, chatID string) (Bot, error) {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		former, err := q.GetBot(ctx, bot)
		if err != nil {
			return notFound(err, "bot", bot)
		}

		row, err := q.GetChat(ctx, chatID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// A chat that is not there is refused as another Bot's is, as OpenMausBot answers both with one 400.
		if err != nil || row.BotID != bot {
			return fmt.Errorf("%w: chat %s is not one of bot %s's chats", ErrInvalid, chatID, bot)
		}

		marks, err := q.GetChatMarks(ctx, chatID)
		if err != nil {
			return err
		}

		switch {
		case marks.Routine != 0:
			return fmt.Errorf("%w: chat %s is a Routine's run; a Routine's chat is never a main chat", ErrInvalid,
				chatID)
		case marks.Channel != 0:
			return fmt.Errorf("%w: chat %s is a Lark chat, which is never a main chat", ErrInvalid, chatID)
		}

		if err := q.SetBotMainChat(ctx, store.SetBotMainChatParams{ChatID: nullString(chatID), Bot: bot}); err != nil {
			return err
		}

		// The open Runs of the former main chat move with it (issue #1169).
		return moveMainRunsTx(ctx, q, now, former.MainChat.String, chatID)
	}); err != nil {
		return Bot{}, fmt.Errorf("setting the main chat of bot %s: %w", bot, err)
	}

	return s.GetBot(ctx, bot)
}

// ListChats returns a page of the chats, of one Bot when bot names one, the most recently active first, after
// skipping offset of them, each with its last line and its marks. The first page also carries every Bot's main chat,
// every chat an Approval waits in and the chat a Bot with no main chat opens, its latest that is not a Routine's run
// (issue #923), whatever their age
// (queries/chats.sql ListChats).
func (s *Service) ListChats(ctx context.Context, bot string, limit, offset int) ([]Chat, error) {
	rows, err := s.read().ListChats(ctx, store.ListChatsParams{
		Bot: bot, Lim: int64(pageLimit(limit)), Skip: int64(max(offset, 0)),
	})
	if err != nil {
		return nil, fmt.Errorf("listing chats: %w", err)
	}

	out := make([]Chat, 0, len(rows))
	for _, r := range rows {
		c := chatFromRow(r.Chat)
		c.LastLine = lastLine(r.LastText)
		c.Routine, c.Channel = r.Routine != 0, r.Channel != 0
		out = append(out, c)
	}

	return out, nil
}

// DeleteChat deletes a chat for good (issue #484), as OpenMausBot deletes a room, Hermes a session and opencode a
// session, with no archive: its messages, setup changes, Connection requests, artifacts and Lark chat go; ended Runs, Routines' runs and
// approvals made in it stay and lose the chat; its gateway keys are revoked and kept. A Bot's main chat deleted
// leaves the Bot without one until the next message from the web or the CLI makes another (issue #471). A chat a Routine tells its runs
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

	// A pi's token bound to the chat ends with it (issue #996), once the delete is committed.
	s.endBotTokens(func(b botToken) bool { return b.chat == id })

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
	open, err := q.ChatHasOpenRuns(ctx, nullString(id))
	if err != nil {
		return fmt.Errorf("checking for open Runs in chat %s: %w", id, err)
	}
	if open {
		return fmt.Errorf("%w: chat %s has open Runs; settle them before deleting the chat", ErrConflict, id)
	}

	// What a Bot was doing in the chat is given back (issue #1004).
	if err := s.inbox.EndAttemptsTx(ctx, q, now, inboxstore.WorkChat, id, "the chat was deleted"); err != nil {
		return err
	}

	blobs, err := q.ListArtifactBlobsOfChat(ctx, id)
	if err != nil {
		return err
	}

	steps := []func(context.Context, string) error{
		q.DetachRunsFromChat,
		func(ctx context.Context, id string) error { return routinestore.DetachFromChatTx(ctx, q, id) },
		q.DetachApprovalsFromChat,
		func(ctx context.Context, id string) error {
			if s.gatewayKeys == nil {
				return nil
			}

			return s.gatewayKeys.RevokeDeletedChatKeysTx(ctx, tx, id, now)
		},
		q.DeleteSetupChangesOfChat,
		q.DeleteConnectionRequestsOfChat,
		func(ctx context.Context, id string) error { return channelstore.DeleteChannelChatOfChatTx(ctx, q, id) },
		// An Inbox item's own chat is no more (issue #1111): the next hand-over opens another.
		func(ctx context.Context, id string) error { return q.ClearTodoChats(ctx, nullString(id)) },
		q.ClearMainChat, q.DeleteArtifactsOfChat, q.DeleteChatQueue, q.DeleteChatMessages, q.DeleteChat,
	}

	// The sealed copies of its links' tokens go with the links its artifacts take (issue #996).
	shareSecrets, err := q.ListShareSecretsOfChat(ctx, id)
	if err != nil {
		return fmt.Errorf("listing the links of chat %s: %w", id, err)
	}

	for _, step := range steps {
		if err := step(ctx, id); err != nil {
			return err
		}
	}

	if err := s.deleteShareSecrets(ctx, tx, shareSecrets); err != nil {
		return err
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

// lastLine is a message's first line that has text, at most 120 characters, as a row shows it.
func lastLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}

		if r := []rune(line); len(r) > 120 {
			line = string(r[:120])
		}

		return line
	}

	return ""
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
		if err := a.Check(chatID); err != nil {
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
		Recall:      r.Recall.String,
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
// (docs/plugins/tool-plugins.md). A chat with no such message is no turn of his. In an Inbox item's chat (issue
// #1229), his own words typed in the Inbox (his prompt as he hands the item over, his comment) are his turn as his
// message is, and so is the hand-off that marked the chat for him.
func (s *Service) OwnerTurn(ctx context.Context, chatID string) (bool, error) {
	t, err := whoseTurn(ctx, s.read(), chatID)

	return t.owner(), err
}

// whose is whose a chat's turn is now.
type whose struct {
	// own: the last input is the owner's own words, his message in the chat or what he typed in the Inbox for the
	// item (decision 7 of issue #1229), as OpenClaw takes owner status from the verified sender
	// (src/auto-reply/command-auth.ts) and OpenMausBot's answersOwnersLine a line he wrote (server/cloud-lending.ts).
	own bool
	// marked is the item the chat's mark is for, when the last input is the hand-off that marked it and nobody else's
	// input ever came in the chat (issue #1229, rules 1 to 3); empty otherwise.
	marked string
}

// owner says the turn is the owner's, which his own accounts are used on (OwnerTurn).
func (w whose) owner() bool { return w.own || w.marked != "" }

// lineFor says whether a Bot passing the item todo on from this turn passes it on the owner's line (issue #1229, rules 1
// and 5): his own words are behind any item it passes, as his message is; a mark only behind the item it is for, as
// OpenMausBot's handsOnLending reaches "never further than this turn itself can" (server/host/cloud-lending-turns.ts).
func (w whose) lineFor(todo string) bool { return w.own || (w.marked != "" && w.marked == todo) }

// whoseTurn reads whose turn a chat is, in the caller's transaction or read. The mark (issue #1229) is the control
// plane's record that a Bot passed an Inbox item into the chat on the owner's line, as OpenMausBot's harness records
// forOwner on the work it hands on and OpenClaw spawnedBySenderIsOwner on a child
// (src/agents/subagents/spawn/spawn-parent-lineage.ts), never the model: it holds in that chat for that item, and keeps
// no timer. A Run's events there are no turn of his and leave it; anyone else's input clears it for good
// (LastTurnInput).
func whoseTurn(ctx context.Context, q *store.Queries, chatID string) (whose, error) {
	last, err := q.LastTurnInput(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return whose{}, nil
	}

	if err != nil {
		return whose{}, fmt.Errorf("reading whose turn chat %s is: %w", chatID, err)
	}

	w := whose{own: (ChatRole(last.Role) == ChatOwner && !last.SenderID.Valid) || last.WakeBy.String == wakeByOwner}
	if last.WakeBy.String == wakeByBotForOwner && last.OwnerOnly == 1 {
		w.marked = last.WakeTodo.String
	}

	return w, nil
}

func (s *Service) ReadRun(ctx context.Context, chatID string) (bool, error) {
	read, err := s.read().IsReadRun(ctx, chatID)
	if err != nil {
		return false, fmt.Errorf("reading routine access of chat %s: %w", chatID, err)
	}

	return read, nil
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
		if str("kind") == string(EscalationPullRequest) {
			return pullRequestWakeText(e, p)
		}

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
