# Recovery

Act only on what `hypomnema tasks show <task_id> --json` and `hypomnema runs show <run_id> --json` say.

## Flaky tests and repeat runs

Copy these rules into any Task spec about a flaky test or asking for repeated test runs (owner, 2026-11-28,
issue #1313):

> Run one package's repeats at a time. For Go, set `GOMAXPROCS=4` and pass `-p 2` to `go test`. For Playwright,
> pass `--workers=2`. Do not start a busy-loop load generator. Run `free -g` between runs. If available memory
> falls below about 20% of total memory, stop the repeats and report the memory stop in the Task result.

## A failed Task

The control plane has already retried it. A worker's reported failure, a failed start and a lost worker each count;
the third counted failure fails the Task, with a back-off before each retry. A rate limit moves the Task to another
account and does not count. The Task's `result` names the attempts and the last cause.

- Report the Task as failed with its last cause. Its work has already used the owner's accounts three times, and
  the count is what stops that from going on, so do not go around it on your own: no reopen, and no new Task for
  the same work. (Before a Task fails, replacing a spec you got wrong is yours to do; see below.)
- When the latest Dispatch's result shows the cause is in the spec (missing context, a wrong assumption), say so
  and offer the corrected spec. Create it as a new Task once the owner agrees.
- Offer a retry that fits the last cause. Out of time or memory: a smaller scope, such as the work split into
  narrower Tasks. A tool or model error: the same spec on another runtime, which the owner names. A Task that
  failed has had its two retries: never offer a third identical attempt; report it and stop.
- Reopen the same Task with `hypomnema tasks reopen <task_id> --json` when the owner asks, for example after fixing
  the cause outside the spec. Reopening resets the count.

## A blocked Task

`blocked_reason` says what it waits for:

- `gate`: a decision the worker asked of the owner. The Task continues when the owner answers; tell the owner it
  waits. When the owner, or a review, gives the worker more to do meanwhile, send it as a follow-up (`hypomnema
  tasks follow-up`): it replaces the gate.
- `needs_auth`: an account on a node needs a login. Tell the owner; once the owner has logged in, run
  `hypomnema tasks reopen <task_id> --json`.
- `dependency_failed`: a Task it depends on failed or was canceled. Resolve that Task first; when you replace it, the
  Tasks that depended on it need replacing too, depending on the new one.
- `dispatch_unknown`: the control plane cannot prove what the worker did, because its node went silent. Tell the
  owner. It settles when the node reports or reattaches, or when the node next connects: a Dispatch the node no
  longer holds, or whose worker never started, ends failed and the Task is tried again (or canceled, if a cancel
  was asked). No command settles it sooner, and nothing settles it while the node stays away.

## Replacing a Task

When a spec you wrote turns out wrong before its Task has failed, cancel the Task, create a corrected one, and
create again the Tasks that depended on it, depending on the new one. Tell the owner what changed.

## Stopping work

- `hypomnema tasks cancel <task_id> --json` stops a running worker before the Task is canceled. Until the stop is
  confirmed the Task has `cancel_requested_at`; report it as stopping, not stopped.
- `hypomnema runs cancel <run_id> --json` does the same for every unfinished Task of the Run. Use it only when the
  owner gives up the Run.
