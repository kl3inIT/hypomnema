package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The scheduler (issue #19, PR 5): every ScheduleEvery, and whenever a commit
// signals, one pass goes through every ready Task and gives each a Dispatch
// when a node has room for its request (Orca coordinator.ts tick; Coder admits
// inside the acquisition transaction). Room is computed, the requests of a
// node's Dispatches against its allocatable (issue #731); the Bots share it.
// Each Bot has a guaranteed share, which orders who goes first as room frees,
// and may borrow any room that fits beyond it (Kueue's nominalQuota with no
// borrowingLimit, YARN's capacity under a maximum-capacity of 100 %). Nothing
// running is ever stopped to make room. A Task left ready is told why it waits
// (tasks.waiting_reason), as OpenMausBot's queued reason
// (server/host/queued-reason.ts).

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

		queues, err := groupQueues(ctx, q, ids)
		if err != nil {
			return err
		}

		for g := nextQueue(queues, p.groups); g != nil; g = nextQueue(queues, p.groups) {
			task := g.tasks[0]
			g.tasks = g.tasks[1:]

			if err := s.scheduleTask(ctx, q, now, p, task.id, &out); err != nil {
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

// queue is one group's ready Tasks in a pass (issue #731): a Bot's, or ""
// the owner's Runs without a Bot, oldest Run first, then by position.
type queue struct {
	group     string
	guarantee int
	tasks     []queued
}

// queued is a ready Task and when its Run was created; a group's queue keeps
// the pass's order, oldest Run first.
type queued struct {
	id         string
	runCreated int64
}

// groupQueues splits the ready Tasks, oldest Run first, into their groups' queues.
func groupQueues(ctx context.Context, q *store.Queries, ids []string) ([]*queue, error) {
	var (
		out     []*queue
		byRun   = map[string]*queue{}
		created = map[string]int64{}
		byName  = map[string]*queue{}
	)

	for _, id := range ids {
		task, err := q.GetTask(ctx, id)
		if err != nil {
			return nil, notFound(err, "task", id)
		}

		g, ok := byRun[task.RunID]
		if !ok {
			run, err := q.GetRun(ctx, task.RunID)
			if err != nil {
				return nil, notFound(err, "run", task.RunID)
			}

			created[task.RunID] = run.CreatedAt

			group, guarantee, err := runGroup(ctx, q, task.RunID)
			if err != nil {
				return nil, err
			}

			if g, ok = byName[group]; !ok {
				g = &queue{group: group, guarantee: guarantee}
				byName[group] = g
				out = append(out, g)
			}

			byRun[task.RunID] = g
		}

		g.tasks = append(g.tasks, queued{id: id, runCreated: created[task.RunID]})
	}

	return out, nil
}

// nextQueue is the group whose next Task is tried: of the groups with a Task
// not yet tried this pass, the one with the lowest used / guarantee, where
// used counts its unsettled Dispatches with those placed in this pass; equal
// ratios go to the group whose next Task's Run is older, then to the smaller
// group id. nil when every Task was tried. From YARN's
// PriorityUtilizationQueueOrderingPolicy, lowest used capacity relative to
// the guarantee first (PriorityUtilizationQueueOrderingPolicy.java:69-74),
// sorted again for each container (:251-259) and a queue with nothing pending
// skipped (AbstractLeafQueue.java:1199-1212); Kueue's classic order puts work
// under its nominal quota first, then FIFO (scheduler.go:1322-1345).
//
// A Task that fits nowhere does not hold the room: it waits, and the next
// Task, of its group or another, is tried, as Kueue's default BestEffortFIFO
// (clusterqueue_types.go:98-110; the owner's answer, 2026-11-17).
func nextQueue(queues []*queue, used map[string]int) *queue {
	var best *queue

	for _, g := range queues {
		if len(g.tasks) == 0 {
			continue
		}

		if best == nil || before(g, best, used) {
			best = g
		}
	}

	return best
}

// before reports whether group a goes before group b: its used / guarantee
// compared exactly, as fractions, then the older Run, then the smaller id.
func before(a, b *queue, used map[string]int) bool {
	if l, r := used[a.group]*b.guarantee, used[b.group]*a.guarantee; l != r {
		return l < r
	}

	// Two Tasks are never at one place in the pass's list, so the place cannot
	// say which Run is older: when the Runs were created can tie (PR #759).
	if a.tasks[0].runCreated != b.tasks[0].runCreated {
		return a.tasks[0].runCreated < b.tasks[0].runCreated
	}

	return a.group < b.group
}

// scheduleTask gives a ready Task a Dispatch, or records why it waits.
func (s *Service) scheduleTask(ctx context.Context, q *store.Queries, now int64, p *placement, taskID string,
	out *Scheduled,
) error {
	task, err := q.GetTask(ctx, taskID)
	if err != nil {
		return notFound(err, "task", taskID)
	}

	r, err := resolve(ctx, q, task)
	if err != nil {
		if errors.Is(err, errRoleEntry) {
			return s.wait(ctx, q, now, task, err.Error(), out)
		}
		return err
	}

	host, err := runHost(ctx, q, task.RunID)
	if err != nil {
		return err
	}

	expiredOn, err := q.NodesWithRecentTaskExpiries(ctx, store.NodesWithRecentTaskExpiriesParams{
		TaskID: task.ID, Since: now - TaskExpiryPenaltyWindow.Milliseconds(),
	})
	if err != nil {
		return fmt.Errorf("reading recent expiries of task %s: %w", task.ID, err)
	}

	recentExpiries := make(map[string]bool, len(expiredOn))
	for _, nodeID := range expiredOn {
		recentExpiries[nodeID] = true
	}

	spec, reason, err := placeTask(ctx, q, p, task, r, host.asking(r), recentExpiries)
	if err != nil {
		return err
	}

	// A Task that waits only for a limit to reset goes to a backup of its profile.
	if spec == nil && len(r.chain) > 0 && (reason == "" || reason == ReasonNoAccount) {
		spec, r, reason, err = placeRoleBackup(ctx, q, p, task, r, host, recentExpiries, reason)
		if err != nil {
			return err
		}
	} else if spec == nil && reason == "" && len(r.chain) == 0 {
		if spec, r, reason, err = placeBackup(ctx, q, p, task, r, host, recentExpiries); err != nil {
			return err
		}
	}

	if spec == nil {
		return s.wait(ctx, q, now, task, reason, out)
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
		fallback := r.profile
		if len(r.chain) > 0 {
			fallback = r.model
		}
		out.Escalations = append(out.Escalations, Escalation{
			Kind: EscalationFallback, DispatchID: id, Profile: r.fallbackFor, Fallback: fallback,
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
	recentExpiries map[string]bool,
) (*DispatchSpec, resolved, string, error) {
	if task.Runtime.String != "" || task.Model.String != "" {
		return nil, r, "", nil
	}

	a := p.availability(r.runtime, r.gateway, r.accounts)
	if a.Reason != ReasonAccountsCooling {
		return nil, r, "", nil
	}

	conv, open, err := openConversation(ctx, q, task.ID)
	if err != nil {
		return nil, r, "", err
	}

	started := conv.ID != ""

	if started {
		if !open {
			return nil, r, "", nil
		}

		if may, err := mayHandOff(ctx, q, task); err != nil || !may {
			return nil, r, "", err
		}
	}

	own, err := q.GetProfileVersion(ctx, r.versionID)
	if err != nil {
		return nil, r, "", notFound(err, "profile version", r.versionID)
	}

	// A Run a Bot created falls back only to profiles on the Bot's workers list, read now (issue #169).
	mayGive, err := runBotGives(ctx, q, task.RunID)
	if err != nil {
		return nil, r, "", err
	}

	var reason string
	for _, name := range splitNames(own.Fallbacks) {
		if !mayGive(name) {
			continue
		}

		v, err := q.CurrentProfileVersion(ctx, name)
		if err != nil {
			return nil, r, "", notFound(err, "profile", name)
		}

		b, err := resolveVersion(ctx, q, task, v)
		if err != nil {
			return nil, r, "", err
		}

		var spec *DispatchSpec
		var wait string
		if started {
			spec, wait = placeHandoff(p, conv, b, host.asking(b))
		} else {
			spec, wait = placeNew(p, task, b, host.asking(b), recentExpiries)
		}
		if wait == ReasonNodesLowDisk {
			reason = wait
		}

		if spec != nil {
			b.fallbackFor, b.fallbackUntil, b.handoff = task.Profile, a.At.UnixMilli(), started

			return spec, b, "", nil
		}
	}

	return nil, r, reason, nil
}

// runBotGives says which profiles the Bot that created a Run may give work
// to; every one for a Run no Bot created (the owner's), or whose Bot is gone.
func runBotGives(ctx context.Context, q *store.Queries, runID string) (func(string) bool, error) {
	every := func(string) bool { return true }

	_, bot, found, err := runBot(ctx, q, runID)
	if err != nil || !found {
		return every, err
	}

	return func(profile string) bool { return bot.MayGive(profile) == nil }, nil
}

// runBot is the Bot that created a Run: its id, "" for a Run no Bot created
// (the owner's), and the Bot when it is still there. A Run a coordinator
// created in a chat without its Bot recorded is that chat's Bot's.
func runBot(ctx context.Context, q *store.Queries, runID string) (string, Bot, bool, error) {
	run, err := q.GetRun(ctx, runID)
	if err != nil {
		return "", Bot{}, false, notFound(err, "run", runID)
	}

	id := run.BotID.String

	if !run.BotID.Valid && run.ChatID.Valid && run.CoordinatorTokenID.Valid {
		chat, err := q.GetChat(ctx, run.ChatID.String)
		if err != nil {
			return "", Bot{}, false, notFound(err, "chat", run.ChatID.String)
		}

		id = chat.BotID
	}

	if id == "" {
		return "", Bot{}, false, nil
	}

	bot, found, err := botIfAny(ctx, q, id)

	return id, bot, found, err
}

// runGroup is the group a Run's Dispatches count in (issue #660), its Bot's
// id or "" for the owner's Runs without a Bot, and the group's guarantee
// (issue #731): the Bot's own, else BotGuaranteeDefault.
// CountActiveDispatchesPerBot groups the same way.
func runGroup(ctx context.Context, q *store.Queries, runID string) (string, int, error) {
	id, bot, found, err := runBot(ctx, q, runID)
	if err != nil {
		return "", 0, err
	}

	if !found {
		return id, BotGuaranteeDefault, nil
	}

	return id, bot.Guarantee(), nil
}

// placeHandoff is where a backup takes over a Conversation's work: on its
// node, where the worktree is, in that worktree. No reference has more than
// one machine; Orca and t3code keep the working directory when a session
// moves to another agent, and here that directory is on one node (inferred).
func placeHandoff(p *placement, conv store.Conversation, b resolved, host where) (*DispatchSpec, string) {
	if p.checkNodeFit(conv.NodeID, b.runtime, host) != nil {
		return nil, ""
	}

	account := GatewayAccountRef
	if !b.gateway {
		account, _ = p.pickAccount(b.runtime)
	}

	if account == "" || (b.gateway && !p.gatewayHasAccount(b.accounts)) {
		return nil, ""
	}
	if p.lowDisk(conv.NodeID) {
		return nil, ReasonNodesLowDisk
	}

	return &DispatchSpec{NodeID: conv.NodeID, Worktree: conv.Worktree, Runtime: b.runtime, AccountRef: account}, ""
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
	last, err := q.LastDispatchOfTask(ctx, task.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if last.RoleChain != "" {
		chain, err := dispatchRoleChain(last)
		if err != nil || last.ChainEntry.Int64 >= int64(len(chain)) {
			return false, err
		}
		return handoffWithoutApprovedWrites(ctx, q, task.ID)
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

	return handoffWithoutApprovedWrites(ctx, q, task.ID)
}

func handoffWithoutApprovedWrites(ctx context.Context, q *store.Queries, taskID string) (bool, error) {
	wrote, err := q.CountApprovedOfTask(ctx, nullString(taskID))
	if err != nil {
		return false, fmt.Errorf("reading the approvals of task %s: %w", taskID, err)
	}

	return wrote == 0, nil
}

// placeTask chooses where a ready Task runs. It returns no spec when the Task
// must wait, with the reason to escalate if the wait lasts ("" for one that
// resolves by itself, such as a cooling account).
func placeTask(ctx context.Context, q *store.Queries, p *placement, task store.Task, r resolved, host where,
	recentExpiries map[string]bool,
) (*DispatchSpec, string, error) {
	conv, open, err := openConversation(ctx, q, task.ID)
	if err != nil {
		return nil, "", err
	}

	if open {
		// A Conversation begun under a node's Claude account before issue #381 does not go on.
		if gatewayOnly(conv.Runtime) && conv.AccountRef != GatewayAccountRef {
			return nil, ReasonNeedsGateway, nil
		}

		// It runs on its own node, where its worktree is, and its request must fit there (issue #731).
		if p.atLimit(conv.NodeID, conv.Runtime, host) {
			return nil, ReasonNodesFull, nil
		}

		if p.tooSmall(conv.NodeID, conv.Runtime, host) {
			return nil, ReasonNoNodeFits, nil
		}

		fits := p.checkNodeFit(conv.NodeID, conv.Runtime, host) == nil
		if !fits {
			return nil, ReasonNoNode, nil
		}
		if len(r.chain) > 0 && r.gateway {
			if p.roleCoolingUntil(r) != 0 {
				return nil, "", nil
			}
			if !p.gatewayHasAccount(r.accounts) {
				return nil, ReasonNoAccount, nil
			}
		}

		switch usable, cooling := p.accountUsable(conv.Runtime, conv.AccountRef); {
		case cooling:
			return nil, "", nil
		case !usable:
			return nil, ReasonNoAccount, nil
		}
		if p.lowDisk(conv.NodeID) {
			return nil, ReasonNodesLowDisk, nil
		}

		return &DispatchSpec{Continue: true}, "", nil
	}

	spec, reason := placeNew(p, task, r, host, recentExpiries)

	return spec, reason, nil
}

// placeNew chooses the node and the account of a Task's new Conversation.
func placeNew(p *placement, task store.Task, r resolved, host where, recentExpiries map[string]bool,
) (*DispatchSpec, string) {
	runtime := r.runtime

	if !r.gateway && gatewayOnly(runtime) {
		return nil, ReasonNeedsGateway
	}

	n := p.pickNode(runtime, host, recentExpiries)
	if runtime == "" || n == nil {
		// A Run without a repository has no host to reach: with no node connected, it has no node.
		if host.host != "" && !p.anyReaches(host.host) {
			return nil, ReasonNoNodeReaches + host.host
		}

		if missing := p.anyHas(host.tools); missing != "" {
			return nil, ReasonNoNodeHasTool + missing
		}

		if runtime != "" && p.diskPreventsPlacement(runtime, host) {
			availability := p.availability(runtime, r.gateway, r.accounts)
			if availability.Reason == ReasonAccountsCooling {
				return nil, ""
			}
			if !availability.Available {
				return nil, availability.Reason
			}
			return nil, ReasonNodesLowDisk
		}

		if runtime != "" && p.anyAtLimit(runtime, host) {
			return nil, ReasonNodesFull
		}

		if runtime != "" && p.anyLowMemory(runtime, host) {
			return nil, ReasonNodesLowMemory
		}

		if runtime != "" && p.anyTooSmall(runtime, host) {
			return nil, ReasonNoNodeFits
		}

		return nil, ReasonNoNode
	}

	// A model the gateway serves needs no account on a node (issue #107).
	account, cooling := GatewayAccountRef, false
	if !r.gateway {
		account, cooling = p.pickAccount(runtime)
	}
	if len(r.chain) > 0 && r.gateway && p.roleCoolingUntil(r) != 0 {
		return nil, ""
	}

	switch {
	case cooling && account == "":
		return nil, ""
	case account == "", r.gateway && !p.gatewayHasAccount(r.accounts):
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

// wait leaves a ready Task without a Dispatch this pass: it records why
// (issue #660), only when that changed and without touching the Task's
// updated_at, and escalates a wait that does not resolve by itself. reason
// is "" for a Task that waits for a cooling account.
func (s *Service) wait(ctx context.Context, q *store.Queries, now int64, task store.Task, reason string,
	out *Scheduled,
) error {
	shown := or(reason, ReasonAccountsCooling)

	if !task.WaitingReason.Valid || task.WaitingReason.String != shown {
		if err := q.SetTaskWaitingReason(ctx, store.SetTaskWaitingReasonParams{
			WaitingReason: nullString(shown), ID: task.ID,
		}); err != nil {
			return fmt.Errorf("recording why task %s waits: %w", task.ID, err)
		}
	}

	if resolvesByItself(reason) {
		return nil
	}

	return s.unplaceable(ctx, q, now, task, reason, out)
}

// resolvesByItself reports whether a wait ends without anyone acting: a
// cooling account, or every node that could run it full now. Such a wait is
// not escalated.
func resolvesByItself(reason string) bool {
	return reason == "" || reason == ReasonAccountsCooling || reason == ReasonNodesFull || reason == ReasonNodesLowDisk
}

// unplaceable escalates a Task that has waited UnplaceableAfter for a node or
// an account, once each time it becomes ready.
func (s *Service) unplaceable(ctx context.Context, q *store.Queries, now int64, task store.Task, reason string,
	out *Scheduled,
) error {
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
