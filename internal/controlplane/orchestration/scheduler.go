package orchestration

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The scheduler (issue #19, PR 5): every ScheduleEvery, and whenever a commit
// signals, one pass gives ready Tasks a Dispatch, oldest Run first, while
// fewer than MaxActiveDispatches have not settled (Orca coordinator.ts tick;
// Coder admits inside the acquisition transaction).

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// Scheduled is what one pass did.
type Scheduled struct {
	Created     []string
	Escalations []Escalation
}

// Schedule makes one pass in one transaction.
func (s *Service) Schedule(ctx context.Context) (Scheduled, error) {
	out := Scheduled{Created: []string{}, Escalations: []Escalation{}}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		p, err := loadPlacement(ctx, q, now)
		if err != nil {
			return err
		}

		ids, err := q.ListSchedulableTaskIDs(ctx, now)
		if err != nil {
			return fmt.Errorf("listing ready tasks: %w", err)
		}

		for _, taskID := range ids {
			if p.active >= MaxActiveDispatches {
				break
			}

			if err := s.scheduleTask(ctx, q, now, p, taskID, &out); err != nil {
				return err
			}
		}

		for _, e := range out.Escalations {
			if err := emitEscalation(ctx, q, now, e); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return Scheduled{}, fmt.Errorf("scheduling: %w", err)
	}

	return out, nil
}

func (s *Service) scheduleTask(ctx context.Context, q *store.Queries, now int64, p *placement, taskID string,
	out *Scheduled,
) error {
	task, err := q.GetTask(ctx, taskID)
	if err != nil {
		return notFound(err, "task", taskID)
	}

	r, err := resolve(ctx, q, task)
	if err != nil {
		return err
	}

	host, err := runHost(ctx, q, task.RunID)
	if err != nil {
		return err
	}

	spec, reason, err := placeTask(ctx, q, p, task, r, host)
	if err != nil {
		return err
	}

	// A Task that waits only for a limit to reset goes to a backup of its profile.
	if spec == nil && reason == "" {
		if spec, r, err = placeBackup(ctx, q, p, task, r, host); err != nil {
			return err
		}
	}

	if spec == nil {
		return s.unplaceable(ctx, q, now, task, reason, out)
	}

	told := int64(1)

	if r.fallbackFor != "" {
		told, err = q.CountFallbacksOfRun(ctx, store.CountFallbacksOfRunParams{
			RunID: task.RunID, FallbackFor: nullString(r.fallbackFor),
			FallbackUntil: sql.NullInt64{Int64: r.fallbackUntil, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("reading the fallbacks of run %s: %w", task.RunID, err)
		}
	}

	id := s.newID("dsp")
	if err := s.createDispatch(ctx, q, now, id, task, *spec, p, r); err != nil {
		return err
	}

	out.Created = append(out.Created, id)

	if told == 0 {
		out.Escalations = append(out.Escalations, Escalation{
			Kind: EscalationFallback, DispatchID: id, Profile: r.fallbackFor, Fallback: r.profile,
			Until: r.fallbackUntil,
		})
	}

	return nil
}

// placeBackup places a Task under the first backup of its profile a worker
// can start under now, when every account of the profile's runtime waits for
// a limit to reset. The owner orders the backups, as OpenMausBot's list per
// bot (server/host/limit-takeover.ts, chosen) and OpenClaw's fallbacks per
// agent. It returns no spec, and r as given, when the Task waits.
//
// A Task that has not started (issue #171) goes to any node the backup can
// run on. A Task whose worker met the limit under way (issue #187) is handed
// off: the backup takes it over in the same worktree, on the Conversation's
// node, with a session of its own, as Orca hands a session to another agent
// (agent-session-continuation.ts) and OpenMausBot sends a limited turn's
// request again on a backup (server/quota-switch.ts, turnFailed).
//
// The Task waits when it names its own runtime or model (that choice is
// strict: OpenClaw, t3code), when its last Conversation is closed, when it
// may not be handed off (mayHandOff), and when no backup can run now
// (OpenMausBot's usable, t3code). A backup's own backups are not followed
// (t3code: one hop).
func placeBackup(ctx context.Context, q *store.Queries, p *placement, task store.Task, r resolved, host where,
) (*DispatchSpec, resolved, error) {
	if task.Runtime.String != "" || task.Model.String != "" {
		return nil, r, nil
	}

	a := p.availability(r.runtime, r.gateway)
	if a.Reason != ReasonAccountsCooling {
		return nil, r, nil
	}

	conv, open, err := openConversation(ctx, q, task.ID)
	if err != nil {
		return nil, r, err
	}

	started := conv.ID != ""

	if started {
		if !open {
			return nil, r, nil
		}

		if may, err := mayHandOff(ctx, q, task); err != nil || !may {
			return nil, r, err
		}
	}

	own, err := q.GetProfileVersion(ctx, r.versionID)
	if err != nil {
		return nil, r, notFound(err, "profile version", r.versionID)
	}

	for _, name := range splitNames(own.Fallbacks) {
		v, err := q.CurrentProfileVersion(ctx, name)
		if err != nil {
			return nil, r, notFound(err, "profile", name)
		}

		b, err := resolveVersion(ctx, q, task, v)
		if err != nil {
			return nil, r, err
		}

		spec, _ := placeNew(p, task, b, host)
		if started {
			spec = placeHandoff(p, conv, b, host)
		}

		if spec != nil {
			b.fallbackFor, b.fallbackUntil, b.handoff = task.Profile, a.At.UnixMilli(), started

			return spec, b, nil
		}
	}

	return nil, r, nil
}

// placeHandoff is where a backup takes over a Conversation's work: on its
// node, where the worktree is, in that worktree. No reference has more than
// one machine; Orca and t3code keep the working directory when a session
// moves to another agent, and here that directory is on one node (inferred).
func placeHandoff(p *placement, conv store.Conversation, b resolved, host where) *DispatchSpec {
	if !p.nodeReady(conv.NodeID, b.runtime, host) {
		return nil
	}

	account := GatewayAccountRef
	if !b.gateway {
		account, _ = p.pickAccount(b.runtime)
	}

	if account == "" {
		return nil
	}

	return &DispatchSpec{NodeID: conv.NodeID, Worktree: conv.Worktree, Runtime: b.runtime, AccountRef: account}
}

// mayHandOff reports whether a Task whose worker met a limit under way may
// be taken over by a backup of its profile:
//
//   - it names no runtime or model of its own, and its profile lists backups;
//   - it was not handed off before (t3code: one hop; omp: each model once);
//   - no request to a service was approved for it: what its worker did
//     outside the worktree cannot be seen there, and could be done twice
//     (OpenClaw, replay-state.ts: a tool with side effects makes a run unsafe
//     to replay; omp, turn-recovery.ts). A write the owner's policy allows
//     without asking leaves no record with the Task and is not seen.
//
// A Task that waits on a question or a gate is not ready, so the scheduler
// never asks about it (OpenMausBot sends nothing again while a question waits).
func mayHandOff(ctx context.Context, q *store.Queries, task store.Task) (bool, error) {
	if task.Runtime.String != "" || task.Model.String != "" {
		return false, nil
	}

	v, err := q.CurrentProfileVersion(ctx, task.Profile)
	if err != nil {
		return false, notFound(err, "profile", task.Profile)
	}

	if v.Fallbacks == "" {
		return false, nil
	}

	moved, err := q.CountFallbacksOfTask(ctx, task.ID)
	if err != nil {
		return false, fmt.Errorf("reading the fallbacks of task %s: %w", task.ID, err)
	}

	if moved > 0 {
		return false, nil
	}

	wrote, err := q.CountApprovedOfTask(ctx, nullString(task.ID))
	if err != nil {
		return false, fmt.Errorf("reading the approvals of task %s: %w", task.ID, err)
	}

	return wrote == 0, nil
}

// placeTask chooses where a ready Task runs. It returns no spec when the Task
// must wait, with the reason to escalate if the wait lasts ("" for one that
// resolves by itself, such as a cooling account).
func placeTask(ctx context.Context, q *store.Queries, p *placement, task store.Task, r resolved, host where,
) (*DispatchSpec, string, error) {
	conv, open, err := openConversation(ctx, q, task.ID)
	if err != nil {
		return nil, "", err
	}

	if open {
		if !p.nodeReady(conv.NodeID, conv.Runtime, host) {
			return nil, ReasonNoNode, nil
		}

		switch usable, cooling := p.accountUsable(conv.Runtime, conv.AccountRef); {
		case cooling:
			return nil, "", nil
		case !usable:
			return nil, ReasonNoAccount, nil
		}

		return &DispatchSpec{Continue: true}, "", nil
	}

	spec, reason := placeNew(p, task, r, host)

	return spec, reason, nil
}

// placeNew chooses the node and the account of a Task's new Conversation.
func placeNew(p *placement, task store.Task, r resolved, host where) (*DispatchSpec, string) {
	runtime := r.runtime

	n := p.pickNode(runtime, host)
	if runtime == "" || n == nil {
		if !p.anyReaches(host.host) {
			return nil, ReasonNoNodeReaches + host.host
		}

		if missing := p.anyHas(host.tools); missing != "" {
			return nil, ReasonNoNodeHasTool + missing
		}

		return nil, ReasonNoNode
	}

	// A model the gateway serves needs no account on a node (issue #107).
	account, cooling := GatewayAccountRef, false
	if !r.gateway {
		account, cooling = p.pickAccount(runtime)
	}

	switch {
	case cooling && account == "":
		return nil, ""
	case account == "":
		return nil, ReasonNoAccount
	}

	return &DispatchSpec{
		NodeID: n.row.ID, Worktree: path.Join(n.row.WorktreeRoot.String, task.RunID, task.ID),
		Runtime: runtime, AccountRef: account,
	}, ""
}

// runHost is what a Run's work needs of a node: the git host of its
// repository, "" for a Run without one, and the tools its Connections are
// called with.
func runHost(ctx context.Context, q *store.Queries, runID string) (where, error) {
	run, err := q.GetRun(ctx, runID)
	if err != nil {
		return where{}, notFound(err, "run", runID)
	}

	return where{host: run.RepoHost.String, tools: toolsOf(splitUses(run.Uses))}, nil
}

// unplaceable escalates a Task that has waited UnplaceableAfter for a node or
// an account, once each time it becomes ready.
func (s *Service) unplaceable(ctx context.Context, q *store.Queries, now int64, task store.Task, reason string,
	out *Scheduled,
) error {
	if reason == "" {
		return nil
	}

	since := task.UpdatedAt
	if task.NotBefore.Valid {
		since = max(since, task.NotBefore.Int64)
	}

	if now-since < UnplaceableAfter.Milliseconds() {
		return nil
	}

	n, err := q.CountTaskEscalationsSince(ctx, store.CountTaskEscalationsSinceParams{
		TaskID: nullString(task.ID), Kind: string(EscalationUnplaceable), Since: since,
	})
	if err != nil {
		return fmt.Errorf("reading the escalations of task %s: %w", task.ID, err)
	}

	if n == 0 {
		out.Escalations = append(out.Escalations, Escalation{
			Kind: EscalationUnplaceable, TaskID: task.ID, Reason: reason,
		})
	}

	return nil
}

// Loop runs Reap and then Schedule every ScheduleEvery and whenever a commit
// signals, until ctx ends. A signal from its own pass is dropped; the next
// tick covers any commit that came during the pass.
func (s *Service) Loop(ctx context.Context) error {
	wake := s.Subscribe(ctx)
	tick := time.NewTicker(ScheduleEvery)

	defer tick.Stop()

	for {
		if _, err := s.Reap(ctx); err != nil {
			return loopErr(ctx, err)
		}

		if _, err := s.Schedule(ctx); err != nil {
			return loopErr(ctx, err)
		}

		select {
		case <-wake:
		default:
		}

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-wake:
		}
	}
}

// loopErr drops the error of a pass cut short because ctx ended.
func loopErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil //nolint:nilerr // ending with ctx is how the loop stops, not a failure
	}

	return err
}

func or(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
