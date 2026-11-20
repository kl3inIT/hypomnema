-- A Task may work on another branch than its own hyp/<id> (issue #690). continues: the Task whose branch and pull
-- request it continues; push_branch: the branch publish_branch pushes to, NULL for hyp/<id>; start_from: the branch
-- its worktree starts from, NULL for the Run's base; pr_base: the base of the pull request it opens, NULL for the
-- Run's base. As t3code's workspaceStrategy baseRef (ThreadLaunchService.ts:69-79) and Orca's pushTarget
-- (pr-start-point.ts:226-245).

-- +goose Up
ALTER TABLE tasks ADD COLUMN continues TEXT;
ALTER TABLE tasks ADD COLUMN push_branch TEXT;
ALTER TABLE tasks ADD COLUMN start_from TEXT;
ALTER TABLE tasks ADD COLUMN pr_base TEXT;
ALTER TABLE nodes ADD COLUMN task_branches INTEGER NOT NULL DEFAULT 0;

-- One unfinished Task per pushed branch is looked up by it.
CREATE INDEX tasks_push_branch ON tasks (push_branch) WHERE push_branch IS NOT NULL;

-- +goose Down
DROP INDEX tasks_push_branch;
ALTER TABLE nodes DROP COLUMN task_branches;
ALTER TABLE tasks DROP COLUMN pr_base;
ALTER TABLE tasks DROP COLUMN start_from;
ALTER TABLE tasks DROP COLUMN push_branch;
ALTER TABLE tasks DROP COLUMN continues;
