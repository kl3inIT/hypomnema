package orchestration

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func (s *Service) CheckWorktreeCleanup(ctx context.Context, nodeID string, paths []nodeproto.WorktreePath) (
	[]nodeproto.CleanupWorktree, error,
) {
	if len(paths) > nodeproto.CleanupBatchSize {
		return nil, fmt.Errorf("%w: cleanup batch is too large", ErrInvalid)
	}

	q := s.read()
	node, err := q.GetNode(ctx, nodeID)
	if err != nil {
		return nil, notFound(err, "node", nodeID)
	}

	if !path.IsAbs(node.WorktreeRoot.String) || NodeStatus(node.Status) == NodeDeleted {
		return nil, nil
	}

	var out []nodeproto.CleanupWorktree
	for _, candidate := range paths {
		if !platform.IsID("run", candidate.RunID) || !platform.IsID("tsk", candidate.TaskID) ||
			candidate.Path != path.Join(node.WorktreeRoot.String, candidate.RunID, candidate.TaskID) {
			continue
		}

		rows, err := q.WorktreeDispatches(ctx, store.WorktreeDispatchesParams{NodeID: nodeID, Worktree: candidate.Path})
		if err != nil {
			return nil, err
		}

		if len(rows) == 0 || len(rows) > 256 || slices.ContainsFunc(rows, func(row store.WorktreeDispatchesRow) bool {
			return row.TaskID != candidate.TaskID || row.RunID != candidate.RunID || !row.SettledAt.Valid ||
				!DispatchStatus(row.Status).settled() ||
				(RunStatus(row.RunStatus) != RunFinished && RunStatus(row.RunStatus) != RunCanceled)
		}) {
			continue
		}

		approved := nodeproto.CleanupWorktree{WorktreePath: candidate, HasRepo: rows[0].Repo.String != ""}
		for _, row := range rows {
			approved.Dispatches = append(approved.Dispatches, row.ID)
			if row.AcceptedAt.Valid {
				approved.Accepted = append(approved.Accepted, row.ID)
			}
		}

		if len(approved.Accepted) > 0 {
			out = append(out, approved)
		}
	}

	return out, nil
}

func checkManagedWorktree(root, worktree, runID, taskID string) error {
	if !path.IsAbs(root) {
		return nil
	}

	root = path.Clean(root)
	prefix := strings.TrimRight(root, "/") + "/"
	clean := path.Clean(worktree)
	rel, inside := strings.CutPrefix(clean, prefix)
	runDir, _, _ := strings.Cut(rel, "/")
	if clean == root || (inside && platform.IsID("run", runDir) && worktree != path.Join(root, runID, taskID)) {
		return fmt.Errorf("%w: a managed worktree must belong to its Run and Task", ErrInvalid)
	}

	return nil
}
