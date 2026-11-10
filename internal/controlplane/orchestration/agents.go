package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Agents using the owner's accounts (issue #103, step 3).
//
// A Connection is used by an agent in one of two ways, and never by default:
//
//   - a Run is given Connections when it is created ("google:work"); its
//     workers may use those and no other. A coordinator may give a Run only
//     what it may use itself.
//   - the coordinator may use, in any chat, the Connections the owner
//     switched on for chats.
//
// A worker's credential to ask for a token is its Dispatch's own key (an
// agent session): made as the offer is sent and revoked when the Dispatch
// settles, as its model gateway key is (issue #107) and as the conventions'
// vault Session per Dispatch. The coordinator's is its own token.

// MaxRunUses bounds the Connections one Run may be given.
const MaxRunUses = 16

// Use names a Connection as it is given to a Run: "google:work".
func Use(integration, label string) string { return integration + ":" + label }

// SplitUse reads "integration:label".
func SplitUse(use string) (integration, label string, ok bool) {
	integration, label, ok = strings.Cut(use, ":")

	return integration, label, ok && integration != "" && ValidConnectionLabel(label)
}

// runUses checks the Connections a new Run asks for and returns them in a
// fixed order. Each must exist; one a coordinator asks for must be switched
// on for chats, so that it passes on only what it has.
func runUses(ctx context.Context, q *store.Queries, asked []string, byCoordinator bool) ([]string, error) {
	if len(asked) > MaxRunUses {
		return nil, fmt.Errorf("%w: a run uses at most %d accounts", ErrInvalid, MaxRunUses)
	}

	out := make([]string, 0, len(asked))

	for _, use := range asked {
		integration, label, ok := SplitUse(strings.TrimSpace(use))
		if !ok {
			return nil, fmt.Errorf("%w: %q: an account is named integration:label, such as google:work", ErrInvalid, use)
		}

		row, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		if err != nil {
			return nil, fmt.Errorf("reading connection %s:%s: %w", integration, label, err)
		}

		if byCoordinator && row.Chats == 0 {
			return nil, fmt.Errorf("%w: the coordinator may not use %s:%s, so it cannot give it to a run; the owner "+
				"allows it for chats in Settings, Integrations", ErrInvalid, integration, label)
		}

		out = append(out, Use(integration, label))
	}

	slices.Sort(out)

	return slices.Compact(out), nil
}

func joinUses(uses []string) sql.NullString { return nullString(strings.Join(uses, ",")) }

func splitUses(stored sql.NullString) []string {
	if stored.String == "" {
		return nil
	}

	return strings.Split(stored.String, ",")
}

// toolsOf names the tools the Integrations of a Run's Connections are called
// with: what a node must have to take the Run's work.
func toolsOf(uses []string) []string {
	var out []string

	for _, use := range uses {
		integration, _, _ := SplitUse(use)
		if def, ok := agenttool.Of(integration); ok && !slices.Contains(out, def.Name) {
			out = append(out, def.Name)
		}
	}

	return out
}

// SetConnectionChats says whether the coordinator may use a Connection in
// chats.
func (s *Service) SetConnectionChats(ctx context.Context, integration, label string, allowed bool) (Connection, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		chats := int64(0)
		if allowed {
			chats = 1
		}

		n, err := q.SetConnectionChats(ctx, store.SetConnectionChatsParams{
			Chats: chats, Now: now, Integration: integration, Label: label,
		})
		if err != nil {
			return fmt.Errorf("changing connection %s:%s: %w", integration, label, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		return nil
	})
	if err != nil {
		return Connection{}, err
	}

	return s.ConnectionOf(ctx, integration, label)
}

// AgentSession is a Dispatch's key to the agent API, without the key.
type AgentSession struct {
	ID         string
	DispatchID string
	// Uses are the Connections the key may ask a token for.
	Uses []string
	// ByCoordinator says the Dispatch's Run was created by a coordinator,
	// which could give it only what the owner allowed for chats. Such a key
	// works for a Connection only while that is still allowed: what the owner
	// takes back from the coordinator, its Runs lose too. A key whose Dispatch
	// cannot be found counts as one.
	ByCoordinator bool
}

// AddAgentSession makes a Dispatch's key and returns it once; only its hash
// is kept.
func (s *Service) AddAgentSession(ctx context.Context, dispatchID string, uses []string) (string, error) {
	if dispatchID == "" || len(uses) == 0 {
		return "", fmt.Errorf("%w: an agent session needs its dispatch and what it may use", ErrInvalid)
	}

	secret, err := bearertoken.Generate(bearertoken.Session)
	if err != nil {
		return "", err
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertAgentSession(ctx, store.InsertAgentSessionParams{
			ID: s.newID("ags"), KeyHash: bearertoken.Hash(secret), DispatchID: dispatchID,
			Uses: strings.Join(uses, ","), Now: now,
		})
	})
	if err != nil {
		return "", fmt.Errorf("keeping the agent session of dispatch %s: %w", dispatchID, err)
	}

	return secret, nil
}

// AgentSessionOf returns the session a key belongs to; ErrNotFound when the
// key is not one, or was revoked.
func (s *Service) AgentSessionOf(ctx context.Context, secret string) (AgentSession, error) {
	if kind, err := bearertoken.Parse(secret); err != nil || kind != bearertoken.Session {
		return AgentSession{}, fmt.Errorf("%w: not an agent session's key", ErrNotFound)
	}

	row, err := s.read().GetAgentSessionByHash(ctx, bearertoken.Hash(secret))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, fmt.Errorf("%w: the agent session ended", ErrNotFound)
	}

	if err != nil {
		return AgentSession{}, fmt.Errorf("reading an agent session: %w", err)
	}

	byCoordinator, err := s.runByCoordinator(ctx, row.DispatchID)
	if err != nil {
		return AgentSession{}, err
	}

	return AgentSession{
		ID: row.ID, DispatchID: row.DispatchID, Uses: strings.Split(row.Uses, ","), ByCoordinator: byCoordinator,
	}, nil
}

// runByCoordinator reports whether a Dispatch's Run was created by a
// coordinator. It is read when asked, not kept with the key. A Dispatch or a
// Run that is gone answers yes: the stricter of the two.
func (s *Service) runByCoordinator(ctx context.Context, dispatchID string) (bool, error) {
	q := s.read()

	dispatch, err := q.GetDispatch(ctx, dispatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading dispatch %s: %w", dispatchID, err)
	}

	task, err := q.GetTask(ctx, dispatch.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading task %s: %w", dispatch.TaskID, err)
	}

	run, err := q.GetRun(ctx, task.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("reading run %s: %w", task.RunID, err)
	}

	return run.CoordinatorTokenID.Valid, nil
}

// RevokeAgentSessions stops every key of a Dispatch from working; none is no
// error.
func (s *Service) RevokeAgentSessions(ctx context.Context, dispatchID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.RevokeAgentSessionsOfDispatch(ctx, store.RevokeAgentSessionsOfDispatchParams{
			Now: sql.NullInt64{Int64: now, Valid: true}, DispatchID: dispatchID,
		})
		if err != nil {
			return fmt.Errorf("revoking the agent sessions of dispatch %s: %w", dispatchID, err)
		}

		return nil
	})
}

// DispatchesWithAgentSessions lists the Dispatches that have a key that
// still works.
func (s *Service) DispatchesWithAgentSessions(ctx context.Context) ([]string, error) {
	ids, err := s.read().ListDispatchesWithAgentSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing dispatches with agent sessions: %w", err)
	}

	return ids, nil
}

// ChatConnections returns the Connections of an Integration the coordinator
// may use in chats, oldest first.
func (s *Service) ChatConnections(ctx context.Context, integration string) ([]Connection, error) {
	all, err := s.ConnectionsOf(ctx, integration)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(c Connection) bool { return !c.Chats }), nil
}
