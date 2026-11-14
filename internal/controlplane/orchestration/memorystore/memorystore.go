// Package memorystore keeps the memories of agents and the one about the
// owner (issue #21, docs/research/2026-11-13-agent-memory.md), a domain store
// of the orchestration database reached through the root package's
// Memories(), as Omnara's storage facade reaches its stores (issue #404). It
// imports the generated store and never the root package; it only reads the
// tables of other domains (profiles, Dispatches, Tasks), through the shared
// queries.
//
// An agent's memory is shared by all its workers, on every machine, as
// OpenMausBot keeps one MEMORY.md per bot and OpenClaw one per agent; the one
// about the owner is given to every worker and Bot, as OpenMausBot's About me
// and Hermes's and OpenClaw's USER.md. Both are entries one per line, changed
// by the rules of Apply and FromOwner, which a Bot's memory follows too.
package memorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Kinds of memory in the memories table.
const (
	ofAgent = "agent"
	ofOwner = "owner"
)

// Limits of a memory. MaxMemory is Hermes's (tools/memory_tool.py,
// memory_char_limit), an agent's and a Bot's: the whole memory is in the
// prompt. MaxOwnerMemory is Hermes's USER.md (user_char_limit).
const (
	MaxMemory      = 2200
	MaxOwnerMemory = 1375
)

// Memory is an agent's memory or the one about the owner.
type Memory struct {
	// Agent is the agent's name; empty for the memory about the owner.
	Agent string
	// Text is the entries, one per line.
	Text string
	// Limit is the most characters Text holds.
	Limit int
	// UpdatedAt is zero for a memory never written.
	UpdatedAt time.Time
}

// Op is one change of a memory.
type Op struct {
	// Kind is add, replace or remove.
	Kind string
	// Text is the entry to add, or what replaces the entry found.
	Text string
	// Match is part of the one entry to replace or remove.
	Match string
}

// Kinds of an Op.
const (
	Add     = "add"
	Replace = "replace"
	Remove  = "remove"
)

// Store keeps the memories of agents and the one about the owner.
type Store struct {
	db    *sql.DB
	clock platform.Clock
	// signal wakes the orchestration's event readers after each commit.
	signal func()
}

// New returns the Store over the orchestration database. signal is the
// root's, called after each commit as for every other write.
func New(db *sql.DB, clock platform.Clock, signal func()) *Store {
	return &Store{db: db, clock: clock, signal: signal}
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

func (s *Store) read() *store.Queries {
	return store.New(s.db)
}

// AgentMemory returns an agent's memory; an agent that has kept nothing has
// an empty one.
func (s *Store) AgentMemory(ctx context.Context, agent string) (Memory, error) {
	q := s.read()

	if _, err := q.GetProfile(ctx, agent); err != nil {
		return Memory{}, storeutil.NotFound(err, "agent", agent)
	}

	return readMemory(ctx, q, ofAgent, agent)
}

// OwnerMemory returns the memory about the owner.
func (s *Store) OwnerMemory(ctx context.Context) (Memory, error) {
	return readMemory(ctx, s.read(), ofOwner, "")
}

// AgentMemoryTx reads an agent's memory with the caller's queries, inside
// its transaction or read; it does not check that the agent exists.
func AgentMemoryTx(ctx context.Context, q *store.Queries, agent string) (Memory, error) {
	return readMemory(ctx, q, ofAgent, agent)
}

// OwnerMemoryTx reads the memory about the owner with the caller's queries.
func OwnerMemoryTx(ctx context.Context, q *store.Queries) (Memory, error) {
	return readMemory(ctx, q, ofOwner, "")
}

// ChangeAgentMemory applies one change to an agent's memory.
func (s *Store) ChangeAgentMemory(ctx context.Context, agent string, op Op) (Memory, error) {
	return s.changeMemory(ctx, ofAgent, agent, op)
}

// ChangeOwnerMemory applies one change to the memory about the owner.
func (s *Store) ChangeOwnerMemory(ctx context.Context, op Op) (Memory, error) {
	return s.changeMemory(ctx, ofOwner, "", op)
}

// ChangeDispatchMemory applies one change a worker makes: to the memory of
// the agent its Dispatch runs as, or, with owner, to the one about the owner.
// A Dispatch that took over for an agent out of quota runs as its fallback,
// whose memory it is.
func (s *Store) ChangeDispatchMemory(ctx context.Context, dispatchID string, owner bool, op Op) (Memory, error) {
	if owner {
		return s.ChangeOwnerMemory(ctx, op)
	}

	agent, err := dispatchAgent(ctx, s.read(), dispatchID)
	if err != nil {
		return Memory{}, err
	}

	return s.ChangeAgentMemory(ctx, agent, op)
}

// SetAgentMemory replaces an agent's whole memory with the owner's text.
func (s *Store) SetAgentMemory(ctx context.Context, agent, text string) (Memory, error) {
	return s.setMemory(ctx, ofAgent, agent, text)
}

// SetOwnerMemory replaces the whole memory about the owner with his text.
func (s *Store) SetOwnerMemory(ctx context.Context, text string) (Memory, error) {
	return s.setMemory(ctx, ofOwner, "", text)
}

func (s *Store) changeMemory(ctx context.Context, kind, name string, op Op) (Memory, error) {
	limit := memoryLimit(kind)

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		// The memory is read again inside the write, so workers writing at
		// once do not undo each other (Hermes's lock, re-read and apply).
		m, err := readMemoryIn(ctx, q, kind, name)
		if err != nil {
			return err
		}

		text, err := Apply(m.Text, op, limit)
		if err != nil {
			return err
		}

		return q.SetMemory(ctx, store.SetMemoryParams{Kind: kind, Name: name, Memory: text, Now: now})
	}); err != nil {
		return Memory{}, fmt.Errorf("changing %s: %w", memoryNoun(kind, name), err)
	}

	return readMemory(ctx, s.read(), kind, name)
}

func (s *Store) setMemory(ctx context.Context, kind, name, text string) (Memory, error) {
	memory, err := FromOwner(text, memoryLimit(kind))
	if err != nil {
		return Memory{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := readMemoryIn(ctx, q, kind, name); err != nil {
			return err
		}

		return q.SetMemory(ctx, store.SetMemoryParams{Kind: kind, Name: name, Memory: memory, Now: now})
	}); err != nil {
		return Memory{}, fmt.Errorf("setting %s: %w", memoryNoun(kind, name), err)
	}

	return readMemory(ctx, s.read(), kind, name)
}

// readMemoryIn reads a memory inside a write, refusing an agent that does
// not exist.
func readMemoryIn(ctx context.Context, q *store.Queries, kind, name string) (Memory, error) {
	if kind == ofAgent {
		if _, err := q.GetProfile(ctx, name); err != nil {
			return Memory{}, storeutil.NotFound(err, "agent", name)
		}
	}

	return readMemory(ctx, q, kind, name)
}

func readMemory(ctx context.Context, q *store.Queries, kind, name string) (Memory, error) {
	m := Memory{Agent: name, Limit: memoryLimit(kind)}

	row, err := q.GetMemory(ctx, store.GetMemoryParams{Kind: kind, Name: name})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return m, nil
	case err != nil:
		return Memory{}, fmt.Errorf("reading %s: %w", memoryNoun(kind, name), err)
	}

	m.Text, m.UpdatedAt = row.Memory, time.UnixMilli(row.UpdatedAt).UTC()

	return m, nil
}

// dispatchAgent is the agent a Dispatch runs as: its Profile version's, or
// its Task's for a Dispatch from before versions.
func dispatchAgent(ctx context.Context, q *store.Queries, dispatchID string) (string, error) {
	d, err := q.GetDispatch(ctx, dispatchID)
	if err != nil {
		return "", storeutil.NotFound(err, "dispatch", dispatchID)
	}

	if d.ProfileVersionID.Valid {
		v, err := q.GetProfileVersion(ctx, d.ProfileVersionID.String)
		if err != nil {
			return "", storeutil.NotFound(err, "profile version", d.ProfileVersionID.String)
		}

		return v.Profile, nil
	}

	t, err := q.GetTask(ctx, d.TaskID)
	if err != nil {
		return "", storeutil.NotFound(err, "task", d.TaskID)
	}

	return t.Profile, nil
}

func memoryLimit(kind string) int {
	if kind == ofOwner {
		return MaxOwnerMemory
	}

	return MaxMemory
}

func memoryNoun(kind, name string) string {
	if kind == ofOwner {
		return "the memory about the owner"
	}

	return "the memory of agent " + name
}

// Apply is a memory with one change made: an entry is one line; a change
// that would pass the limit is refused, so the agent shortens or removes
// entries itself and nothing is cut silently (Hermes).
func Apply(memory string, op Op, limit int) (string, error) {
	text := strings.Join(strings.Fields(op.Text), " ")

	entries, err := changeEntries(memoryEntries(memory), op.Kind, text, strings.TrimSpace(op.Match))
	if err != nil {
		return "", err
	}

	out := strings.Join(entries, "\n")
	if n := utf8.RuneCountInString(out); n > limit {
		return "", fmt.Errorf("%w: the memory would be %d characters, over its limit of %d; replace or remove "+
			"entries first", storeerr.ErrInvalid, n, limit)
	}

	return out, nil
}

// FromOwner is the owner's text as a memory: each line an entry, empty lines
// and an entry given twice dropped, within the limit.
func FromOwner(text string, limit int) (string, error) {
	var entries []string

	for line := range strings.SplitSeq(text, "\n") {
		entry := strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- ")), " ")
		if entry != "" && !slices.Contains(entries, entry) {
			entries = append(entries, entry)
		}
	}

	memory := strings.Join(entries, "\n")
	if n := utf8.RuneCountInString(memory); n > limit {
		return "", fmt.Errorf("%w: the memory would be %d characters, over its limit of %d", storeerr.ErrInvalid,
			n, limit)
	}

	return memory, nil
}

func memoryEntries(memory string) []string {
	if memory == "" {
		return nil
	}

	return strings.Split(memory, "\n")
}

func changeEntries(entries []string, kind, text, match string) ([]string, error) {
	if kind == Add {
		switch {
		case text == "":
			return nil, fmt.Errorf("%w: an entry needs text", storeerr.ErrInvalid)
		case slices.Contains(entries, text):
			return entries, nil
		}

		return append(entries, text), nil
	}

	if kind != Replace && kind != Remove {
		return nil, fmt.Errorf("%w: unknown change %q; known: add, replace, remove", storeerr.ErrInvalid, kind)
	}

	if match == "" {
		return nil, fmt.Errorf("%w: say part of the entry to %s", storeerr.ErrInvalid, kind)
	}

	found := -1

	for i, e := range entries {
		if !strings.Contains(e, match) {
			continue
		}

		if found >= 0 {
			return nil, fmt.Errorf("%w: %q is in more than one entry; say more of the one you mean",
				storeerr.ErrInvalid, match)
		}

		found = i
	}

	switch {
	case found < 0:
		return nil, fmt.Errorf("%w: no entry has %q", storeerr.ErrInvalid, match)
	case kind == Remove:
		return slices.Delete(entries, found, found+1), nil
	case text == "":
		return nil, fmt.Errorf("%w: say what replaces the entry", storeerr.ErrInvalid)
	}

	entries[found] = text

	return entries, nil
}
