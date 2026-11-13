package orchestration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// DefaultBot is the Bot that is always there: a chat that names no other
// talks with it. It was the Profile "coordinator" until issue #169.
const DefaultBot = "coordinator"

// botRuntime is what every Bot runs: pi, started by the control plane.
const botRuntime = "pi"

// Limits of a Bot's texts. The memory's is Hermes's (tools/memory_tool.py,
// memory_char_limit): the whole memory is in pi's prompt in every chat.
const (
	MaxPersona = 16000
	MaxMemory  = 2200
)

// Bot is a long-lived agent of the control plane (issue #169): its model,
// what it is (the persona, which only the owner writes) and what it keeps
// across chats (the memory, which it writes itself). It is apart from a
// Profile, which is a worker's recipe.
type Bot struct {
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
	CreatedAt time.Time
	UpdatedAt time.Time
}

func botFromRow(r store.Bot) Bot {
	return Bot{
		Name: r.Name, Provider: r.Provider.String, Model: r.Model.String, Effort: r.Effort.String,
		Persona: r.Persona, Memory: r.Memory, Skills: splitNames(r.Skills), SkillsOff: splitNames(r.SkillsOff),
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
	}
}

// PutBot makes a Bot or changes its model and persona; its memory stays.
func (s *Service) PutBot(ctx context.Context, b Bot) (Bot, error) {
	b.Persona = strings.TrimSpace(b.Persona)

	switch {
	case !namePattern.MatchString(b.Name):
		return Bot{}, fmt.Errorf("%w: a bot's name is lowercase letters, digits and dashes, starting with a letter",
			ErrInvalid)
	case utf8.RuneCountInString(b.Persona) > MaxPersona:
		return Bot{}, fmt.Errorf("%w: a persona is at most %d characters", ErrInvalid, MaxPersona)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		provider, err := checkModel(ctx, q, botRuntime, b.Provider, b.Model, b.Effort)
		if err != nil {
			return err
		}

		if err := checkSkills(ctx, q, b.Skills, b.SkillsOff); err != nil {
			return err
		}

		return q.UpsertBot(ctx, store.UpsertBotParams{
			Name: b.Name, Provider: nullString(provider), Model: nullString(b.Model), Effort: nullString(b.Effort),
			Persona: b.Persona, Skills: strings.Join(b.Skills, ","), SkillsOff: strings.Join(b.SkillsOff, ","),
			Now: now,
		})
	}); err != nil {
		return Bot{}, fmt.Errorf("saving bot %s: %w", b.Name, err)
	}

	return s.GetBot(ctx, b.Name)
}

// GetBot returns one Bot.
func (s *Service) GetBot(ctx context.Context, name string) (Bot, error) {
	row, err := s.read().GetBot(ctx, name)
	if err != nil {
		return Bot{}, notFound(err, "bot", name)
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

// DeleteBot removes a Bot nothing depends on: not the first one, and not one
// with chats or a memory, which would be left without their Bot, nor one a
// Lark Channel talks to.
func (s *Service) DeleteBot(ctx context.Context, name string) error {
	if name == DefaultBot {
		return fmt.Errorf("%w: the bot %s is always there", ErrConflict, DefaultBot)
	}

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetBot(ctx, name)
		if err != nil {
			return notFound(err, "bot", name)
		}

		chats, err := q.CountChatsOfBot(ctx, name)
		if err != nil {
			return err
		}

		channels, err := q.CountLarkChannelsOfBot(ctx, name)
		if err != nil {
			return err
		}

		routines, err := q.CountRoutinesOfBot(ctx, name)
		if err != nil {
			return err
		}

		switch {
		case routines > 0:
			return fmt.Errorf("%w: bot %s has %d routines; remove them first", ErrConflict, name, routines)
		case channels > 0:
			return fmt.Errorf("%w: %d Lark channels talk to bot %s; give them another bot or remove them first",
				ErrConflict, channels, name)
		case chats > 0:
			return fmt.Errorf("%w: bot %s has %d chats", ErrConflict, name, chats)
		case row.Memory != "":
			return fmt.Errorf("%w: bot %s has a memory; empty it first", ErrConflict, name)
		}

		return q.DeleteBot(ctx, name)
	})
	if err != nil {
		return fmt.Errorf("removing bot %s: %w", name, err)
	}

	return nil
}

// MemoryOp is one change of a Bot's memory.
type MemoryOp struct {
	// Kind is add, replace or remove.
	Kind string
	// Text is the entry to add, or what replaces the entry found.
	Text string
	// Match is part of the one entry to replace or remove.
	Match string
}

// Kinds of a MemoryOp.
const (
	MemoryAdd     = "add"
	MemoryReplace = "replace"
	MemoryRemove  = "remove"
)

// ChangeBotMemory applies one change to a Bot's memory and returns the Bot.
// It follows Hermes's memory tool (tools/memory_tool_store.py): the memory is
// read again inside the write, so two chats do not undo each other; a change
// that would pass the limit is refused, so the Bot shortens or removes
// entries itself; nothing is cut silently.
func (s *Service) ChangeBotMemory(ctx context.Context, name string, op MemoryOp) (Bot, error) {
	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetBot(ctx, name)
		if err != nil {
			return notFound(err, "bot", name)
		}

		memory, err := appliedMemory(row.Memory, op, MaxMemory)
		if err != nil {
			return err
		}

		return q.SetBotMemory(ctx, store.SetBotMemoryParams{Name: name, Memory: memory, Now: now})
	}); err != nil {
		return Bot{}, fmt.Errorf("changing the memory of bot %s: %w", name, err)
	}

	return s.GetBot(ctx, name)
}

// SetBotMemory replaces a Bot's whole memory with the owner's text: he edits
// it by hand, as the owner of an OpenClaw, Hermes or OpenMausBot agent edits
// its memory file. Each line is an entry; empty lines and an entry given
// twice are dropped. The limit holds here too, since the whole memory is in
// pi's prompt.
func (s *Service) SetBotMemory(ctx context.Context, name, text string) (Bot, error) {
	memory, err := ownersMemory(text, MaxMemory)
	if err != nil {
		return Bot{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetBot(ctx, name); err != nil {
			return notFound(err, "bot", name)
		}

		return q.SetBotMemory(ctx, store.SetBotMemoryParams{Name: name, Memory: memory, Now: now})
	}); err != nil {
		return Bot{}, fmt.Errorf("setting the memory of bot %s: %w", name, err)
	}

	return s.GetBot(ctx, name)
}

func memoryEntries(memory string) []string {
	if memory == "" {
		return nil
	}

	return strings.Split(memory, "\n")
}

func changeMemory(entries []string, kind, text, match string) ([]string, error) {
	if kind == MemoryAdd {
		switch {
		case text == "":
			return nil, fmt.Errorf("%w: an entry needs text", ErrInvalid)
		case slices.Contains(entries, text):
			return entries, nil
		}

		return append(entries, text), nil
	}

	if kind != MemoryReplace && kind != MemoryRemove {
		return nil, fmt.Errorf("%w: unknown change %q; known: add, replace, remove", ErrInvalid, kind)
	}

	if match == "" {
		return nil, fmt.Errorf("%w: say part of the entry to %s", ErrInvalid, kind)
	}

	found := -1

	for i, e := range entries {
		if !strings.Contains(e, match) {
			continue
		}

		if found >= 0 {
			return nil, fmt.Errorf("%w: %q is in more than one entry; say more of the one you mean", ErrInvalid, match)
		}

		found = i
	}

	switch {
	case found < 0:
		return nil, fmt.Errorf("%w: no entry has %q", ErrInvalid, match)
	case kind == MemoryRemove:
		return slices.Delete(entries, found, found+1), nil
	case text == "":
		return nil, fmt.Errorf("%w: say what replaces the entry", ErrInvalid)
	}

	entries[found] = text

	return entries, nil
}
