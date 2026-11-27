package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
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

// ConnectionScope is what a Bot may reach of one Connection it may use (issue
// #562): of a Superset Connection, the dashboards it may read, by number.
// A Bot with no scope for a Connection reaches all the account does, as
// OpenMausBot's per-bot connector scopes (packages/phone-protocol/src/
// connector-scopes.ts L1-4: "Absent means every connected app"), and one
// with a scope that names no dashboard reads none.
type ConnectionScope struct {
	Dashboards []int64 `json:"dashboards"`
}

// scopedIntegration is the Integration whose Connections a Bot's scope may
// narrow: the dashboards of a Superset account.
const scopedIntegration = "superset"

// ScopeOf is the Bot's scope of a Connection, named integration:label;
// limited is false when it has none.
func (b Bot) ScopeOf(use string) (scope ConnectionScope, limited bool) {
	scope, limited = b.Scopes[use]

	return scope, limited
}

func scopesOf(stored sql.NullString) map[string]ConnectionScope {
	if !stored.Valid || stored.String == "" {
		return nil
	}

	var out map[string]ConnectionScope

	// The column is this package's own and written only by storedScopes.
	if err := json.Unmarshal([]byte(stored.String), &out); err != nil {
		panic(fmt.Sprintf("reading a bot's scopes: %v", err))
	}

	return out
}

func storedScopes(scopes map[string]ConnectionScope) sql.NullString {
	if len(scopes) == 0 {
		return sql.NullString{}
	}

	raw, err := json.Marshal(scopes)
	if err != nil {
		panic(fmt.Sprintf("encoding a bot's scopes: %v", err))
	}

	return sql.NullString{String: string(raw), Valid: true}
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
		profile = profilestore.DefaultProfile
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

	if err := checkScopes(ctx, q, b.Scopes, before.Scopes); err != nil {
		return fmt.Errorf("scopes: %w", err)
	}

	return nil
}

// checkScopes checks a Bot's scopes: each of a Superset Connection that
// exists, unless the Bot had it before, as checkNames keeps a name; each
// dashboard a number, once.
func checkScopes(ctx context.Context, q *store.Queries, scopes, before map[string]ConnectionScope) error {
	for use, scope := range scopes {
		integration, label, ok := SplitUse(use)
		if !ok || integration != scopedIntegration {
			return fmt.Errorf("%w: %q: a scope is of a superset connection, such as superset:prod", ErrInvalid, use)
		}

		seen := map[int64]bool{}

		for _, d := range scope.Dashboards {
			if d <= 0 || seen[d] {
				return fmt.Errorf("%w: %s: dashboard %d is not a dashboard's number, or is named twice", ErrInvalid,
					use, d)
			}

			seen[d] = true
		}

		if _, had := before[use]; had {
			continue
		}

		_, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		if err != nil {
			return fmt.Errorf("reading connection %s: %w", use, err)
		}
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
