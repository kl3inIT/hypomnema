package orchestration

import (
	"context"
	"errors"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

func (p *placement) roleCoolingUntil(r resolved) int64 {
	runtime := r.runtime
	if r.gateway {
		runtime = r.accounts
	}
	var earliest int64
	for _, account := range p.accounts {
		if account.Runtime != runtime || AccountStatus(account.Status) != AccountActive || runsOnNode(account) == r.gateway {
			continue
		}
		until := p.cooling[[2]string{runtime, account.ID}]
		if until == 0 {
			return 0
		}
		if earliest == 0 || until < earliest {
			earliest = until
		}
	}
	return earliest
}

func placeRoleBackup(ctx context.Context, q *store.Queries, p *placement, task store.Task, current resolved, host where,
	recentExpiries map[string]bool, reason string,
) (*DispatchSpec, resolved, string, error) {
	if task.Runtime.Valid || task.Model.Valid || current.chainIndex+1 >= len(current.chain) {
		return nil, current, reason, nil
	}
	conv, open, err := openConversation(ctx, q, task.ID)
	if err != nil {
		return nil, current, reason, err
	}
	started := conv.ID != ""
	until := p.roleCoolingUntil(current)
	if until == 0 && !(open && reason == ReasonNoAccount) {
		return nil, current, reason, nil
	}
	if started {
		if !open {
			return nil, current, reason, nil
		}
		may, err := mayHandOff(ctx, q, task)
		if err != nil || !may {
			return nil, current, reason, err
		}
	}
	version, err := q.GetProfileVersion(ctx, current.versionID)
	if err != nil {
		return nil, current, reason, err
	}
	for index := current.chainIndex + 1; index < len(current.chain); index++ {
		backup, err := resolveRoleEntry(ctx, q, version, task, current.chain, index)
		if errors.Is(err, errRoleEntry) {
			return nil, current, err.Error(), nil
		}
		if err != nil {
			return nil, current, reason, err
		}
		if p.roleCoolingUntil(backup) != 0 {
			continue
		}
		var spec *DispatchSpec
		if started {
			var wait string
			spec, wait = placeHandoff(p, conv, backup, host.asking(backup))
			if wait != "" {
				return nil, current, wait, nil
			}
			if spec == nil {
				return nil, current, "role handoff unavailable: " + backup.runtime + "/" + backup.model, nil
			}
		} else {
			var wait string
			spec, wait = placeNew(p, task, backup, host.asking(backup), recentExpiries)
			if spec == nil {
				return nil, current, wait, nil
			}
		}
		backup.fallbackFor, backup.fallbackUntil, backup.handoff = task.Profile, max(until, task.UpdatedAt), started
		return spec, backup, "", nil
	}
	return nil, current, reason, nil
}
