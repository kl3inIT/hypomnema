package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

func checkTools(preset string, on, off []string) error {
	switch agenttool.DefaultPreset(preset) {
	case agenttool.PresetFull, agenttool.PresetReport:
	default:
		return fmt.Errorf("%w: preset %q must be full or report", ErrInvalid, preset)
	}

	if err := agenttool.CheckToolSwitches(on, off); err != nil {
		return fmt.Errorf("%w: tools: %w", ErrInvalid, err)
	}

	return nil
}

// ToolSwitches are the tool switches of a Bot's pi: its Bot's, read now, so
// a change reaches a pi already running at its next call.
func (b Bot) ToolSwitches() agenttool.ToolSwitches {
	return agenttool.ToolSwitches{Preset: b.Preset, On: b.Tools, Off: b.ToolsOff}
}

func (p Profile) ToolSwitches() agenttool.ToolSwitches {
	return agenttool.ToolSwitches{Preset: p.Preset, On: p.Tools, Off: p.ToolsOff}
}

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
