package orchestration

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// DefaultBotID is the id of the main Bot, the one that is always there: a chat that names no other talks with it,
// and it alone changes the Bots and profiles from its chat. It was the Bot "coordinator", known by its name, until
// issue #466; its id never changes whatever it is called, as Hermes's default profile keeps the id "default"
// (hermes_cli/profiles.py, rename_profile) and OpenClaw names its system agent by id (systemAgent.agentId).
const DefaultBotID = "bot_mainaaaaaaaaaaaaaaaaaaaaaa"

// maxBotName bounds a Bot's name, as a node's (maxNodeName).
const maxBotName = 64

// botRuntime is what every Bot runs: pi, started by the control plane.
const botRuntime = profilestore.BotRuntime

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
	ID        string
	Name      string
	Provider  string
	Model     string
	Fallbacks []string
	Effort    string
	Persona   string
	// Memory is the Bot's entries, one per line.
	Memory string
	// Skills are the skills the Bot gets beside those given to everyone;
	// SkillsOff the ones given to everyone it does not get (issue #21).
	Skills    []string
	SkillsOff []string
	Preset    string
	Tools     []string
	ToolsOff  []string
	// Connections are the Connections the Bot may use and Workers the
	// profiles it may give work to (issue #169, part 3; botaccess.go).
	Connections AllowList
	Workers     AllowList
	// Scopes narrow what it may reach of a Connection it may use, keyed
	// integration:label (issue #562; botaccess.go); nil narrows none.
	Scopes map[string]ConnectionScope
	// ToolDisplay is what its chats show of its tool calls; empty is ToolsOn.
	ToolDisplay ToolDisplay
	// OwnerAccounts is what it may do with the owner's own accounts (issue #1023, the owner's choice of 2026-11-21
	// on #1037): OwnerAccountsUse, as every Bot until then, or OwnerAccountsRead, by which every write and send
	// through his Lark sign-in, his Google, his Zalo and his WhatsApp is refused at once, in every chat and Routine
	// of the Bot, whatever his approvals say; reads stay. Only the owner changes it.
	OwnerAccounts string
	// GuaranteedDispatches is the Bot's guaranteed share of the room, in
	// Dispatches across every node (issue #731; called its limit, max_dispatches,
	// until 2026-11-17): a first claim on room as it frees, not a cap; 0 is
	// BotGuaranteeDefault (Guarantee).
	GuaranteedDispatches int
	// Position is the Bot's place in the order the owner sets, 0 first (issue #470), and Department the id of the
	// department the sidebar lists it in, empty for none (issue #471; departments.go). Neither is a setting: PutBot
	// keeps them as they are.
	Position   int
	Department string
	// MainChat is the chat a click on the Bot opens and the web and the CLI write to (issue #471); empty until the
	// first message from either makes it. Its other chats stay as they are.
	MainChat  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// What a Bot may do with the owner's own accounts (Bot.OwnerAccounts).
const (
	OwnerAccountsUse  = "use"
	OwnerAccountsRead = "read"
)

// ReadsOwnerAccountsOnly says the Bot only reads the owner's own accounts (Bot.OwnerAccounts).
func (b Bot) ReadsOwnerAccountsOnly() bool { return b.OwnerAccounts == OwnerAccountsRead }

func botFromRow(r store.Bot) Bot {
	return Bot{
		OwnerAccounts: r.OwnerAccounts,
		ID:            r.ID, Name: r.Name, Provider: r.Provider.String, Model: r.Model.String, Effort: r.Effort.String,
		Fallbacks: splitNames(r.Fallbacks),
		Persona:   r.Persona, Memory: r.Memory, Skills: splitNames(r.Skills), SkillsOff: splitNames(r.SkillsOff),
		Preset: r.Preset, Tools: splitNames(r.Tools), ToolsOff: splitNames(r.ToolsOff),
		Connections: allowListOf(r.Connections), Workers: allowListOf(r.Workers), ToolDisplay: ToolDisplay(r.ToolDisplay),
		GuaranteedDispatches: int(r.GuaranteedDispatches.Int64),
		Position:             int(r.Position), Department: r.Department.String, Scopes: scopesOf(r.ConnectionScopes),
		MainChat:  r.MainChat.String,
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
	}
}

// Guarantee is the Bot's guaranteed share in Dispatches: its own number, else BotGuaranteeDefault.
func (b Bot) Guarantee() int {
	if b.GuaranteedDispatches > 0 {
		return b.GuaranteedDispatches
	}

	return BotGuaranteeDefault
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

	if b.GuaranteedDispatches < 0 || b.GuaranteedDispatches > MaxDispatchesLimit {
		return Bot{}, fmt.Errorf("%w: a bot is guaranteed 1 to %d workers", ErrInvalid, MaxDispatchesLimit)
	}

	if b.OwnerAccounts == "" {
		b.OwnerAccounts = OwnerAccountsUse
	}

	if b.OwnerAccounts != OwnerAccountsUse && b.OwnerAccounts != OwnerAccountsRead {
		return Bot{}, fmt.Errorf("%w: a bot uses or reads the owner's accounts", ErrInvalid)
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

		// Only the owner changes what a Bot may do with his own accounts, like a Routine's access (routinestore).
		if b.OwnerAccounts != stored.OwnerAccounts {
			if chat != "" {
				return fmt.Errorf("%w: only the owner changes what a bot may do with his own accounts", ErrForbidden)
			}

			if err := q.SetBotOwnerAccounts(ctx, store.SetBotOwnerAccountsParams{
				OwnerAccounts: b.OwnerAccounts, Now: now, ID: b.ID,
			}); err != nil {
				return fmt.Errorf("setting what bot %s may do with the owner's accounts: %w", b.ID, err)
			}
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
	provider, err := profilestore.CheckModelTx(ctx, q, botRuntime, b.Provider, b.Model, b.Effort)
	if err != nil {
		return err
	}

	b.Provider = provider
	b.Preset = agenttool.DefaultPreset(b.Preset)

	if err := checkBotFallbacks(ctx, q, b); err != nil {
		return err
	}

	if err := profilestore.CheckSkillsTx(ctx, q, b.Skills, b.SkillsOff); err != nil {
		return err
	}

	if err := profilestore.CheckTools(b.Preset, b.Tools, b.ToolsOff); err != nil {
		return err
	}

	return checkAllowLists(ctx, q, *b, had)
}

func checkBotFallbacks(ctx context.Context, q *store.Queries, b *Bot) error {
	if len(b.Fallbacks) == 0 {
		return nil
	}
	if len(b.Fallbacks) > profilestore.MaxFallbacks {
		return fmt.Errorf("%w: a bot can have at most %d fallbacks", ErrInvalid, profilestore.MaxFallbacks)
	}
	if b.Model == "" {
		return fmt.Errorf("%w: choose a primary model before adding fallbacks", ErrInvalid)
	}

	primary, err := gatewayBotModel(ctx, q, b.Provider, b.Model)
	if err != nil {
		return err
	}

	seen := map[string]bool{primary: true}
	for _, ref := range b.Fallbacks {
		provider, model, _ := strings.Cut(ref, "/")
		if provider == "" || model == "" || strings.ContainsAny(ref, ",\r\n") || strings.TrimSpace(ref) != ref {
			return fmt.Errorf("%w: fallback %q must be provider/model", ErrInvalid, ref)
		}
		if _, err := profilestore.CheckModelTx(ctx, q, botRuntime, provider, model, ""); err != nil {
			return err
		}
		name, err := gatewayBotModel(ctx, q, provider, model)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("%w: fallback %q repeats a model in the chain", ErrInvalid, ref)
		}
		seen[name] = true
	}
	return nil
}

func gatewayBotModel(ctx context.Context, q *store.Queries, provider, model string) (string, error) {
	row, err := q.GetProvider(ctx, provider)
	if err != nil {
		return "", notFound(err, "provider", provider)
	}
	if !profilestore.ThroughGateway(row.Kind) {
		return "", fmt.Errorf("%w: bot fallbacks require gateway models; %s/%s is not one", ErrInvalid, provider, model)
	}
	return profilestore.GatewayModelName(row.Kind, provider, model), nil
}

func (s *Service) ChatFallbacks(ctx context.Context, chatID, requested string) ([]string, error) {
	chat, err := s.GetChat(ctx, chatID)
	if err != nil || chat.Choice.Model != "" {
		return nil, err
	}
	bot, err := s.GetBot(ctx, chat.Bot)
	if err != nil || len(bot.Fallbacks) == 0 {
		return nil, err
	}
	primary, err := s.PiModel(ctx, ChatModel{Provider: bot.Provider, Model: bot.Model})
	if err != nil || primary.Provider != profilestore.PiProvider || primary.Model != requested {
		return nil, err
	}

	names := make([]string, 0, len(bot.Fallbacks))
	for _, ref := range bot.Fallbacks {
		provider, model, _ := strings.Cut(ref, "/")
		name, err := gatewayBotModel(ctx, s.read(), provider, model)
		if err == nil {
			names = append(names, name)
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalid) {
			return nil, err
		}
	}
	return names, nil
}

func insertBot(ctx context.Context, q *store.Queries, now int64, b Bot) error {
	if b.ToolDisplay == "" {
		b.ToolDisplay = ToolsOn
	}

	err := q.InsertBot(ctx, store.InsertBotParams{
		ID: b.ID, Name: b.Name, Provider: nullString(b.Provider), Model: nullString(b.Model),
		Fallbacks: strings.Join(b.Fallbacks, ","),
		Effort:    nullString(b.Effort), Persona: b.Persona, Skills: strings.Join(b.Skills, ","),
		SkillsOff: strings.Join(b.SkillsOff, ","), Tools: strings.Join(b.Tools, ","),
		Preset: agenttool.DefaultPreset(b.Preset), ToolsOff: strings.Join(b.ToolsOff, ","),
		Connections: b.Connections.stored(), Workers: b.Workers.stored(),
		ToolDisplay: string(b.ToolDisplay), ConnectionScopes: storedScopes(b.Scopes),
		GuaranteedDispatches: storedLimit(b.GuaranteedDispatches), Now: now,
		OwnerAccounts: cmp.Or(b.OwnerAccounts, OwnerAccountsUse),
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
		Fallbacks: strings.Join(b.Fallbacks, ","),
		Persona:   b.Persona, Skills: strings.Join(b.Skills, ","), SkillsOff: strings.Join(b.SkillsOff, ","),
		Preset: agenttool.DefaultPreset(b.Preset), Tools: strings.Join(b.Tools, ","), ToolsOff: strings.Join(b.ToolsOff, ","),
		Connections: b.Connections.stored(), Workers: b.Workers.stored(), ToolDisplay: string(b.ToolDisplay),
		ConnectionScopes: storedScopes(b.Scopes), GuaranteedDispatches: storedLimit(b.GuaranteedDispatches), Now: now,
		ID: b.ID,
	})
}

// storedLimit is a Bot's limit as kept: NULL for the default.
func storedLimit(n int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(n), Valid: n > 0}
}

// restoreBot puts back what a Bot was before a change: made again with its id when it was removed, else its
// settings and its name.
func restoreBot(ctx context.Context, q *store.Queries, now int64, b Bot, removed bool) error {
	if removed {
		// Put back, it is no longer named as removed (issue #1139).
		if err := q.ForgetRemovedBotName(ctx, b.ID); err != nil {
			return err
		}

		return insertBot(ctx, q, now, b)
	}

	if err := updateBot(ctx, q, now, b); err != nil {
		return err
	}

	// An undo puts back that a Bot read the owner's accounts only; it never makes one use them again, which only the
	// owner does (issue #1023).
	if b.OwnerAccounts == OwnerAccountsRead {
		if err := q.SetBotOwnerAccounts(ctx, store.SetBotOwnerAccountsParams{
			OwnerAccounts: OwnerAccountsRead, Now: now, ID: b.ID,
		}); err != nil {
			return fmt.Errorf("putting back what bot %s may do with the owner's accounts: %w", b.ID, err)
		}
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

	err := s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
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

		if err := s.removeBotRow(ctx, tx, q, row, now); err != nil {
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

	s.endBotTokens(func(b botToken) bool { return b.bot == id })

	return nil
}

// ForceDeleteBot removes a Bot with its chats and its memory (issue #330; owner, 2026-11-13): the owner's way out
// when DeleteBot is refused only for those. Each chat is deleted as DeleteChat deletes one (issue #484): its
// messages, setup changes and artifacts go, as Onyx's hard delete takes a chat's messages and then the chat
// (backend/onyx/db/chat.py, delete_chat_session). Ended Runs, Routines, their runs and approvals made in those chats stay
// and lose the chat; the chats' gateway keys are revoked and kept, so the gateway's usage records stay whole. It is
// still refused for the main Bot, and for one with open Runs, Routines or Lark
// Channels: those are live and are removed or moved first. The chats are
// returned: their files, outside the database, are the caller's to remove
// after the commit, as DeleteChat's are (issue #503).
func (s *Service) ForceDeleteBot(ctx context.Context, id string) ([]Chat, error) {
	if id == DefaultBotID {
		return nil, fmt.Errorf("%w: the main bot is always there", ErrConflict)
	}

	var deleted []Chat

	err := s.writeTx(ctx, func(tx *sql.Tx, q *store.Queries, now int64) error {
		deleted = nil

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
			row, err := q.GetChat(ctx, chat)
			if err != nil {
				return fmt.Errorf("reading chat %s: %w", chat, err)
			}

			if err := s.deleteChatTx(ctx, tx, q, chat, now); err != nil {
				return fmt.Errorf("deleting chat %s: %w", chat, err)
			}

			deleted = append(deleted, chatFromRow(row))
		}

		// Its memory's notes (issue #589) go with it, as its MEMORY.md does with its row.
		if err := memorystore.DeleteBotNotes(ctx, q, id); err != nil {
			return err
		}

		return s.removeBotRow(ctx, tx, q, row, now)
	})
	if err != nil {
		return nil, fmt.Errorf("removing bot %s with its chats: %w", id, err)
	}

	s.endBotTokens(func(b botToken) bool { return b.bot == id })

	return deleted, nil
}

// removeBotRow removes a Bot's row; the Bots after it move up a place, so the order stays 0 to n-1 (issue #470), as
// OpenMausBot's fleet is one list a removed bot leaves. Its gateway keys end in the same transaction (issue #996);
// its pi's tokens, kept in memory, end after the commit (endBotTokens). Its Inbox items not closed become the main
// Bot's (issue #1124).
func (s *Service) removeBotRow(ctx context.Context, tx *sql.Tx, q *store.Queries, row store.Bot, now int64) error {
	if err := s.inbox.TakeOverTx(ctx, q, now, row.ID, row.Name, DefaultBotID); err != nil {
		return err
	}

	if s.gatewayKeys != nil {
		if err := s.gatewayKeys.RevokeBotKeysTx(ctx, tx, row.ID, now); err != nil {
			return err
		}
	}

	if err := deleteBotRow(ctx, q, row); err != nil {
		return err
	}

	return q.CloseBotPosition(ctx, row.Position)
}

// deleteBotRow deletes a Bot's row and keeps its name with its id (issue #1139): its id stays in what it did, the
// Inbox's items and the gateway's usage, which name it by that name, as Slack names a deactivated member.
func deleteBotRow(ctx context.Context, q *store.Queries, row store.Bot) error {
	if err := q.KeepRemovedBotName(ctx, store.KeepRemovedBotNameParams{ID: row.ID, Name: row.Name}); err != nil {
		return fmt.Errorf("keeping the name of bot %s: %w", row.ID, err)
	}

	return q.DeleteBot(ctx, row.ID)
}

// RemovedBot is a Bot removed since, with the name it had when it was removed (issue #1139).
type RemovedBot struct {
	ID   string
	Name string
}

// ListRemovedBots returns the Bots removed since, so that what they did is named by their name, never by their id.
func (s *Service) ListRemovedBots(ctx context.Context) ([]RemovedBot, error) {
	rows, err := s.read().ListRemovedBots(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing removed bots: %w", err)
	}

	out := make([]RemovedBot, len(rows))
	for i, r := range rows {
		out[i] = RemovedBot{ID: r.ID, Name: r.Name}
	}

	return out, nil
}

// ReorderBots moves Bots in the one order the sidebar lists them in (issue #470) and returns every Bot's id in the
// new order. It is OpenMausBot's reorder (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// shared/bot-order.ts, reorderInPlace and reorderStable): the Bots named take, in the order given, the places they
// already hold together; every other Bot keeps its place. An empty list, a repeated or unknown id, and the main Bot,
// which is always first, are refused and change nothing.
func (s *Service) ReorderBots(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: name the bots to move", ErrInvalid)
	}

	rank := make(map[string]int, len(ids))
	for i, id := range ids {
		if id == DefaultBotID {
			return nil, fmt.Errorf("%w: the main bot is always first", ErrInvalid)
		}

		if _, twice := rank[id]; twice {
			return nil, fmt.Errorf("%w: bot %s is named twice", ErrInvalid, id)
		}

		rank[id] = i
	}

	var order []string

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		had, err := q.ListBotIDsInOrder(ctx)
		if err != nil {
			return err
		}

		order, err = reorderStable(had, rank)
		if err != nil {
			return err
		}

		for i, id := range order {
			if err := q.SetBotPosition(ctx, store.SetBotPositionParams{Position: int64(i), ID: id}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reordering bots: %w", err)
	}

	return order, nil
}

// reorderStable is OpenMausBot's reorderStable over ids: the slots the ranked ids hold get them in their rank's
// order; every other id stays in its slot. An id ranked that the order does not hold is refused.
func reorderStable(order []string, rank map[string]int) ([]string, error) {
	var slots []int

	for i, id := range order {
		if _, ok := rank[id]; ok {
			slots = append(slots, i)
		}
	}

	if len(slots) != len(rank) {
		for id := range rank {
			if !slices.Contains(order, id) {
				return nil, fmt.Errorf("%w: unknown bot %s", ErrInvalid, id)
			}
		}
	}

	moved := make([]string, len(slots))
	for i, slot := range slots {
		moved[i] = order[slot]
	}

	slices.SortFunc(moved, func(a, b string) int { return rank[a] - rank[b] })

	out := slices.Clone(order)
	for i, slot := range slots {
		out[slot] = moved[i]
	}

	return out, nil
}

// ChatIDsOfBot returns the ids of a Bot's chats.
func (s *Service) ChatIDsOfBot(ctx context.Context, id string) ([]string, error) {
	ids, err := s.read().ListChatIDsOfBot(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing the chats of bot %s: %w", id, err)
	}

	return ids, nil
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

	notes, err := memorystore.BotHasNotes(ctx, q, row.ID)
	if err != nil {
		return err
	}

	switch {
	case chats > 0:
		return fmt.Errorf("%w: bot %s has %d chats", ErrConflict, name, chats)
	case row.Memory != "" || notes:
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
