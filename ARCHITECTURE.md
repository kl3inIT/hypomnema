# Hypomnema architecture

This page states what is implemented today, and nothing else. The target is in [docs/vision.md](docs/vision.md);
planned work is in GitHub issues and never appears here as a fact. Update this page in the change that makes a
statement true or false.

## Binaries

| Binary | Commands | What it does today |
|---|---|---|
| `cmd/hypomnema` (control plane) | `migrate`, `serve`, `version` | `migrate` brings the database schema up to the binary's version; `serve` refuses any other schema, then serves `/livez` and `/readyz` |
| `cmd/hypomnema-node` (node) | `version` | Prints its version |

Configuration comes from the environment: `HYPOMNEMA_HTTP_ADDR` (default `127.0.0.1:8080`),
`HYPOMNEMA_DATABASE_PATH` (required, absolute), `HYPOMNEMA_LOG_LEVEL`, `HYPOMNEMA_LOG_FORMAT` (`json` or `text`).
Logs are JSON on stdout; `serve` stops on SIGINT or SIGTERM and lets requests in flight finish. `/readyz` answers 503
with `Retry-After` while the database does not answer.

The database ([decision](docs/decisions/2026-11-07-sqlite.md)) is one SQLite file opened by `internal/shared/sqlite`:
one connection, WAL, `synchronous=FULL`, pragmas read back after opening; its directory is made 0700 and a new file
0600, and an existing file that group or others can access, or that another user owns, is refused (Linux).

Migrations are goose SQL files embedded in the binary (`internal/controlplane/orchestration/migrations`); the schema
holds Runs, Tasks, Task dependencies, Dispatches, Conversations, receipts, Dispatch effects and resources, account
cooldowns, questions, the event log, nodes and accounts. `internal/controlplane/orchestration` is their only writer: named operations, each one `BEGIN IMMEDIATE`
transaction, every status change a guarded `UPDATE` checked against a transition table (`transitions.go`).

- Runs and Tasks: create a Run, create Tasks with dependencies, cancel or reopen a Task, finish or cancel a Run. A
  Task becomes ready when all its dependencies succeeded; a failed or canceled Task blocks every Task downstream of
  it; reopening a failed Task resets its failure count and unblocks them.
- Dispatches: the coordinator creates one for a ready Task, in a new Conversation or continuing the open one; a node
  accepts it (a new epoch and a 90 s lease), reports it started, renews the lease, reports progress, effects
  (intent, then done) and resources, and reports the outcome. A lost lease or an exit without a report makes the
  outcome unknown and blocks the Task; a report from the same epoch, a reattach by the same node and process, a
  settlement by the coordinator or an abandon ends it. A cancel stops the Dispatch and cancels the Task only once
  the stop is confirmed. Worker errors, failed launches and lost Dispatches count toward a breaker of three with a
  back-off before each retry; rate limits cool the account (runtime, account) down on every node and are not counted; a login
  that is needed blocks the Task. Every node call carries a request id and is answered once (receipts).
- `Reap` applies the timers in `limits.go`: queued Dispatches expire after 5 min, lost leases and unconfirmed stops
  are marked, stalls (15 min without progress), Dispatches past 4 h and nodes with three expiries in a row are
  returned as escalations, as are open questions to the owner after 1 h, then daily; nothing is killed.
- Questions: a worker asks the coordinator or the owner. A `wait` question keeps the worker running, pauses its stall
  and duration timers, and its answer reaches the node in the heartbeat reply until the node acknowledges it. A
  Gate is a final report: the Dispatch ends `gated`, the Task is blocked until the answer, and the next Dispatch
  resumes the Conversation with the question and answer. The first answer wins; the coordinator answers only what
  is addressed to it and can forward a question to the owner. Open questions close when their Dispatch settles (a
  `wait` question) or their Task is canceled (a Gate); a Gate never expires.

- Events: every change writes its events in the same transaction to one log with an increasing `seq`: Runs,
  Tasks and Dispatches with the status they left, Conversations closed, questions opened, answered, forwarded or
  canceled, and the reaper's escalations. Lease renewals, progress, effects and resources are not events. With one
  writer, a later commit always has higher `seq` values, so a reader following `ListEvents` (cursor, limit up to
  500, `has_more`, filters by Run or Task) misses nothing. `Subscribe` wakes readers after each commit with a
  signal that carries no data.

- Nodes and accounts: the owner adds a node, which mints a token shown once (only its SHA-256 is stored); the node
  reports its CLIs (path and version) and worktree root at each connect, and is connected while heard from within
  30 s (any node call counts). A node can be drained and resumed; three expiries in a row mark it unavailable until
  its next hello or a resume. Accounts (runtime, label, position, active or disabled) are kept here with no secret
  value.
- Scheduler: `Schedule` is one pass in one transaction, and `Loop` runs `Reap` and `Schedule` every 2 s and on each
  commit signal. At most 4 Dispatches that have not settled exist at once, one waiting on a question included; the
  coordinator's `CreateDispatch` meets the same limit and checks. Ready Tasks of open Runs go oldest Run first, then
  by position; the runtime, model and effort are the Task's own or its role's default, and the Dispatch records the
  model and effort. An open Conversation continues only on its node, once that node is active, connected, not
  unavailable and below its limit; a new one goes to the least loaded such node that has the runtime and a worktree
  root, ties by id, with the first active account of the runtime that is not cooling down, in
  `<worktree root>/<run>/<task>`. A Task with no node or no account for 5 min is escalated once (`unplaceable`), and
  a rate limit over an hour that leaves its runtime with no free account is escalated (`account_cooling`).

Nothing calls these operations from outside the process yet: there is no node API, node daemon, vault or mailbox.

## Code

```
internal/controlplane     package controlplane: configuration, migrate and serve
internal/controlplane/orchestration  Runs, Tasks, Dispatches, Conversations, questions, events, nodes, accounts, the scheduler; migrations; queries generated by sqlc (internal/store)
internal/shared/platform  environment settings, slog logger, HTTP server and probes, version, clock, ids
internal/shared/sqlite    SQLite opening, pragmas and file permissions
```

There is no `internal/node` and no vault yet. Boundaries ([decision](docs/decisions/2026-11-07-two-deployables.md))
are checked by `make verify`: depguard rules per tree in `.golangci.yml` (the SQLite driver only in `shared/sqlite`,
`database/sql` only in packages that own tables), and `deps-check`, which refuses the control plane in the node binary
and the node in the control plane binary.

## Verification

- `make verify`: gofmt, `go mod tidy -diff` for both modules, golangci-lint (`.golangci.yml`, tools pinned in
  `tools/ci/go.mod`), the dependency check, `sqlc-check` (generated queries match `sqlc generate`),
  `migration-check` (migrations already on `origin/main` are unchanged), unit tests and race tests.
- Tests that need a database open a real file in a temporary directory; no containers.
- CI (`.github/workflows/ci.yml`) runs `make verify` and `govulncheck` on every pull request and on `main`.
