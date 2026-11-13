package orchestration

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
)

// Memories of agents and the one about the owner (issue #21,
// docs/research/2026-11-13-agent-memory.md). An agent's is shared by all its
// workers, on every machine, as OpenMausBot keeps one MEMORY.md per bot and
// OpenClaw one per agent; the one about the owner is given to every worker and
// Bot, as OpenMausBot's About me and Hermes's and OpenClaw's USER.md. Both are
// entries one per line and change as a Bot's memory does.

// Kinds of memory in the memories table.
const (
	memoryOfAgent = "agent"
	memoryOfOwner = "owner"
)

// MaxOwnerMemory is the most characters of the memory about the owner:
// Hermes's USER.md (tools/memory_tool.py, user_char_limit). An agent's holds
// MaxMemory, as a Bot's.
const MaxOwnerMemory = 1375

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

// AgentMemory returns an agent's memory; an agent that has kept nothing has
// an empty one.
func (s *Service) AgentMemory(ctx context.Context, agent string) (Memory, error) {
	q := s.read()

	if _, err := q.GetProfile(ctx, agent); err != nil {
		return Memory{}, notFound(err, "agent", agent)
	}

	return readMemory(ctx, q, memoryOfAgent, agent)
}

// OwnerMemory returns the memory about the owner.
func (s *Service) OwnerMemory(ctx context.Context) (Memory, error) {
	return readMemory(ctx, s.read(), memoryOfOwner, "")
}

// ChangeAgentMemory applies one change to an agent's memory.
func (s *Service) ChangeAgentMemory(ctx context.Context, agent string, op MemoryOp) (Memory, error) {
	return s.changeMemory(ctx, memoryOfAgent, agent, op)
}

// ChangeOwnerMemory applies one change to the memory about the owner.
func (s *Service) ChangeOwnerMemory(ctx context.Context, op MemoryOp) (Memory, error) {
	return s.changeMemory(ctx, memoryOfOwner, "", op)
}

// ChangeDispatchMemory applies one change a worker makes: to the memory of
// the agent its Dispatch runs as, or, with owner, to the one about the owner.
// A Dispatch that took over for an agent out of quota runs as its fallback,
// whose memory it is.
func (s *Service) ChangeDispatchMemory(ctx context.Context, dispatchID string, owner bool, op MemoryOp,
) (Memory, error) {
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
func (s *Service) SetAgentMemory(ctx context.Context, agent, text string) (Memory, error) {
	return s.setMemory(ctx, memoryOfAgent, agent, text)
}

// SetOwnerMemory replaces the whole memory about the owner with his text.
func (s *Service) SetOwnerMemory(ctx context.Context, text string) (Memory, error) {
	return s.setMemory(ctx, memoryOfOwner, "", text)
}

func (s *Service) changeMemory(ctx context.Context, kind, name string, op MemoryOp) (Memory, error) {
	limit := memoryLimit(kind)

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		// The memory is read again inside the write, so workers writing at
		// once do not undo each other (Hermes's lock, re-read and apply).
		m, err := readMemoryIn(ctx, q, kind, name)
		if err != nil {
			return err
		}

		text, err := appliedMemory(m.Text, op, limit)
		if err != nil {
			return err
		}

		return q.SetMemory(ctx, store.SetMemoryParams{Kind: kind, Name: name, Memory: text, Now: now})
	}); err != nil {
		return Memory{}, fmt.Errorf("changing %s: %w", memoryNoun(kind, name), err)
	}

	return readMemory(ctx, s.read(), kind, name)
}

func (s *Service) setMemory(ctx context.Context, kind, name, text string) (Memory, error) {
	memory, err := ownersMemory(text, memoryLimit(kind))
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
	if kind == memoryOfAgent {
		if _, err := q.GetProfile(ctx, name); err != nil {
			return Memory{}, notFound(err, "agent", name)
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
		return "", notFound(err, "dispatch", dispatchID)
	}

	if d.ProfileVersionID.Valid {
		v, err := q.GetProfileVersion(ctx, d.ProfileVersionID.String)
		if err != nil {
			return "", notFound(err, "profile version", d.ProfileVersionID.String)
		}

		return v.Profile, nil
	}

	t, err := q.GetTask(ctx, d.TaskID)
	if err != nil {
		return "", notFound(err, "task", d.TaskID)
	}

	return t.Profile, nil
}

func memoryLimit(kind string) int {
	if kind == memoryOfOwner {
		return MaxOwnerMemory
	}

	return MaxMemory
}

func memoryNoun(kind, name string) string {
	if kind == memoryOfOwner {
		return "the memory about the owner"
	}

	return "the memory of agent " + name
}

// appliedMemory is a memory with one change made: an entry is one line; a
// change that would pass the limit is refused, so the agent shortens or
// removes entries itself and nothing is cut silently (Hermes).
func appliedMemory(memory string, op MemoryOp, limit int) (string, error) {
	text := strings.Join(strings.Fields(op.Text), " ")

	entries, err := changeMemory(memoryEntries(memory), op.Kind, text, strings.TrimSpace(op.Match))
	if err != nil {
		return "", err
	}

	out := strings.Join(entries, "\n")
	if n := utf8.RuneCountInString(out); n > limit {
		return "", fmt.Errorf("%w: the memory would be %d characters, over its limit of %d; replace or remove "+
			"entries first", ErrInvalid, n, limit)
	}

	return out, nil
}

// ownersMemory is the owner's text as a memory: each line an entry, empty
// lines and an entry given twice dropped, within the limit.
func ownersMemory(text string, limit int) (string, error) {
	var entries []string

	for line := range strings.SplitSeq(text, "\n") {
		entry := strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "- ")), " ")
		if entry != "" && !slices.Contains(entries, entry) {
			entries = append(entries, entry)
		}
	}

	memory := strings.Join(entries, "\n")
	if n := utf8.RuneCountInString(memory); n > limit {
		return "", fmt.Errorf("%w: the memory would be %d characters, over its limit of %d", ErrInvalid, n, limit)
	}

	return memory, nil
}
