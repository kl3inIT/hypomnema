package orchestration

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The scheduler (issue #19, PR 5): every ScheduleEvery, and whenever a commit
// signals, one pass gives ready Tasks a Dispatch, oldest Run first, while
// fewer than MaxActiveDispatches have not settled (Orca coordinator.ts tick;
// Coder admits inside the acquisition transaction).

// Role is a role's default runtime, model and effort; a Task's own values
// win (docs/research/2026-11-07-workers-accounts-gateway.md §2).
type Role struct {
	Runtime string
	Model   string
	Effort  string
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithRoles sets the role defaults.
func WithRoles(roles map[string]Role) ServiceOption {
	return func(s *Service) { s.roles = roles }
}

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

	runtime, model, effort := s.resolve(task)

	spec, reason, err := placeTask(ctx, q, p, task, runtime)
	if err != nil {
		return err
	}

	if spec == nil {
		return s.unplaceable(ctx, q, now, task, reason, out)
	}

	id := s.newID("dsp")
	if err := s.createDispatch(ctx, q, now, id, task, *spec, p, model, effort); err != nil {
		return err
	}

	out.Created = append(out.Created, id)

	return nil
}

// placeTask chooses where a ready Task runs. It returns no spec when the Task
// must wait, with the reason to escalate if the wait lasts ("" for one that
// resolves by itself, such as a cooling account).
func placeTask(ctx context.Context, q *store.Queries, p *placement, task store.Task, runtime string,
) (*DispatchSpec, string, error) {
	conv, open, err := openConversation(ctx, q, task.ID)
	if err != nil {
		return nil, "", err
	}

	if open {
		if !p.nodeReady(conv.NodeID, conv.Runtime) {
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

	n := p.pickNode(runtime)
	if runtime == "" || n == nil {
		return nil, ReasonNoNode, nil
	}

	account, cooling := p.pickAccount(runtime)

	switch {
	case cooling && account == "":
		return nil, "", nil
	case account == "":
		return nil, ReasonNoAccount, nil
	}

	return &DispatchSpec{
		NodeID: n.row.ID, Worktree: path.Join(n.row.WorktreeRoot.String, task.RunID, task.ID),
		Runtime: runtime, AccountRef: account,
	}, "", nil
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

// resolve returns the runtime, model and effort a Task runs with: its own
// values, then its role's defaults.
func (s *Service) resolve(task store.Task) (runtime, model, effort string) {
	role := s.roles[task.Role]

	return or(task.Runtime.String, role.Runtime), or(task.Model.String, role.Model), or(task.Effort.String, role.Effort)
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
