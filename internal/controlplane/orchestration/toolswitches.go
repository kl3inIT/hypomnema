package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// A Bot's pi's tool switches are its Bot's (AgentSpec.ToolSwitches), read now, so a change reaches a pi already
// running at its next call.

// ToolSwitchesOfDispatch reads the immutable profile policy and, for a Bot Run, caps it by the Bot's current policy.
func (s *Service) ToolSwitchesOfDispatch(ctx context.Context, dispatchID string) (agenttool.ToolSwitches, error) {
	return dispatchToolSwitches(ctx, s.read(), dispatchID)
}

func dispatchToolSwitches(ctx context.Context, q *store.Queries, dispatchID string) (agenttool.ToolSwitches, error) {
	row, err := q.ToolSwitchesOfDispatch(ctx, dispatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return agenttool.ToolSwitches{}, nil
	}

	if err != nil {
		return agenttool.ToolSwitches{}, fmt.Errorf("reading the tool switches of dispatch %s: %w", dispatchID, err)
	}

	policy := agenttool.ToolSwitches{Preset: row.Preset, On: splitNames(row.Tools), Off: splitNames(row.ToolsOff)}
	if row.BotID != "" {
		policy.Parent = &agenttool.ToolSwitches{
			Preset: row.BotPreset, On: splitNames(row.BotTools), Off: splitNames(row.BotToolsOff),
		}
		if row.BotPreset == "" {
			policy.Parent.Off = []string{"*"}
		}
	}

	return policy, nil
}
