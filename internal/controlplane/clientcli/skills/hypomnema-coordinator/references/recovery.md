# Recovery

Act only on what `hypomnema tasks show <task_id> --json` and `hypomnema runs show <run_id> --json` say.

## A failed Task

The control plane has already retried it. A worker's reported failure, a failed start and a lost worker each count;
the third counted failure fails the Task, with a back-off before each retry. A rate limit moves the Task to another
account and does not count. The Task's `result` names the attempts and the last cause.

- Report the Task as failed with its last cause. Its work has already used the owner's accounts three times, and
  the count is what stops that from going on, so do not go around it on your own: no reopen, and no new Task for
  the same work. (Before a Task fails, replacing a spec you got wrong is yours to do; see below.)
- When the latest Dispatch's result shows the cause is in the spec (missing context, a wrong assumption), say so
  and offer the corrected spec. Create it as a new Task once the owner agrees.
- Reopen the same Task with `hypomnema tasks reopen <task_id> --json` when the owner asks, for example after fixing
  the cause outside the spec. Reopening resets the count.

## A blocked Task

`blocked_reason` says what it waits for:

- `gate`: a decision the worker asked of the owner. The Task continues when the owner answers; tell the owner it
  waits.
- `needs_auth`: an account on a node needs a login. Tell the owner; once the owner has logged in, run
  `hypomnema tasks reopen <task_id> --json`.
- `dependency_failed`: a Task it depends on failed or was canceled. Resolve that Task first; when you replace it, the
  Tasks that depended on it need replacing too, depending on the new one.
- `dispatch_unknown`: the control plane cannot prove what the worker did, because its node went silent. Tell the
  owner; the record settles when the node reports.

## Replacing a Task

When a spec you wrote turns out wrong before its Task has failed, cancel the Task, create a corrected one, and
create again the Tasks that depended on it, depending on the new one. Tell the owner what changed.

## Stopping work

- `hypomnema tasks cancel <task_id> --json` stops a running worker before the Task is canceled. Until the stop is
  confirmed the Task has `cancel_requested_at`; report it as stopping, not stopped.
- `hypomnema runs cancel <run_id> --json` does the same for every unfinished Task of the Run. Use it only when the
  owner gives up the Run.
