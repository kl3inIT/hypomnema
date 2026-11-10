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
	// NodeDeleted: removed by the owner; kept only for the record.
	NodeDeleted NodeStatus = "deleted"
)

// nodeTokenTouchEvery is how often a token's last use is written (Omnara
// touches a machine token at most hourly).
const nodeTokenTouchEvery = time.Hour

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
	System       SystemInfo
}

// SystemInfo is what a node is: its daemon and its machine. A zero field was
// not reported.
type SystemInfo struct {
	DaemonVersion string
	OS            string
	Arch          string
	CPUs          int
	MemoryBytes   int64
	// DiskBytes is the size of the volume that holds the worktree root.
	DiskBytes int64
}

// SystemStats is what a node measured at one moment.
type SystemStats struct {
	// CPUPercent is the share of all CPUs in use since the measurement before, 0 to 100.
	CPUPercent      float64
	MemoryUsedBytes int64
	// DiskUsedBytes is of the volume that holds the worktree root.
	DiskUsedBytes int64
	// Load1 is the one-minute load average; zero where the system has none.
	Load1 float64
	At    time.Time
}

// Node is one machine that runs workers.
type Node struct {
	ID            string
	Name          string
	Status        NodeStatus
	MaxDispatches int
	WorktreeRoot  string
	Runtimes      []RuntimeInfo
	System        SystemInfo
	// Stats is the node's latest measurement; nil until it has sent one.
	Stats      *SystemStats
	LastSeenAt *time.Time
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

// NodeToken is one credential of a node. It holds no secret.
type NodeToken struct {
	ID         string
	NodeID     string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// firstNodeTokenName names the token a node is added with.
const firstNodeTokenName = "first"

// AddNode records a node and returns it with its first token. The token is
// shown only here; only its hash is kept.
func (s *Service) AddNode(ctx context.Context, name string, maxDispatches int) (Node, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || maxDispatches < 0 {
		return Node{}, "", fmt.Errorf("%w: a node needs a name and a limit of zero (default) or more", ErrInvalid)
	}

	if maxDispatches == 0 {
		maxDispatches = NodeDispatchesDefault
	}

	token, err := newNodeToken()
	if err != nil {
		return Node{}, "", err
	}

	id := s.newID("nod")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertNode(ctx, store.InsertNodeParams{
			ID: id, Name: name, MaxDispatches: int64(maxDispatches), Now: now,
		}); err != nil {
			return fmt.Errorf("%w: inserting node %q: %w", ErrConflict, name, err)
		}

		return q.InsertNodeToken(ctx, store.InsertNodeTokenParams{
			ID: s.newID("ntk"), NodeID: id, Name: firstNodeTokenName, TokenHash: tokenHash(token), Now: now,
		})
	})
	if err != nil {
		return Node{}, "", fmt.Errorf("adding a node: %w", err)
	}

	n, err := s.GetNode(ctx, id)

	return n, token, err
}

// RemoveNode takes a node out of the registry: its tokens stop working, it
// is no longer listed and its name is free again. Its row stays for the
// Dispatches that name it (Omnara deletes a machine the same way). A node
// that still runs Dispatches is refused: drain it and let them end first.
func (s *Service) RemoveNode(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		active, err := q.CountActiveDispatchesOfNode(ctx, store.CountActiveDispatchesOfNodeParams{
			NodeID: nodeID, Statuses: activeDispatchStatuses(),
		})
		if err != nil {
			return fmt.Errorf("counting the dispatches of node %s: %w", nodeID, err)
		}

		if active > 0 {
			return fmt.Errorf("%w: node %s still runs %d Dispatches; drain it and let them end", ErrConflict, nodeID,
				active)
		}

		n, err := q.DeleteNode(ctx, store.DeleteNodeParams{Now: now, ID: nodeID})
		if err := oneRow(n, err, "node", nodeID); err != nil {
			return err
		}

		if err := q.RevokeNodeTokens(ctx, store.RevokeNodeTokensParams{Now: validInt(now), NodeID: nodeID}); err != nil {
			return fmt.Errorf("revoking the tokens of node %s: %w", nodeID, err)
		}

		return nil
	})
}

// AddNodeToken gives a node one more token and returns it with its secret,
// which is shown only here.
func (s *Service) AddNodeToken(ctx context.Context, nodeID, name string) (NodeToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return NodeToken{}, "", fmt.Errorf("%w: a node token needs a name", ErrInvalid)
	}

	token, err := newNodeToken()
	if err != nil {
		return NodeToken{}, "", err
	}

	id := s.newID("ntk")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.GetNode(ctx, nodeID)
		if err != nil {
			return notFound(err, "node", nodeID)
		}

		if NodeStatus(n.Status) == NodeDeleted {
			return fmt.Errorf("%w: node %s", ErrNotFound, nodeID)
		}

		return q.InsertNodeToken(ctx, store.InsertNodeTokenParams{
			ID: id, NodeID: nodeID, Name: name, TokenHash: tokenHash(token), Now: now,
		})
	})
	if err != nil {
		return NodeToken{}, "", fmt.Errorf("adding a node token: %w", err)
	}

	row, err := s.read().GetNodeToken(ctx, id)
	if err != nil {
		return NodeToken{}, "", fmt.Errorf("reading node token %s: %w", id, err)
	}

	return nodeTokenOut(row), token, nil
}

// ListNodeTokens returns a node's tokens, oldest first, revoked ones included.
func (s *Service) ListNodeTokens(ctx context.Context, nodeID string) ([]NodeToken, error) {
	rows, err := s.read().ListNodeTokens(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("listing the tokens of node %s: %w", nodeID, err)
	}

	out := make([]NodeToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, nodeTokenOut(r))
	}

	return out, nil
}

// RevokeNodeToken makes a node token stop working. A node connected with it
// is refused at its next heartbeat.
func (s *Service) RevokeNodeToken(ctx context.Context, tokenID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.RevokeNodeToken(ctx, store.RevokeNodeTokenParams{Now: validInt(now), ID: tokenID})

		return oneRow(n, err, "live node token", tokenID)
	})
}

// AuthenticateNode returns the node a token belongs to.
func (s *Service) AuthenticateNode(ctx context.Context, token string) (Node, error) {
	n, _, err := s.AuthenticateNodeToken(ctx, token)

	return n, err
}

// AuthenticateNodeToken returns the node a token belongs to and the token's
// id, which CheckNodeToken takes while the node stays connected. A revoked
// token and a removed node's token are unknown.
func (s *Service) AuthenticateNodeToken(ctx context.Context, token string) (Node, string, error) {
	row, err := s.read().GetLiveNodeTokenByHash(ctx, tokenHash(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, "", fmt.Errorf("%w: no node has this token", ErrNotFound)
	}

	if err != nil {
		return Node{}, "", fmt.Errorf("authenticating a node: %w", err)
	}

	before := s.clock.Now().Add(-nodeTokenTouchEvery).UnixMilli()
	if !row.LastUsedAt.Valid || row.LastUsedAt.Int64 <= before {
		err := s.write(ctx, func(q *store.Queries, now int64) error {
			return q.TouchNodeToken(ctx, store.TouchNodeTokenParams{Now: now, ID: row.ID, Before: before})
		})
		if err != nil {
			return Node{}, "", fmt.Errorf("recording the use of node token %s: %w", row.ID, err)
		}
	}

	n, err := s.GetNode(ctx, row.NodeID)

	return n, row.ID, err
}

// CheckNodeToken says whether a token a node connected with still works:
// ErrNotFound once it is revoked or its node removed.
func (s *Service) CheckNodeToken(ctx context.Context, tokenID string) error {
	row, err := s.read().GetNodeToken(ctx, tokenID)
	if err != nil {
		return notFound(err, "node token", tokenID)
	}

	if row.RevokedAt.Valid {
		return fmt.Errorf("%w: node token %s is revoked", ErrNotFound, tokenID)
	}

	return nil
}

func newNodeToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a node token: %w", err)
	}

	return "hyn_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func nodeTokenOut(r store.NodeToken) NodeToken {
	return NodeToken{
		ID: r.ID, NodeID: r.NodeID, Name: r.Name, CreatedAt: millis(r.CreatedAt),
		LastUsedAt: optionalTime(r.LastUsedAt), RevokedAt: optionalTime(r.RevokedAt),
	}
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
			Now: now, WorktreeRoot: nullString(h.WorktreeRoot), Runtimes: string(runtimes),
			DaemonVersion: nullString(h.System.DaemonVersion), Os: nullString(h.System.OS),
			Arch: nullString(h.System.Arch), Cpus: nullPositive(int64(h.System.CPUs)),
			MemoryBytes: nullPositive(h.System.MemoryBytes), DiskBytes: nullPositive(h.System.DiskBytes), ID: nodeID,
		})

		return oneRow(n, err, "node", nodeID)
	})
}

// NodeHeartbeat records that the node is still there. Every node call about
// a Dispatch counts as one too.
func (s *Service) NodeHeartbeat(ctx context.Context, nodeID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.GetNode(ctx, nodeID)
		if err != nil {
			return notFound(err, "node", nodeID)
		}

		if NodeStatus(n.Status) == NodeDeleted {
			return fmt.Errorf("%w: node %s", ErrNotFound, nodeID)
		}

		return q.TouchNode(ctx, store.TouchNodeParams{Now: now, ID: nodeID})
	})
}

// NodeStats records what a node measured, replacing its last measurement.
func (s *Service) NodeStats(ctx context.Context, nodeID string, st SystemStats) error {
	if st.CPUPercent < 0 || st.CPUPercent > 100 || st.MemoryUsedBytes < 0 || st.DiskUsedBytes < 0 || st.Load1 < 0 {
		return fmt.Errorf("%w: a measurement out of range", ErrInvalid)
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetNodeStats(ctx, store.SetNodeStatsParams{
			CpuPercent:      sql.NullFloat64{Float64: st.CPUPercent, Valid: true},
			MemoryUsedBytes: validInt(st.MemoryUsedBytes), DiskUsedBytes: validInt(st.DiskUsedBytes),
			Load1: sql.NullFloat64{Float64: st.Load1, Valid: true}, Now: validInt(now), ID: nodeID,
		})

		return oneRow(n, err, "node", nodeID)
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

// nullPositive keeps a size a node reported; zero or less means it did not.
func nullPositive(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v > 0} }

func validInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

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
