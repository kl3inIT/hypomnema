package orchestration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Nodes and accounts (issue #19, PR 5). The owner adds a node, which mints a
// token shown once (Omnara docs/machines/connect.mdx); the node reports its
// CLIs at each connect (Orca local-agent-cli-presence.ts) and stays connected
// while it is heard from within NodeOfflineAfter. Accounts are managed here;
// their secret values belong to the vault.

// NodeStatus says whether a node takes new work.
type NodeStatus string

// Node statuses.
const (
	NodeActive  NodeStatus = "active"
	NodeDrained NodeStatus = "drained"
)

// RuntimeInfo is one CLI a node found.
type RuntimeInfo struct {
	Runtime string `json:"runtime"`
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Hello is what a node reports when it connects.
type Hello struct {
	WorktreeRoot string
	Runtimes     []RuntimeInfo
}

// Node is one machine that runs workers.
type Node struct {
	ID            string
	Name          string
	Status        NodeStatus
	MaxDispatches int
	WorktreeRoot  string
	Runtimes      []RuntimeInfo
	LastSeenAt    *time.Time
	// UnavailableAt is set when the node let Dispatches expire in a row.
	UnavailableAt *time.Time
	// Connected: heard from within NodeOfflineAfter.
	Connected bool
	// Active counts its Dispatches that have not settled.
	Active    int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AccountStatus says whether placement may use an account.
type AccountStatus string

// Account statuses.
const (
	AccountActive   AccountStatus = "active"
	AccountDisabled AccountStatus = "disabled"
)

// Account is one credential a runtime runs under. It holds no secret.
type Account struct {
	ID       string
	Runtime  string
	Label    string
	Status   AccountStatus
	Position int
	// CoolingUntil is set while a rate limit cools the account down.
	CoolingUntil *time.Time
}

// AddNode records a node and returns it with its token. The token is shown
// only here; only its hash is kept.
func (s *Service) AddNode(ctx context.Context, name string, maxDispatches int) (Node, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || maxDispatches < 0 {
		return Node{}, "", fmt.Errorf("%w: a node needs a name and a limit of zero (default) or more", ErrInvalid)
	}

	if maxDispatches == 0 {
		maxDispatches = NodeDispatchesDefault
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Node{}, "", fmt.Errorf("making a node token: %w", err)
	}

	token := "hyn_" + base64.RawURLEncoding.EncodeToString(raw)
	id := s.newID("nod")

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertNode(ctx, store.InsertNodeParams{
			ID: id, Name: name, TokenHash: tokenHash(token), MaxDispatches: int64(maxDispatches), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting node %q: %w", ErrConflict, name, err)
		}

		return nil
	})
	if err != nil {
		return Node{}, "", fmt.Errorf("adding a node: %w", err)
	}

	n, err := s.GetNode(ctx, id)

	return n, token, err
}

// AuthenticateNode returns the node a token belongs to.
func (s *Service) AuthenticateNode(ctx context.Context, token string) (Node, error) {
	row, err := s.read().GetNodeByTokenHash(ctx, tokenHash(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, fmt.Errorf("%w: no node has this token", ErrNotFound)
	}

	if err != nil {
		return Node{}, fmt.Errorf("authenticating a node: %w", err)
	}

	return s.GetNode(ctx, row.ID)
}

// NodeHello records what a node found when it connected. It also clears an
// unavailable mark: the node has restarted.
func (s *Service) NodeHello(ctx context.Context, nodeID string, h Hello) error {
	for _, r := range h.Runtimes {
		if r.Runtime == "" {
			return fmt.Errorf("%w: a runtime needs a name", ErrInvalid)
		}
	}

	runtimes, err := json.Marshal(nonNil(h.Runtimes))
	if err != nil {
		return fmt.Errorf("encoding runtimes: %w", err)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.NodeHello(ctx, store.NodeHelloParams{
			Now: now, WorktreeRoot: nullString(h.WorktreeRoot), Runtimes: string(runtimes), ID: nodeID,
		})

		return oneRow(n, err, "node", nodeID)
	})
}

// NodeHeartbeat records that the node is still there. Every node call about
// a Dispatch counts as one too.
func (s *Service) NodeHeartbeat(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetNode(ctx, nodeID); err != nil {
			return notFound(err, "node", nodeID)
		}

		return q.TouchNode(ctx, store.TouchNodeParams{Now: now, ID: nodeID})
	})
}

// DrainNode stops new work on a node; its running Dispatches go on.
func (s *Service) DrainNode(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetNodeStatus(ctx, store.SetNodeStatusParams{Now: now, Status: string(NodeDrained), ID: nodeID})

		return oneRow(n, err, "node", nodeID)
	})
}

// ResumeNode lets a node take new work again and clears its unavailable mark.
func (s *Service) ResumeNode(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.ResumeNode(ctx, store.ResumeNodeParams{Now: now, ID: nodeID})

		return oneRow(n, err, "node", nodeID)
	})
}

// GetNode returns one node, with whether it is connected and its load.
func (s *Service) GetNode(ctx context.Context, id string) (Node, error) {
	p, err := loadPlacement(ctx, s.read(), s.clock.Now().UnixMilli())
	if err != nil {
		return Node{}, err
	}

	n, ok := p.nodes[id]
	if !ok {
		return Node{}, fmt.Errorf("%w: node %s", ErrNotFound, id)
	}

	return p.nodeOut(n), nil
}

// ListNodes returns every node by name.
func (s *Service) ListNodes(ctx context.Context) ([]Node, error) {
	p, err := loadPlacement(ctx, s.read(), s.clock.Now().UnixMilli())
	if err != nil {
		return nil, err
	}

	out := make([]Node, 0, len(p.order))
	for _, n := range p.order {
		out = append(out, p.nodeOut(n))
	}

	return out, nil
}

// AddAccount records an account for a runtime, placed after the others.
func (s *Service) AddAccount(ctx context.Context, runtime, label string) (Account, error) {
	if runtime == "" || strings.TrimSpace(label) == "" {
		return Account{}, fmt.Errorf("%w: an account needs a runtime and a label", ErrInvalid)
	}

	id := s.newID("acc")

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertAccount(ctx, store.InsertAccountParams{
			ID: id, Runtime: runtime, Label: strings.TrimSpace(label), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting account %s/%s: %w", ErrConflict, runtime, label, err)
		}

		return nil
	})
	if err != nil {
		return Account{}, fmt.Errorf("adding an account: %w", err)
	}

	return s.getAccount(ctx, id)
}

// DisableAccount keeps placement off an account.
func (s *Service) DisableAccount(ctx context.Context, id string) error {
	return s.setAccountStatus(ctx, id, AccountDisabled)
}

// EnableAccount lets placement use an account again.
func (s *Service) EnableAccount(ctx context.Context, id string) error {
	return s.setAccountStatus(ctx, id, AccountActive)
}

func (s *Service) setAccountStatus(ctx context.Context, id string, status AccountStatus) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetAccountStatus(ctx, store.SetAccountStatusParams{Now: now, Status: string(status), ID: id})

		return oneRow(n, err, "account", id)
	})
}

// ListAccounts returns every account by runtime and position.
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	p, err := loadPlacement(ctx, s.read(), s.clock.Now().UnixMilli())
	if err != nil {
		return nil, err
	}

	out := make([]Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		out = append(out, p.accountOut(a))
	}

	return out, nil
}

func (s *Service) getAccount(ctx context.Context, id string) (Account, error) {
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		return Account{}, err
	}

	for _, a := range accounts {
		if a.ID == id {
			return a, nil
		}
	}

	return Account{}, fmt.Errorf("%w: account %s", ErrNotFound, id)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func oneRow(n int64, err error, kind, id string) error {
	if err != nil {
		return fmt.Errorf("updating %s %s: %w", kind, id, err)
	}

	if n != 1 {
		return fmt.Errorf("%w: %s %s", ErrNotFound, kind, id)
	}

	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
