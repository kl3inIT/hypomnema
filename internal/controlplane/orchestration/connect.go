package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// What a connected node is told (issue #32). On each connect the node lists
// the Dispatches it still holds and is told what to do with each (Omnara's
// process dispositions, internal/daemonprotocol/protocol.go); while it stays
// connected it is offered its queued Dispatches, the stops asked of it and
// the answers its workers wait for.

// Claim is a Dispatch a node still holds when it connects, at the epoch it
// last accepted.
type Claim struct {
	DispatchID string `json:"dispatch_id"`
	Epoch      int    `json:"epoch"`
}

// DispositionKind says what a node does with a Dispatch it claims.
type DispositionKind string

// Dispositions.
const (
	// DispositionRetain: the Dispatch is still the node's at that epoch; carry on.
	DispositionRetain DispositionKind = "retain"
	// DispositionReattach: its outcome was marked unknown while the node was
	// away; reattach to the same process (ReattachDispatch) or report it ended.
	DispositionReattach DispositionKind = "reattach"
	// DispositionRelease: it is no longer the node's (settled, never accepted,
	// another epoch or unknown); stop its worker and clean up.
	DispositionRelease DispositionKind = "release"
)

// Disposition is the answer for one Claim.
type Disposition struct {
	DispatchID string          `json:"dispatch_id"`
	Kind       DispositionKind `json:"kind"`
}

// NodeConnect records a node's hello and answers each Dispatch it claims.
func (s *Service) NodeConnect(ctx context.Context, nodeID string, h Hello, claims []Claim) ([]Disposition, error) {
	if err := s.NodeHello(ctx, nodeID, h); err != nil {
		return nil, err
	}

	q := s.read()
	out := make([]Disposition, 0, len(claims))

	for _, c := range claims {
		kind, err := disposition(ctx, q, nodeID, c)
		if err != nil {
			return nil, err
		}

		out = append(out, Disposition{DispatchID: c.DispatchID, Kind: kind})
	}

	return out, nil
}

func disposition(ctx context.Context, q *store.Queries, nodeID string, c Claim) (DispositionKind, error) {
	d, err := q.GetDispatch(ctx, c.DispatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return DispositionRelease, nil
	}

	if err != nil {
		return "", fmt.Errorf("reading claimed dispatch %s: %w", c.DispatchID, err)
	}

	status := DispatchStatus(d.Status)

	switch {
	case d.NodeID != nodeID || status.settled() || status == DispatchQueued:
		// A queued Dispatch was never accepted, so nothing of it runs; it is offered again.
		return DispositionRelease, nil
	case status == DispatchUnknown || status == DispatchStopUnknown:
		return DispositionReattach, nil
	case int(d.Epoch) == c.Epoch:
		return DispositionRetain, nil
	default:
		return DispositionRelease, nil
	}
}

// Offer is a queued Dispatch with what the node needs to prepare it.
type Offer struct {
	DispatchID string `json:"dispatch_id"`
	TaskID     string `json:"task_id"`
	RunID      string `json:"run_id"`
	Profile    string `json:"profile"`
	// Instruction is the profile's, added to the worker's built-in instructions.
	Instruction string `json:"instruction,omitempty"`
	Spec        string `json:"spec"`
	Repo        string `json:"repo,omitempty"`
	// RepoHost and RepoKind say where Repo is and how that host is used; a
	// node from before hosts existed reads neither and assumes GitHub.
	RepoHost       string `json:"repo_host,omitempty"`
	RepoKind       string `json:"repo_kind,omitempty"`
	Base           string `json:"base,omitempty"`
	ConversationID string `json:"conversation_id"`
	Worktree       string `json:"worktree"`
	Runtime        string `json:"runtime"`
	AccountRef     string `json:"account_ref"`
	// CLISessionRef is set when the Dispatch continues a Conversation.
	CLISessionRef string `json:"cli_session_ref,omitempty"`
	Model         string `json:"model,omitempty"`
	Effort        string `json:"effort,omitempty"`
}

// StopRequest is a stop asked of a Dispatch on the node.
type StopRequest struct {
	DispatchID string `json:"dispatch_id"`
	Epoch      int    `json:"epoch"`
}

// Pending is what a connected node is to be told now.
type Pending struct {
	Offers  []Offer
	Stops   []StopRequest
	Answers []Question
}

// NodePending returns the node's queued Dispatches, the stops asked of it and
// the answers not yet acknowledged. Sending them again is harmless: accepting
// twice is refused, and stops and answers are idempotent on the node.
func (s *Service) NodePending(ctx context.Context, nodeID string) (Pending, error) {
	q := s.read()

	queued, err := q.ListQueuedDispatchesOfNode(ctx, nodeID)
	if err != nil {
		return Pending{}, fmt.Errorf("listing queued dispatches of %s: %w", nodeID, err)
	}

	out := Pending{Offers: []Offer{}, Stops: []StopRequest{}, Answers: []Question{}}

	for _, d := range queued {
		o, err := offerOf(ctx, q, d)
		if err != nil {
			return Pending{}, err
		}

		out.Offers = append(out.Offers, o)
	}

	stops, err := q.ListStoppingDispatchesOfNode(ctx, nodeID)
	if err != nil {
		return Pending{}, fmt.Errorf("listing stops of %s: %w", nodeID, err)
	}

	for _, r := range stops {
		out.Stops = append(out.Stops, StopRequest{DispatchID: r.ID, Epoch: int(r.Epoch)})
	}

	answers, err := q.ListUndeliveredAnswersOfNode(ctx, nodeID)
	if err != nil {
		return Pending{}, fmt.Errorf("listing answers for %s: %w", nodeID, err)
	}

	for _, row := range answers {
		a, err := questionFromRow(row)
		if err != nil {
			return Pending{}, err
		}

		out.Answers = append(out.Answers, a)
	}

	return out, nil
}

func offerOf(ctx context.Context, q *store.Queries, d store.Dispatch) (Offer, error) {
	task, err := q.GetTask(ctx, d.TaskID)
	if err != nil {
		return Offer{}, notFound(err, "task", d.TaskID)
	}

	run, err := q.GetRun(ctx, task.RunID)
	if err != nil {
		return Offer{}, notFound(err, "run", task.RunID)
	}

	conv, err := q.GetConversation(ctx, d.ConversationID)
	if err != nil {
		return Offer{}, notFound(err, "conversation", d.ConversationID)
	}

	var instruction string

	if d.ProfileVersionID.Valid {
		v, err := q.GetProfileVersion(ctx, d.ProfileVersionID.String)
		if err != nil {
			return Offer{}, notFound(err, "profile version", d.ProfileVersionID.String)
		}

		instruction = v.Instruction.String
	}

	host, kind := repoHost(run), ""

	if host != "" {
		h, err := gitHostOf(ctx, q, host)
		if err != nil {
			return Offer{}, err
		}

		kind = string(h.Kind)
	}

	return Offer{
		DispatchID: d.ID, TaskID: task.ID, RunID: run.ID, Profile: task.Profile, Instruction: instruction,
		Spec: task.Spec,
		Repo: run.Repo.String, RepoHost: host, RepoKind: kind,
		Base: run.Base.String, ConversationID: conv.ID, Worktree: conv.Worktree,
		Runtime: conv.Runtime, AccountRef: conv.AccountRef, CLISessionRef: conv.CliSessionRef.String,
		Model: d.Model.String, Effort: d.Effort.String,
	}, nil
}
