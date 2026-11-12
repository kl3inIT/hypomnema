# Hypomnema architecture

This page states what is implemented today, and nothing else. The target is in [docs/vision.md](docs/vision.md);
planned work is in GitHub issues and never appears here as a fact. Update this page in the change that makes a
statement true or false.

## Binaries

| Binary | Commands | What it does today |
|---|---|---|
| `cmd/hypomnema` (control plane) | `migrate`, `serve`, `version`, `nodes add\|list\|drain\|resume\|rm`, `nodes token add\|list\|revoke`, `machines providers add\|list\|rm\|check`, `machines create\|stop\|start`, `accounts add\|login\|list\|refresh\|rm`, `tokens add\|list\|revoke`, `secrets keygen\|add\|list\|rotate\|rm\|rewrap`, `gateway models\|keys`, `login`, `run`, `answer`, `questions` | `migrate` brings the database schema up to the binary's version; `serve` refuses any other schema, then serves `/livez`, `/readyz`, the node API, the client API, the model gateway and the web client, and runs the scheduler loop; the other commands are the owner's (below) |
| `cmd/hypomnema-node` (node) | `install`, `init`, `check`, `accounts set`, `run`, `service`, `shim`, `mcp`, `version` | `install --url` puts the node on the machine (below); `init --url --token-file` writes the node's configuration; `check` says hello once and lists the CLIs it found; `accounts set` records where an account's Claude token is; `run` runs the daemon with the Claude runner; `service` prints a systemd user unit; `shim` runs one worker and `mcp` serves its tools (both started for a worker, not by hand) |

Configuration comes from the environment: `HYPOMNEMA_HTTP_ADDR` (default `127.0.0.1:8080`),
`HYPOMNEMA_DATABASE_PATH` (required, absolute), `HYPOMNEMA_LOG_LEVEL`, `HYPOMNEMA_LOG_FORMAT` (`json` or `text`),
and the vault's root keys: `HYPOMNEMA_VAULT_KEYS` (a comma-separated list of `id:base64` entries of 32 bytes) with
`HYPOMNEMA_VAULT_ACTIVE_KEY_ID`, set together or not at all.
Which runtime and model workers use is stored, as profiles (below), and which model a Bot uses in the Bot, not
configured.
`HYPOMNEMA_COORDINATOR_COMMAND` (a JSON array) starts pi for a chat id appended to it; unset, chats keep their
messages and nothing answers.

The coordinator (`internal/controlplane/coordinator`, issue #41 PR 3): `serve` runs one `pi --mode rpc` per chat
that has something for pi, as the chat's Bot (`--bot`) with the Bot's model (`--provider`, `--model`, `--thinking`),
and stops it after 30 min idle by closing its stdin; it starts again with the same session id. pi's own retries are off. Owner
messages and wake-ups go as one `prompt`, or a follow-up while pi works, and are marked delivered once pi accepted
them. pi's activity is kept and relayed as Omnara does (issue #41 PR 4): each assistant `message_end` is kept as
the coordinator's message with its text and thinking as parts, and each finished tool call as a `tool` message with
its arguments and result (at most 16 KiB). Text as pi writes it, tools starting and ending, and working or idle go to
the chat's watchers from memory only and are never stored; `abort` stops pi's turn. On
the server pi runs as the `coordinator` user through `deploy/coordinator/`: a root-owned launcher (`hypomnema-pi`,
a clean environment, only a chat id, a Bot's name and model flags), one sudoers rule limited to it, the instructions
every Bot gets, pi's version with its lockfile (`package.json`, which Dependabot
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
  worktree volume's use, the one-minute load average and, on a machine with NVIDIA GPUs, each GPU's use, memory,
  temperature and power (read with `nvidia-smi` once a heartbeat, after Beszel's agent); only the latest measurement is kept, with its time
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
    node, with a first-boot script (cloud-init user data) that makes a `hypomnema` user without sudo, installs git,
    Node.js 22 from nodejs.org (one archive, checked against its published checksum) and the Claude Code and Codex
    CLIs, and runs the install script of `GET /install/node.sh` with the token. The
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
  - Stop and start (`machines stop|start <node>`, `POST /nodes/{node_id}/stop` and `/start`, owner only): a
    machine that is ready and on is turned off at the provider and keeps its disk, as Coder's AWS template stops
    an instance instead of destroying it. Its node is drained first, and a node that runs a Dispatch is refused.
    The machine's power is `stopping`, then `stopped`; a start makes the node active again and the power
    `starting` until the node says hello. A stop or a start the provider did not carry out is asked again every
    pass, with the reason recorded. A stopped machine still counts toward its provider's limit and can be removed.
  - `machines providers check <name>` proves the key works and lists the provider's machines that no node stands
    for; it runs when asked, not on a timer, and destroys nothing.
  - Not built: other providers, and delivering the CLIs' logins to a new machine, which the owner still sets on
    it once.
- A node updates itself (issue #69, after Nezha's agent, `cmd/agent/main.go`): the control plane names where
  the node's releases are at each hello (`HYPOMNEMA_NODE_RELEASE_URL`, the latest GitHub release by default). A
  node of a released version that systemd runs looks there 30 s after it starts and then at a random interval of
  one to two days (`update_every_minutes` in its configuration sets another), and when the release is newer it
  downloads it, requires the SHA-256 its manifest names and the version the binary itself reports, as the install
  script does, renames it over its own binary and ends with a failure, which makes systemd start the new one.
  It does not end while a terminal is open; workers are kept across the restart by their shims. A node built
  from a commit, a node not run by systemd, and one with `disable_auto_update` in its configuration keep their
  version.
- A terminal on a node, from the web (issue #116, [research](docs/research/2026-11-10-web-terminal.md); after
  Coder's reconnecting PTY, read for behaviour and not copied since it is AGPL, and Nezha's IOStream):
  - The node keeps shells by session id (`internal/node/terminal`): the node user's own shell on a pseudo-terminal
    (`creack/pty`), in the user's home, with `TERM=xterm-256color`. A shell outlives the connection watching it:
    its last 64 KiB of output go to a connection that attaches, a second connection takes a session over from the
    first, and a shell nobody watches for five minutes is ended. Linux and macOS only.
  - A node says at each hello whether it opens terminals (`nodes.terminal`): its build can, and its own
    configuration has not set `disable_terminal`, which the control plane cannot undo. It is on by default.
  - The owner asks for a ticket (`POST /nodes/{node_id}/terminal` with a session id; refused for a node that is
    not connected or opens no terminal), then opens the WebSocket `/api/v1/terminals/{ticket}`, which the ticket
    opens once within 30 s and only from the web client's own origin. There the page sends JSON, `{data}` for
    what is typed and `{height, width}` for a resize (2 to 500 rows, 2 to 1000 columns), and receives the shell's
    bytes in binary frames.
  - The control plane names a stream and asks the node on its socket (`terminal_open`); the node opens a second
    WebSocket (`/node/v1/terminal?stream=`) with its token and presents the stream; the two ends meet within
    10 s. The control plane only copies both ways, and passes the node's closing reason to the page.
  - Each terminal is logged when it opens and closes: the node, the session, who asked, from which address, how
    long it lasted. What is typed and shown is not recorded.
  - Not built: a command given in a link, several viewers of one shell, files, a shell for coordinators or
    workers, Windows.
- A browser every agent shares (issue #193, step 1 in issue #196,
  [research](docs/research/2026-11-12-embedded-browser.md); `internal/controlplane/browser`). Built so far: the
  engine on the control plane, an agent's calls to it, and the tools in a worker's hands. Not built: a person
  watching or taking control, the page in the web client, a node holding the browser instead.
  - It is one headless Chrome on the control plane's machine with one persistent profile, so a sign-in is made
    once and every agent has it, driven through agent-browser (Vercel Labs), as OpenMausBot's is. Each agent is an
    agent-browser session attached to that Chrome and pinned to a tab of its own
    (`--cdp`, `--pin-tab`); a worker's session is named after its Dispatch.
  - Both programs are pinned by version, size and SHA-256 (`release.go`) and fetched at the first use into
    `HYPOMNEMA_BROWSER_DIR` (default: `browser` beside the database), which also holds the profile and
    agent-browser's sockets: `tools/<name>/<version>/`, `profile/`, `home/`, `run/`. A file that does not match
    its pin is not installed. agent-browser is run with none of the control plane's environment but `PATH`.
  - Chrome is started by an agent's first call. A tab unused for 30 minutes is closed, and Chrome once it has had
    no tab for 5 minutes (T3 Code's rules); the sign-ins stay in the profile. Chrome found gone is started again,
    and the tabs it held are not used again. A Chrome left by an earlier control plane is closed at the first call.
  - The agent API has `GET /agent/v1/browser/tools` and `POST /agent/v1/browser/call` (`{tool, arguments}`), for
    any agent the API knows; the answer is the tool's result as MCP has it, `content` and `isError`, and nothing
    of how the engine is set up. Which agent used which tool is logged, never the arguments.
  - The tools are 22 of agent-browser's `core` set: open, snapshot, click, fill, type, press, check, uncheck,
    select, scroll, back, forward, reload, the waits, screenshot, get text, URL and title, eval. Tabs, `close`,
    flows and `read` are not offered. A call keeps only the tool's own arguments: the ones agent-browser takes on
    every tool (`session`, `extraArgs`, `restore` and the like) and a screenshot's path are dropped. `open` takes
    an http(s) address or `about:blank`; a bare host means https.
  - Chrome keeps its sandbox. On a machine that blocks it (Ubuntu 23.10 and later) a call fails and says to run
    `deploy/browser/setup-browser.sh` once with sudo, which writes an AppArmor profile and installs missing
    libraries (T3 Code's `t3 browser setup`, MIT, its license in `third_party/t3code/`).
    `HYPOMNEMA_BROWSER_SANDBOX=0` runs Chrome without the sandbox; nothing else does.
  - Every worker has the tools (issue #212), as T3 Code's and OpenMausBot's agents have theirs unless a setting
    turns them off; `HYPOMNEMA_BROWSER=0` on the control plane gives them to no agent. Every Dispatch gets a key
    to the agent API, also one whose Run was given no Connection: that key opens no account and says which worker
    asks. The runner writes the control plane's address and the key into the worker's bridge directory
    (`agent.json`), and the worker's MCP server (`internal/node/workertools`) calls the agent API itself, as
    OpenMausBot's stdio proxy calls its server: `tools/list` answers `ask`, `finish` and what the control plane
    lists, under agent-browser's names, so the list changes without a node release; any other tool's call is
    passed to `POST /agent/v1/browser/call` and its answer, a screenshot as image content, is passed back as it
    is. A control plane that gives no browser leaves the worker `ask` and `finish`. The tab is not closed when
    the Dispatch settles: the idle rules close it.
  - What the browser may reach is limited by a gate (issue #202; `internal/controlplane/browser/egress`, adapted
    from OpenMausBot's `server/browser/egress/`, Apache-2.0, its notice in `third_party/openmausbot/`). Chrome is
    launched with a SOCKS5 server inside the control plane, on a loopback port, as its proxy for every connection,
    loopback included, and is never started without it. The gate resolves each name itself, classes every address
    and connects only to the addresses it checked, so a redirect or a name that changes where it points reaches
    nothing else. The public internet passes. Cloud metadata, link-local and reserved addresses never pass.
    Loopback, the machine's own addresses and private networks (10/8, 100.64/10, 172.16/12, 192.168/16, fc00::/7)
    pass only where `HYPOMNEMA_BROWSER_ALLOW` lists them (`10.0.0.5`, `192.168.1.0/24`, `10.0.0.5:8080`,
    `[fd12::1]:443`, separated by commas; empty by default; a page opened as `localhost` needs its port listed, as
    `127.0.0.1:3000`, which covers `::1` too), and the control plane's own port, the gate's and
    Chrome's DevTools port never open on that machine. A refusal is logged as `browser.refused` with the host, the
    port and the reason; the agent sees Chrome's connection error.
- Chats (`orchestration/chats.go`, issue #41 PR 3, after Omnara;
  [research](docs/research/2026-11-09-chat-storage.md)): the owner's conversations with pi. Every message is kept
  (`owner`, `coordinator`, `tool`, `system`; replies and tools with JSON `parts`); owner messages and wake-ups wait
  for pi and are marked delivered one by one. A
  Run records the chat whose pi created it (`runs.chat_id`), and each event of such a Run that needs a coordinator (a
  Task ended or was blocked, a question to the coordinator, an escalation, the Run ended) adds one wake-up to the chat,
  unique per event.
- Bots (`orchestration/bots.go`, issue #169, after OpenMausBot's bot records and OpenClaw's `agents.entries`): a
  Bot is a row with a name, a model (provider, name, effort, checked as a profile's is, for pi), a persona and a
  memory. It is apart from the workers' profiles (owner, 2026-11-11). The Bot `coordinator` is always there: it is
  what the profile `coordinator` was, and a chat that names no Bot talks with it. A chat records its Bot. A Bot with
  chats or a memory, or one a Lark Channel talks to, is not removed, and `coordinator` never is; a Bot is not
  renamed. A provider change that would
  leave a Bot's model unserved is refused, as for a profile.
  - The persona is the owner's text of what the Bot is, without versions, as `SOUL.md` of OpenClaw and Hermes. How
    to give work to workers is the persona of `coordinator`; the instructions every Bot gets
    (`deploy/coordinator/AGENTS.md`) keep the skills, the wake-ups and the language.
  - The memory is the Bot's entries, one line each, at most 2,200 characters in all (Hermes's limit), the same in
    every chat with the Bot, whoever writes in it (one store per agent, as all three references have). The Bot
    changes it through `POST /bots/{name}/memory`: add, replace or remove the one entry that has a text. The change reads
    the memory again inside its transaction, and one that would pass the limit is refused and changes nothing, as
    Hermes's memory tool does (`tools/memory_tool_store.py`): the Bot makes room itself. The control plane keeps
    the memory in its database, not as a file in the Bot's directory as the references do: the control plane's
    user cannot read the `coordinator` user's home, and the owner reads the memory on the web. The owner edits it
    by hand, as the owner of a reference's agent edits its memory file (owner, 2026-11-11): `PUT
    /bots/{name}/memory` replaces the whole memory with his lines (empty lines and repeats dropped, the same
    limit), from the Bot's page or `hypomnema memory set`. No journal, no undo.
  - When pi starts for a chat the launcher makes the Bot's directory (`~/.hypomnema-coordinator/bots/<name>/`, pi's
    working directory; for `coordinator` a link to the directory pi always worked in, so its chats go on), reads
    `hypomnema bots prompt <name>` with pi's own token (the persona, the memory and how to change it) and gives it
    to pi with `--append-system-prompt`. The memory is thus read once per pi process, as Hermes gives its memory
    once per session; a change is in the prompt from pi's next start. pi's sessions stay in one directory for all
    Bots. pi changes the memory with `hypomnema memory add|replace|remove`, which knows the Bot from
    `HYPOMNEMA_BOT`. When the Bot cannot be read pi does not start.
  - Every token reads Bots and changes a memory an entry at a time; only the owner makes, changes and removes a
    Bot (`hypomnema bots list|show|set|rm`) and replaces a whole memory. Bots run as one server user with one token: a Bot's directory and memory are its own by
    convention, not by a wall.
  - A Bot's page lists the Lark Channels that talk to it, each a link to `/settings/channels`.
  - Not built: the accounts and tools a Bot may use, MCP servers and pi extensions per Bot, isolation between
    Bots, a Bot on another CLI than pi (issue #169).
- Profiles (`orchestration/profiles.go`, issue #42, after Omnara's agent configs): a profile is a YAML document
  (`name`, `description` of what it is for, `runtime`, optional `model: {provider, name, effort}`, `instruction`). Applying a changed source makes a
  new immutable version and points the name at it; the same source returns the current version. Providers (kind
  `anthropic`, `openai_compatible`, `claude_subscription` or `codex_subscription`, base URL, the name of where the
  API key is kept, never the key) list the models they offer. A profile and a new Task are checked when applied or
  created: a known runtime (`claude`, `codex`, `omp`, `opencode`; `pi` is a Bot's and is refused), an effort from `low` to `max`, and a model a provider of a
  kind that runtime can use offers (found from the model when the provider is not named). A provider change that
  would leave a profile's model or runtime unserved is refused. A Task names a profile, `implementer` by default.
  A provider of kind `model_gateway` is the control plane's own model gateway (issue #107): `codex` can run against
  it, and a Task under it needs no account on a node. Its Conversation names `gateway` where an account's id would
  be, and a rate limit cools no account down: the gateway looks after its own accounts.
  A profile's current version says whether a worker can start under it now (issue #139, as Omnigent's agent list
  says `available_on_host`): `available`, and when not, `unavailable_reason` (`no_node`: no connected, active node
  has its CLI; `no_account`: its runtime has no active account on a node; `accounts_cooling`, with `available_at`,
  the time the first account is free). It is computed from the scheduler's own view of nodes and accounts when
  the profile is read. A node at its limit still counts; a model the gateway serves needs no account, and the
  state of the gateway's own accounts is not looked at. `profiles
  list` shows it, and the coordinator skill tells the coordinator to read it before creating Tasks.
  A profile may name up to five `fallbacks`, other workers' profiles in the owner's order (issue #171, after
  OpenMausBot's backups of a bot and OpenClaw's fallbacks of an agent;
  [research](docs/research/2026-11-11-work-moves-when-out-of-quota.md)); they are kept in the version.
  The starting set: `implementer`, `reviewer` and `explorer` on Claude Code with its default model; provider
  `claude-subscription`. The reviewer (effort `high`) and the explorer say in their
  instruction how to report (issue #155, after omp's `reviewer` and `scout`): the reviewer a verdict, `correct` or
  `incorrect`, and findings with a priority P0 to P3, the file and the lines, only for what the change introduced;
  the explorer an answer, the files that matter and the detail with `path:line`. Both are told to change nothing;
  nothing enforces it. `deploy/profiles/` holds samples for profiles on Codex, whose model is a gateway model
  named by the deployment.
- Scheduler: `Schedule` is one pass in one transaction, and `Loop` runs `Reap` and `Schedule` every 2 s and on each
  commit signal. At most 4 Dispatches that have not settled exist at once, one waiting on a question included; the
  coordinator's `CreateDispatch` meets the same limit and checks. Ready Tasks of open Runs go oldest Run first, then
  by position; the runtime, model and effort are the Task's own or its profile's current version, and the Dispatch
  records the model, the effort and that version; its worker gets the version's instruction after the built-in rules. An open Conversation continues only on its node, once that node is active, connected, not
  unavailable and below its limit; a new one goes to the least loaded such node that has the runtime and a worktree
  root, ties by id, with the first active account of the runtime that is not cooling down (none for a model the
  gateway serves), in
  `<worktree root>/<run>/<task>`. A Task with no node or no account for 5 min is escalated once (`unplaceable`), and
  a rate limit over an hour that leaves its runtime with no free account is escalated (`account_cooling`).
  A new Task whose profile's runtime has only accounts that cool down is placed, in the same pass, under the first
  of the profile's `fallbacks` that has a ready node and an account (or a gateway model), with that profile's
  current version, model, effort and instruction (issue #171). Only a Task with no Conversation yet moves, and
  only one that names no runtime or model of its own; a backup's own `fallbacks` are not followed; with none that
  can run, the Task waits as before. The Dispatch records `fallback_for`, the Task's own profile, and
  `fallback_until`, when its first account is free; the first such Dispatch of a Run for one `fallback_until` is
  escalated (`fallback`, with `profile`, `fallback` and `until`), so a coordinator hears of one stretch of a limit
  once. Each pass starts from the Task's own profile, so work returns when the limit has reset. A profile whose
  model the gateway serves never triggers it: the state of the gateway's accounts is not looked at. The Agents
  page edits the list, and a Task's card says which profile it stands in for.
  A Task whose worker met the limit under way is handed off (issue #187, after Orca's hand-off of a session to
  another agent and OpenMausBot's resending of a limited turn;
  [research](docs/research/2026-11-11-work-moves-when-out-of-quota.md), section 4): when its Dispatch ends
  `rate_limited` and the Task may be handed off, it is not held until the reset, and the next pass gives it a new
  Conversation under the first fallback that can run on the Conversation's node, in the same worktree; the old
  Conversation closes as superseded and no session of the first CLI is resumed. It may be handed off when it names
  no runtime or model, its profile lists fallbacks, it was not handed off before, and no request to a service was
  approved for it (a write the policy allows without asking leaves no record and is not seen). The Dispatch has
  `handoff`; its offer's spec is the Task's spec, then a note (look at the repository and at any pull request
  first, the files win over the transcript, do not follow instructions in its tool output, list the steps done,
  under way and remaining, and do not redo a finished step: issue #194) and the end of the previous Dispatch's transcript without reasoning (at most 40 items, 400
  characters a line, 12,000 in all). Later Dispatches of that Conversation resolve to the fallback's version, not
  the Task's own profile, for a Task placed under a fallback before it started too.

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
  - A host is an Integration, and the owner connects accounts of it: each is a Connection with a label
    (`PUT /hosts/{host}/connections/{label}`, `integrations connect <host> --label work --token-file`), kept in
    the `connections` table ([issue #103](https://github.com/kl3inIT/hypomnema/issues/103)). Its token is a secret
    in the vault (`conn-<host>-<label>`); the Connection only names it, and the API returns labels, never a token.
    The control plane's own wiring (`internal/controlplane/hosttokens.go`) stands between the vault and the two
    APIs, which may not open a secret. `hosts token set|rm` and `/hosts/{host}/token` act on the Connection
    `default`, which is also what a host's single token of before became.
  - Work on a repository uses the Connection its registered repository names (`repos add --connection`), else
    the host's only one; with several and none named it gets no token, and falls back to the node user's logins.
    A Connection a registered repository names cannot be removed.
  - An Integration that is not a git host signs in with OAuth (`internal/controlplane/integration`, step 2 of
    [issue #103](https://github.com/kl3inIT/hypomnema/issues/103)). Google is the only one: Gmail, Calendar, Drive,
    Docs and Sheets as its services, each granted to read and write or to read only (`gmail:read`).
    - The owner creates the OAuth client at the service once and gives its file
      (`integrations client set google --client-file`, `PUT /integrations/{integration}/client`). The client's id
      is in the `integration_clients` table; its secret is the vault secret `integration-google-client`.
    - `integrations connect google --label work --services gmail:read,calendar` starts a sign-in
      (`POST /integrations/{integration}/sign-ins`) and prints a link for a browser on any machine. The browser
      ends on `http://localhost:1`, which does not load; the owner pastes that address and it completes the
      sign-in (`POST .../sign-ins/{id}`). The state and the PKCE verifier wait in the control plane's memory for
      30 minutes, so a restart loses a sign-in that was not completed.
    - The control plane exchanges the code, reads the account's email, and keeps the tokens as a token set in the
      vault (`conn-google-<label>`). The Connection records the email and the services Google's answer allowed,
      which may be less than was asked. Signing a label in again replaces its tokens and services.
    - `integration.Service.Token` gives a Connection's access token and refreshes it first when under two
      minutes are left, through the vault's lease. A refresh Google refuses for good marks the Connection as
      needing a sign-in (`sign_in_required`).
    - An Integration declares the command-line tool its skill calls, pinned to one version with a release file
      and its SHA-256 for each system: Google's is `gws` 0.22.5 (`github.com/googleworkspace/cli`) for Linux on
      amd64 and arm64. Once the Integration has a Connection, the control plane names the tool to every node of
      such a system at hello and with each heartbeat's answer (`nodeproto.Tool`). The node fetches the file over
      HTTPS, installs nothing when the checksum differs, and unpacks the command to
      `~/.hypomnema-node/tools/<name>/<version>/<name>`; it tries a failed install again every 5 minutes. Its
      heartbeat says what it has or why an install failed, kept in `nodes.tools` and shown as `tools` on a node
      in the client API.
    - **Agents use an account only when the owner says so** (step 3 of issue #103), in one of two ways. A Run is
      given Connections when it is created (`runs create --use google:work`, `uses` on a Run); its workers may use
      those and no other. The coordinator may use, in any chat, the Connections the owner switched on for chats
      (`integrations chats google:work on`, "Use in chats" on the account in Settings), and may give a Run only
      those.
    - **One way for every agent to call a tool** (`internal/shared/agenttool`, in both binaries). A script named
      after the tool (`gws`) is first on the agent's `PATH`; it runs `hypomnema tool gws` or
      `hypomnema-node tool gws`, the launcher. On every call the launcher asks the control plane's agent API
      (`internal/controlplane/agentapi`: `GET /agent/v1/connections`) which accounts the agent may use and runs
      the real tool. `--account <label>` names one of several and never reaches the tool.
    - **Google goes through the service proxy** (issue #154,
      [decision](docs/decisions/2026-11-11-service-proxy-in-the-control-plane.md),
      [research](docs/research/2026-11-11-service-proxy-and-approvals.md); `internal/controlplane/serviceproxy`).
      The tool is given a placeholder where a token would be, a proxy on loopback and the proxy's certificate
      (`GET /agent/v1/ca`). The launcher carries each connection the tool opens over a websocket to the control
      plane (`GET /agent/v1/tunnel`), which decrypts it with a certificate authority of its own, made when it
      starts and kept in memory. Each request is matched by method, host and path to an **action** of the
      Integration's catalog (send mail, share a file; data, as Onyx's) and answered by that action's Policy for the
      Connection: **allow** (reading, drafts, a new file), **ask** (what reaches other people or cannot be taken
      back; and any request that matches no action), or **never**. Only then is the account's token added. No
      token of a proxied Integration is handed to an agent. A request that the proxy and the service could read
      differently is refused: another address, a path that climbs or hides a slash, a method override, a query
      that does not parse strictly. What an agent may use is asked again for every request. Every request is
      logged with the agent, the Connection, the action and the verdict. A request is the action whose route
      names the most of its path. No launcher asks for a token any more; `POST /agent/v1/token` answers only an
      owner's token.
    - **Lark goes through it too** (issue #175). Its catalog is lark-cli's own (`lark_methods.json`, from
      lark-cli 1.0.100, MIT): each method's path with the risk lark-cli gives it. A service (messages, mail,
      calendar, tasks, drive and ten more) has up to three actions: read (allow), create or change (ask), delete
      and other high-risk changes (ask). A request to a service's part of the API that lark-cli's list does not
      name, as sending a message is not, is that service's write; a GET it does not name is a read; anything
      else asks as unnamed. The app's id, which lark-cli will not run without, is told with the list of accounts.
    - **An Approval** (migration 00039) is the owner's answer to a request that asked. The request is held up to
      100 s; the Approval keeps who asked (a Dispatch with its Task and Run, or a chat), the action and the request
      decoded for reading: a mail's recipients, subject and text, a file's name and size, a JSON body's values.
      The owner approves once, approves the action for the rest of the Run or the chat, or rejects. An Approval
      not answered in time stays open (owner, 2026-11-11): the tool is told it waits; a coordinator ends its turn
      and a worker asks the owner and waits. When it is answered later, the coordinator is woken in its chat, or
      the question the worker waits on is answered, and the repeat of the same request, recognized by its decoded
      content and not its bytes, passes once. Only the owner's token lists and answers Approvals
      (`GET /approvals`, `POST /approvals/{id}/decision`; `hypomnema approvals`) and sets an action's answer
      (`.../connections/{label}/actions`; `hypomnema integrations actions`). On the web an Approval is a card
      above the message box of the chat that asked, whose row in the sidebar says it needs the owner, and in the
      Task of the worker that asked; an account on the Integrations page says what agents may do with it and
      opens the list of actions.
    - **Who an agent is decides what it gets.** A worker presents its Dispatch's own key (`agent_sessions`,
      migration 00037): made by the node API as the offer is sent, for the Run's Connections, and revoked when the
      Dispatch settles, as its model gateway key is. The coordinator presents its coordinator token and gets the
      Connections allowed for chats. An owner's token gets every Connection. Each token handed out is logged with
      who got it, never the token.
      The key of a Dispatch whose Run a coordinator created works for a Connection only while the owner still
      allows it for chats: the switch is read at every request, so what he takes back from the coordinator its
      Runs lose too. A Run the owner created keeps what he gave it.
    - **A Task goes to a node that has the tool.** A Run that uses a Google account is placed only on a node
      that reported `gws` installed; while none has, the Task waits with `no_node_has_tool:gws`.
    - **On a node**, for a Dispatch with Connections, the runner writes the script and the skill's guide under the
      node's home (`tools/bin`, `skills/google-workspace`) and adds to the worker's environment the scripts'
      directory on `PATH`, the control plane's address, the Dispatch's key, and where the guide is; the worker's
      first message names the accounts, the command and the guide. **On the control plane's machine**,
      `deploy/coordinator/setup-coordinator.sh` installs the tools for the coordinator's user
      (`hypomnema tool install`), `hypomnema-pi` puts the scripts on pi's `PATH`, and the launcher uses the token
      pi is logged in with.
    - **The skill** (`internal/shared/agenttool/skills/google-workspace`) is taken from the Google Workspace
      CLI's skills (Apache-2.0) and Hermes' references (MIT), each file naming its source and what was changed:
      Gmail, Calendar and Drive commands, the mail search operators and a morning brief. Its guide replaces the
      tool's own sign-in with Hypomnema's, and tells the agent to get the owner's yes before anything that
      writes. `hypomnema skills install` installs its stub beside the coordinator's.
    - `integrations disconnect google:work` asks Google to revoke the tokens, then forgets the Connection and its
      secret; the answer says when Google could not be asked.
    - `GET /integrations` lists these Integrations with their Connections and their kind (`oauth`, `app`,
      `account`) for every token; a token, a password or a client secret is in no answer.
  - Lark is an Integration of kind `app` ([issue #135](https://github.com/kl3inIT/hypomnema/issues/135)): its
    client is a custom app of the owner's Lark organization, and nothing is signed in.
    - The owner gives the app's id and secret in a JSON file with `app_id` and `app_secret`
      (`integrations client set lark --client-file`); they are kept as an OAuth client's is, the secret as
      `integration-lark-client`.
    - `integrations connect lark --label work` (`PUT /integrations/{integration}/connections/{label}`) asks Lark
      for a tenant access token with the id and secret, reads the app's name with it, and records the Connection
      under that name. It has no services: the app may do what its scopes in Lark's console allow. A secret Lark
      refuses connects nothing. `POST .../sign-ins` refuses an app, and this refuses an Integration that signs in.
    - `integration.Service.Token` gives the tenant access token. It is kept in memory only and asked of Lark
      again when under two minutes are left of its two hours, or when the client was replaced. The agent API
      hands it to an agent that was given the Connection, as it hands Google's.
    - Its tool is `lark-cli` 1.0.100 (`github.com/larksuite/cli`) for Linux on amd64 and arm64, in the tool
      catalog beside `gws` (`internal/shared/agenttool`) and installed and launched the same way. It will not run
      on a token alone, so the agent API's answer also carries the app's id, and the launcher sets
      `LARKSUITE_CLI_TENANT_ACCESS_TOKEN`, `LARKSUITE_CLI_APP_ID` and `LARKSUITE_CLI_BRAND=lark` for it.
    - **Its skill** (`internal/shared/agenttool/skills/lark`) is one guide, adapted from `lark-cli`'s
      `lark-shared` skill (MIT): the command is already signed in, acts as the app and never as a person, an
      empty answer may mean the app was not given the thing, nothing is written without the owner's yes, and
      `lark-cli auth`, `config`, `profile` and `update` are never run. It has no references of its own: for each
      kind of work it names the guide `lark-cli` carries itself (`lark-cli skills read lark-doc`), which matches
      the installed version. It reaches a worker and the coordinator as Google's does.
    - `integrations disconnect lark:work` forgets the Connection and keeps the app's id and secret.
    - Only Lark international (`open.larksuite.com`) is declared; Feishu is not. A person's own sign-in (their
      mail, calendar and drive) is not built: a Connection acts as the app.
  - Superset is an Integration of kind `account` ([issue #203](https://github.com/kl3inIT/hypomnema/issues/203),
    [research](docs/research/2026-11-12-superset-integration.md)): a third kind beside `oauth` and `app`, for a
    self-hosted service. It has no client and no fixed address: a Connection is one deployment's address, a user
    name and a password.
    - `integrations connect superset --label vmaps --url URL --user NAME --password-file FILE`
      (`PUT /integrations/{integration}/connections/{label}` with a body) signs a local account in once
      (`POST {address}/api/v1/security/login`, `provider: db`), which shows the address and the account work, and
      records the Connection with the address, the user name and the version Superset says it runs
      (`GET {address}/version`). The password is the Connection's vault secret, `conn-superset-<label>`;
      migration 00049 adds `address`, `version` and `sign_in_failed` to `connections`. A refused sign-in stores
      nothing.
    - The access and refresh tokens are kept in memory only (`internal/controlplane/integration/account.go`). A
      request Superset refuses for its token (401, or the 422 of a token it cannot read) is sent again with a new
      one: from the refresh token first, then from the password. When the password no longer signs in, the
      Connection is marked (`sign_in_failed`) and shows "Needs sign-in" until it is connected again. A request
      that is not a GET carries the CSRF token, the session cookie it came with and a Referer.
    - **Its catalog** (`internal/controlplane/serviceproxy/superset.go`) is made from Superset's own description
      of its API (`superset_methods.json`: the resource, method and path of each of its 210 methods on 157
      paths). A part of Superset (dashboards, charts, datasets, databases, SQL Lab, queries, alerts and reports,
      annotations, tags, CSS templates, row level security, import and export, logs, the account and the server)
      has up to three actions: read (allow), create or change (ask), delete (ask). `POST /api/v1/chart/data`,
      laying SQL out and a dashboard's screenshot are reads though they are POSTs. Running SQL is one action of
      its own (ask), and so is administering users and roles (ask), beside reading them (allow). A method the
      description does not name is the read, the change or the delete of its part when that action exists, a GET
      elsewhere under `/api/v1/` is a read, and anything else asks as unnamed. The routes name no host: a request
      is matched by method and path and sent to the Connection's own address, and no tunnel is opened for
      Superset. The owner sets an action's answer as for Google and Lark. 41 actions are listed.
    - **A relayed request** (`serviceproxy.Proxy.Relay`). Superset has no vendor tool behind the launcher. A
      caller names a method and a path; the control plane answers the request with the same code as a request
      through a tunnel (`Proxy.judge`: the request that could be read two ways is refused, the action is matched,
      its Policy applied, the Approval asked and waited for, the line logged), and then has the service that signs
      accounts in send it to the Connection's address with the token (`integration.Service.Call`). The answer is
      returned unchanged. The Approval of a relayed request names the request in its title and first line
      ("Create or change: PUT /api/v1/chart/69"), since which chart is changed is in its path.
    - **The reads** (`internal/controlplane/superset`) are made of relayed requests, so each obeys the owner's
      answer for its action: `GET /integrations/superset/connections/{label}/dashboards?q=` (dashboards by title),
      `.../dashboards/{dashboard}` (its charts with their kind and dataset, its filters with the value each starts
      with) and `.../dashboards/{dashboard}/charts/{chart}/data` (column names, rows, the SQL Superset ran, when
      the figures were computed; JSON, or CSV with `format=csv`). A chart's data is asked with the query Superset
      saved with the chart when there is one; otherwise with a query built from the chart's settings as
      Superset's own plugin builds it, for `table`, `big_number_total`, `pie`, `echarts_timeseries_line` and
      `echarts_timeseries_bar`. Both get the dashboard's filters at the values they start with. Another kind
      without a saved query is refused as "not read yet" with its name. The chart's own row limit holds.
      `POST .../api` relays any other request.
    - **Who may ask**: the owner, and a coordinator token when the Connection is switched on for chats; the
      chat a coordinator asks from is passed with the request, so an Approval is answered there. A worker has no
      way to it yet.
    - **Commands**: `hypomnema superset dashboards [--search S]`, `superset dashboard <dashboard>`,
      `superset chart <dashboard> <chart> [--format json|csv] [--sql]` and
      `superset api <METHOD> <path> [--data FILE|-]`, each with `--connection L` (default: the only Superset
      Connection). They wait up to 240 s, longer than the 100 s an Approval is held.
    - **Its skill** (`internal/shared/agenttool/skills/superset`) is one guide for a Bot: what the commands
      return, that figures are as of the call, that a chart's row limit holds, that lists of people are personal
      data, and that writes and SQL may wait for the owner's approval. It is installed with the coordinator's
      skills and is not written to a node.
    - **The log**: each read is one line with who asked, the Connection, the action, the dashboard and chart and
      the number of rows (`superset.read`); each relayed request one with its action, method and status
      (`superset.relayed`), beside the proxy's own line with the verdict. Never a row, the password or a token.
    - **On the web** the Integrations page has a Superset card: add an account (name, address, user name,
      password), see its user name, address, version and whether the last sign-in worked, what agents may do
      with it, "Use in chats", connect again, disconnect.
  - Lark Channels (`internal/controlplane/larkbot`, issues
    [#136](https://github.com/kl3inIT/hypomnema/issues/136) and
    [#169](https://github.com/kl3inIT/hypomnema/issues/169)): a Channel is a door to one Bot, a Lark app and who
    may talk through it. A person's direct message to the app at Lark becomes a message of a chat with the
    Channel's Bot, and what the Bot says there goes back to Lark. There may be several Channels, and several may
    talk to one Bot (OpenClaw's `bindings[]`: one channel account routed to one agent). What Lark calls a bot is
    a Channel's app.
    - A Channel is a row of `lark_channels`: its name (as a Bot's, at most 50 characters), its app's id, the
      name of the vault secret with the app's secret, the Bot it talks to, its mode, the owner's `open_id` and
      whether it is on. One app is one Channel: an app that another Channel has is refused (Hermes refuses two
      profiles on one app). The one Lark bot there was before migration 00047 is the Channel `main`, which talks
      to `coordinator`.
    - It talks to Lark through Lark's Go SDK (`github.com/larksuite/oapi-sdk-go/v3`): events come over a
      persistent connection the control plane opens, one per Channel, so nothing public is opened for it. Only
      Lark international is served.
    - `channels lark set <channel> --app-file FILE --owner EMAIL [--bot BOT]`
      (`PUT /channels/lark/{channel}`) sets one up: the app's id, its secret (the vault secret
      `lark-channel-<channel>`, apart from the Lark Integration's; `main` keeps `lark-bot-app`, which it had as
      the one bot), the owner, whose email is looked up at Lark with the app and kept as his `open_id`, and the
      Bot, which must exist and is `coordinator` unless named. A new Channel is off and in mode `owner`.
      `--bot`, `--mode owner|allowlist|everyone` and `--on`/`--off` change it;
      `channels lark allow|disallow <channel> <email>` keep its allowlist; `channels lark list` and
      `channels lark show <channel>` say whether each is connected and why not; `channels lark rm <channel>`
      forgets it with its people, its Lark chats' rows and its secret, and the chats stay. All are the owner's.
    - A Channel that is set up and on connects at start, or at once when it is set up or switched on; one that
      could not connect is tried again every half minute. The SDK cannot close a persistent connection:
      switching a Channel off or removing it makes it take no message, and another app is used from the control
      plane's next start.
    - A message is taken when a person sent it in their direct chat with the Channel's app, once (a row per
      Lark message id, kept a day). The sender's organization must be the app's. The owner always may; others
      by the mode: the people he added, or every member of the organization. People are kept per Channel
      (`lark_channel_people`): Lark gives a person another `open_id` in each app. A refusal, a message that is
      not text and a pi that is not running are answered with a short notice. A group is not served.
    - Each person's direct chat is one chat with the Channel's Bot (`lark_channel_chats`), and `/new` opens
      another with it. A chat that is open keeps its Bot when the Channel is given another. The owner's text is
      added as it is; another person's is added under a line that names them and says approvals are the
      owner's. It is an owner message in the record, so pi answers it as any other.
    - The reply is a CardKit card: opened with the first text pi writes, updated at most twice a second from the
      supervisor's live frames, and closed with the kept message. A kept Bot message with nothing written live
      (a wake-up's answer) is a card of its own. The last message sent is recorded per chat, so a restart sends
      nothing twice.
    - A question for the owner is a card in his direct chat with a Channel's app (`lark_channel_questions`). It
      goes out through one Channel: the one whose Lark chat is the chat that started the question's Run
      (`runs.chat_id`), as Hermes sends an approval to the conversation the work came from, and for a Run
      started elsewhere the oldest Channel that is on. A Channel that is off sends none. The card has a button
      for each option, or "Reply to this card to answer" for a question without options. Only a press by the
      owner's `open_id` in that app answers; another person is told so. His reply to the card is the answer in
      his own words and does not go to the Bot. Once the question is answered or canceled, here or on the web,
      the card says how it ended and has no buttons. It needs the app's callback `card.action.trigger` over
      the persistent connection, configured in Lark's console.
    - On the web, `/settings/channels` (under Connections) has a card for each Channel: add one (its name, its
      app with the secret in a password field, the owner's email, its Bot), change its app, switch it on and
      off, choose its Bot and who may talk to it, add and remove people, remove it; it says whether it is
      connected and why not. It does what `channels lark ...` does.
    - What a person sees while the Bot works: their message carries the emoji `Typing` until the reply is
      there, and `CrossMark` when the model's call failed; the card answers that message (Lark shows it
      quoted), shows the tool that is running (`⚡ name: arguments`) before and under the text, and ends with a
      link to the same chat on the web when `HYPOMNEMA_PUBLIC_URL` is set. A message sent while a turn runs is
      told it is queued, by the name of the chat's Bot. A reply longer than 8,000 characters goes on in further
      cards. Lark's mention tags in a reply are written out, so nobody is notified. A card Lark stopped writing
      after ten minutes is switched on again. `/new` gives the person's next message a new chat; `/stop` stops
      the turn that runs: its card closes with what was written and the line "Stopped.", and the message is
      not marked as failed.
    - A question's card is orange while it waits, green once answered, red when canceled.
    - Not built: an option that also takes text, groups; a person's reaction as an answer; images
      and voice.
  - The token reaches a worker with its offer: the node API asks for it by host and repository as it sends the offer, and
    orchestration never holds it. The node gives it to that Dispatch only, as environment: git sends it to that
    host alone (an `http.https://<host>/.extraheader` through `GIT_CONFIG_COUNT`), `gh` reads `GH_TOKEN` (or
    `GH_ENTERPRISE_TOKEN` with `GH_HOST`), `glab` reads `GITLAB_TOKEN` and `GITLAB_HOST`. It is in no command's
    arguments and no clone URL. On the node it is in the offer kept in the node's state database until the
    Dispatch ends, and in the worker's spec file until the worker starts, as the Claude token is. The worker
    itself can read it: this stands in for the vault's credential sessions and the node's proxy
    ([issue #66](https://github.com/kl3inIT/hypomnema/issues/66)), with which a worker would never see it.
  - A host without a token works as before: git and the host's tool use the node user's own logins.
  - A node says which hosts it reaches. The control plane names the hosts it knows in the answer to hello and to
    every heartbeat; the node tries each when the list changes and every minute (an HTTPS request with no
    credential, through the environment's proxy; any answer counts, and so does a certificate the machine does not
    trust, so that git says why it fails there) and puts the hosts it reached in its heartbeat. They are kept on
    the node's row (`nodes.git_hosts`, `git_hosts` in the API), replaced by each report.
  - Placement gives a Task on a host only to a node that reaches it. When no connected, active node does, the Task
    waits, and after `UnplaceableAfter` the escalation says `no_node_reaches:<host>`. A node that has not said
    (one from before nodes tried hosts) is taken to reach every host; a Run without a repository runs anywhere.
    The control plane never calls a git host itself.
- Client tokens: kind `owner` or `coordinator`, in Omnara's format (`hyp_<kind>_v1_`, 43 Base62 characters and a
  CRC32 checksum, checked before the database is read; `internal/controlplane/bearertoken`). Shown once; only the
  SHA-256 is stored; a revoked token no longer authenticates.
- Users and web sessions (issue #114; [research](docs/research/2026-11-10-web-login.md), each rule from a
  reference): a user is a name and a password kept as bcrypt at cost 10, made on the server; none is shipped. Signing
  in opens a web session: a row with the SHA-256 of a random secret, good for 30 days after its last use (extended
  at most once an hour). A session acts as the owner. A new password ends every session of the user.
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
  coordinator or web package may import the vault (`depguard`).
- The owner reads the vault through the client API
  ([issue #108](https://github.com/kl3inIT/hypomnema/issues/108),
  [decision](docs/decisions/2026-11-10-owner-reads-secret-values.md)): `GET /secrets` lists secrets without values
  and with what uses each (a provider, a machine provider, an account, a Connection); `GET /secrets/{name}/value`
  returns one secret's values, with `Cache-Control: no-store`; `PUT /secrets/{name}` keeps a value as a new generic
  secret or a new version, which whatever uses the secret then gets; a token set is replaced with its complete
  set, as in Omnara's edit; `DELETE` is refused while something uses it. All four are owner-only by the policy table; a coordinator's token is
  refused, and nothing on a node has such an operation. The control plane's own wiring
  (`internal/controlplane/ownersecrets.go`) stands between the API and the vault.
- Each read of a value is a row of `secret_reads` (the secret's name and the token, or `host`; never the value). It
  is not in the event log, which every token reads. Nothing shows these rows yet.
- `hypomnema secrets show <name>` on the control plane host prints a value and records the read.
- A provider names a secret as `vault:<name>` in its `api_key_ref`; nothing resolves that reference yet. Deleting a
  secret is refused while a provider or an account names it.
- The tables `secrets`, `secret_versions` and `secret_refresh_leases` are in the one migration stream and are read and written only by this
  package, through its own generated queries.

Subscription accounts (`internal/controlplane/subscription`, issue #20 step 2, after CLIProxyAPI, 9router-go and the
Codex CLI; [research](docs/research/2026-11-10-subscription-login-and-refresh.md)) are logged in once on the control
plane, which keeps their tokens fresh. The gateway's Codex provider runs under them (below).

- One `Vendor` per kind of subscription starts a login, exchanges a refresh token and reads who a token set belongs
  to. Three exist, Codex, Antigravity and Claude. Codex: a device code (a link and a one-time code, 15 minutes), or, when OpenAI offers this client
  none, the browser login whose loopback address the owner pastes back, checked against the state and the PKCE
  verifier the control plane kept.
- Antigravity (after CLIProxyAPI, omp and Google's Gemini CLI;
  [research](docs/research/2026-11-10-antigravity-subscription-adapter.md)): Google's consent page for the
  Antigravity IDE's client, whose loopback address the owner pastes back. The login then reads the account's email
  and finds the Cloud Code project it works in (`loadCodeAssist`), asking Google to make one for the default tier
  when there is none (`onboardUser`, followed until done); a login with no project fails, and an account Google wants
  verified first is not created and the owner is given Google's link. The daily Cloud Code host is asked first, then
  the other. Google is called with the IDE's user agent only, at the version read from the IDE's update manifest
  (`internal/controlplane/cloudcode`). The client's OAuth secret comes from `HYPOMNEMA_ANTIGRAVITY_CLIENT_SECRET` and
  is not in the source; without it no Antigravity account can be logged in or refreshed. Antigravity's terms name
  access through other software as a breach that may end the account; the owner chose to build this knowing it.
  The gateway's Antigravity provider runs under such an account (below).
- Claude (issue #20 step 5, after 9router-go and CLIProxyAPI;
  [research](docs/research/2026-11-11-claude-subscription-at-a-gateway.md)): Claude Code's own OAuth client at
  `claude.ai`, whose `code#state` the owner pastes back, checked against the state and the PKCE verifier kept. The
  token is opaque, so who it belongs to is the login's to record (the account id, a derived one when the token
  answer and the profile name none). Anthropic's terms forbid routing a subscription through other software; the
  owner chose to build it for his own subscription and agents knowing that. The gateway's Claude provider runs under
  such an account (below).
- A login makes an account, or gives new tokens to the account that already has the vendor's id (Codex: the
  ChatGPT account id, read with the email and the plan from the id token; Antigravity: Google's id of the user, with
  the tier as the plan and the project). The tokens are a vault secret of kind
  `oauth_token_set`, named after the account, with the time the access token ends.
- `serve` looks every 5 minutes for access tokens that end within the vendor's lead (Codex: 30 minutes, Claude: 4
  hours) and
  refreshes them through the vault's `Refresh`; Antigravity's lead is 30 minutes too, and Google keeps the refresh
  token as it is. Two accounts due in one pass are refreshed one after the other, 1.5 seconds apart, 12 for
  Antigravity (9router-go). A refusal for good (401, or an error code such as `invalid_grant` or
  `refresh_token_reused`) marks the account as needing a new login and is not tried again. Any other failure keeps
  the tokens and is tried again after 5 minutes, doubling up to a day; its reason is recorded with the status and
  error code only.

The model gateway (`internal/controlplane/modelgateway`, issue #20,
[decision](docs/decisions/2026-11-10-model-gateway.md)) serves chat to clients that speak OpenAI's API, from
OpenAI-compatible providers the owner declares and from three built-in providers, the owner's Codex, Antigravity and
Claude subscriptions. Other provider formats and other capabilities do not exist yet.
- A model is a YAML document: a public `name`, a `capability` (`chat`, the only one), and `targets` (a provider and
  the model's name there) in the order tried. Applying a name again replaces it. A target must name a provider of a
  kind the gateway has an adapter for (`openai_compatible`, `codex_subscription`, `antigravity_subscription`,
  `claude_subscription`) and
  a model that provider offers, so a model that cannot
  be served is refused when applied.
- A gateway key (`hyp_gateway_v1_…`, the client tokens' format) is shown once and kept as its SHA-256, with the
  public model names it may use (`*`: every model). It is not a client token: the client API refuses it, and
  `tokens add` cannot make one.
- A Dispatch whose model the gateway serves gets a key of its own (issue #107, as the git host's token travels):
  made as its offer is sent to the node, good for the Dispatch's model only, carried in the offer (`model_key`). An
  offer sent again carries a new key and the earlier one stops. The control plane revokes a Dispatch's keys when the
  Dispatch settles, looked for at every commit of orchestration and every minute. The Codex runner uses it.
- A chat's pi gets a key of its own too (issue #204, after Omnigent, whose pi asks the process that started it):
  - The coordinator's supervisor asks for it as it starts pi for the chat (`AddChatKey`): good for the Bot's model,
    or for every model when the Bot names none. Its value stays in the supervisor's memory with that process; the
    row records the chat (`gateway_keys.chat_id`). A key made earlier for the chat stops as the new one is made.
  - The key is revoked when that pi exits or is stopped, and every chat's key is revoked as the control plane
    starts, since no pi outlives it.
  - pi reads it at each model request: the `apiKey` of its `models.json` is the command `hypomnema model-key`,
    which calls `GET /chats/{chat_id}/gateway-key` with pi's coordinator token and the chat of
    `HYPOMNEMA_CHAT_ID`. No pi running for the chat is 404. Only pi's own token gets it, by its name
    (`HYPOMNEMA_COORDINATOR_TOKEN`, `pi-coordinator` when unset, as the setup script names it): a chat's id is on
    the Runs every token reads, so another coordinator and the owner are refused with 403.
  - pi's `models.json` is written by the launcher as pi starts, from `hypomnema pi-models`: one provider of pi for
    each provider of the control plane that is the gateway (kind `model_gateway`, or an OpenAI-compatible provider
    whose address is the gateway's own), with the models that provider lists. A Bot may name a provider of kind
    `model_gateway`. No key is made by hand for pi, and the setup script installs none.
- `POST /v1/responses` (issue #107, after 9router-go;
  [research](docs/research/2026-11-10-codex-responses-passthrough.md)) is for a client that speaks OpenAI's Responses
  format, which the Codex CLI does and nothing else. It serves a model's `codex_subscription` targets only:
  - The client's body is passed on, not translated (`modelgateway/codex`, `PassThrough`): the model's name at the
    backend, `stream` on and `store` off, a string input as a user message, references to stored items and their ids
    taken out, `service_tier` `fast` as `priority`, the lite shape for the models that take it, the client's session
    as the prompt cache key when it names none, and only the fields the backend takes.
  - None of the client's headers goes to the backend but its session (`session-id`, `session_id` or
    `x-session-id`; one per gateway key without). The account, the Codex CLI's identity, a refused token, a usage
    limit and the next account are the chat route's.
  - The backend's event stream goes back line for line; the usage is read from `response.completed`. A refusal
    keeps its status and body.
  - Not served: a model with no Codex target (400 `model_not_served_here`), an answer that is not a stream (400),
    `/v1/responses/compact`, a WebSocket.
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
- A provider is of one of two sorts. An `openai_compatible` one is data the owner declares (a base URL, a key in the
  vault, its models), and its adapter passes the request and the answer through. A built-in one is seeded by a
  migration and has an adapter of its own, which translates both ways and handles everything specific to its vendor;
  the answer it returns is already in OpenAI's chat format, so failover, the stream's end and the usage record work
  the same for both.
- A built-in provider runs under the accounts of its kind logged in on the control plane, several when there are
  several ([research](docs/research/2026-11-10-several-accounts.md); every rule is one a reference has):
  - A request's session (its prompt cache key) hashes to a home account and every request of that conversation
    starts there, so the account's prompt cache stays warm (the cache is per account; omp `affinity.ts`, 9router-go
    `session_affinity.go`, CLIProxyAPI `selector.go`). A cooling home is stepped over to the next account and
    returned to once it is free. A request with no session takes the next account round-robin, to spread load. The
    home is a hash over all the runtime's accounts sorted by id, so a cooldown is a detour, not a move.
  - Inside one request the next account is tried when one answers 401 (after one refresh of its tokens), 402, 403,
    429, 502, 503 or 504, each account once and only before anything was sent to the client (9router-go, CLIProxyAPI).
    When none serves, the client gets the last refusal; when none could even be tried, a 429 with `Retry-After` to
    the time the first is free (CLIProxyAPI).
  - A failure cools the account down, with 9router-go's numbers: until the reset the provider names, at most 2 hours;
    1 hour for a quota that is used up with no reset named; 2 seconds, doubling with each failure in a row up to 5
    minutes, after a rate limit or an overload (the count is in memory and an answer clears it); 2 minutes after a
    401, 402 or 403; 30 seconds after another 5xx.
  - An Antigravity account with a tenth or less of a model's quota left is used after the others, not refused (omp's
    reserve, at opencode-antigravity-auth's 90 %). The quota is asked of Google after a successful request, at most
    every 15 minutes per account, and trusted for 30; an account whose quota is not known is used.
  - A Claude or Codex account with a tenth or less of its whole quota left is kept in reserve
    ([research](docs/research/2026-11-11-account-reserve.md)): it comes after the accounts that have more in the
    order above, a session's home too, and serves only when no other can; when every account is in reserve the
    order is unchanged (omp `select.ts` and `rank.ts`, at the same tenth). What is left is read from the headers of
    each answer served under the account, with no further call: one minus the fuller of Anthropic's 5-hour and 7-day
    windows (`anthropic-ratelimit-unified-*-utilization`) or of Codex's primary and secondary windows
    (`x-codex-*-used-percent`), as omp reads them. The reading is in memory and trusted for 30 minutes; an account
    with none is used.
- `codex-subscription` (kind `codex_subscription`, after 9router-go, CLIProxyAPI and the Codex CLI;
  [research](docs/research/2026-11-10-codex-subscription-adapter.md)) is the first built-in provider. It runs under
  the Codex accounts logged in on the control plane (above).
  - `modelgateway/codex` holds the translation, with no network in it. A chat body becomes a Responses body: system
    and developer messages become `instructions`, a tool call and its result become items tied by a call id, tools
    are flattened and not strict unless the client says so, `response_format` becomes `text.format`,
    `reasoning_effort` becomes `reasoning.effort` with a summary asked for. Only what the backend takes is sent:
    sampling settings, token limits, `user`, `metadata` and `stream_options` are left out. Tool names and call ids
    are made to fit the backend (64 bytes, a narrow alphabet) and the names are mapped back in the answer. The
    models `gpt-6.1-sol`, `gpt-6-sol` and `gpt-6-luna` take the "lite" shape the Codex CLI sends them: tools and
    instructions at the head of the input, one tool call at a time, reasoning over every turn with no summary.
  - The call goes to `chatgpt.com/backend-api/codex/responses`, always as a stream, with the account's access token
    and ChatGPT account id, the Codex CLI's `originator`, `User-Agent` and `version` (constants that need upkeep), and
    a session and prompt cache key that are the client's `prompt_cache_key` or one derived from the gateway key.
  - The backend's events become chat chunks: text, the reasoning summary as `reasoning_content`, tool calls by
    index with their arguments sent once, then the finish reason (`tool_calls`, `stop`, or `length` for an answer
    cut at the token limit) and the usage in a chunk of its own. A client that did not ask for a stream gets the
    whole answer as one completion.
  - A 401 refreshes the account's tokens once, through the vault, and the request is sent again. A usage limit
    (`usage_limit_reached`) cools the account down until the time the backend names, as a worker's rate limit does,
    and the client, or the model's next target, gets a 429 with `Retry-After`; while every account cools the backend
    is not called. Any other refusal reaches the client with the backend's status and body. An error inside the
    stream before anything was shown becomes an error status, so the next target can be tried; after, the stream
    ends as a cut one.
  - Not built: reasoning carried from one turn to the next, hosted tools (web search, images), the list of models
    read from the backend, a choice among several accounts beyond the first that is free.
- `antigravity-subscription` (kind `antigravity_subscription`, after six implementations and Google's own client;
  [research](docs/research/2026-11-10-antigravity-subscription-adapter.md)) serves Gemini, Claude and gpt-oss models
  from the Antigravity accounts logged in on the control plane (above). Antigravity's terms name this use as a breach that may end the
  account; the owner chose to build it knowing that, and the provider adds nothing to pass for the IDE beyond its
  user agent: tools keep their names, no tool or prompt is added, no `requestType` is sent.
  - `modelgateway/antigravity` holds the translation. A chat body becomes
    `{project, model, userAgent, requestId, request}` with the conversation in Gemini's shape: system messages as
    the instruction, a tool call as a `functionCall` part and its result as a `functionResponse` part of a user turn
    with the call's id and name, turns of one role merged, the user speaking first and last. The first call of a
    model turn carries `skip_thought_signature_validator`, the word Google's own client writes where a thought
    signature is missing, since a chat client sends none back. Tool schemas are cleaned of what Gemini refuses, and
    a tool with no parameter gets one. Function names are made to fit and mapped back. A Gemini model's
    `reasoning_effort` becomes a thinking level; the output limit is capped at 64000.
  - The call goes to `daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent`, always as a stream, with
    the account's token and project and the IDE's user agent (`internal/controlplane/cloudcode`).
  - The backend's chunks become chat chunks: text, thoughts as `reasoning_content`, each tool call whole, then the
    finish reason and the usage, which counts thought tokens as output.
  - An account runs at most 3 requests at once; one more gets a 429 at once. A quota that is used up makes that model
    of that account wait until the reset Google names (half an hour when it names none), a rate limit as long as
    Google asks; meanwhile the model is not sent to the backend, and other models are. No capacity at Google makes
    nothing wait. These waits are in memory. A 401 refreshes the tokens once.
  - An account Google suspended for its terms is taken out of use for a day, one Google wants verified for ten
    minutes; the cooldown's reason, with Google's link, shows in `accounts list`, and the request is answered 503
    so the model's next target serves it.
  - The models the provider is seeded with are names read in the references; the ones Google serves an account are
    what `accounts models` lists, and the owner applies the provider again with those when they differ.
  - Not built: real thought signatures kept across turns, images by URL, Google search as a tool, the provider's
    models kept in step with the account's by itself, several accounts rotated.
- `claude-subscription` (kind `claude_subscription`, after 9router-go and CLIProxyAPI;
  [research](docs/research/2026-11-11-claude-subscription-at-a-gateway.md)) serves Claude models from the Claude
  accounts logged in on the control plane (above). Anthropic forbids routing a subscription through other software
  and reroutes a request that does not look like Claude Code's; the owner chose to build it knowing that, so the
  provider dresses each request as Claude Code's, which is what makes it work at all.
  - `modelgateway/claude` holds the translation. A chat body becomes an Anthropic Messages body: system messages
    lifted out, a tool call and its result tied by a sanitized id, turns of one role merged, `reasoning_effort` a
    thinking budget, `max_tokens` filled when absent.
  - The disguise (9router-go `claude_cloaking.go`; CLIProxyAPI `checkSystemInstructions` for a client that is not
    Claude Code): the request carries the `oauth-2025-04-20` beta on a Bearer token and Claude Code's identity
    headers; the top-level system is made exactly Claude Code's — a billing block then the line "You are Claude Code,
    Anthropic's official CLI for Claude." — and the client's own system text is moved under the first user turn as a
    `<system-reminder>`, so Anthropic's classifier reads a Claude Code prefix; a `metadata.user_id` stable per token;
    the client's tools renamed with an `_ide` suffix among the twenty Claude Code decoy tools, undone in the answer.
  - Prompt caching, as the Codex adapter has it: the billing block's fingerprint is deterministic (CLIProxyAPI
    `computeFingerprint`, not 9router-go's random build and body hash) so the prompt's top is byte-identical between
    turns, and the cache breakpoints Claude Code sets go on — the system prefix, the last tool, the last message
    (pi/omp `applyAnthropicCacheControl`). A probe (Claude Code's own `quota`/`ping` check) is left minimal, with no
    decoys and no breakpoints.
  - The call goes to `api.anthropic.com/v1/messages?beta=true`, always as a stream, with the account's token in the
    Bearer header (never the api-key header), a refresh on a 401, a cooldown on a 429.
  - The backend's Messages events become chat chunks: text, thinking as `reasoning_content`, each tool call with the
    client's name again, then the finish reason and the usage, which counts cache reads as prompt tokens.
  - Not built: uTLS fingerprinting, a signed `cch`, header order and casing forced over the wire, model-specific
    thinking tables, several accounts rotated.
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
- Every request carries a web session's cookie or a client token (`Authorization: Bearer`); the cookie is looked
  at first, and without a valid one of the two the answer is 401. Signing in is the one operation that needs neither.
- `POST /session` takes a user's name and password and sets the session as a cookie: HttpOnly, `SameSite=Lax`,
  `Path=/`, and on HTTPS (the connection's TLS or `X-Forwarded-Proto`) `Secure` with the name
  `__Host-hypomnema_session`. A wrong name and a wrong password get the same 401. Five wrong tries from one address
  within an hour lock it for 30 seconds, then 2, 10 and 30 minutes (429 with `Retry-After`); the count is in memory
  and a successful sign-in clears it. `DELETE /session` removes the session and tells the browser to drop the cookie.
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
  - `nodes rename <node> <new name>` gives a node another name (`PATCH /nodes/{node_id}`, owner only): its id, its
    tokens and what it runs stay, and the name of another node that is not removed is refused;
  - `nodes token add <node> <name>` prints one more token once; `nodes token list <node>` and
    `nodes token revoke <id>`. A node connected with a revoked token is refused at its next heartbeat;
  - `machines providers add <name> --kind aws --key vault:<secret> --region R --size S [--max N]
    [--option name=value]…`, `machines providers list`, `machines providers rm|check <name>` and
    `machines create <name> --provider P [--region R] [--size S] [--max N]` manage provider machines; a machine
    is removed with `nodes rm`;
  - `accounts add claude <label> --token FILE|-` records an account whose token the control plane keeps (issue
    #150; [research](docs/research/2026-11-11-claude-credentials-for-workers.md)): the token of `claude
    setup-token`, read from a file or standard input and stored in the vault as the secret the account names. Such
    an account (`sent_to_node`) is placed like one a node holds, on any node that has Claude Code; the node API
    reads its token from the vault as it sends an offer (`account_token`), and the Claude Code runner starts the
    worker under it, with the account's own `CLAUDE_CONFIG_DIR`. The node keeps the token with the offer until the
    Dispatch ends. An account logged in on the control plane for the gateway is still not given to a worker.
  - `accounts disable <account>` keeps placement off an account, and `accounts enable <account>` lets it be used
    again; neither touches its token.
  - `accounts add <runtime> <label>` records an account whose CLI is logged in on a node; `accounts login codex|antigravity
    [--label L]` logs a subscription in here, printing a link and a code and waiting, or asking for the pasted
    address; `accounts list` shows each with the state of its tokens; `accounts models <account>` asks the vendor which
    models it serves the account, with what is left of each quota (Antigravity only); `accounts limits <account>`
    prints what is left of each window of a Claude or Codex subscription, 5 hours, week and a model's own week,
    with its reset (issue #199, step 3, after 9router-go, Orca and OpenMausBot): one call with the account's
    token to `api.anthropic.com/api/oauth/usage` or `chatgpt.com/backend-api/wham/usage`, neither a published
    API; the service keeps a reading for 60 seconds, does not ask for 3 minutes after a 429 (or the wait it
    names, up to 15 minutes) and gives the last reading when a call fails; a token the vendor refuses (401 or 403,
    as a Claude token pasted by hand is) is not sent again until it changes, and the refusal is said in the
    vendor's words; the limits are then what the headers of the account's last answer through the gateway said
    (`anthropic-ratelimit-unified-5h|7d|7d_oi-utilization` and `-reset`, after omp), kept in the server's memory,
    so the command, a process of its own, does not see them; `accounts refresh <account>` and
    `accounts rm <account>` (which removes the tokens too) are for those logged in here;
  - `tokens add <name> [--kind owner|coordinator]` prints a client token once; `tokens list` and
    `tokens revoke <id>`;
  - `users add <name>` makes a user of the web and `users passwd <name>` gives one a new password, both reading the
    password (at least 8 characters) from standard input; `users list`;
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
  - `hosts list|add|rm`, `integrations list|connect|disconnect` (and the older `hosts token set|rm`) and
    `repos list|add|rm`: the git hosts a Run can be on, the accounts connected to each, and the repositories
    registered under a name; every token reads them, only the owner's changes them.
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
  the guide compiled into the binary, `--reference <name>` one reference (`recovery`, `investigate`, `review`).
  The guide names no runtime and no vendor ([research](docs/research/2026-11-10-coordinator-routing-and-capabilities.md),
  issue #155): a profile is chosen by its description, which the owner writes; a question that changes nothing
  goes to a profile that reads and reports; a pull request is reviewed, when the owner asks or has said so as a
  standing rule, by a profile whose description says it reviews that kind of change, given the pull request and
  the spec only; findings that make a change incorrect are reported with an offer of a fix Task, two rounds at
  most; the model is the profile's unless the owner names one; and a profile or model that cannot be served is
  reported, not replaced. `skills install` writes the stub
  `SKILL.md` to `~/.claude/skills` and `~/.agents/skills`, replacing a copy only when it matches a released stub
  (otherwise only with `--force`, keeping a backup). Tests check that every command in the skill parses and that the
  stub and guide share their frontmatter.

Node (`internal/node`):
- `init` writes `~/.hypomnema-node/config.json` (mode 0600, directory 0700) with the control plane's URL, the token
  read from a file, and the worktree root.
- At connect the node finds `claude`, `codex`, `omp` and `opencode` on its PATH and reads their `--version`.
- The daemon has a runner per runtime and gives a Dispatch to the runner of its offer's runtime (issue #107).
- A Codex worker (`internal/node/codex`; [research](docs/research/2026-11-10-codex-responses-passthrough.md)) has
  no login on the node: its model comes from the control plane's gateway under the offer's key. One turn is one
  process under a shim, as a Claude Code worker's: `codex exec --json --skip-git-repo-check --ignore-user-config
  --dangerously-bypass-approvals-and-sandbox`, with `-c` overrides that name the gateway (`<control plane>/v1`,
  `wire_api = "responses"`, the key read from the environment variable `HYPOMNEMA_MODEL_KEY`) and the tools server
  (`ask`, `finish`), the model and the effort, and the prompt on standard input. `CODEX_HOME` is
  `conversations/<conversation>/codex` under the node's home, so a follow-up Dispatch continues with
  `exec resume <thread id>`. The thread id of `thread.started` is the session; the outcome is `finish`'s report,
  or else what `turn.failed` said. A stop kills the process.
- omp and opencode workers (`internal/node/acp`; [research](docs/research/2026-11-10-acp-omp-opencode.md)) share one
  runner that speaks the Agent Client Protocol, and their model comes from the gateway too. Each is an entry of a
  table: the command (`omp acp`, `opencode acp`) and the configuration written under
  `conversations/<conversation>/<runtime>` before a turn, which names the gateway with the key as an environment
  variable's name (omp: `models.yml` and `config.yml` in its agent directory; opencode: a configuration file of its
  own, away from the user's and the project's). A turn is `initialize`, `session/new` with the tools server as an
  MCP server (or `session/resume`, else `session/load`, for a follow-up; a session that is gone starts anew),
  `session/prompt`, then the agent's input is closed. The messages are written to the shim and read from its
  journal, so a turn survives the daemon; no ACP library holds the connection. A permission request is answered
  with the option that allows. Tried on 2026-11-10 on stc-hy-app with omp 18.18.6 and opencode 2.2.7, one turn each
  against the gateway, driven by a script that sends the runner's messages with the files the runner writes: both
  made a file and called `finish` as a plain tool, and neither asked for permission. The runner then ran a Task of
  each to its end there (node v0.6.0, profiles `omp-worker` and `opencode-worker`). A follow-up that resumes a
  session has not been tried with the real CLIs. The two CLIs were installed there by hand with their own
  installers and linked into `/usr/local/bin`; installing them by design is not built.
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
    and `--effort` when set).
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
    ([research](docs/research/2026-11-10-worker-sessions.md); one forwarder, `internal/node/transcript`, for every
    runtime since issue #157):
    - Each journal line becomes items of one shape for every runtime: `message`, `reasoning`, `tool_call`,
      `tool_result`, `notice`. The first instruction and the final result are not items. An item is cut to 64 KiB
      and says how much was left out.
    - A journal is in the format its runner names in a file `format` beside it: `claude` (stream-JSON; also a journal
      with no such file), `codex` (`exec --json`) or `acp`. Each has a decoder in its runner's package. Codex's
      commands are calls of `shell`, its file changes of `apply_patch`, its MCP calls `mcp__<server>__<tool>`; an
      ACP agent's chunks of text are one message or thought, a call is one item once its input is known (omp has
      it at once, opencode in the call's first update), and the worker's own tools are named
      `mcp__hypomnema__<tool>` whatever the CLI calls them. Usage, plans, command lists and permission requests
      are not items.
    - A decoder may hold something open: a message whose chunks go on, a call without its input. The cursor then
      stays before the line that opened it, so a daemon that restarts in the middle reads it again; what is open
      when the worker has exited is closed and sent.
    - The forwarder looks at `run/` each second and follows every journal there, so a runner does not tell it about
      a Dispatch; one whose directory the node released is forgotten.
    - A second cursor beside the journal (`sent`: offset and last seq) says how far the control plane has it. Each
      second the forwarder sends the items of the next whole lines, at most 32 items or 256 KiB, numbered from 1, and
      one batch of a Dispatch at a time. The cursor moves on the acknowledgement, which brings the next batch at
      once; a batch not acknowledged in 15 s is sent again with the same numbers.
    - The control plane stores items by `(dispatch, seq)`, ignores a seq it holds, takes them in any status of the
      Dispatch, and refuses for good a Dispatch that is not on that node. Items are dropped 30 days after the
      Dispatch settled.
    - A transcript the control plane has whole after the worker exited is marked `sent.done`. A new daemon sends the
      journals under `run/` that are not marked, those from before this existed too.
  - The worker's tools (`internal/node/workertools`, `hypomnema-node mcp`) come in through `--mcp-config` with
    `--strict-mcp-config`; AskUserQuestion and the subagent tool (`Task`, `Agent`) are disallowed. `ask` and `finish` reach the
    runner only through a bridge directory beside the journal (`internal/node/bridge`), so a question survives a
    daemon restart; the shared browser's tools go to the control plane (see "A browser every agent shares").
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
- A repository's setup script (issue #186, [research](docs/research/2026-11-12-a-repository-tools-for-workers.md)):
  a repository may declare one under `scripts.setup` in `hypomnema.yaml` at its root, as Orca's `orca.yaml` and
  t3code's `t3.json` do. The node runs it in a shell (bash, else sh) in a worktree it has just made, with
  `HYPOMNEMA_WORKTREE_PATH` set and git's prompts off, and starts the worker when it has ended (Orca's
  `wait-for-setup`, here always: nobody watches a headless worker that starts before its tools exist). It may run
  120 seconds, then its process group is killed. A script that fails, runs out of time or cannot be read is written
  to the node's log with the end of its output, and the worker starts all the same, as in Orca. A worktree that is
  reused (a continued Conversation) is not set up again. The daemon starts an accepted worker, fetch and setup
  included, on a goroutine of its own, so the session keeps reading, beating and renewing leases while a script runs
  (issue #218; Omnara's `launchWorker`); it runs under the daemon's context, so a reconnect does not cut it short.
  This repository's script (`deploy/worker/setup.sh`) puts
  Go at the version `go.mod` names under `$HOME/.local/go` on a Linux machine that lacks it, checked against the
  SHA-256 Go publishes. Not built: the script's failure in the transcript, asking the owner before a script runs
  the first time (Orca), and putting what it installed on the worker's `PATH`.
- There is no vault, permission rule or mailbox yet.

## Web client

- `web/`: a Vite single-page app with React, TypeScript, TanStack Router (file routes in `src/routes`) and Query,
  Tailwind and shadcn on Base UI (preset `b43eDcOJc`), pnpm
  ([stack and look](docs/research/2026-11-09-directories-sessions-web-stack.md)).
- The look follows t3code (MIT): its light and dark colors with a blue-indigo primary, 14px text, a 46rem chat lane,
  a full-height 16rem sidebar, its work-log rows, composer and markdown styles (`src/index.css`, `features/chat/`),
  each adapted file naming its source. Five status tokens (running, done, needs you, failed, queued) follow its
  palette; dark is the default.
- Sign-in (issue #114): a user's name and password, sent once to `POST /session`. The session is the server's
  HttpOnly cookie, which the browser sends by itself; the page keeps nothing in storage and asks `GET /me` on load
  to know whether it is signed in. A request answered 401 shows the sign-in screen again. Until 2026-11-10 the owner
  pasted an owner token that the browser kept in `localStorage`, as OpenClaw's control UI does.
- The API client is generated from `api/openapi.yaml` by `@hey-api/openapi-ts` into `src/api/generated` (as Omnara;
  its `next` build, since the release needs TypeScript 5's compiler API); `make web-api-check` fails when it is stale.
- Agents (issue #142, after Omnara's agent profiles, Omnigent's custom agents and OpenMausBot's history; in the
  main sidebar between Runs and Machines): `/agents` lists the profiles as cards or a table, each with its
  description, runtime, model, effort, version and whether a worker can start under it now, in words; it is read
  again every 10 s. `/agents/$name` shows one profile: a builder (the name, which cannot change, the description,
  the runtime, a model from `GET /models` in a picker with a search (issue #200, after Omnara's model combobox
  and t3code's model search: grouped by provider, every word typed found in the name or the provider; the Bots
  page uses the same field; a row leads with the mark of the model's maker, issue #205, and says the model by
  name with its id beside it, "Claude Opus 5.5" for `claude-opus-5-5`, issue #208, after t3code's
  `formatModelSlugName`; the cards say the same name and a profile still stores the id), its effort as a row of choices, the instructions,
  the fallbacks) and the YAML in Monaco with `monaco-yaml` (issue #177, as Omnara's editor): completion, hover and
  marks against a schema of the profile document written in the client, fetched when the YAML tab first opens.
  Both are saved with `PUT /profiles/{name}` as a new version, a refusal shown in the server's words; and its
  versions, each opening to its source, an earlier one restored by applying its source again. `/agents/new` makes
  a profile with the same builder; "Duplicate" on an agent's page opens it filled in from that agent
  (`/agents/new?from=<name>`, issue #200, after OpenMausBot's duplicate and Hermes's clone) under the first free
  name of `<name>-copy`, `<name>-copy-2`, and nothing is stored until it is created. The builder writes the YAML and does not read it back, so YAML edits are
  discarded, after a question, on the way back to the builder; leaving with unsaved changes asks first. The list
  is searched by name and description. An agent has an avatar (issue #177): one of OpenMausBot's ten mascot bodies
  and ten of its colours with its idle eyes, drawn still, both chosen by a hash of the agent's name, so nothing is
  stored; the mark of its CLI sits at the avatar's corner in the list. A version saved at time 0, as the starting
  profiles' are, shows no age. Not there: deleting, renaming or duplicating a profile (the API has no delete), and
  a check that nobody else saved meanwhile.
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
  from 65 % and red from 90 % and are grey while it is not connected, its load average, each of its GPUs when it has any (use with
  temperature, and memory, with the same meters; the table has the GPU columns only when a machine has one), its
  Dispatches against its limit, its runtimes, its daemon version and platform, and its source: "Your own", or the size and region of a
  machine created at a provider. Such a machine that is not ready says Creating, Not created, Removing or Not
  removed instead, with the provider's reason for a failure; one that is turned off says Stopping, Stopped or
  Starting, and a stopped one says that its disk is still billed and is started from its card. A machine that is on
  has "Stop machine" in its menu. A connected machine whose node opens terminals has "Terminal" in its menu, which
  opens `/machines/<node>/terminal` in a new tab: a page that is only the terminal (xterm.js), with the session's
  id in its address (`?reconnect=`), so a reload returns to the same shell. A dropped connection is said above
  the terminal and tried again by itself; a shell that ended offers a new session; a node without a terminal has
  the item switched off with the reason. `GET /nodes` is read again every 5 s while the tab is
  visible, since no event says a measurement changed. A machine's menu renames it (a dialog that starts from its name; only the
  name changes), drains or resumes it and removes it after a confirmation; a refusal shows the server's message. Where the server has machine providers, "Add machine" first asks where: a machine of the owner's own, or a
  new one at a provider, shown with its size, region and how many machines it has of its limit; a new one takes
  a size and a region, each the provider's default when left empty, is created at once and needs no token, and a
  provider at its limit creates none. A machine that was not created is removed from its own card, behind the
  same confirmation as the menu's. For a machine of the owner's own,
  "Add machine" takes a name and an optional limit, then shows
  the node's token and the install line once, each with a copy button; the token is kept only while that dialog is
  open.
- Settings (issue #44): one "Settings" entry at the sidebar's foot leads to `/settings`, where the sidebar's body
  gives way to the settings sections under headings, with a way back (the headings are t3code's, the swap is
  Omnigent's; [research](docs/research/2026-11-10-menus-and-settings.md)). A section is listed once it has a page;
  on a phone the sections are a row of links under the header.
  - `/settings/integrations` (under Connections) lists the git hosts as cards after Orca's integration card (MIT):
    the mark of what the host runs (GitHub's or GitLab's), its name, a "Connected" or "No token" badge, and the
    actions. "Add token" / "Replace token" takes the token in a
    password field and sends it once; it is never read back. A company's server is added with its address (a name
    or a pasted URL), what it runs and, if wanted, its token. Removing a token or a declared server asks first.
  - `/settings/repositories` has no entry in the menu while its place is being decided (issue #44); it lists the registered repositories, adds one from its URL under a
    proposed name with an optional base branch, and removes one after a confirmation.
  - `/settings/models` (under Models, [issue #199](https://github.com/kl3inIT/hypomnema/issues/199), step 1;
    [research](docs/research/2026-11-12-model-gateway-pages.md)) shows where models come from
    (`web/src/features/models/`).
    - A subscription (a provider of kind `claude_subscription`, `codex_subscription` or
      `antigravity_subscription`) is a card with the accounts of its runtime as rows, in the order the gateway tries
      them, after 9router's provider page, OpenClaw's model providers and Orca's accounts pane. A row shows the
      account's label, e-mail and plan, whether it was signed in on the control plane or on a machine, and the one
      thing wrong with it: off, needing a sign-in, or resting until a time.
    - A switch takes an account out of use and puts it back; the card's switch does so for all its accounts at
      once, as 9router's card toggle. An account signed in on the control plane can be removed, after a
      confirmation. Signing in, the order, the limits and the routes are not changed here yet.
    - A provider reached with a key shows its address and where its key is kept. The gateway's models are listed
      with the targets each is served from, in order, and whether a provider of kind `model_gateway` offers it to
      agents: a table on a wide screen, a block a model on a phone.
    - The client API behind it: `GET /accounts` gives each account's e-mail, plan and the state of its sign-in
      (`sign_in`: `active`, `login_required`, `no_tokens`; absent for an account signed in on a node);
      `POST /accounts/{id}/enable` and `/disable` and `DELETE /accounts/{id}` are the owner's;
      `GET /gateway/models` lists the gateway's models with their targets.
    - Signing in and the order (step 2 of the issue): "Add account" on a subscription's card and "Sign in again"
      on an account open a dialog with the two sign-ins `accounts login` has, after 9router's dialog on a remote
      host and CLIProxyAPI's management API. `POST /account-sign-ins` begins one at the vendor and answers with a
      link, and either `paste` (the owner pastes the address the browser ends on) or a `user_code` to enter at
      the vendor; `POST /account-sign-ins/{id}` completes it, with the address or by waiting. A sign-in begun is
      held in the control plane's memory until it ends, at most 16 at a time, and is completed once.
      `POST /accounts/{id}/move` moves an account one place up or down among the accounts of its runtime, as
      9router moves a connection; positions are written again from 1. All three are the owner's.
    - An account signed in on the control plane shows its limits under its row (step 3 of the issue): a meter a
      window (5 hours, week, a model's own week) with the part used, the Machines page's meter, and when the
      window starts again; a button asks the vendor now. `GET /accounts/{id}/limits` (the owner's; `fresh` asks
      now) gives the reading the subscription service keeps for a minute. An account with nothing to show is
      answered without windows and with the reason (`unavailable`: `not_given`, `sign_in`, `resting`, `failed`),
      and the page then shows no bars, or one line when the reading failed.
  - `/settings/vault` (under Connections, [issue #108](https://github.com/kl3inIT/hypomnema/issues/108)) is a
    table of the vault's secrets, after Infisical's secrets dashboard (MIT) and Omnara's secrets list
    (Apache-2.0): Name, Value, Type, Version and Updated as columns, sorted by Name or Updated, with a search box
    above. A
    value shows as asterisks and is not on the page until the owner reveals its row, or every row from the table's
    head: only then is `GET /secrets/{name}/value` called, and the value is dropped when hidden or the page is
    left. A token set shows each of its values. Edit replaces a generic secret's value, or a token set with its
    complete set; Delete says why when the server refuses. Add takes a name and a value in a password field.
  - `/settings/integrations` also lists the services that sign in with an account, above the git hosts (step 4
    of [issue #103](https://github.com/kl3inIT/hypomnema/issues/103)): Google's card with its accounts, each
    with its email, what it may do and whether it needs a sign-in. "Add account" and "Sign in again" take a name
    and, for each service, read and write, read only or not connected; then give a link to Google's page and take
    the address the browser ended on. "Disconnect" asks first. A service without its OAuth client shows the
    command that gives one; the client itself is given from the CLI only. Lark's card
    ([issue #135](https://github.com/kl3inIT/hypomnema/issues/135)) has "Connect app", which takes a name only,
    and its Connection has no "Sign in again".
  - Both pages do only what `hosts ...`, `repos ...` and `integrations ...` do in the CLI.
  - The Settings screens are built on the standard components ([issue #123](https://github.com/kl3inIT/hypomnema/issues/123)):
    every shadcn component of the project's style is in `web/src/components/ui`; a form is TanStack Form with a
    zod schema, and says what is wrong when it is sent; the Vault's table is TanStack Table; a removal asks with
    an alert dialog; what the server refused shows as an alert. The rules are in
    [conventions](docs/conventions.md#web-client).
  - The other screens use the standard components where a control is an ordinary one
    ([issue #131](https://github.com/kl3inIT/hypomnema/issues/131)): the Board's repository select, the buttons of
    a Task's panel and of a Run, the sign-in form, the Add machine dialog. Left as they are, each for a stated
    reason in that issue: the chat, the question form, a Task's card and a Run's rows, a transcript's rows, the
    Machines cards, table, switch and menu, the Board's "Group by" switch, the Task panel as a side panel.
  - A change the owner made in Settings that succeeded says so in a toast (`said` in
    `web/src/features/settings/use-integrations.ts`).
  - The build runs the React Compiler (`babel-plugin-react-compiler` through `@rolldown/plugin-babel`,
    `web/vite.config.ts`); the unit tests do not, the scenarios in `web/tests` run the built client.
  - One `TooltipProvider` and one `Toaster` wrap the app (`web/src/main.tsx`). On the Machines page a column's
    name says what it measures when pointed at, and a machine's runtimes are their marks with their names while
    they fit on one line, and the marks alone when they do not.
- `web/embed.go` embeds the build (`web/dist`) into `hypomnema`. `make build` builds the client first.
  - The control plane serves it with `internal/controlplane/webui`, after Omnara's `spa.go`:
    - files as they are, with a year's cache under `assets/`;
    - `index.html` for any other path, with a Content-Security-Policy that allows only its own origin;
    - a 404 for a missing asset or a server path (`api`, `node`, `livez`, `readyz`).
  - A binary built without the client (only `dist/.gitkeep`) serves none.
- In development, `pnpm dev` forwards `/api`, `/livez` and `/readyz` to the control plane (`HYPOMNEMA_API_URL`,
  default `http://127.0.0.1:8080`).

## Where it runs

Since 2026-11-09, on the owner's tailnet (Tailscale). Since 2026-11-10 the control plane is also reachable from
the internet, so that a machine created at a provider can install the node from it and connect to it:

- Control plane: zm, user `dat`, `~/hypomnema-cp` (binary, `env`, SQLite in `data/`), tmux session `hyp-cp`,
  listening on `127.0.0.1:18990`. Tailscale runs without root (userspace networking, tmux `hyp-ts`, state in
  `~/hypomnema-cp/tailscale`) and Tailscale Funnel publishes it to the internet as
  `https://hypomnema.taild3b6dc.ts.net`, which is `HYPOMNEMA_PUBLIC_URL` in `env`. The client API still refuses a
  call without a token. `tailscale funnel --https=443 off` makes it tailnet-only again, and then no machine can
  be created at a provider.
- Machines at AWS, since 2026-11-10: the provider `aws` (`ap-southeast-1`, `t3.medium`, up to 3 machines), its
  key in the vault as `aws`.
- Model gateway, since 2026-11-10: the vault's root key is in `~/hypomnema-cp/env`. The owner's Codex account is
  logged in on the control plane, and the gateway serves the model `gpt-6.1-sol` from `codex-subscription` only.
  Until 2026-11-10 its second target was `ninerouter`, 9router on the same machine; the gateway no longer calls it.
  The provider `ninerouter` and its key, the secret `ninerouter`, are still stored and used by nothing: a provider
  cannot be removed yet. pi calls the gateway with its own key (provider `gateway`, the Bot `coordinator`, which the
  profile `coordinator` became on 2026-11-12 with the launcher that takes `--bot`; the Bot's memory is empty).
- An Antigravity account of the owner's is logged in too, since 2026-11-10; the OAuth secret of the IDE's client is
  in `~/hypomnema-cp/env`. Provider `antigravity-subscription` was applied again there with the models Google
  serves that account (`accounts models`): Gemini 2.5, 3 and 3.8, `claude-sonnet-4-6`, `claude-opus-4-6-thinking`,
  `gpt-oss-120b-medium`. Since 2026-11-11 the gateway model `claude-sonnet` is served from it
  (`claude-sonnet-4-6`); the profile `omp-claude` runs omp on it.
- Lark, since 2026-11-10: the owner's Lark app (Lark international) is the client of the Integration `lark`, its
  secret in the vault as `integration-lark-client`, and connected as `lark:work`. Nodes installed `lark-cli`
  1.0.100 themselves. A worker calls it from node v0.6.1 on: a Run given `lark:work` on aioffice-app ran
  `lark-cli whoami` and a call to Lark as the app, and found the guide. A node writes a tool's script from the
  tool catalog compiled into it, so a new tool reaches workers with a node release; the other nodes take v0.6.1
  at their next update check. The coordinator on zm has neither `lark-cli` nor the skill yet: its tools are
  installed by `deploy/coordinator/setup-coordinator.sh`, which needs root.
- The Lark bot, since 2026-11-11: set up on zm with the owner's Lark app, mode `owner`, switched on, and
  connected. The app's callback `card.action.trigger` was added over the persistent connection and released as
  version 1.0.3 of the app. Tried: a worker's question with two options was answered by the owner nine seconds
  after it was asked, and the worker went on with the option chosen. Not tried yet: a message from the owner
  through to the coordinator's answer.
- Nodes, since 2026-11-10, five: aioffice-app (user `hypnode`), and aioffice-serving, elitex, kdlstc-dev and
  stc-hy-app (user `hypomnema`, installed with the first-boot script, each reaching the control plane at its
  public URL). Each runs the node as a systemd user unit from `~/.hypomnema-node`, at v0.7.0 on 2026-11-11, and
  looks for a newer release every 60 minutes (`update_every_minutes` in its `config.json`; the default is one to
  two days). All have Claude Code; the four new ones have the Codex CLI; stc-hy-app also has omp 18.18.6 and
  opencode 2.2.7, installed by hand with their own installers and linked into `/usr/local/bin`. The earlier
  control plane on aioffice-app (`~ubuntu/hypomnema-cp`) is stopped and kept for rollback.
- Accounts, since 2026-11-11: every one is kept by the control plane. `claude kept` is a token sent to the node with
  each Dispatch; the earlier `claude personal`, a token file on aioffice-app, is disabled. Workers under Codex, omp
  and opencode run from the profiles `codex-worker`, `omp-worker`, `opencode-worker` and `omp-claude`, against the
  provider `model-gateway`.
- The owner's laptop joined as `laptop` and is logged in with a coordinator token; the coordinator skill is
  installed.
- Each machine joins the tailnet once, approved in the owner's Tailscale account. There is no service manager yet:
  a reboot needs the tmux sessions started again.

## Code

```
api                       the client API's contract (openapi.yaml) and oapi-codegen's configuration
internal/controlplane     package controlplane: configuration, migrate, serve and the owner's server commands
internal/controlplane/bearertoken  client token format: making, checking and hashing
internal/controlplane/browser  the browser every agent shares: the pinned agent-browser and headless Chrome, one Chrome and a session an agent, the tools offered, the idle rules
internal/controlplane/browser/egress  the gate on what the shared browser may reach: the SOCKS5 server Chrome goes through, the address classes, the owner's allow list
internal/controlplane/clientapi  the client API: policies, handlers, events and their stream; generated code in openapi/
internal/controlplane/clientcli  the commands that call the client API, and the coordinator skill (skills/)
internal/controlplane/nodeapi  the node API: hello and the node WebSocket
internal/controlplane/webui    serves the embedded web client
internal/e2e               tests only: the control plane's node API with the node daemon
internal/node             package node: configuration, CLI detection, init, check, run and the systemd unit
internal/node/bridge      the mailbox between a worker's tools and the runner: questions, answers, the final report
internal/node/claude      the Claude Code runner: worker environment, stream-JSON, journal reading, outcome
internal/node/codex       the Codex CLI runner: the gateway as its provider, `exec --json`, journal reading, outcome, and its transcript decoder
internal/node/acp         the ACP runner for omp and opencode: each CLI's configuration, the conversation, outcome, and its transcript decoder
internal/node/transcript  the forwarder of every worker's transcript, the decoder interface, the cutting of items
internal/node/daemon      the node's connection: hello, socket, offers, outbox, Runner interface
internal/node/shim        one worker kept across daemon restarts: journal, input socket, exit file
internal/node/statedb     the node's SQLite state: Dispatches held and the report outbox
internal/node/terminal    the shells opened from the web: a session per id, its ring of output, the idle end
internal/node/workertools the worker's MCP server: ask and finish, and the shared browser's tools passed on to the control plane
internal/node/worktree    bare mirrors and git worktrees per Task
internal/controlplane/cloudcode  what is presented to Google's Cloud Code backend: its hosts, the Antigravity IDE's user agent and version
internal/controlplane/coordinator  the supervisor of pi, the coordinator: one pi in RPC mode per chat
internal/controlplane/machines  machines created at a provider: the Provider interface, the AWS (EC2) provider, the first-boot script, create, remove and the pass that settles what is left between
internal/controlplane/orchestration  Runs, Tasks, Dispatches, Conversations, questions, events, nodes, accounts, machine providers, the scheduler; migrations; queries generated by sqlc (internal/store)
internal/controlplane/modelgateway/antigravity  the Antigravity subscription's formats: chat to Gemini's shape and back, the schema cleaner
internal/controlplane/modelgateway/codex  the Codex subscription's formats: chat to Responses and back
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
- CI (`.github/workflows/ci.yml`; [research](docs/research/2026-11-10-ci-and-tests.md)) runs on every pull request
  and on `main`, as jobs side by side: `Static` (`make verify-static` and `govulncheck`), `Unit` (`make unit`),
  `Race` (`make race`) and `Web` (`make web-check`, `make web-e2e`). Each Go job keeps its own build cache. A first
  job, `Changes`, finds whether anything but documentation changed (`docs/**`, `*.md`); when nothing did, the four
  are skipped. A last job, `Required`, passes when none of them failed and is the one check to wait for. Chromium
  for the browser tests is cached by the Playwright version, as t3code's CI does. `make verify` is still the three
  Go targets in a row, for a laptop.
- A test that needs a migrated database opens a copy of one: `internal/shared/sqlite/sqlitetest` runs the migrations
  once in a test process and hands each test a copy of that file (issue #129, as Coder and Omnara copy a Postgres
  template). Migrating took 1.5 s for each test under the race detector. Tests of the migrations themselves, and
  the owner commands' tests, which migrate through the command, still migrate their own.
- Dependabot (`.github/dependabot.yml`) proposes updates weekly for the Go modules (`/`, `/tools/ci`), the web
  client, pi (`deploy/coordinator`) and the GitHub Actions, with minor and patch updates grouped.
