package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// DefaultBotID is the id of the main Bot, the one that is always there: a chat that names no other talks with it,
// and it alone changes the Bots and profiles from its chat. It was the Bot "coordinator", known by its name, until
// issue #466; its id never changes whatever it is called, as Hermes's default profile keeps the id "default"
// (hermes_cli/profiles.py, rename_profile) and OpenClaw names its system agent by id (systemAgent.agentId).
const DefaultBotID = "bot_mainaaaaaaaaaaaaaaaaaaaaaa"

// maxBotName bounds a Bot's name, as a node's (maxNodeName).
const maxBotName = 64

// botRuntime is what every Bot runs: pi, started by the control plane.
const botRuntime = "pi"

// Limits of a Bot's texts. The memory's is an agent's, memorystore's
// (Hermes's): the whole memory is in pi's prompt in every chat.
const (
	MaxPersona = 16000
	MaxMemory  = memorystore.MaxMemory
)

// ToolDisplay is what a Bot's chats show of its tool calls (issue #441), to everyone who reads them, after
// OpenClaw's verboseDefault per agent (src/agents/agent-scope-config.ts, resolveAgentVerboseDefault).
type ToolDisplay string

// The levels, as OpenClaw's (src/auto-reply/reply/followup-turn-execution.ts, shouldEmitToolResult and
// shouldEmitToolOutput).
const (
	// ToolsOff shows no tool call: only the Bot's answers.
	ToolsOff ToolDisplay = "off"
	// ToolsOn shows a row per tool call with its tool's name, without its arguments or output.
	ToolsOn ToolDisplay = "on"
	// ToolsFull shows each call's arguments, output, error and time as well.
	ToolsFull ToolDisplay = "full"
)

// Bot is a long-lived agent of the control plane (issue #169): its model,
// what it is (the persona, which only the owner writes) and what it keeps
// across chats (the memory, which it writes itself). It is apart from a
// Profile, which is a worker's recipe.
type Bot struct {
	// ID never changes; Name is shown and may change (issue #466).
	ID       string
	Name     string
	Provider string
	Model    string
	Effort   string
	Persona  string
	// Memory is the Bot's entries, one per line.
	Memory string
	// Skills are the skills the Bot gets beside those given to everyone;
	// SkillsOff the ones given to everyone it does not get (issue #21).
	Skills    []string
	SkillsOff []string
	// Connections are the Connections the Bot may use and Workers the
	// profiles it may give work to (issue #169, part 3; botaccess.go).
	Connections AllowList
	Workers     AllowList
	// ToolDisplay is what its chats show of its tool calls; empty is ToolsOn.
	ToolDisplay ToolDisplay
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func botFromRow(r store.Bot) Bot {
	return Bot{
		ID: r.ID, Name: r.Name, Provider: r.Provider.String, Model: r.Model.String, Effort: r.Effort.String,
		Persona: r.Persona, Memory: r.Memory, Skills: splitNames(r.Skills), SkillsOff: splitNames(r.SkillsOff),
		Connections: allowListOf(r.Connections), Workers: allowListOf(r.Workers), ToolDisplay: ToolDisplay(r.ToolDisplay),
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
	}
}

// Main says the Bot is the main one.
func (b Bot) Main() bool {
	return b.ID == DefaultBotID
}

// PutBot makes a Bot, when it has no id, or changes the model, persona, skills and what it may use of the Bot of
// its id; its name and memory stay. A new Bot gets an id that never changes and the name given.
func (s *Service) PutBot(ctx context.Context, b Bot) (Bot, error) {
	return s.putBot(ctx, "", b)
}

// putBot is PutBot; with a chat, the change is the main Bot's from that chat and is recorded with what undoes it
// (setupchanges.go).
func (s *Service) putBot(ctx context.Context, chat string, b Bot) (Bot, error) {
	b.Persona = strings.TrimSpace(b.Persona)

	if utf8.RuneCountInString(b.Persona) > MaxPersona {
		return Bot{}, fmt.Errorf("%w: a persona is at most %d characters", ErrInvalid, MaxPersona)
	}

	if b.ToolDisplay == "" {
		b.ToolDisplay = ToolsOn
	}

	if !b.ToolDisplay.valid() {
		return Bot{}, fmt.Errorf("%w: a bot's tool display is off, on or full", ErrInvalid)
	}

	if b.ID == "" {
		name, err := botName(b.Name)
		if err != nil {
			return Bot{}, err
		}

		b.ID, b.Name = s.newID("bot"), name
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := changeFromChat(ctx, q, chat); err != nil {
			return err
		}

		stored, found, err := botIfAny(ctx, q, b.ID)
		if err != nil {
			return err
		}

		if !found {
			return s.makeBot(ctx, q, now, chat, b)
		}

		b.Name = stored.Name

		if err := checkBot(ctx, q, &b, &stored); err != nil {
			return err
		}

		if err := updateBot(ctx, q, now, b); err != nil {
			return err
		}

		if chat == "" || botRevision(stored) == botRevision(b) {
			return nil
		}

		return s.recordChange(ctx, q, now, chat, changeBot, b.ID, actionChanged, settingsOfBot(stored),
			botRevision(b))
	}); err != nil {
		return Bot{}, fmt.Errorf("saving bot %s: %w", b.Name, err)
	}

	return s.GetBot(ctx, b.ID)
}

// makeBot stores a new Bot; one that names an id that is not there is not made.
func (s *Service) makeBot(ctx context.Context, q *store.Queries, now int64, chat string, b Bot) error {
	if b.Name == "" {
		return fmt.Errorf("%w: bot %s", ErrNotFound, b.ID)
	}

	if err := checkBot(ctx, q, &b, nil); err != nil {
		return err
	}

	if err := insertBot(ctx, q, now, b); err != nil {
		return err
	}

	if chat == "" {
		return nil
	}

	return s.recordChange(ctx, q, now, chat, changeBot, b.ID, actionMade, nil, botRevision(b))
}

// botName checks a Bot's name as a node's is checked (RenameNode): trimmed, 1 to 64 characters, any letters.
func botName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxBotName {
		return "", fmt.Errorf("%w: a bot's name is 1 to %d characters", ErrInvalid, maxBotName)
	}

	return name, nil
}

// RenameBot gives a Bot another name (issue #466). The name of another Bot is refused; its id, chats, Routines,
// Channels and memory stay.
func (s *Service) RenameBot(ctx context.Context, id, name string) (Bot, error) {
	return s.renameBot(ctx, "", id, name)
}

// RenameBotFromChat is RenameBot by the main Bot from its chat, recorded with what undoes it.
func (s *Service) RenameBotFromChat(ctx context.Context, chat, id, name string) (Bot, error) {
	if chat == "" {
		return Bot{}, fmt.Errorf("%w: a bot's change is made from a chat", ErrInvalid)
	}

	return s.renameBot(ctx, chat, id, name)
}

func (s *Service) renameBot(ctx context.Context, chat, id, name string) (Bot, error) {
	name, err := botName(name)
	if err != nil {
		return Bot{}, err
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := changeFromChat(ctx, q, chat); err != nil {
			return err
		}

		row, err := q.GetBot(ctx, id)
		if err != nil {
			return notFound(err, "bot", id)
		}

		if row.Name == name {
			return nil
		}

		if err := renameBotRow(ctx, q, now, id, name); err != nil {
			return err
		}

		if chat == "" {
			return nil
		}

		had := botFromRow(row)

		return s.recordChange(ctx, q, now, chat, changeBot, id, actionChanged, settingsOfBot(had), botRevision(had))
	})
	if err != nil {
		return Bot{}, fmt.Errorf("renaming bot %s: %w", id, err)
	}

	return s.GetBot(ctx, id)
}

// renameBotRow names a Bot; the name of another is a conflict, as a node's is (RenameNode).
func renameBotRow(ctx context.Context, q *store.Queries, now int64, id, name string) error {
	n, err := q.RenameBot(ctx, store.RenameBotParams{Name: name, Now: now, ID: id})
	if err != nil {
		return fmt.Errorf("%w: another bot is named %q: %w", ErrConflict, name, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: bot %s", ErrNotFound, id)
	}

	return nil
}

// checkBot checks a Bot's model, skills and lists before it is stored, and
// sets the provider its model is found at.
func checkBot(ctx context.Context, q *store.Queries, b *Bot, had *Bot) error {
	provider, err := checkModel(ctx, q, botRuntime, b.Provider, b.Model, b.Effort)
	if err != nil {
		return err
	}

	b.Provider = provider

	if err := checkSkills(ctx, q, b.Skills, b.SkillsOff); err != nil {
		return err
	}

	return checkAllowLists(ctx, q, *b, had)
}

func insertBot(ctx context.Context, q *store.Queries, now int64, b Bot) error {
	if b.ToolDisplay == "" {
		b.ToolDisplay = ToolsOn
	}

	err := q.InsertBot(ctx, store.InsertBotParams{
		ID: b.ID, Name: b.Name, Provider: nullString(b.Provider), Model: nullString(b.Model),
		Effort: nullString(b.Effort), Persona: b.Persona, Skills: strings.Join(b.Skills, ","),
		SkillsOff: strings.Join(b.SkillsOff, ","), Connections: b.Connections.stored(), Workers: b.Workers.stored(),
		ToolDisplay: string(b.ToolDisplay), Now: now,
	})
	if err != nil {
		return fmt.Errorf("%w: another bot is named %q: %w", ErrConflict, b.Name, err)
	}

	return nil
}

func updateBot(ctx context.Context, q *store.Queries, now int64, b Bot) error {
	if b.ToolDisplay == "" {
		b.ToolDisplay = ToolsOn
	}

	return q.UpdateBot(ctx, store.UpdateBotParams{
		Provider: nullString(b.Provider), Model: nullString(b.Model), Effort: nullString(b.Effort),
		Persona: b.Persona, Skills: strings.Join(b.Skills, ","), SkillsOff: strings.Join(b.SkillsOff, ","),
		Connections: b.Connections.stored(), Workers: b.Workers.stored(), ToolDisplay: string(b.ToolDisplay),
		Now: now, ID: b.ID,
	})
}

// restoreBot puts back what a Bot was before a change: made again with its id when it was removed, else its
// settings and its name.
func restoreBot(ctx context.Context, q *store.Queries, now int64, b Bot, removed bool) error {
	if removed {
		return insertBot(ctx, q, now, b)
	}

	if err := updateBot(ctx, q, now, b); err != nil {
		return err
	}

	row, err := q.GetBot(ctx, b.ID)
	if err != nil {
		return notFound(err, "bot", b.ID)
	}

	if row.Name == b.Name {
		return nil
	}

	return renameBotRow(ctx, q, now, b.ID, b.Name)
}

func (d ToolDisplay) valid() bool {
	return d == ToolsOff || d == ToolsOn || d == ToolsFull
}

// ChatToolDisplay is what the chat's Bot shows of its tool calls, read at each call: a change applies at once.
func (s *Service) ChatToolDisplay(ctx context.Context, chatID string) (ToolDisplay, error) {
	d, err := s.read().ToolDisplayOfChat(ctx, chatID)
	if err != nil {
		return "", notFound(err, "chat", chatID)
	}

	return ToolDisplay(d), nil
}

// GetBot returns the Bot of an id.
func (s *Service) GetBot(ctx context.Context, id string) (Bot, error) {
	row, err := s.read().GetBot(ctx, id)
	if err != nil {
		return Bot{}, notFound(err, "bot", id)
	}

	return botFromRow(row), nil
}

// ListBots returns the Bots, the oldest first.
func (s *Service) ListBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.read().ListBots(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing bots: %w", err)
	}

	out := make([]Bot, len(rows))
	for i, r := range rows {
		out[i] = botFromRow(r)
	}

	return out, nil
}

// DeleteBot removes a Bot nothing depends on: not the main one, and not one
// with chats or a memory, which would be left without their Bot, nor one a
// Lark Channel talks to.
func (s *Service) DeleteBot(ctx context.Context, id string) error {
	return s.deleteBot(ctx, "", id)
}

// deleteBot is DeleteBot; with a chat, the removal is the main Bot's from
// that chat and is recorded with what undoes it. The main Bot never removes
// itself, as OpenMausBot's Chief is never the target of its own removal
// (server/index.ts L444-450): it is always there.
func (s *Service) deleteBot(ctx context.Context, chat, id string) error {
	if id == DefaultBotID {
		return fmt.Errorf("%w: the main bot is always there", ErrConflict)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := changeFromChat(ctx, q, chat); err != nil {
			return err
		}

		row, err := q.GetBot(ctx, id)
		if err != nil {
			return notFound(err, "bot", id)
		}

		if err := botRemovable(ctx, q, row); err != nil {
			return err
		}

		if err := q.DeleteBot(ctx, id); err != nil {
			return err
		}

		if chat == "" {
			return nil
		}

		return s.recordChange(ctx, q, now, chat, changeBot, id, actionRemoved, settingsOfBot(botFromRow(row)), "")
	})
	if err != nil {
		return fmt.Errorf("removing bot %s: %w", id, err)
	}

	return nil
}

// ForceDeleteBot removes a Bot with its chats and its memory (issue #330; owner, 2026-11-13): the owner's way out
// when DeleteBot is refused only for those. Each chat is deleted as DeleteChat deletes one (issue #484): its
// messages, setup changes and artifacts go, as Onyx's hard delete takes a chat's messages and then the chat
// (backend/onyx/db/chat.py, delete_chat_session). Runs, Routines, their runs and approvals made in those chats stay
// and lose the chat; the chats' gateway keys are revoked and kept, so the gateway's usage records stay whole. It is
// still refused for the main Bot, and for one with Routines or Lark
// Channels: those are live and are removed or moved first.
func (s *Service) ForceDeleteBot(ctx context.Context, id string) error {
	if id == DefaultBotID {
		return fmt.Errorf("%w: the main bot is always there", ErrConflict)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetBot(ctx, id)
		if err != nil {
			return notFound(err, "bot", id)
		}

		if err := botLive(ctx, q, row); err != nil {
			return err
		}

		chats, err := q.ListChatIDsOfBot(ctx, id)
		if err != nil {
			return err
		}

		// Each chat goes as DeleteChat deletes one (issue #484), its artifacts with it.
		for _, chat := range chats {
			if err := deleteChatTx(ctx, q, chat, now); err != nil {
				return fmt.Errorf("deleting chat %s: %w", chat, err)
			}
		}

		return q.DeleteBot(ctx, id)
	})
	if err != nil {
		return fmt.Errorf("removing bot %s with its chats: %w", id, err)
	}

	return nil
}

// botRemovable refuses to remove a Bot something depends on.
func botRemovable(ctx context.Context, q *store.Queries, row store.Bot) error {
	name := row.Name

	if err := botLive(ctx, q, row); err != nil {
		return err
	}

	chats, err := q.CountChatsOfBot(ctx, row.ID)
	if err != nil {
		return err
	}

	switch {
	case chats > 0:
		return fmt.Errorf("%w: bot %s has %d chats", ErrConflict, name, chats)
	case row.Memory != "":
		return fmt.Errorf("%w: bot %s has a memory; empty it first", ErrConflict, name)
	}

	return nil
}

// botLive refuses to remove a Bot that Routines or Lark Channels use, even with its chats.
func botLive(ctx context.Context, q *store.Queries, row store.Bot) error {
	name := row.Name

	channels, err := channelstore.CountChannelsOfBotTx(ctx, q, row.ID)
	if err != nil {
		return err
	}

	routines, err := routinestore.CountRoutinesOfBotTx(ctx, q, row.ID)
	if err != nil {
		return err
	}

	switch {
	case routines > 0:
		return fmt.Errorf("%w: bot %s has %d routines; remove them first", ErrConflict, name, routines)
	case channels > 0:
		return fmt.Errorf("%w: %d Lark channels talk to bot %s; give them another bot or remove them first",
			ErrConflict, channels, name)
	}

	return nil
}

// MemoryOp is one change of a Bot's memory, made by the memories' rules
// (memorystore.Apply).
type MemoryOp = memorystore.Op

// Kinds of a MemoryOp.
const (
	MemoryAdd     = memorystore.Add
	MemoryReplace = memorystore.Replace
	MemoryRemove  = memorystore.Remove
)

// ChangeBotMemory applies one change to a Bot's memory and returns the Bot.
// It follows Hermes's memory tool (tools/memory_tool_store.py): the memory is
// read again inside the write, so two chats do not undo each other; a change
// that would pass the limit is refused, so the Bot shortens or removes
// entries itself; nothing is cut silently.
func (s *Service) ChangeBotMemory(ctx context.Context, id string, op MemoryOp) (Bot, error) {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetBot(ctx, id)
		if err != nil {
			return notFound(err, "bot", id)
		}

		memory, err := memorystore.Apply(row.Memory, op, MaxMemory)
		if err != nil {
			return err
		}

		return q.SetBotMemory(ctx, store.SetBotMemoryParams{ID: id, Memory: memory, Now: now})
	}); err != nil {
		return Bot{}, fmt.Errorf("changing the memory of bot %s: %w", id, err)
	}

	return s.GetBot(ctx, id)
}

// SetBotMemory replaces a Bot's whole memory with the owner's text: he edits
// it by hand, as the owner of an OpenClaw, Hermes or OpenMausBot agent edits
// its memory file. Each line is an entry; empty lines and an entry given
// twice are dropped. The limit holds here too, since the whole memory is in
// pi's prompt.
func (s *Service) SetBotMemory(ctx context.Context, id, text string) (Bot, error) {
	memory, err := memorystore.FromOwner(text, MaxMemory)
	if err != nil {
		return Bot{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetBot(ctx, id); err != nil {
			return notFound(err, "bot", id)
		}

		return q.SetBotMemory(ctx, store.SetBotMemoryParams{ID: id, Memory: memory, Now: now})
	}); err != nil {
		return Bot{}, fmt.Errorf("setting the memory of bot %s: %w", id, err)
	}

	return s.GetBot(ctx, id)
}
