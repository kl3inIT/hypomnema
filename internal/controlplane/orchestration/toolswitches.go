package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// checkTools checks the tools a profile or a Bot keeps and turns off (issue
// #536, step 7): each a tool's name or a family that names one, named once,
// and not in both lists, as checkSkills checks its skills.
func checkTools(on, off []string) error {
	if err := agenttool.CheckToolSwitches(on, off); err != nil {
		return fmt.Errorf("%w: tools: %w", ErrInvalid, err)
	}

	return nil
}

// ToolSwitches are the tool switches of a Bot's pi: its Bot's, read now, so
// a change reaches a pi already running at its next call.
func (b Bot) ToolSwitches() agenttool.ToolSwitches {
	return agenttool.ToolSwitches{On: b.Tools, Off: b.ToolsOff}
}

// ToolSwitchesOfDispatch are the tool switches of a worker: those of the
// profile version its Dispatch runs with, as the skills it was given are
// (recordSkills). A Dispatch made before profiles, with no profile version,
// has none.
func (s *Service) ToolSwitchesOfDispatch(ctx context.Context, dispatchID string) (agenttool.ToolSwitches, error) {
	row, err := s.read().ToolSwitchesOfDispatch(ctx, dispatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return agenttool.ToolSwitches{}, nil
	}

	if err != nil {
		return agenttool.ToolSwitches{}, fmt.Errorf("reading the tool switches of dispatch %s: %w", dispatchID, err)
	}

	return agenttool.ToolSwitches{On: splitNames(row.Tools), Off: splitNames(row.ToolsOff)}, nil
}
