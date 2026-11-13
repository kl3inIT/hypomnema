package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// What a Bot may use (issue #169, part 3): the Connections it may use and the
// worker profiles it may give work to. Each is a list that is either absent,
// every one, or present, only those, after OpenMausBot's connector scopes
// (shared/connector-scopes.ts L1-4: "Absent means every connected app;
// present, an app not listed is off") and OpenClaw's subagents.allowAgents
// (src/agents/subagents/spawn/subagent-target-policy.ts L146-200). A
// Connection the owner did not switch on for chats stays off for every Bot:
// a list narrows what the owner allowed, it never adds.

// AllowList is a Bot's list of what it may use.
type AllowList struct {
	// Limited says the list holds; without it the Bot may use every one.
	Limited bool `json:"limited"`
	// Names are what it may use while Limited: "integration:label" for a
	// Connection, a profile's name for a worker.
	Names []string `json:"names,omitempty"`
}

// Allows says whether the list lets a Bot use name.
func (l AllowList) Allows(name string) bool {
	return !l.Limited || slices.Contains(l.Names, name)
}

func allowListOf(stored sql.NullString) AllowList {
	if !stored.Valid {
		return AllowList{}
	}

	return AllowList{Limited: true, Names: splitNames(stored.String)}
}

func (l AllowList) stored() sql.NullString {
	if !l.Limited {
		return sql.NullString{}
	}

	return sql.NullString{String: strings.Join(l.Names, ","), Valid: true}
}

// MayUse says whether the Bot may use a Connection the owner switched on for
// chats.
func (b Bot) MayUse(c Connection) bool {
	return c.Chats && b.Connections.Allows(Use(c.Integration, c.Label))
}

// MayGive refuses a Task for a profile the Bot may not give work to, naming
// the ones it may, as OpenClaw's spawn refusal does ("allowed: a, b" or
// "none").
func (b Bot) MayGive(profile string) error {
	if profile == "" {
		profile = DefaultProfile
	}

	if b.Workers.Allows(profile) {
		return nil
	}

	allowed := "none"
	if len(b.Workers.Names) > 0 {
		allowed = strings.Join(b.Workers.Names, ", ")
	}

	return fmt.Errorf("%w: bot %s may not give work to profile %s (allowed: %s)", ErrForbidden, b.Name, profile,
		allowed)
}

// checkAllowLists checks a Bot's lists: each name once, in its form, and a
// name the Bot did not have before must exist. A name it kept may have gone
// since, as OpenMausBot keeps a scope of an app no longer connected.
func checkAllowLists(ctx context.Context, q *store.Queries, b Bot, had *Bot) error {
	var before Bot
	if had != nil {
		before = *had
	}

	if err := checkNames(b.Connections, before.Connections, func(use string) error {
		integration, label, ok := SplitUse(use)
		if !ok {
			return fmt.Errorf("%w: %q: a connection is named integration:label, such as google:work", ErrInvalid, use)
		}

		_, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		return err
	}); err != nil {
		return fmt.Errorf("connections: %w", err)
	}

	if err := checkNames(b.Workers, before.Workers, func(profile string) error {
		if _, err := q.GetProfile(ctx, profile); err != nil {
			return notFound(err, "profile", profile)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("workers: %w", err)
	}

	return nil
}

func checkNames(l, before AllowList, exists func(string) error) error {
	seen := map[string]bool{}

	for _, name := range l.Names {
		if seen[name] {
			return fmt.Errorf("%w: %q is named twice", ErrInvalid, name)
		}

		seen[name] = true

		if slices.Contains(before.Names, name) {
			continue
		}

		if err := exists(name); err != nil {
			return err
		}
	}

	return nil
}

// noWiderThan refuses a change of the Bot coordinator that would let a Bot
// use more than it may: a Bot it changes may not get a Connection or a worker
// it did not have, a Bot it makes no more than the Bot coordinator has, and
// it does not change its own lists. Widening stays with the owner. After
// OpenMausBot (server/inherited-apps.ts L18-31: a new bot never gets more
// than its creator) and OpenClaw (docs/gateway/permission-modes.md L94-97: an
// agent cannot raise its own rights).
func noWiderThan(ctx context.Context, q *store.Queries, b Bot, had *Bot) error {
	if had != nil && b.Name == DefaultBot {
		if !sameList(b.Connections, had.Connections) || !sameList(b.Workers, had.Workers) {
			return fmt.Errorf("%w: the bot %s does not change its own connections or workers; the owner does",
				ErrForbidden, DefaultBot)
		}

		return nil
	}

	bound := had
	if bound == nil {
		creator, found, err := botIfAny(ctx, q, DefaultBot)
		if err != nil {
			return err
		}

		if found {
			bound = &creator
		}
	}

	if bound == nil {
		return nil
	}

	if widens(b.Connections, bound.Connections) || widens(b.Workers, bound.Workers) {
		return fmt.Errorf("%w: only the owner gives bot %s a connection or a worker it did not have", ErrForbidden,
			b.Name)
	}

	return nil
}

// widens says whether a list lets a Bot use something the bound does not.
func widens(l, bound AllowList) bool {
	if !bound.Limited {
		return false
	}

	if !l.Limited {
		return true
	}

	return slices.ContainsFunc(l.Names, func(n string) bool { return !slices.Contains(bound.Names, n) })
}

func sameList(a, b AllowList) bool {
	return a.Limited == b.Limited && slices.Equal(slices.Sorted(slices.Values(a.Names)),
		slices.Sorted(slices.Values(b.Names)))
}

// botIfAny reads a Bot inside a transaction; found is false when there is
// none of that name.
func botIfAny(ctx context.Context, q *store.Queries, name string) (b Bot, found bool, err error) {
	row, err := q.GetBot(ctx, name)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Bot{}, false, nil
	case err != nil:
		return Bot{}, false, notFound(err, "bot", name)
	}

	return botFromRow(row), true, nil
}
