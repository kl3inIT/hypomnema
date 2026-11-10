# Hypomnema architecture

This page states what is implemented today, and nothing else. The target is in [docs/vision.md](docs/vision.md);
planned work is in GitHub issues and never appears here as a fact. Update this page in the change that makes a
statement true or false.

## Binaries

| Binary | Commands | What it does today |
|---|---|---|
| `cmd/hypomnema` (control plane) | `migrate`, `serve`, `version`, `nodes add\|list\|drain\|resume\|rm`, `nodes token add\|list\|revoke`, `machines providers add\|list\|rm\|check`, `machines create`, `accounts add\|login\|list\|refresh\|rm`, `tokens add\|list\|revoke`, `secrets keygen\|add\|list\|rotate\|rm\|rewrap`, `gateway models\|keys`, `login`, `run`, `answer`, `questions` | `migrate` brings the database schema up to the binary's version; `serve` refuses any other schema, then serves `/livez`, `/readyz`, the node API, the client API, the model gateway and the web client, and runs the scheduler loop; the other commands are the owner's (below) |
| `cmd/hypomnema-node` (node) | `install`, `init`, `check`, `accounts set`, `run`, `service`, `shim`, `mcp`, `version` | `install --url` puts the node on the machine (below); `init --url --token-file` writes the node's configuration; `check` says hello once and lists the CLIs it found; `accounts set` records where an account's Claude token is; `run` runs the daemon with the Claude runner; `service` prints a systemd user unit; `shim` runs one worker and `mcp` serves its tools (both started for a worker, not by hand) |

Configuration comes from the environment: `HYPOMNEMA_HTTP_ADDR` (default `127.0.0.1:8080`),
`HYPOMNEMA_DATABASE_PATH` (required, absolute), `HYPOMNEMA_LOG_LEVEL`, `HYPOMNEMA_LOG_FORMAT` (`json` or `text`),
and the vault's root keys: `HYPOMNEMA_VAULT_KEYS` (a comma-separated list of `id:base64` entries of 32 bytes) with
`HYPOMNEMA_VAULT_ACTIVE_KEY_ID`, set together or not at all.
Which runtime and model agents use is stored, as profiles (below), not configured.
`HYPOMNEMA_COORDINATOR_COMMAND` (a JSON array) starts pi for a chat id appended to it; unset, chats keep their
messages and nothing answers.

The coordinator (`internal/controlplane/coordinator`, issue #41 PR 3): `serve` runs one `pi --mode rpc` per chat
that has something for pi, with the `coordinator` profile's model (`--provider`, `--model`, `--thinking`), and stops
it after 30 min idle by closing its stdin; it starts again with the same session id. pi's own retries are off. Owner
messages and wake-ups go as one `prompt`, or a follow-up while pi works, and are marked delivered once pi accepted
them. pi's activity is kept and relayed as Omnara does (issue #41 PR 4): each assistant `message_end` is kept as
the coordinator's message with its text and thinking as parts, and each finished tool call as a `tool` message with
its arguments and result (at most 16 KiB). Text as pi writes it, tools starting and ending, and working or idle go to
the chat's watchers from memory only and are never stored; `abort` stops pi's turn. On
the server pi runs as the `coordinator` user through `deploy/coordinator/`: a root-owned launcher (`hypomnema-pi`,
a clean environment, only a chat id and model flags), one sudoers rule limited to it, pi's instructions, a
`models.json` that names the model gateway (`/v1` of the control plane) and reads pi's gateway key from a 0600
file, pi's version with its lockfile (`package.json`, which Dependabot
updates; the setup installs it with `npm ci`), and the setup script the owner runs with sudo.
Logs are JSON on stdout; `serve` stops on SIGINT or SIGTERM and lets requests in flight finish. `/readyz` answers 503
with `Retry-After` while the database does not answer.

The database ([decision](docs/decisions/2026-11-07-sqlite.md)) is one SQLite file opened by `internal/shared/sqlite`:
one connection, WAL, `synchronous=FULL`, pragmas read back after opening; its directory is made 0700 and a new file
0600, and an existing file that group or others can access, or that another user owns, is refused (Linux).

Migrations are goose SQL files embedded in the binary (`internal/controlplane/orchestration/migrations`); the schema
holds Runs, Tasks, Task dependencies, Dispatches, Conversations, receipts, Dispatch effects and resources, account
cooldowns, questions, the event log, nodes, accounts and client tokens, and the vault's secrets, which only the vault touches. `internal/controlplane/orchestration` is their only writer: named operations, each one `BEGIN IMMEDIATE`
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

- Nodes and accounts: the owner adds a node, which mints a token shown once (only its SHA-256 is stored). A node
  has any number of tokens (`node_tokens`, after Omnara's `machine_daemon_tokens`): each has a name, is revoked on
  its own, never expires, and records its last use at most once an hour. At each connect the node reports its CLIs
  (path and version), its worktree root, and what it is: its version, OS, architecture, CPU count, memory, and the
  size of the volume that holds the worktree root. It is connected while heard from within 30 s (any node call
  counts). With each heartbeat it sends what it measured: CPU in use since the heartbeat before, memory in use, the
  worktree volume's use and the one-minute load average; only the latest measurement is kept, with its time
  (`GET /nodes` shows it as `stats`). A node can be drained and resumed; three expiries in a row mark it unavailable until its next hello or
  a resume. A removed node is kept as `deleted`: its tokens are revoked, it is no longer listed or placed on, and
  its name is free; one that still runs a Dispatch is not removed. Accounts (runtime, label, position, active or
  disabled) are kept here with no secret value. One the owner logged in on the control plane (below) also has the
  vendor's id of the account, its email and plan, the name of the vault secret holding its tokens, and how its
  refreshes went; placement leaves these alone, since no node holds their tokens.
- Machines created at a provider (issue #69, step 3; `orchestration/machines.go` keeps the record,
  `internal/controlplane/machines` calls the provider, after Omnara's `internal/machinepool`). One kind exists, `aws`
  (EC2); [setup](deploy/machines/aws.md).
  - A machine provider (`machine_providers`) has a name, a kind, the vault secret that holds its key, a default
    region and size, a limit on machines (3 by default) and the options its kind takes. The vault refuses to delete
    a secret one names, and a provider with a machine that is not removed is not removed.
  - A node created at a provider records the provider, the provider's id for the machine, its region and size, and
    a machine state: `provisioning`, `ready`, `provision_failed`, `deleting`, `delete_failed`. A node the owner
    installed has none. `GET /nodes` shows it as `machine`.
  - Create: the region, the size and the public URL are checked first; then the node and its token are written in
    one transaction, refused at the provider's limit; then the provider is asked for one machine named after the
    node, with a first-boot script (cloud-init user data) that makes a `hypomnema` user without sudo, installs git
    and the Claude Code and Codex CLIs, and runs the install script of `GET /install/node.sh` with the token. The
    node's hello makes the machine `ready`. A refused create leaves the node `provision_failed` with the reason;
    so does no hello within 10 minutes, and a hello after that still makes it `ready`.
  - On AWS the instance is the newest Ubuntu 24.04 image of Canonical for the size's architecture, tagged with
    the provider's and the node's ids, with instance metadata readable only through IMDSv2 from the instance
    itself, because the script holds the token. A repeated create finds the instance by its tags, and the token's
    id is the request's client token, so one node never has two instances.
  - Remove (`nodes rm`, `DELETE /nodes/{node_id}`): the node is drained and its tokens revoked, the machine is
    destroyed at the provider, and only then is the node `deleted`. A failed destroy leaves it listed as
    `delete_failed` and is tried again after 30 s, doubling to an hour, until it works.
  - While the server runs, every 30 s a pass settles what is left between: a create that was cut short is looked
    up at the provider after 2 minutes and taken up or marked failed, a machine that never said hello is marked
    failed, and a due destroy is tried again. Nothing is created or destroyed without the owner asking.
  - `machines providers check <name>` proves the key works and lists the provider's machines that no node stands
    for; it runs when asked, not on a timer, and destroys nothing.
  - Not built: other providers, choosing a region or size on the web (the API and the CLI take them), and
    delivering the CLIs' logins to a new machine, which the owner still sets on it once.
- Chats (`orchestration/chats.go`, issue #41 PR 3, after Omnara;
  [research](docs/research/2026-11-09-chat-storage.md)): the owner's conversations with pi. Every message is kept
  (`owner`, `coordinator`, `tool`, `system`; replies and tools with JSON `parts`); owner messages and wake-ups wait
  for pi and are marked delivered one by one. A
  Run records the chat whose pi created it (`runs.chat_id`), and each event of such a Run that needs a coordinator (a
  Task ended or was blocked, a question to the coordinator, an escalation, the Run ended) adds one wake-up to the chat,
  unique per event.
- Profiles (`orchestration/profiles.go`, issue #42, after Omnara's agent configs): a profile is a YAML document
  (`name`, `description` of what it is for, `runtime`, optional `model: {provider, name, effort}`, `instruction`). Applying a changed source makes a
  new immutable version and points the name at it; the same source returns the current version. Providers (kind
  `anthropic`, `openai_compatible`, `claude_subscription` or `codex_subscription`, base URL, the name of where the
  API key is kept, never the key) list the models they offer. A profile and a new Task are checked when applied or
  created: a known runtime (`claude`, `codex`, `pi`), an effort from `low` to `max`, and a model a provider of a
  kind that runtime can use offers (found from the model when the provider is not named). A provider change that
  would leave a profile's model or runtime unserved is refused. A Task names a profile, `implementer` by default.
  The starting set: `implementer`, `reviewer`, `explorer` (told to change nothing; nothing enforces it yet) on
  Claude Code with its default model, and `coordinator` on pi; provider `claude-subscription`.
- Scheduler: `Schedule` is one pass in one transaction, and `Loop` runs `Reap` and `Schedule` every 2 s and on each
  commit signal. At most 4 Dispatches that have not settled exist at once, one waiting on a question included; the
  coordinator's `CreateDispatch` meets the same limit and checks. Ready Tasks of open Runs go oldest Run first, then
  by position; the runtime, model and effort are the Task's own or its profile's current version, and the Dispatch
  records the model, the effort and that version; its worker gets the version's instruction after the built-in rules. An open Conversation continues only on its node, once that node is active, connected, not
  unavailable and below its limit; a new one goes to the least loaded such node that has the runtime and a worktree
  root, ties by id, with the first active account of the runtime that is not cooling down, in
  `<worktree root>/<run>/<task>`. A Task with no node or no account for 5 min is escalated once (`unplaceable`), and
  a rate limit over an hour that leaves its runtime with no free account is escalated (`account_cooling`).

- Runs name the repository their workers work in and its base branch (default `main`), and the coordinator token
  that coordinates them, if a coordinator created them. Runs are listed newest first, a page at a time.
- A repository is a git host and a path (issue #44; `internal/shared/githost`,
  [research](docs/research/2026-11-10-git-hosts-and-personal-work.md)).
  - A Run is given `owner/name` (GitHub, as before), the URL of a repository, git's `user@host:path`, or the name
    of a registered repository; it keeps the host and the path. A Run from before hosts existed is on `github.com`.
  - Hosts are of two kinds, `github` and `gitlab`, one adapter each: the clone URL, the command that opens a pull
    request, the request's URL, and reading a request from a worker's evidence. `github.com` and `gitlab.com` are
    known; the owner declares a company's server with its kind, and a Run on a host that is not known is refused
    with the command that declares it. A declared host is forgotten only when no open Run and no registered
    repository is on it.
  - A repository can be registered under a short name with the base branch its Runs start from.
  - A host can have a token, given by the owner (`PUT /hosts/{host}/token`, `hosts token set <host> --token-file`).
    It is a secret in the vault (`git-host-<host>`); the host's record only names it, and the API never returns
    it. The control plane's own wiring (`internal/controlplane/hosttokens.go`) stands between the vault and the
    two APIs, which may not open a secret.
  - The token reaches a worker with its offer: the node API reads it from the vault as it sends the offer, and
    orchestration never holds it. The node gives it to that Dispatch only, as environment: git sends it to that
    host alone (an `http.https://<host>/.extraheader` through `GIT_CONFIG_COUNT`), `gh` reads `GH_TOKEN` (or
    `GH_ENTERPRISE_TOKEN` with `GH_HOST`), `glab` reads `GITLAB_TOKEN` and `GITLAB_HOST`. It is in no command's
    arguments and no clone URL. On the node it is in the offer kept in the node's state database until the
    Dispatch ends, and in the worker's spec file until the worker starts, as the Claude token is. The worker
    itself can read it: this stands in for the vault's credential sessions and the node's proxy
    ([issue #66](https://github.com/kl3inIT/hypomnema/issues/66)), with which a worker would never see it.
  - A host without a token works as before: git and the host's tool use the node user's own logins.
  - Which nodes reach which host is not known to placement yet.
- Client tokens: kind `owner` or `coordinator`, in Omnara's format (`hyp_<kind>_v1_`, 43 Base62 characters and a
  CRC32 checksum, checked before the database is read; `internal/controlplane/bearertoken`). Shown once; only the
  SHA-256 is stored; a revoked token no longer authenticates.
- Creating a Run or Tasks can carry an idempotency key: the same caller and key with the same request return the first
  result, with another request it is a conflict. The key is kept as a receipt, as node calls are.

The vault (`internal/controlplane/vault`, issue #66, after Omnara's `internal/secrets`) is the secret store: API keys
and subscription token sets, encrypted at rest. Its other half, credential Sessions per Dispatch and the node's
proxy, does not exist yet.
- A secret has a name, a kind (`generic`: one value; `oauth_token_set`: a refresh token with, optionally, the access
  and id tokens), metadata that is not secret, and versions. `Rotate` adds a version and points the secret at it;
  earlier versions stay, encrypted.
- Each version is sealed as `aes-256-gcm-envelope-v1`: the payload under a data key of its own, the data key under
  the root key named by `key_id`. Both are bound to the secret's id, the version's id and number, the kind and the
  names of the payload's entries, so a version edited or moved in the database does not open.
- The root keys are a keyring from the environment. Without one the vault is not configured: secrets can be listed
  and deleted, nothing is sealed or opened, and no path stores a value in the clear. A version sealed under a key
  that is not in the keyring is listed as such and left untouched. `Rewrap` wraps every data key under the active
  root key without decrypting a value, and changes nothing when one version cannot be unwrapped.
- `Refresh` replaces a token set through a function that asks the provider, one refresher at a time: callers in
  the process share one exchange; across processes a lease row (`secret_refresh_leases`, about 2.5 min) names the
  refresher and the version it read, and the result is stored only while the secret is still at that version. The
  exchange and the write go on when the caller stops waiting, and the write is retried. A caller that names a
  version the secret has moved past gets the newer set without an exchange. A failure that may pass keeps the token
  set and frees the lease; a rejection for good marks the secret as needing a new login until the owner rotates it.
  After a refresh only the new version and the one before it are kept. Subscription accounts call it (below).
- `Open` returns a value to callers inside the control plane: the model gateway, for a provider's key, the
  wiring that hands a git host's token to an offer, and subscription accounts, for a token set. No API,
  coordinator or web package may import the vault (`depguard`), and the owner's commands never print a value.
- A provider names a secret as `vault:<name>` in its `api_key_ref`; nothing resolves that reference yet. Deleting a
  secret is refused while a provider or an account names it.
- The tables `secrets`, `secret_versions` and `secret_refresh_leases` are in the one migration stream and are read and written only by this
  package, through its own generated queries.

Subscription accounts (`internal/controlplane/subscription`, issue #20 step 2, after CLIProxyAPI, 9router-go and the
Codex CLI; [research](docs/research/2026-11-10-subscription-login-and-refresh.md)) are logged in once on the control
plane, which keeps their tokens fresh. Nothing uses the tokens yet: the gateway's adapters come next.

- One `Vendor` per kind of subscription starts a login, exchanges a refresh token and reads who a token set belongs
  to. Only Codex exists: a device code (a link and a one-time code, 15 minutes), or, when OpenAI offers this client
  none, the browser login whose loopback address the owner pastes back, checked against the state and the PKCE
  verifier the control plane kept.
- A login makes an account, or gives new tokens to the account that already has the vendor's id (Codex: the
  ChatGPT account id, read with the email and the plan from the id token). The tokens are a vault secret of kind
  `oauth_token_set`, named after the account, with the time the access token ends.
- `serve` looks every 5 minutes for access tokens that end within the vendor's lead (Codex: 30 minutes) and
  refreshes them through the vault's `Refresh`. A refusal for good (401, or an error code such as `invalid_grant` or
  `refresh_token_reused`) marks the account as needing a new login and is not tried again. Any other failure keeps
  the tokens and is tried again after 5 minutes, doubling up to a day; its reason is recorded with the status and
  error code only.

The model gateway (`internal/controlplane/modelgateway`, issue #20,
[decision](docs/decisions/2026-11-10-model-gateway.md)) serves chat from OpenAI-compatible providers to clients that
speak OpenAI's API. Subscriptions, other provider formats and other capabilities do not exist yet.
- A model is a YAML document: a public `name`, a `capability` (`chat`, the only one), and `targets` (a provider and
  the model's name there) in the order tried. Applying a name again replaces it. A target must name a provider of a
  kind the gateway has an adapter for (`openai_compatible`) and a model that provider offers, so a model that cannot
  be served is refused when applied.
- A gateway key (`hyp_gateway_v1_…`, the client tokens' format) is shown once and kept as its SHA-256, with the
  public model names it may use (`*`: every model). It is not a client token: the client API refuses it, and
  `tokens add` cannot make one.
- `GET /v1/models` with `Authorization: Bearer <key>` answers OpenAI's list shape with the models the key may use;
  errors are OpenAI's `{"error": {message, type, code}}`, 401 without a valid key.
- `POST /v1/chat/completions` forwards the request to the model's first target, as 9router-go does: the body is
  read as a map of its top-level keys, the model's name is replaced by the provider's and everything else goes
  through, each value as the bytes the client sent. The client's headers are not forwarded; the provider's key comes
  from the vault, so a provider served by the gateway names it as `vault:<name>`. Bodies are limited to 10 MiB.
- A stream is asked for its usage (`stream_options.include_usage`) whether or not the client asked, and the
  usage-only frame is kept from a client that did not. Data lines are passed on as the provider sent them;
  comments are not. A stream whose provider sends nothing for 5 min, or that ends without a finish reason or its end
  mark, ends with an error event (`upstream_truncated`) instead of looking whole.
- The next target is tried when one gives no answer, or answers 429 or 5xx, and only while nothing was sent to the
  client. Any other answer of a provider reaches the client with its status and body. When no target can be reached
  the answer is 502; the gateway's own refusals are 401 (key), 403 (the key may not use the model), 400 (the model
  is not declared, or the body is wrong) and 413.
- Each request served is one row of `gateway_usage`: the key, the model, the target of the last attempt, how many
  were tried, the outcome (`ok`, `upstream_error`, `transport_error`, `truncated`, `client_gone`), the provider's
  status, the duration, and the tokens the provider reported (prompt, completion, cached, reasoning; the largest
  seen in a stream). When it reported none they are bytes divided by four and marked as estimated.
- The tables `gateway_models`, `gateway_model_targets`, `gateway_keys` and `gateway_usage` are in the one migration
  stream and are read and written only by this package, through its own generated queries. Providers are read
  through orchestration and keys through the vault.

Node API (`internal/controlplane/nodeapi`, protocol in `internal/shared/nodeproto`, after Omnara's daemon protocol):
- A node registers with `POST /node/v1/hello` and its bearer token. It sends its CLIs, its worktree root, its
  version and machine (measured with gopsutil, as Nezha's and Beszel's agents) and the Dispatches it still holds. Each of those is answered `retain` (still its own at that epoch), `reattach` (marked
  unknown while it was away) or `release`.
- It then holds one WebSocket at `/node/v1/socket` with JSON `{type, payload}` frames of at most 1 MiB.
- Installing a node (after Omnara's `GET /install/omnarad.sh` and `omnarad install`): the control plane serves a
  shell script at `GET /install/node.sh`, which holds no secret. It names the control plane (`HYPOMNEMA_PUBLIC_URL`,
  or the host it was asked at) and where releases are (`HYPOMNEMA_NODE_RELEASE_URL`, by default the latest GitHub
  release). On the machine it downloads the manifest for the platform (`<os>-<arch>.txt`: version, url, sha256),
  then the binary, refuses one whose SHA-256 or version differs from the manifest or whose URL is not https, and
  runs `hypomnema-node install`. That copies the binary into the node's home, takes the token from
  `HYPOMNEMA_NODE_TOKEN` or asks for it on the terminal, writes the configuration, says hello once, and on Linux
  installs and starts a systemd user unit. Linux and macOS, amd64 and arm64.
- Releases: a tag `v*` runs `.github/workflows/release.yml`, which builds the node for those four platforms with
  their manifests (`make release-node`) and publishes them as a GitHub release.
- The control plane offers the node its queued Dispatches with what it needs (Task, repository, worktree, runtime,
  account, model), sends stops and answers, and acknowledges every heartbeat and report. Each is sent once per
  connection, and again after a reconnect. It looks again on every commit and every 30 s.
- A report is one node call of the orchestration core, with its report id as the request id. A resent report gets
  the same answer. One that can never apply is `permanent_reject`; one that may pass is `transient_error`.
- A second connection of the same node replaces the first, which is told `presence_replaced`.

Client API (`internal/controlplane/clientapi`, contract `api/openapi.yaml`, after Omnara's `internal/httpapi`):
- Served at `/api/v1`. The spec is written by hand; oapi-codegen generates the types, a strict server, a Go client and
  the embedded spec into `clientapi/openapi`.
- Every request is checked against the spec first (`validation_failed`), and bodies are limited to 1 MiB. Errors are
  `{code, message}`.
- Every request carries a client token (`Authorization: Bearer`); without a valid one the answer is 401.
- Each operation has a policy. Every token reads everything and may create a Run; a Run created with a coordinator
  token is coordinated by it. Changing a Run, its Tasks or its questions needs the owner, or the Run's coordinator
  (403 otherwise), and so does reading a worker's transcript. An answer is recorded as the owner's or the
  coordinator's by the token's kind.
- Operations: who am I; list, create, show (with each Task's latest Dispatch), finish and cancel Runs; create Tasks
  with dependencies; show, cancel and reopen a Task; list the Tasks a board shows (`GET /tasks`: every Task of an
  open Run and other Runs' Tasks changed in the last 24 h, with their latest Dispatch, their Runs, and the newest
  event's seq to follow the stream from); read a worker's transcript (`GET /dispatches/{id}/items`: the items after a
  seq, or the newest ones before a seq, 100 a page; it can wait up to 50 s for the next item); list, show, answer
  and forward questions; list events; list nodes and accounts; add a node (`POST /nodes`, which answers with
  its token once; with a `provider` it creates a machine there instead and answers with no token, and a create the
  provider refused is answered as the failed node), list the machine providers with how many machines each has
  (`GET /machine-providers`), drain, resume and remove a node (owner only; removing a node that still runs a
  Dispatch is a 409).
  Creating a Run or Tasks takes an `Idempotency-Key`.
  A Dispatch carries `activity`, one line about the newest thing its worker said or did.
- Events: a page after a cursor, which can wait up to 50 s for the next event (`wait`) instead of polling; and a
  stream of Server-Sent Events at `/api/v1/events/stream`, each with its seq as id, resumed with `Last-Event-ID`,
  with a comment every 10 s. Waits and streams end when the server shuts down.

The owner's commands:
- On the server, with the same database as `serve`:
  - `nodes add <name>` prints the node's token once, and the line that installs the node; `nodes list` shows each node's state, load, CLIs and
    machine; `nodes drain|resume|rm <node>` take the node's id or name;
  - `nodes token add <node> <name>` prints one more token once; `nodes token list <node>` and
    `nodes token revoke <id>`. A node connected with a revoked token is refused at its next heartbeat;
  - `machines providers add <name> --kind aws --key vault:<secret> --region R --size S [--max N]
    [--option name=value]…`, `machines providers list`, `machines providers rm|check <name>` and
    `machines create <name> --provider P [--region R] [--size S] [--max N]` manage provider machines; a machine
    is removed with `nodes rm`;
  - `accounts add <runtime> <label>` records an account whose CLI is logged in on a node; `accounts login codex
    [--label L]` logs a subscription in here, printing a link and a code and waiting, or asking for the pasted
    address; `accounts list` shows each with the state of its tokens; `accounts refresh <account>` and
    `accounts rm <account>` (which removes the tokens too) are for those logged in here;
  - `tokens add <name> [--kind owner|coordinator]` prints a client token once; `tokens list` and
    `tokens revoke <id>`;
  - `secrets keygen [id]` prints a new root key; `secrets add <name> --from-file FILE|-` (with `--kind` and
    `--meta key=value`), `secrets list`, `secrets rotate <name> --from-file FILE|-`, `secrets rm <name>` and
    `secrets rewrap` manage the vault's secrets. A value is read from a file or standard input and never printed;
  - `gateway models apply FILE`, `gateway models list`, `gateway models rm <name>` manage the gateway's model
    table; `gateway keys add <name> [--models a,b]` prints a gateway key once, with `gateway keys list` and
    `gateway keys revoke <id>`; `gateway usage [--limit N]` lists the latest requests it served.
- From any machine, over the client API (`internal/controlplane/clientcli`):
  - `login --url URL --token-file FILE` checks the token and keeps both in `~/.hypomnema/client.json` (0600);
  - `run --repo owner/name <objective>` creates a Run with one Task and prints its events (and any question to the
    owner) until the Task ends, then finishes the Run; each read waits for the next event;
  - `answer <question> <number or text>`, and `questions` lists the open questions to the owner;
  - the coordinator's commands: `whoami`; `runs create|list|show|finish|cancel|wait`; `tasks create|show|cancel|reopen`
    (several Tasks with dependencies from one JSON file); `questions [show|forward]`. Each takes `--json`; long text
    comes from a file or `-` for standard input; flags may follow the arguments; a create takes `--key` so a create
    whose answer was lost can be repeated.
  - `hosts list|add|rm`, `hosts token set|rm` and `repos list|add|rm`: the git hosts a Run can be on, their
    tokens, and the repositories registered under a name; every token reads them, only the owner's changes them.
    `hosts add` takes a host's name or a URL copied from the browser (`--kind`, default `gitlab`); `hosts token
    set` reads the token from `--token-file` (or `-`), never from an argument; `repos add` takes `owner/name` or a
    URL (`--name`, `--base`). `--repo` of `run` and `runs create` takes a registered name, `owner/name` or a URL.
  - `profiles list|show|versions|apply`, `providers list|apply` and `models list`; every token reads them, only the
    owner's applies a YAML file (the API's `PUT /profiles/{name}` and `PUT /providers/{name}`).
  - `runs wait <run>` returns when a Task of the Run ended or was blocked, a question reached the coordinator, an
    escalation came or the Run ended, with the cursor for the next wait and the next commands (`questions show`,
    `tasks show`, `runs finish` once every Task ended); it sends one request per 50 s while nothing happens.
  - `tasks list` prints the board's Tasks (`GET /tasks`), a line a Task with what its worker does now or its
    result. `tasks transcript <task>` prints the latest attempt's transcript, a line an item (`--after`,
    `--json` for one item a line); `--follow` keeps printing until the worker has ended and nothing more comes.
- `chat [message]` writes to pi and prints its replies and the tools it ran until it is idle (`--chat`, `--new`,
  `--text-file`); without a message it shows the chat. `chats` lists them. `runs create` sends `HYPOMNEMA_CHAT_ID`
  when pi's environment has it.
- The coordinator skill (`internal/controlplane/clientcli/skills`, after Orca's
  [decision](docs/decisions/2026-11-09-coordinator-cli-and-skill.md)): `skills get hypomnema-coordinator` prints
  the guide compiled into the binary, `--reference recovery` one reference; `skills install` writes the stub
  `SKILL.md` to `~/.claude/skills` and `~/.agents/skills`, replacing a copy only when it matches a released stub
  (otherwise only with `--force`, keeping a backup). Tests check that every command in the skill parses and that the
  stub and guide share their frontmatter.

Node (`internal/node`):
- `init` writes `~/.hypomnema-node/config.json` (mode 0600, directory 0700) with the control plane's URL, the token
  read from a file, and the worktree root.
- At connect the node finds `claude` and `codex` on its PATH and reads their `--version`.
- The daemon (`internal/node/daemon`) says hello claiming the Dispatches it holds and applies each disposition, then
  holds the socket:
  - heartbeats every 10 s (or as the control plane says), a WebSocket ping every 20 s;
  - reconnects with backoff from 1 s to 30 s with jitter, and stops if the token is refused.
- It records an offer and queues its accept in one transaction, and starts the worker only once the accept is
  acknowledged. It renews each running Dispatch's lease every 30 s and passes stops and answers to its Runner.
- Durable reports (accept, started, question, answer acknowledged, the final report, reattach) wait in a local SQLite
  outbox (`internal/node/statedb`) and are resent after every reconnect until acknowledged; heartbeats and progress
  are not resent.
- Workers (`internal/node/claude`) are Claude Code under a shim (`internal/node/shim`):
  - The daemon starts `hypomnema-node shim --dir run/<dispatch>` in its own session. The shim runs `claude -p` with
    bidirectional stream-JSON and host permission prompts (`--resume` when the Conversation continues, `--model`
    when set).
  - The worker gets a clean environment: PATH, HOME and the locale, plus the account's token
    (`CLAUDE_CODE_OAUTH_TOKEN`, read from the file named in the node's configuration) and its own
    `CLAUDE_CONFIG_DIR`. The spec holding them is deleted once the worker runs.
  - The shim appends each line of the worker's stdout to a journal with an fsync, writes what arrives on its unix
    socket to the worker's stdin, and writes `exit.json` when the worker exits. The runner reads the journal from a
    saved offset, so a new daemon adopts a running worker. A stop sends an interrupt and closes stdin; a worker still
    alive after 30 s is killed with its whole session group.
  - Reported:
    - the session id from `system/init`, as started;
    - progress at most every 30 s;
    - one turn per Dispatch: stdin is closed after the turn's `result`.
  - The worker's transcript goes to the control plane apart from its reports
    ([research](docs/research/2026-11-10-worker-sessions.md)):
    - Each journal line becomes items of one shape for every runtime: `message`, `reasoning`, `tool_call`,
      `tool_result`, `notice`. The first instruction and the final result are not items. An item is cut to 64 KiB
      and says how much was left out.
    - A second cursor beside the journal (`sent`: offset and last seq) says how far the control plane has it. Each
      second the runner sends the items of the next whole lines, at most 32 items or 256 KiB, numbered from 1, and
      one batch of a Dispatch at a time. The cursor moves on the acknowledgement, which brings the next batch at
      once; a batch not acknowledged in 15 s is sent again with the same numbers.
    - The control plane stores items by `(dispatch, seq)`, ignores a seq it holds, takes them in any status of the
      Dispatch, and refuses for good a Dispatch that is not on that node. Items are dropped 30 days after the
      Dispatch settled.
    - A transcript the control plane has whole after the worker exited is marked `sent.done`. A new daemon sends the
      journals under `run/` that are not marked, those from before this existed too.
  - The worker's tools (`internal/node/workertools`, `hypomnema-node mcp`) come in through `--mcp-config` with
    `--strict-mcp-config`; AskUserQuestion and the subagent tool (`Task`, `Agent`) are disallowed. They reach the
    runner only through a bridge directory beside the journal (`internal/node/bridge`), so a question survives a
    daemon restart.
    - `ask`: the runner submits the question, records the control plane's id, writes the answer for the waiting
      call, and acknowledges it. While it waits, the call sends `notifications/progress` every minute; tool calls may
      last 24 h (`MCP_TOOL_TIMEOUT`). A canceled call withdraws the question.
    - `finish`: the final report, with summary, evidence and an optional Gate. It is the outcome once the worker
      exits.
  - Without `finish`, the outcome is one of:
    - `rate_limited`, after a rejected rate-limit event;
    - `needs_auth`, on a login error;
    - `worker_error`, "stopped without finish", as Orca reports a stop without `worker_done`;
    - exited with no result: the outcome is unknown;
    - a worker that never started is `start_failed`.
  - Every permission prompt is allowed and logged (`node.tool_allowed`). The node's machine or user is the boundary
    ([research](docs/research/2026-11-08-worker-permissions.md)).
  - The first turn, after Orca's preamble, has:
    - the Task's spec, the repository and branch, and the instruction to open a pull request with the host's
      command: `gh pr create --base <base> --head <branch>` (without `--head`, `gh` does not find the branch in a
      worktree of the mirror) or `glab mr create --target-branch <base> --source-branch <branch> --yes`;
    - `ask` instead of guessing, `finish` exactly once (with a Gate for decisions the owner must make), and no
      subagents;
    - the recovery note and a resolved Gate.
- Worktrees (`internal/node/worktree`): a bare mirror per repository under the node's home, fetched into
  `origin/*` before each new worktree. `git worktree add -b hyp/<task>` from `origin/<base>`, or from the existing
  branch on a retry; reused while it exists. Git uses the node user's own credentials. The offer names the
  repository's host and kind; the node checks them and the path before git sees them, and clones from the host's
  address. A GitHub mirror stays under `repos/<owner>/<name>.git`; another host's is under `repos/_hosts/<host>/`.
- There is no vault, permission rule or mailbox yet.

## Web client

- `web/`: a Vite single-page app with React, TypeScript, TanStack Router (file routes in `src/routes`) and Query,
  Tailwind and shadcn on Base UI (preset `b43eDcOJc`), pnpm
  ([stack and look](docs/research/2026-11-09-directories-sessions-web-stack.md)).
- The look follows t3code (MIT): its light and dark colors with a blue-indigo primary, 14px text, a 46rem chat lane,
  a full-height 16rem sidebar, its work-log rows, composer and markdown styles (`src/index.css`, `features/chat/`),
  each adapted file naming its source. Five status tokens (running, done, needs you, failed, queued) follow its
  palette; dark is the default.
- Sign-in: the owner pastes an owner token once; the browser keeps it (`localStorage`, as OpenClaw's control UI)
  and sends it on every request.
- The API client is generated from `api/openapi.yaml` by `@hey-api/openapi-ts` into `src/api/generated` (as Omnara;
  its `next` build, since the release needs TypeScript 5's compiler API); `make web-api-check` fails when it is stale.
- Chats with pi (issue #41 PR 4, after Omnara; learned from assistant-ui, ai-elements and pi-web): a list of chats
  beside the open one. A chat follows `GET /chats/{id}/stream`: kept messages, then pi's live frames, reopened after
  the last kept message when the server ends it. pi's thoughts and tools between replies form one line that opens,
  with each command and its output; replies render as markdown with streamdown and shiki, loaded apart from the
  screen. Enter sends unless an input method is composing; Stop asks pi to end its turn.
- The board and the Runs (issue #41 PR 5; [research](docs/research/2026-11-09-board-and-questions-ui.md)):
  - `/board`: the Tasks in flight in three columns, Needs you, Working and Done (the last 24 h), after Orca's agent
    kanban (MIT). Needs you holds an open question to the owner, a failure in an open Run, a stop for a decision
    and a missing login. Rows per Run or per repository and a repository filter are kept in the URL. A card shows
    the runtime's mark (from OpenMausBot, Apache 2.0), the Task, its Run, what its worker does now (the Dispatch's
    `activity`) and how long it has run, the machine, the model and the pull request (a worker reports its URL; `owner/repo#N` from earlier workers is
    linked on GitHub; a GitLab merge request reads as a pull request with its number). While a worker runs the
    board is read again every 10 s, since its activity changes without an event. On a phone the columns stack,
    Needs you first.
  - A card opens the Task over the page, as Omnigent opens a session over its canvas (`?task=` in the URL; Escape
    closes it): the question to answer, start again or cancel, the result, the Task and the worker's transcript.
  - The transcript (issue #43; [research](docs/research/2026-11-10-worker-sessions.md)) is read from its end, 100
    items a page with "Load earlier", and followed by waiting on `GET /dispatches/{id}/items`.
    - The worker's messages are prose. Its thoughts and tool calls between two messages are one group: a line
      that says the calls in words ("Ran 1 shell command, read 2 files") and opens into one row a call. While the
      worker runs, the last 3 steps of its newest group stay out of the fold.
    - A row is a verb and what the call is about; a shell call is its command; an edit shows the lines it added
      and removed and opens to them; a tool with no title of its own is named from its name, with its MCP server
      as a tag. Titles, categories and the group's line are adapted from Omnigent (`tool-title.ts`, Apache 2.0).
    - A row opens to the result, 80 lines or 12 000 characters until "Show all".
    - A worker that succeeded folds to "Worked for 12m" and its last message, as t3code folds a finished turn; a
      failed or stopped one stays open.
    - The panel opens at a running worker's newest step and follows it until the reader scrolls away
      (`use-stick-to-bottom`); with a question, or for a finished Task, it opens at the top.
  - Questions are answered as in t3code's pending-input panel: options as radios that 1 to 9 pick, text where the
    option takes it, Enter sends. A question to pi is shown, and the owner can take it over (forward).
  - `/runs` lists the Runs, those that ask the owner first; `/runs/{id}` shows the objective, the open questions
    answered in place, the Tasks and the answered questions; Finish and Cancel.
  - The sidebar lists the places (New chat, Board with the count that needs the owner, Runs, Machines) above the
    chats.
    Every page follows `GET /events/stream` from the board's cursor and refreshes what an event may have changed.
- The machines (issue #69): `/machines` lists the nodes after Beszel's systems table (MIT), as cards (Grid, the
  default) or a Table, both from one column definition; the choice is kept in `localStorage`. A machine shows its
  state (Online, Drained, or Offline with when it was last seen), meters for CPU, memory and disk that turn amber
  from 65 % and red from 90 % and are grey while it is not connected, its load average, its Dispatches against its
  limit, its runtimes, its daemon version and platform, and its source: "Your own", or the size and region of a
  machine created at a provider. Such a machine that is not ready says Creating, Not created, Removing or Not
  removed instead, with the provider's reason for a failure. `GET /nodes` is read again every 5 s while the tab is
  visible, since no event says a measurement changed. A machine's menu drains or resumes it and removes it after a
  confirmation; a refusal shows the server's message. Where the server has machine providers, "Add machine" first asks where: a machine of the owner's own, or a
  new one at a provider, shown with its size, region and how many machines it has of its limit; a new one is
  created at once and needs no token, and a provider at its limit creates none. For a machine of the owner's own,
  "Add machine" takes a name and an optional limit, then shows
  the node's token and the install line once, each with a copy button; the token is kept only while that dialog is
  open.
- Settings (issue #44): one "Settings" entry at the sidebar's foot leads to `/settings`, where the sidebar's body
  gives way to the settings sections under headings, with a way back (the headings are t3code's, the swap is
  Omnigent's; [research](docs/research/2026-11-10-menus-and-settings.md)). A section is listed once it has a page;
  on a phone the sections are a row of links under the header.
  - `/settings/integrations` (under Connections) lists the git hosts as cards after Orca's integration card (MIT):
    name, a "Connected" or "No token" pill, and the actions. "Add token" / "Replace token" takes the token in a
    password field and sends it once; it is never read back. A company's server is added with its address (a name
    or a pasted URL), what it runs and, if wanted, its token. Removing a token or a declared server asks first.
  - `/settings/repositories` has no entry in the menu while its place is being decided (issue #44); it lists the registered repositories, adds one from its URL under a
    proposed name with an optional base branch, and removes one after a confirmation.
  - Both pages do only what `hosts ...` and `repos ...` do in the CLI.
- `web/embed.go` embeds the build (`web/dist`) into `hypomnema`. `make build` builds the client first.
  - The control plane serves it with `internal/controlplane/webui`, after Omnara's `spa.go`:
    - files as they are, with a year's cache under `assets/`;
    - `index.html` for any other path, with a Content-Security-Policy that allows only its own origin;
    - a 404 for a missing asset or a server path (`api`, `node`, `livez`, `readyz`).
  - A binary built without the client (only `dist/.gitkeep`) serves none.
- In development, `pnpm dev` forwards `/api`, `/livez` and `/readyz` to the control plane (`HYPOMNEMA_API_URL`,
  default `http://127.0.0.1:8080`).

## Where it runs

Since 2026-11-09, on the owner's tailnet (Tailscale); nothing is public:

- Control plane: zm, user `dat`, `~/hypomnema-cp` (binary, `env`, SQLite in `data/`), tmux session `hyp-cp`,
  listening on `127.0.0.1:18990`. Tailscale runs without root (userspace networking, tmux `hyp-ts`, state in
  `~/hypomnema-cp/tailscale`) and `tailscale serve` publishes it to the tailnet as
  `https://hypomnema.taild3b6dc.ts.net`.
- Model gateway, since 2026-11-10: the vault's root key is in `~/hypomnema-cp/env`; the secret `ninerouter` holds
  the key of 9router, which runs on the same machine and serves the owner's Codex subscription; provider
  `ninerouter` names it as `vault:ninerouter`; the gateway serves the model `gpt-6.1-sol` from it. pi calls the
  gateway with its own key (provider `gateway`, profile `coordinator`), no longer 9router itself.
- Node: aioffice-app, user `hypnode`, `~/bin/hypomnema-node`, tmux session `hyp-node`, pointed at that URL; the
  machine joined the tailnet as `aioffice-node`. The earlier control plane there (`~ubuntu/hypomnema-cp`) is
  stopped and kept for rollback.
- The owner's laptop joined as `laptop` and is logged in with a coordinator token; the coordinator skill is
  installed.
- Each machine joins the tailnet once, approved in the owner's Tailscale account. There is no service manager yet:
  a reboot needs the tmux sessions started again.

## Code

```
api                       the client API's contract (openapi.yaml) and oapi-codegen's configuration
internal/controlplane     package controlplane: configuration, migrate, serve and the owner's server commands
internal/controlplane/bearertoken  client token format: making, checking and hashing
internal/controlplane/clientapi  the client API: policies, handlers, events and their stream; generated code in openapi/
internal/controlplane/clientcli  the commands that call the client API, and the coordinator skill (skills/)
internal/controlplane/nodeapi  the node API: hello and the node WebSocket
internal/controlplane/webui    serves the embedded web client
internal/e2e               tests only: the control plane's node API with the node daemon
internal/node             package node: configuration, CLI detection, init, check, run and the systemd unit
internal/node/bridge      the mailbox between a worker's tools and the runner: questions, answers, the final report
internal/node/claude      the Claude Code runner: worker environment, stream-JSON, journal reading, outcome
internal/node/daemon      the node's connection: hello, socket, offers, outbox, Runner interface
internal/node/shim        one worker kept across daemon restarts: journal, input socket, exit file
internal/node/statedb     the node's SQLite state: Dispatches held and the report outbox
internal/node/workertools the worker's MCP server: ask and finish
internal/node/worktree    bare mirrors and git worktrees per Task
internal/controlplane/coordinator  the supervisor of pi, the coordinator: one pi in RPC mode per chat
internal/controlplane/machines  machines created at a provider: the Provider interface, the AWS (EC2) provider, the first-boot script, create, remove and the pass that settles what is left between
internal/controlplane/orchestration  Runs, Tasks, Dispatches, Conversations, questions, events, nodes, accounts, machine providers, the scheduler; migrations; queries generated by sqlc (internal/store)
internal/controlplane/subscription  subscription accounts: a vendor's login and refresh, tokens kept in the vault
internal/shared/githost   git hosts: how a repository is named, and each kind's clone URL, command and links
internal/shared/nodeproto the wire protocol between a node and the control plane
internal/shared/platform  environment settings, slog logger, HTTP server and probes, version, clock, ids
internal/shared/sqlite    SQLite opening, pragmas and file permissions
web                       the web client (pnpm) and embed.go, which embeds its build
```

There is no vault yet. `internal/e2e` holds only tests, so neither binary links it; it is the one package that
imports both trees. Boundaries ([decision](docs/decisions/2026-11-07-two-deployables.md))
are checked by `make verify`: depguard rules per tree in `.golangci.yml` (the SQLite driver only in `shared/sqlite`,
`database/sql` only in packages that own tables), and `deps-check`, which refuses the control plane and `web` in the node binary
and the node in the control plane binary.

## Verification

- `make verify`: gofmt, `go mod tidy -diff` for both modules, golangci-lint (`.golangci.yml`, tools pinned in
  `tools/ci/go.mod`), the dependency check, `sqlc-check` (generated queries match `sqlc generate`),
  `openapi-check` (generated client API code matches `api/openapi.yaml`), `migration-check` (migrations already on
  `origin/main` are unchanged), unit tests and race tests.
- Tests that need a database open a real file in a temporary directory; no containers.
- `make web-doctor`: React Doctor's report on the web client (pinned, local checks only: no score API, crash
  reports or dependency lookups; `web/doctor.config.json`). It is read, not a gate. Its config turns off the
  complexity count (a chain of states with early returns is not a defect), and two rules where they misread the
  code: route files export their `Route`, the Task panel's mutations refresh through one helper, and the
  transcript's reads stop on their abort signal.
- Oxlint loads `@shadcn/lint` (`jsPlugins` in `web/.oxlintrc.json`) with `no-raw-colors`, `no-restyle`,
  `no-unknown-classes` and `require-static-classes`; `no-arbitrary-values` and `no-inline-styles` are not enabled
  yet. Both tools leave the primitives alone (`web/src/components`: shadcn's components and theme provider).
- `make web-check`: oxlint, oxfmt, `tsc -b` and vitest for the web client; `make web-e2e`: Playwright on the
  production build, at a phone's and a desktop's size.
- CI (`.github/workflows/ci.yml`) runs `make verify` and `govulncheck`, and in a second job `make web-check` and
  `make web-e2e`, on every pull request and on `main`. Chromium for the browser tests is cached by the Playwright
  version, as t3code's CI does.
- Dependabot (`.github/dependabot.yml`) proposes updates weekly for the Go modules (`/`, `/tools/ci`), the web
  client, pi (`deploy/coordinator`) and the GitHub Actions, with minor and patch updates grouped.
