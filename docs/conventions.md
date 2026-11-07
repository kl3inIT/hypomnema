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

Hypomnema is a technical system, not a business domain: its parts share few tables and little vocabulary, so
business-module ceremony (tables, published API and an allowed-edge list per module) buys nothing. Only boundaries
that protect security, placement or concurrent state are drawn and enforced. For a new part:

1. **Glossary**: every term it introduces has one meaning in the [glossary](glossary.md).
2. **Owner**: one package is the source of truth for each table, lifecycle and invariant.
3. **Package first**: a new part is a package inside an existing tree; package boundaries inside a tree are not
   enforced beyond what the compiler does.
4. **New boundary or deployable only with evidence**: a security, placement, scaling or failure reason, recorded in an
   ADR. The three deployables have theirs: the node runs on every machine and reads untrusted text; the vault keeps
   secrets away from both.

## Layout and boundaries

Compared in [the Go repository research](research/2026-11-07-go-repository-layout.md): Grafana, Teleport and Gitea
enforce a few boundaries with `depguard`; Tailscale and Nomad also check each binary's full dependency list; Omnara
keeps one flat `internal/` with a storage package behind named methods.

```
cmd/hypomnema, cmd/hypomnema-node, cmd/hypomnema-vault   entry points
internal/controlplane/...   flat packages: execution, brain, mcpbridge, policy, channel/<provider>, httpapi…
internal/node/...           flat packages: connection, runtime/<name>, proxy, statedb…
internal/vault/...          flat packages: keys, secrets, sessions, audit…
internal/shared/...         platform (config, logging, HTTP, errors, ids, clock), nodeproto, vaultproto
```

The three boundaries that matter:

1. **Between deployables**: each tree imports only itself and `internal/shared`. `shared` stays small, because a
   binary links whatever of it the binary imports: `shared/platform` depends on no driver and goes into every binary;
   `shared/postgres` is for the control plane and the vault, and the dependency check keeps it out of the node. Vault
   cryptography never lives in `shared`.
2. **Execution state has one writer**: only `controlplane/execution` uses the generated sqlc code for Run, Task,
   Dispatch, Gate, leases and nodes, and exposes named operations (`ClaimNextDispatch`, `ExpireLeases`,
   `MarkNodeLost`…), as Omnara's `internal/storage`. These rows change together under concurrency, so one package
   holds one lock order.
3. **Untrusted input passes policy**: every state change requested by the Brain, a channel or a webhook goes through
   `controlplane/policy`. This one cannot be linted; it is held by design review and tests.

**Enforcement**, run by `make verify` locally and in CI:

- `depguard` in `.golangci.yml`: the tree rule (as Grafana's per-directory rules); generated sqlc code only inside
  its owner (as Omnara and Gitea); the node's SQLite only in `statedb`; a banned-import list where every entry names
  its replacement (as Teleport: `log` → `log/slog`, `github.com/pkg/errors` → `errors`, `math/rand` →
  `crypto/rand`).
- **Full dependency check per binary** (as Nomad's `make check`, Tailscale's `depaware`): `go list -deps` of the node
  and the vault must not contain `pgx`, `internal/controlplane` or each other's tree. `depguard` sees only direct
  imports and would miss a leak through `shared`.
- **Determinism in the execution core** (as Nomad's semgrep rule on its state machine): no `time.Now` or `math/rand`
  in `controlplane/execution`; a clock is passed in, so lease and timeout tests are exact.
- **Generated and tidy**: `go mod tidy`, `sqlc generate` and the OpenAPI generation leave `git diff --exit-code`
  clean; released migrations are never edited (as Omnara's `migration-check`).
- `sqlc vet` rules for query invariants, once queries exist.

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
  agree. A compatibility check compares the contract with the one on `main` and refuses breaking changes, as Omnara's
  `openapi-compat-check`, because mobile clients generated from it outlive a release.
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

- Assertions use testify's `require` (stop at the first failure) and `assert` (several independent checks), as
  Omnara does; `go-cmp` for structural diffs. `testify/mock` and `testify/suite` are refused by `depguard`: fakes are
  hand-written at the boundary, and tests stay plain functions.
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
- Applications follow the twelve-factor rules: configuration from the environment, logs to stdout, graceful shutdown
  on SIGTERM, migrations and other admin tasks as explicit commands.
- Every blocking call takes a `context.Context`; long-running loops stop on cancellation.
- Logging with `log/slog`, structured, with the request id, run id and dispatch id as attributes.
- Tooling follows Omnara: one `Makefile` with `make verify` (static checks, unit and race tests) that CI also runs, a
  curated `.golangci.yml` focused on correctness, and actions pinned by commit.

## Agent skills

- **One source per topic.** Two skills that cover the same topic are never installed together, because when they
  disagree an agent cannot tell which to follow. Before adding a skill, check the installed list for the same topic.
- **The repository wins.** When a skill says otherwise than these documents, the documents apply; the conflict is noted
  in `.claude/skills/SOURCES.md`.
- Vendored skills are copied unchanged with their source, commit and license in `SOURCES.md`, and updated by copying
  again. A skill for a library this project does not use is not installed.
- No maintained skill exists for sqlc, goose, huma, River, `coder/websocket` or the MCP Go SDK; when one is needed, a
  short project skill is written from that project's own documentation.
