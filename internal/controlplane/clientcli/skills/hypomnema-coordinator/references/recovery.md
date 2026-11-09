# Recovery

Act only on what `hypomnema tasks show <task_id> --json` and `hypomnema runs show <run_id> --json` say.

## A failed Task

The control plane has already retried it. A worker's reported failure, a failed start and a lost worker each count;
the third counted failure fails the Task, with a back-off before each retry. A rate limit moves the Task to another
account and does not count. The Task's `result` names the attempts and the last cause.

- Read the latest Dispatch's result. When the cause is in the spec (missing context, a wrong assumption), create a
  new Task with a corrected spec and say why in your report.
- When the result names no such cause (only `worker_error`, say), the retries have already happened: report the
  Task as failed with its last cause, and offer the owner to reopen it.
- Reopen the same Task with `hypomnema tasks reopen <task_id> --json` only when the owner asks, or after the owner
  fixed the cause outside the spec. Reopening resets the count, and the count is what stops a failing Task from
  using the owner's accounts again and again.

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

## Stopping work

- `hypomnema tasks cancel <task_id> --json` stops a running worker before the Task is canceled. Until the stop is
  confirmed the Task has `cancel_requested_at`; report it as stopping, not stopped.
- `hypomnema runs cancel <run_id> --json` does the same for every unfinished Task of the Run. Use it when the owner
  gives up the Run.
