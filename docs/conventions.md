# Conventions

Engineering rules for all code. Working rules for agents are in [AGENTS.md](../AGENTS.md); terms are in the
[glossary](glossary.md). Rules adapted from `second-bite` and `BeyondPilot` are marked with their origin only where the
reason is not obvious.

## Change design

- Build the smallest complete production path, not scaffolding for a hypothetical one.
- No interface, adapter, configuration mode or deployment unit without a current caller and an owned lifecycle. The
  one deliberate exception is [Strategy behind a registry](#interchangeable-implementations-strategy-behind-a-registry).
- Clean cutover: migrate every caller and remove obsolete code, configuration, comments and tests in the same change.
- Never ship a temporary runtime mode, one-shot profile, convenience endpoint or unused abstraction to make an
  incomplete flow work. Disposable helpers stay in tests or scratch files.
- A new dependency edge between modules changes [ARCHITECTURE.md](../ARCHITECTURE.md) and the boundary check in the
  same change.

## Interchangeable implementations: Strategy behind a registry

Use it when one function is performed by one of several interchangeable implementations chosen by configuration or
stored data. In Hypomnema these families exist from the start:

| Function | Implementations |
|---|---|
| Runtime (drive a worker) | `claude-cli`, `codex`, `pi` |
| Executor (reach a node) | `node`, `ssh` |
| Machine provider (later) | EC2, Hetzner, Modal, Daytona, E2B… |
| Model provider for the brain | per provider |
| Channel | Telegram, later web, Lark |

- One interface per function, named after the function (`Runtime`, `Executor`); one type per implementation, named
  after the vendor and function (`CodexRuntime`, `SSHExecutor`), inside the owning module.
- One registry resolves an implementation by key and fails at startup on a missing or duplicate key.
- The key is identity only. What an implementation supports comes from a **capabilities** value it returns (for a
  Runtime: integration mode, how it asks questions, resume, authentication), not from flags on the key.
- Shared mechanics (process supervision, timeouts, retries, metrics, output validation) live once and delegate to the
  implementation.
- Do not use the pattern for code that has one implementation by nature, such as a module's own store.

## Reference-based design and scope control

- Production readiness for the agreed scope is required from the start: correct behaviour, authorization, data
  integrity, bounded resources, failure handling, observability. Scope control never means shipping a prototype.
- Scope creep is adding behaviour or mechanism without showing why the agreed scope needs it. Establish need from
  requirements, source analysis, credible failure scenarios or measurements.
- Design from references read in their code: Orca, Omnara, OpenClaw, Omnigent, durable-harness, Infisical, pi, and the
  owner's own repositories. Cite the revision and path. Separate observed behaviour, inferred rationale, proposed
  change and accepted decision.
- Before departing from a reference, write down the requirement, the reference behaviour and its strengths, the
  difference, its benefit and cost, and the simpler baseline. If the benefit does not pay for the cost, keep the
  baseline.
- Do not hand-write what a maintained library does correctly. Weigh a dependency by what it removes, its maintenance,
  license, transitive weight and fit. Exception: cryptography in the vault uses Go's standard library and `x/crypto`
  only.
- Do not prebuild workers, queues, registries or plugin mechanisms for hypothetical future use.
- Carry accepted decisions across turns; do not reopen rejected ones or silently widen scope.

## Boundary discovery

Short form of BeyondPilot's sequence, sized for one owner and a mostly technical domain. Every new module, and every
material change to a boundary:

1. **Glossary**: every term the module introduces has one meaning in the [glossary](glossary.md).
2. **Owner**: one module is the source of truth for each table, lifecycle and invariant. No two modules write the same
   table or import each other's persistence package.
3. **Context map**: what the module provides and requires, the direction of each dependency, which are calls and
   which are events, and what happens on failure.
4. **Package module first**: the smallest complete vertical slice as a package with a narrow public root.
5. **Physical split only with evidence**: a new deployable needs a security, placement, scaling or failure boundary,
   recorded in an ADR. The three deployables have theirs: the node runs on every machine; the vault keeps secrets
   away from the host that reads untrusted text.

## Module layout

Each deployable is a Go modular monolith under `internal/`, with `cmd/<deployable>` as its entry point.

- **Edge modules are vertical slices**, as in `second-bite/server`: `identity`, `channel`, `brain`, `integration`,
  `mcpbridge`, `audit`. Each owns its tables (prefixed by the module name), its queries and its handlers. Its root
  package is its published API; everything else is under its own `internal/`.
- **The execution core is one module**, as Omnara's storage layer: `execution` owns Run, Task, Dispatch, Gate,
  leases, node registry and placement. These tables change together under concurrency (claiming, lease expiry, node
  loss, session revocation), so one module holds one lock order and exposes only named operations
  (`ClaimNextDispatch`, `ExpireLeases`, `MarkNodeLost`…). No caller writes its own query against these tables.
- `internal/platform` holds technical code shared by every module and depends on none.
- **Enforcement**, checked by `make verify` in CI:
  - `boundaries_test.go` reads the allowed module dependencies from ARCHITECTURE.md (as second-bite);
  - `depguard` in `.golangci.yml` forbids importing another module's `internal/` and generated sqlc packages outside
    their owner, and confines the node's SQLite to its `statedb` package (as Omnara);
  - `sqlc vet` rules for query invariants.

## Execution core

- **Transitions are guarded writes**: one transaction, `UPDATE … WHERE id = $1 AND state = ANY($allowed)`, which must
  change exactly one row or fail with a conflict. The allowed transitions are listed in one place in code.
- **One lock order** for every transaction that touches more than one entity, written in the module and followed by
  every operation.
- **Claiming** uses `FOR UPDATE SKIP LOCKED` and a lease with a heartbeat. A worker or node that lost its lease cannot
  write; a reaper releases expired leases.
- **Fencing**: every Dispatch has an `epoch`. Every write from a node is a compare-and-swap on
  `(dispatch_id, epoch, state)`; a stale epoch is refused.
- **Unknown is a state**: `start_unknown`, `stop_unknown` and similar states exist wherever an outcome cannot be
  proven. Silence or a timeout never proves that a process exited.
- **Replay policy**: every effect with consequences outside the control plane (push, opening a PR, a merge request, a
  message) is declared `safe` or `never`. Intent is recorded before the effect and the outcome after. A `never`
  effect interrupted by a crash is never repeated; the Brain receives a recovery note saying what finished and what
  may have taken effect, and reconciles (for example by reading GitHub).
- **Idempotency**: every mutation from the Brain, the CLI, a channel or a node carries a request id. A receipt keyed by
  caller and request id stores the payload hash; a reused id with another payload is refused; a lost reply can be
  checked instead of repeated.
- **Failure propagates**: when a Task fails or is cancelled, its dependents become `blocked` with a reason. A retry
  chain carries the failure count; a circuit breaker stops a Task after repeated failures.
- **Gates have a timeout** and a reminder; only the owner's answer unblocks a Task.
- **Mailbox**: typed messages ordered by sequence; a reader receives the same unacknowledged batch until it
  acknowledges; message ids are derived from content so a repeated report is a duplicate, not a new message.

## Events between modules

- A **call** to another module's published API when the caller needs the answer to go on, when the data is a secret,
  or when two writes must commit together.
- An **event** when the change is complete without the listener: a side effect, a notice, another module catching up,
  or when a call would create a dependency cycle.
- Never ask through an event and wait for a reply. An event states a past fact (`execution.DispatchCompletedEvent`,
  key `execution.dispatch_completed`), carries identifiers only, and is written to the outbox (River) in the
  transaction that made the change. Listeners run after the commit, are idempotent, and do not depend on order.

## API

- The control plane's HTTP API is defined in code with `huma`, which generates the OpenAPI 3.1 contract. A change to
  the API regenerates the contract and the clients in the same change; a contract test checks that code and contract
  agree.
- Every error is an RFC 9457 problem with a stable `code` and the request id. Every request has an id, returned in
  `X-Request-Id` and logged.
- Client events (web, mobile) are served over SSE from the event log, resumable with `Last-Event-ID`. A message sent
  by a client carries a dedupe key; a retry never starts a second run.

## Data and security

- Chat text, issue text, web pages and tool output are **untrusted input** while agents hold credentials.
- Only the owner, or an allowlist, can assign work. A public channel never starts work directly; it goes through a
  Gate.
- Workers never hold a raw secret. The node's egress proxy attaches credentials from a vault Session scoped to one
  Dispatch; workers see placeholders. Egress is deny by default per Dispatch.
- Policies answer ALLOW, DENY or ASK and **fail closed**. They are evaluated in Go behind `mcpbridge` and the node
  proxy; the pi extension only forwards. Spawn bounds and blast radius limits apply to the Brain.
- **Trusted machines, untrusted content.** The owner and the owner's machines are trusted; what agents read is not.
  As Orca does, the isolation unit is the machine: no nested sandbox inside a node by default, because it costs setup
  on every host, breaks easily and slows every command for little gain on a dedicated VM. What is always on, being
  cheap: tokens scoped to the repositories of the Dispatch and without merge rights (the owner merges at Gate 2); one
  worktree per Dispatch; work accepted only from the owner; no unrelated secrets on a worker host. A node should be a
  VM used only by Hypomnema.
- Stronger isolation is opt-in: the CLI's own sandbox (Claude Code `sandbox` settings, Codex `workspace-write`) for a
  node that needs it (it requires `bubblewrap` and, on Ubuntu, an AppArmor profile), and a disposable cloud sandbox
  per Dispatch, as Omnara's pools, for work from untrusted sources. Flags that disable a CLI's own permission checks
  are allowed only inside such a sandbox.
- The vault encrypts with AES-256-GCM in a three-level key hierarchy; the root key lives only on the vault host, with
  an offline backup and a rotation procedure.
- Logs and transcripts are scrubbed of secret patterns before they are stored.

## Testing

- Every test names the observable contract and the regression it would catch, at the smallest boundary that can
  detect it.
- Persistence and concurrency are tested against a real Postgres (testcontainers): guarded transitions, lease expiry,
  two claimers on one Dispatch, stale epochs.
- Runtime adapters are tested against recorded CLI output, plus a live test per runtime run on demand.
- Node and control plane share a resilience contract: dropped connections, restarts mid-Dispatch and duplicated
  messages are tested with a fault proxy.

## Go

- One `go.mod` at the repository root; the Go version is pinned there. `golangci-lint` configuration in the root.
- Errors are wrapped with context (`fmt.Errorf("claim dispatch: %w", err)`) and matched with `errors.Is` / `errors.As`;
  expected failures have typed errors.
- Every blocking call takes a `context.Context`; long-running loops stop on cancellation.
- Logging with `log/slog`, structured, with the request id, run id and dispatch id as attributes.
- The Go skills vendored from `second-bite` (`golang-*`, `go-code-review`) apply when they are added to the
  repository.
