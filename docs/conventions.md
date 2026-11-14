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
| Runtime (drive a worker) | `claude`, `codex`, `omp`, `opencode` |
| Machine provider | `aws` (EC2); later Hetzner, Modal, Daytona, E2B… |
| Model provider of the gateway | per kind of provider |
| Channel | Lark; later Telegram (the web chat is the client API itself) |

- One interface per function, named after the function (`Runtime`, `Channel`); one type per implementation, named
  after the vendor and function (`CodexRuntime`, `TelegramChannel`), inside the owning module.
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
- Carry accepted decisions across turns; do not reopen rejected ones or silently widen scope on your own. When the
  owner changes a decision or a name, that is normal: follow [AGENTS.md](../AGENTS.md#working-rules).

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
   ADR. The two deployables have theirs: the node runs on every machine and reads untrusted text. The vault is a
   package of the control plane until a reason to move it appears
   ([decision](decisions/2026-11-07-two-deployables.md)).

## Layout and boundaries

Compared in [the Go repository research](research/2026-11-07-go-repository-layout.md): Grafana, Teleport and Gitea
enforce a few boundaries with `depguard`; Tailscale and Nomad also check each binary's full dependency list; Omnara
keeps one flat `internal/` with a storage package behind named methods.

```
cmd/hypomnema, cmd/hypomnema-node   entry points
internal/controlplane/...   flat packages: orchestration, vault, coordinator, clientapi, nodeapi, agentapi, serviceproxy, larkbot, modelgateway…
internal/node/...           flat packages: daemon, shim, claude, codex, acp, statedb, worktree…
internal/shared/...         platform (config, logging, HTTP, errors, ids, clock), sqlite, nodeproto, githost…
```

The three boundaries that matter:

1. **Between deployables**: each tree imports only itself and `internal/shared`. `shared` stays small, because a
   binary links whatever of it the binary imports: `shared/platform` depends on no driver and goes into every binary;
   `shared/sqlite` opens the database of the control plane and of the node's state alike. Vault cryptography
   never lives in `shared`: it stays in `controlplane/vault`, the only package that holds the root key.
2. **Orchestration state has one writer**: only `controlplane/orchestration` uses the generated sqlc code for Run, Task,
   Dispatch, Gate, leases and nodes, and exposes named operations (`ClaimNextDispatch`, `ExpireLeases`,
   `MarkNodeLost`…), as Omnara's `internal/storage`. These rows change together under concurrency, so one package
   holds one lock order.
3. **Untrusted input passes policy**: every state change requested by a Coordinator, a Bot, a Channel or a webhook goes
   through the client API, which checks what the token may do, and every request to a service through the service
   proxy, which answers by the Connection's actions. This one cannot be linted; it is held by design review and tests.

**Enforcement**, run by `make verify` locally and in CI:

- `depguard` in `.golangci.yml`: the tree rule (as Grafana's per-directory rules); generated sqlc code only inside
  its owner (as Omnara and Gitea); the SQLite driver only in `shared/sqlite` and `database/sql` only in the packages
  that own tables (as Omnara's statedb rule); a banned-import list where every entry names
  its replacement (as Teleport: `log` → `log/slog`, `github.com/pkg/errors` → `errors`, `math/rand` →
  `crypto/rand`).
- **Full dependency check per binary** (as Nomad's `make check`, Tailscale's `depaware`): `go list -deps` of the node
  must not contain `internal/controlplane`, and the control plane's must not contain `internal/node`. `depguard` sees
  only direct imports and would miss a leak through `shared`.
- **Determinism in the orchestration core** (as Nomad's semgrep rule on its state machine): no `time.Now` or `math/rand`
  in `controlplane/orchestration`; a clock is passed in, so lease and timeout tests are exact.
- **Generated and tidy**: `go mod tidy`, `sqlc generate` and the OpenAPI generation leave `git diff --exit-code`
  clean; released migrations are never edited (as Omnara's `migration-check`).

## Orchestration core

- **Transitions are guarded writes**: one transaction, `UPDATE … WHERE id = $1 AND state = ANY($allowed)`, which must
  change exactly one row or fail with a conflict. The allowed transitions are listed in one place in code.
- **One lock order** for every transaction that touches more than one entity, written in the module and followed by
  every operation.
- **One writer**: the control plane is one process with one SQLite connection, and every write transaction takes the
  write lock when it begins (`_txlock=immediate`), so two claimers cannot both win and no row lock is needed (Orca's
  orchestration database; `FOR UPDATE SKIP LOCKED` belongs to many writer processes on PostgreSQL). Claiming takes a
  lease with a heartbeat. A worker or node that lost its lease cannot write; a reaper releases expired leases.
- **Fencing**: every Dispatch has an `epoch`. Every write from a node is a compare-and-swap on
  `(dispatch_id, epoch, state)`; a stale epoch is refused. A Transcript's items are the one exception: they are
  taken from the Dispatch's node at any epoch and in any state, because the last items arrive after the Dispatch
  settled, and they change no state. An item is the journal's line under its number, the same at every epoch, and a
  number already held is ignored.
- **Unknown is a state**: `start_unknown`, `stop_unknown` and similar states exist wherever an outcome cannot be
  proven. Silence or a timeout never proves that a process exited.
- **Replay policy**: every effect with consequences outside the control plane (push, opening a PR, a merge request, a
  message) is declared `safe` or `never`. Intent is recorded before the effect and the outcome after. A `never`
  effect interrupted by a crash is never repeated; the Coordinator receives a recovery note saying what finished and what
  may have taken effect, and reconciles (for example by reading GitHub).
- **Idempotency**: every mutation from the Coordinator, the CLI, a channel or a node carries a request id. A receipt keyed by
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
- Never ask through an event and wait for a reply. An event states a past fact (`orchestration.DispatchCompletedEvent`,
  key `orchestration.dispatch_completed`), carries identifiers only, and is written to the event log in the
  transaction that made the change. Listeners run after the commit, are idempotent, and do not depend on order.

## API

- The client API is written contract first in `api/openapi.yaml` (OpenAPI 3.0); `oapi-codegen` generates the
  server's types and the CLI's client from it, and `@hey-api/openapi-ts` the web client's. A change to the API
  changes the contract and regenerates both in the same change; `make openapi-check` and `make web-api-check` fail
  when generated code is stale. A compatibility check against the contract on `main`, as Omnara's
  `openapi-compat-check`, is not built.
- Every error is `{code, message}` with a stable `code` from the contract's `ErrorCode`. A write that a client may
  repeat takes an `Idempotency-Key`.
- Everything the owner or a coordinator can do is a command of the `hypomnema` CLI: an operation added to the client
  API gets its command in the same change, and the web client offers nothing the CLI cannot do (owner, 2026-11-10).
  Orca gives its agents every operation through its CLI the same way.
- Client events (web, mobile) are served over SSE from the event log, resumable with `Last-Event-ID`. A message sent
  by a client carries a dedupe key; a retry never starts a second run.

## Data and security

- Chat text, issue text, web pages and tool output are **untrusted input** while agents hold credentials.
- Only the owner, or an allowlist, can assign work. A public channel never starts work directly; it goes through a
  Gate.
- Workers never hold a raw secret. For an Integration, the control plane's service proxy attaches the account's
  token to each request an agent's tool makes, and the tool holds a placeholder
  ([decision, 2026-11-11](decisions/2026-11-11-service-proxy-in-the-control-plane.md)); a Dispatch's own key is its
  Session. Google and Lark go through it. A git host does not (owner, 2026-11-11): a worker's git work can be
  undone and is reviewed in a pull request, and no reference proxies git's own traffic. Egress of a worker as a
  whole, deny by default per Dispatch, is not built.
- Policies answer ALLOW, DENY or ASK and **fail closed**. They are evaluated in Go behind the client API and the
  service proxy. ASK is an Approval the owner answers; only the owner's token reads or answers one.
- **Spawning follows the references' default** (OpenClaw `sessions_spawn`, Omnara `spawn_agent`, Omnigent dispatch):
  every tool, `spawn` included, has a permission of ALLOW, ASK or DENY, and `spawn` is ALLOW. Hard limits always apply
  and are checked in Go: workers running at once, spawn depth, dispatches per turn (as Omnigent's `spawn_bounds`) and
  per Run, and a cost budget that ASKs when reached (as Omnigent's subagent budget). Plan approval before spawning,
  as genie's `wish`, is an option for large work, not the default.
- **Separate OS users**: the control plane and vault run as a user of their own (the owner names it in
  `deploy/controlplane/setup-controlplane.sh`), the Bots as `coordinator` (pi with its built-in tools, a clean
  environment as Omnigent's `clean_agent_env`, each chat's pi in a bubblewrap box), and a node's workers as a user
  without sudo (`hypomnema` on a machine the control plane created). The control plane starts pi and a Routine's
  script through sudoers rules limited to their launchers. No secret is passed on a command line; the database is a
  0600 file of the control plane's user, and every local service (the control plane API)
  requires authentication, as localhost is not a trust boundary once the Coordinator has a shell.
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
- The vault is a secret manager the owner manages (create, list without values, read one value at a time, rotate
  by version, delete refused while in use). Only the owner's token reads a value, and each read is recorded; a
  coordinator, a worker and a node have no operation that returns one
  ([decision](decisions/2026-11-10-owner-reads-secret-values.md), which replaced "values are never read back" on
  2026-11-10). It is encrypted as Omnara's `internal/secrets`: a data key per secret version, wrapped by a root key chosen
  by ID from a keyring, with the secret's identity as associated data. The root key comes from the environment of the
  control plane host, with an offline backup and a rotation procedure that rewraps data keys.
- A worker's Claude token and a git host's token go to its Dispatch alone, as environment, as OpenHands and Omnara
  deliver them; the node is the boundary and there is no proxy on it
  ([decision, 2026-11-13](decisions/2026-11-13-worker-credentials-in-the-environment.md), which replaced the plan of a proxy as
  Infisical's Agent Vault). No tool returns a vault value to the Coordinator or a worker. Research:
  [secrets for agents](research/2026-11-07-secrets-for-agents.md).
- Logs and transcripts are scrubbed of secret patterns before they are stored.

## Testing

- Assertions use testify's `require` (stop at the first failure) and `assert` (several independent checks), as
  Omnara does; `go-cmp` for structural diffs. `testify/mock` and `testify/suite` are refused by `depguard`: fakes are
  hand-written at the boundary, and tests stay plain functions.
- Every test names the observable contract and the regression it would catch, at the smallest boundary that can
  detect it.
- Persistence and concurrency are tested against a real SQLite file in `t.TempDir()`, opened with
  `internal/shared/sqlite` as in production: guarded transitions, lease expiry, two claimers on one Dispatch, stale
  epochs.
- Runtime adapters are tested against recorded CLI output, plus a live test per runtime run on demand.
- Node and control plane share a resilience contract: dropped connections, restarts mid-Dispatch and duplicated
  messages are tested with a fault proxy.

## Web client

How the references keep a UI consistent is in
[the research](research/2026-11-10-web-ui-practices.md); these are the rules taken from it (owner, 2026-11-10).

- **The Machines page is the look to follow** (owner, 2026-11-10): a card a thing, with its state and a menu in
  the card's head and its facts as rows of an icon, a label and a value; a meter where a value is a share; a
  switch between the grid and a table. A new screen that lists things starts from it
  (`web/src/features/machines`, after Beszel).
- **Primitives first.** A screen is built from `web/src/components/ui`, added with the shadcn CLI. A raw
  `<button>`, `<input>`, `<select>`, `<table>` or a dialog from Base UI directly is not styled to look like one
  (Orca's style guide, t3code's web UI rules, Onyx).
- **A primitive owns its look.** Pick a `variant` or a `size`; layout classes go on the parent. A treatment the
  design needs becomes a variant in the primitive's own file. `shadcn/no-restyle` and `shadcn/no-raw-colors` fail
  the lint otherwise (t3code, Orca, Coder).
- **Colors are tokens** from `web/src/index.css`, never a Tailwind palette color or a `dark:` override (Orca, Coder,
  Onyx).
- **One way to build each thing.** A form is TanStack Form with a zod schema, its fields `Field` with `FieldError`
  (Omnara's form library; every reference has one form convention). A table of records is TanStack Table on
  `Table` (Onyx, Infisical). Nothing to show is `Empty`; something taken away is asked with `AlertDialog`; what the
  server refused is an `Alert` with the server's words.
- **Every view has its loading, error and empty state** (Coder).
- **A label that is not plain to a newcomer explains itself when pointed at**, with a `Tooltip` (owner,
  2026-11-10). A list that does not fit its line shows less of each item, with the rest in a tooltip, rather than
  wrapping (Coder's `MCPServerIconStack`, Orca's `CrewAgentIcons`, Beszel).
- **A choice is a `Select`**, never a `NativeSelect`: the native one's open list is drawn by the system, grey on
  Windows, unlike the rest of the app (t3code and Orca use their `Select` almost everywhere). The linter refuses
  `native-select` (owner, 2026-11-12).
- **A dialog is as wide as what it holds**: the primitive's default is t3code's `max-w-lg`; a dialog with one
  field narrows it, one with a command or a table of choices widens it, by the dialog's `size` (t3code, Orca: each dialog sets
  its own).
- **Icons** are lucide's. A brand's mark, which lucide does not carry, is an SVG component in
  `web/src/components/icons` (Orca, Omnigent).
- **A thing that has a logo is shown with it** beside its name: a git host, a runtime, a service, a model's
  maker (owner, 2026-11-10). A generic icon is for what has none.
- **A shared component is made when a second screen needs it**, not before (Coder). What Settings shares is in
  `web/src/features/settings/settings-parts.tsx`.
- **What is its own design stays its own**: a control that is not an ordinary button, input, select or dialog (a
  card that is pressed, a row that opens, the chat's composer, a switch whose state the primitive shows less
  clearly) is not forced into a primitive. The reason is written where it is kept (owner, 2026-11-10).
- **A change that succeeded says so** in a toast (Omnigent, Orca, Infisical, Coder).
- **A UI change is looked at** on a desktop and a phone before its pull request, in pictures taken by its
  scenario in `web/tests` (t3code, Omnigent).

## Go

- One application module, `go.mod` at the repository root, where the Go version is pinned; the linters and generators
  are `tool` directives of a second module, `tools/ci/go.mod`. `golangci-lint` configuration in the root.
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
- No maintained skill exists for sqlc, goose, oapi-codegen, `coder/websocket` or the MCP Go SDK; when one is needed, a
  short project skill is written from that project's own documentation.
