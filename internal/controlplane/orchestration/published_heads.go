package orchestration

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// PublishedHead is a branch update accepted by the Git host and reported by the node.
type PublishedHead struct {
	Branch string
	SHA    string
}

// RecordPublishedHead binds first-hand Git push evidence to its Dispatch's Run.
func (s *Service) RecordPublishedHead(ctx context.Context, call NodeCall, head PublishedHead) error {
	if _, err := hex.DecodeString(head.SHA); err != nil || len(head.SHA) != 40 || head.SHA != strings.ToLower(head.SHA) {
		return fmt.Errorf("%w: expected a GitHub commit SHA", ErrInvalid)
	}
	return nodeVoid(ctx, s, call, "published_head", head, func(q *store.Queries, now int64, d store.Dispatch) error {
		if err := checkEpoch(d, call); err != nil {
			return err
		}
		if d.NodeID == "" || !d.StartedAt.Valid || head.Branch != "hyp/"+d.TaskID {
			return fmt.Errorf("%w: push must update the branch of a started dispatch", ErrConflict)
		}
		task, err := q.GetTask(ctx, d.TaskID)
		if err != nil {
			return err
		}
		run, err := q.GetRun(ctx, task.RunID)
		if err != nil {
			return err
		}
		if run.RepoHost.String != githost.GitHubHost || run.Repo.String == "" {
			return fmt.Errorf("%w: push evidence requires a GitHub Run repository", ErrConflict)
		}
		return q.RecordPublishedHead(ctx, store.RecordPublishedHeadParams{
			DispatchID: d.ID, Branch: head.Branch, Sha: nullString(head.SHA), Now: now,
		})
	})
}

// ListPublishedHeads returns the push evidence of all Dispatches in a Run, not just the latest attempt.
func (s *Service) ListPublishedHeads(ctx context.Context, runID string) ([]PublishedHead, error) {
	rows, err := s.read().ListPublishedHeads(ctx, runID)
	if err != nil {
		return nil, err
	}
	heads := make([]PublishedHead, 0, len(rows))
	for _, row := range rows {
		heads = append(heads, PublishedHead{Branch: row.Branch, SHA: row.Sha})
	}
	return heads, nil
}
