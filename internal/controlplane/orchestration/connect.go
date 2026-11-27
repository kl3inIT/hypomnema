package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
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
	// Its lease is renewed with the answer.
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

// What a Dispatch settled at a node's hello is told, after Omnara's reasons
// (machine_daemon_runtime_store.go:33, LocalProcessMissingAfterDaemonReconnectReason).
const (
	// summaryMissingAfterReconnect: the node came back and no longer holds the Dispatch.
	summaryMissingAfterReconnect = "Lost: the node reconnected without this Dispatch " +
		"(local_process_missing_after_node_reconnect)."
	// summaryNotStarted: the node holds it, but its worker never started, so there is nothing to reattach to.
	summaryNotStarted = "The worker never started before its outcome became unknown (execution_not_started)."
)

// NodeConnect records a node's hello and answers each Dispatch it claims.
// A node that lists every Dispatch it holds (Hello.ListsHeld) also ends the
// ones it no longer holds (issue #590).
func (s *Service) NodeConnect(ctx context.Context, nodeID string, h Hello, claims []Claim) ([]Disposition, error) {
	if err := s.NodeHello(ctx, nodeID, h); err != nil {
		return nil, err
	}

	out := make([]Disposition, 0, len(claims))

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		out = out[:0]

		if h.ListsHeld {
			if err := s.settleMissing(ctx, q, now, nodeID, h.Held, claims); err != nil {
				return err
			}
		}

		for _, c := range claims {
			kind, err := s.disposition(ctx, q, now, nodeID, c)
			if err != nil {
				return err
			}

			out = append(out, Disposition{DispatchID: c.DispatchID, Kind: kind})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// settleMissing ends, as lost, every Dispatch whose worker may run on the
// node and that the node neither claims nor holds (Omnara's daemon
// reconciliation, machine_daemon_reconciliation_store.go:54-92: a process
// the reconnecting daemon does not claim is completed with
// local_process_missing_after_daemon_reconnect). A held Dispatch the node
// does not claim is one whose accept or final report is still queued in its
// outbox; that report settles it. The settlement is not first-hand, so the
// Conversation is closed; a cancel that was asked for cancels the Task.
func (s *Service) settleMissing(ctx context.Context, q *store.Queries, now int64, nodeID string, held []string,
	claims []Claim,
) error {
	live, err := q.ListLiveDispatchesOfNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("listing the dispatches of node %s: %w", nodeID, err)
	}

	holds := make(map[string]bool, len(held)+len(claims))
	for _, id := range held {
		holds[id] = true
	}

	for _, c := range claims {
		holds[c.DispatchID] = true
	}

	for _, d := range live {
		if holds[d.ID] {
			continue
		}

		if err := s.settle(ctx, q, now, d, DispatchFailed, FailureLost,
			&Result{Summary: summaryMissingAfterReconnect}, nil, false); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) disposition(ctx context.Context, q *store.Queries, now int64, nodeID string,
	c Claim,
) (DispositionKind, error) {
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
	case (status == DispatchUnknown || status == DispatchStopUnknown) && !d.ProcessRef.Valid:
		// Its worker never started, so a reattach, which names the process, cannot match
		// (Omnara fails a claimed process whose execution was never committed,
		// execution_not_started). The node is told to release it.
		if err := s.settle(ctx, q, now, d, DispatchFailed, FailureStartFailed,
			&Result{Summary: summaryNotStarted}, nil, false); err != nil {
			return "", err
		}

		return DispositionRelease, nil
	case status == DispatchUnknown || status == DispatchStopUnknown:
		return DispositionReattach, nil
	case int(d.Epoch) == c.Epoch:
		// Retaining renews the lease: it may have run out while the control
		// plane was down, and a lease that ran out is never renewed by a
		// heartbeat (issue #833). Omnara's registration ends the same way, by
		// renewing the lease of the runtime it reconciled
		// (machine_daemon_runtime_store.go:218-233, "finalize daemon runtime
		// registration lease").
		if _, err := q.RetainLease(ctx, store.RetainLeaseParams{
			Now: now, LeaseExpiresAt: now + LeaseDuration.Milliseconds(), ID: d.ID, Epoch: d.Epoch,
		}); err != nil {
			return "", fmt.Errorf("renewing the lease of retained dispatch %s: %w", d.ID, err)
		}

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
	RoleRouted bool   `json:"role_routed,omitempty"`
	// Instruction is the profile's, added to the worker's built-in instructions.
	Instruction string `json:"instruction,omitempty"`
	// Memory is what the agent keeps across its workers, and OwnerMemory what
	// every worker and Bot is told about the owner (issue #21): entries one per
	// line, as they were when the offer was made.
	Memory      string `json:"memory,omitempty"`
	OwnerMemory string `json:"owner_memory,omitempty"`
	// MemoryNotes lists the notes of the agent's memory (issue #589), each its
	// path and size, which the worker reads with the memory's tool when needed.
	MemoryNotes []string `json:"memory_notes,omitempty"`
	Spec        string   `json:"spec"`
	Repo        string   `json:"repo,omitempty"`
	// RepoHost and RepoKind say where Repo is and how that host is used.
	RepoHost string `json:"repo_host,omitempty"`
	RepoKind string `json:"repo_kind,omitempty"`
	// RepoToken is the host's token for this Dispatch. Orchestration never
	// fills it: the node API does, from the vault, as it sends the offer.
	RepoToken string `json:"repo_token,omitempty"`
	// GitName and GitEmail are the account RepoToken belongs to (issue #991).
	// Orchestration never fills them: the node API does, with the token.
	GitName  string `json:"git_name,omitempty"`
	GitEmail string `json:"git_email,omitempty"`
	// RepoSSHURL is where the node clones Repo from over SSH, when the owner
	// registered it with one (issue #625).
	RepoSSHURL string `json:"repo_ssh_url,omitempty"`
	// ModelKey is the model gateway's key for this Dispatch, when the gateway
	// serves its model. Orchestration never fills it: the node API does, as
	// it sends the offer.
	ModelKey string `json:"model_key,omitempty"`
	// ModelAPI is the wire a worker that can choose one speaks to the gateway for the model: "anthropic-messages"
	// for a Claude subscription's, "openai-responses" for a Codex subscription's, empty for chat (issue #549).
	ModelAPI string `json:"model_api,omitempty"`
	// ModelContextWindow and ModelMaxOutput are the gateway's sizes of the model, in tokens, when it knows them
	// (issue #1019). Orchestration never fills them: the node API does, as it sends the offer.
	ModelContextWindow int64 `json:"model_context_window,omitempty"`
	ModelMaxOutput     int64 `json:"model_max_output,omitempty"`
	// Uses are the Connections the Run's workers may use; AgentKey is this
	// Dispatch's own key to ask for their tokens. Orchestration never fills
	// the key: the node API does, as it sends the offer.
	Uses     []string `json:"uses,omitempty"`
	AgentKey string   `json:"agent_key,omitempty"`
	// Device is set by the node API as the offer is sent: the device every
	// worker is given, while a node holds one (issue #417).
	Device string `json:"device,omitempty"`
	Base   string `json:"base,omitempty"`
	// PushBranch is the branch publish_branch pushes the Task's commits to, when it is not the Task's own hyp/<id>:
	// the branch of the Task it continues. StartFrom is the branch its worktree starts from and PRBase the base of
	// the pull request it opens, when they are not Base (issue #690).
	PushBranch     string `json:"push_branch,omitempty"`
	StartFrom      string `json:"start_from,omitempty"`
	PRBase         string `json:"pr_base,omitempty"`
	ConversationID string `json:"conversation_id"`
	Worktree       string `json:"worktree"`
	Runtime        string `json:"runtime"`
	AccountRef     string `json:"account_ref"`
	// CLISessionRef is set when the Dispatch continues a Conversation.
	CLISessionRef string `json:"cli_session_ref,omitempty"`
	Model         string `json:"model,omitempty"`
	Effort        string `json:"effort,omitempty"`
	// Skills are the skills the worker is given, each at the revision
	// recorded when the Dispatch was made (issue #21).
	Skills []nodeproto.Skill `json:"skills,omitempty"`
	// Tools are set by the node API as the offer is sent: the names of the
	// tools the worker gets, from which its node writes the "Tools" block
	// (issue #723).
	Tools []string `json:"tools,omitempty"`
	// QuestionParts is set by the node API as the offer is sent: this control
	// plane keeps a question's several decisions (issue #1219).
	QuestionParts bool `json:"question_parts,omitempty"`
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

	agent := task.Profile

	if d.ProfileVersionID.Valid {
		v, err := q.GetProfileVersion(ctx, d.ProfileVersionID.String)
		if err != nil {
			return Offer{}, notFound(err, "profile version", d.ProfileVersionID.String)
		}

		instruction, agent = v.Instruction.String, v.Profile
	}

	memory, err := memorystore.AgentMemoryTx(ctx, q, agent)
	if err != nil {
		return Offer{}, err
	}

	owner, err := memorystore.OwnerMemoryTx(ctx, q)
	if err != nil {
		return Offer{}, err
	}

	notes, err := memorystore.AgentNotesTx(ctx, q, agent)
	if err != nil {
		return Offer{}, err
	}

	// A worker that takes over another's work is told so with the Task.
	spec := task.Spec

	if d.Handoff == 1 {
		if spec, err = handoffSpec(ctx, q, d, task.Spec); err != nil {
			return Offer{}, err
		}
	}

	skills, err := dispatchSkills(ctx, q, d.ID)
	if err != nil {
		return Offer{}, err
	}

	host, kind, sshURL := run.RepoHost.String, "", ""

	if host != "" {
		h, err := gitHostOf(ctx, q, host)
		if err != nil {
			return Offer{}, err
		}

		kind = string(h.Kind)

		if sshURL, err = sshURLOf(ctx, q, host, run.Repo.String); err != nil {
			return Offer{}, err
		}
	}

	api, err := modelAPI(ctx, q, conv.AccountRef, d.Model.String)
	if err != nil {
		return Offer{}, err
	}

	return Offer{
		ModelAPI:   api,
		DispatchID: d.ID, TaskID: task.ID, RunID: run.ID, Profile: task.Profile, Instruction: instruction,
		RoleRouted: d.ChainEntry.Valid,
		Spec:       spec,
		Repo:       run.Repo.String, RepoHost: host, RepoKind: kind, RepoSSHURL: sshURL,
		Base: run.Base.String, ConversationID: conv.ID, Worktree: conv.Worktree,
		PushBranch: task.PushBranch.String, StartFrom: task.StartFrom.String, PRBase: task.PrBase.String,
		Runtime: conv.Runtime, AccountRef: conv.AccountRef, CLISessionRef: conv.CliSessionRef.String,
		Model: d.Model.String, Effort: d.Effort.String, Uses: splitUses(run.Uses), Skills: skills,
		Memory: memory.Text, OwnerMemory: owner.Text, MemoryNotes: notes,
	}, nil
}
