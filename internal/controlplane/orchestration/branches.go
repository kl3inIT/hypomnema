package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// The values of NewTask.PRBase (issue #690): the pull request of a Task that starts from another branch targets
// that branch, stacked, or the Run's base.
const (
	PRBaseStartFrom = "start_from"
	PRBaseRunBase   = "run_base"
)

// taskBranches are the branches a Task works with, as stored: an empty one is the default (its own hyp/<id>, the
// Run's base).
type taskBranches struct {
	continues, push, start, prBase string
}

// TaskPushBranch is the branch a Task's publish_branch pushes to: the branch it continues, else its own.
func TaskPushBranch(id, push string) string {
	if push != "" {
		return push
	}

	return "hyp/" + id
}

func pushBranchOf(t store.Task) string { return TaskPushBranch(t.ID, t.PushBranch.String) }

// resolveBranches checks a new Task's branches and turns them into what is stored (issue #690). A Task that
// continues another works on that Task's branch and pushes into it, so its pull request takes the commits, as
// Orca's PR worktree pushes to the PR's head (pr-start-point.ts:226-245) and t3code's sets its upstream to it
// (GitManager.ts:1013-1069). One that starts from a branch makes its worktree there and opens its pull request
// against it or the Run's base, as t3code's baseRef for stacked work (orchestrator-mcp-server.md:354-355).
func resolveBranches(ctx context.Context, q *store.Queries, run store.Run, nt NewTask) (taskBranches, error) {
	if nt.Continues == "" && nt.StartFrom == "" && nt.PRBase == "" {
		return taskBranches{}, nil
	}

	switch {
	case run.Repo.String == "":
		return taskBranches{}, fmt.Errorf("%w: task %q names a branch, but run %s has no repository",
			ErrInvalid, nt.Ref, run.ID)
	case nt.Continues != "" && (nt.StartFrom != "" || nt.PRBase != ""):
		return taskBranches{}, fmt.Errorf("%w: task %q continues a task: it takes neither start_from nor pr_base",
			ErrInvalid, nt.Ref)
	case nt.PRBase != "" && nt.StartFrom == "":
		return taskBranches{}, fmt.Errorf("%w: task %q names pr_base without start_from", ErrInvalid, nt.Ref)
	case nt.PRBase != "" && nt.PRBase != PRBaseStartFrom && nt.PRBase != PRBaseRunBase:
		return taskBranches{}, fmt.Errorf("%w: task %q: pr_base is %s or %s", ErrInvalid, nt.Ref,
			PRBaseStartFrom, PRBaseRunBase)
	case nt.Continues != "":
		return continuedBranches(ctx, q, run, nt.Continues)
	}

	start, err := startBranch(ctx, q, run, nt.StartFrom)
	if err != nil {
		return taskBranches{}, err
	}

	if start == run.Base.String {
		return taskBranches{}, nil
	}

	b := taskBranches{start: start}
	if nt.PRBase != PRBaseRunBase {
		b.prBase = start
	}

	return b, nil
}

// continuedBranches are the branches of a Task that continues id: it starts from and pushes to id's branch, and a
// pull request it has to open targets what id's targeted.
func continuedBranches(ctx context.Context, q *store.Queries, run store.Run, id string) (taskBranches, error) {
	earlier, earlierRun, err := repoTask(ctx, q, run, id)
	if err != nil {
		return taskBranches{}, err
	}

	push := pushBranchOf(earlier)
	if err := branchFree(ctx, q, push, ""); err != nil {
		return taskBranches{}, err
	}

	b := taskBranches{continues: earlier.ID, push: push, start: push}
	if base := or(earlier.PrBase.String, earlierRun.Base.String); base != run.Base.String {
		b.prBase = base
	}

	return b, nil
}

// startBranch checks the branch a Task starts from. A Task's branch hyp/<id> is the branch that Task pushes to, so
// starting from a Task that continues another starts from the branch they share.
func startBranch(ctx context.Context, q *store.Queries, run store.Run, name string) (string, error) {
	if !ValidBranchName(name) {
		return "", fmt.Errorf("%w: %q is not a branch name", ErrInvalid, name)
	}

	id, ok := strings.CutPrefix(name, "hyp/")
	if !ok {
		return name, nil
	}

	parent, _, err := repoTask(ctx, q, run, id)
	if err != nil {
		return "", err
	}

	return pushBranchOf(parent), nil
}

// repoTask is the Task id, of any Run of run's repository: a Task of a finished Run still has its branch.
func repoTask(ctx context.Context, q *store.Queries, run store.Run, id string) (store.Task, store.Run, error) {
	t, err := q.GetTask(ctx, id)
	if err != nil {
		return store.Task{}, store.Run{}, notFound(err, "task", id)
	}

	r, err := q.GetRun(ctx, t.RunID)
	if err != nil {
		return store.Task{}, store.Run{}, notFound(err, "run", t.RunID)
	}

	if r.RepoHost.String != run.RepoHost.String || !strings.EqualFold(r.Repo.String, run.Repo.String) {
		return store.Task{}, store.Run{}, fmt.Errorf("%w: task %s works on %s/%s, not on run %s's repository %s/%s",
			ErrConflict, id, r.RepoHost.String, r.Repo.String, run.ID, run.RepoHost.String, run.Repo.String)
	}

	return t, r, nil
}

// branchFree fails when an unfinished Task other than except pushes to branch: two workers pushing to one branch
// would race, as t3code refuses a second worktree on an existing branch (ThreadLaunchService.ts:443-448). The
// node's push is never forced, so a push on a branch that moved is refused anyway.
func branchFree(ctx context.Context, q *store.Queries, branch, except string) error {
	other, err := q.UnfinishedTaskPushingTo(ctx, store.UnfinishedTaskPushingToParams{Except: except, Branch: branch})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("reading the tasks that push to %s: %w", branch, err)
	default:
		return fmt.Errorf("%w: task %s, which is not finished, already pushes to %s", ErrConflict, other, branch)
	}
}

// ValidBranchName reports whether name is a branch name git takes (git check-ref-format --branch), and one that
// cannot be read as an option.
func ValidBranchName(name string) bool {
	if name == "" || len(name) > 255 || name == "@" || name == "HEAD" || strings.HasPrefix(name, "-") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") ||
		strings.ContainsAny(name, " ~^:?*[\\") {
		return false
	}

	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return false
		}
	}

	for part := range strings.SplitSeq(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}

	return true
}
