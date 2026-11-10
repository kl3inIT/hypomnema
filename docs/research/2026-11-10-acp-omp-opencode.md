# Driving omp and opencode over ACP

Date: 2026-11-10. For issue #107: omp and opencode as workers whose model the gateway serves. Read by a Sonnet scout
in the clones under `.tmp/`: omp at 660a31d, opencode at 145e948 (OpenCode 2), Orca at 4c13ae46, the OpenHands SDK at
e4160d1, Coder at b798061, Omnigent at cf1021e. Nothing was run; what is marked inferred was not read in code.

ACP, the Agent Client Protocol, is JSON-RPC 2.0 over an agent's standard input and output, protocol version 1.

## 1. The two CLIs

| | omp (`omp acp`) | opencode (`opencode acp`) |
|---|---|---|
| Its own directory for a run | `PI_CODING_AGENT_DIR` (agent directory), `--session-dir` | `OPENCODE_CONFIG` (a file), the `XDG_*` directories or `OPENCODE_DB` for its data |
| Naming a gateway | `models.yml` in the agent directory: a provider with `baseUrl`, `api: openai-completions`, `apiKey` (an environment variable's name first, else the literal), and its models (`docs/models.md`) | a provider with `package: "@opencode/ai/providers/openai-compatible"`, `settings.baseURL`, `settings.apiKey` as `{env:NAME}`, and its models (`providers.mdx`); the version 1 form is still read |
| The model and the effort | `--model provider/id`, `--thinking <level>` | `"model": "provider/id"` in the configuration; the effort as a variant |
| A session after the first turn | `session/resume` (no replay) or `session/load` (replays); needs the same session and agent directories | the same two; the `cwd` must be the session's |
| MCP servers of `session/new` | honoured; stdio as `{name, command, args, env: [{name, value}]}`; by default behind `xd://` and not plain tools, unless `tools.xdev: false` in `config.yml` | honoured; behind the `execute` tool unless `execute` is wholly denied (`core/src/tool.ts` L260) |
| Asking for permission | never by default ("yolo") | asks unless a rule allows; options `once`, `always`, `reject` |
| The client's files and terminal | used only when the client offers them | never |
| The end of a turn | the result of `session/prompt`: `stopReason` (`end_turn`, `cancelled`, `max_tokens`, `refusal`) and `usage` | the same |
| A model's failure | a message of the agent's and `end_turn`; a missing key as a JSON-RPC error -32603 | a JSON-RPC error with `data.errorName` such as `provider.rate-limit` |
| The process | lives until its input closes; nothing but ACP on its output | the same |

omp can also take its credentials from elsewhere than its own store: `OMP_AUTH_BROKER_URL` with a token replaces the
store with a remote one, refresh tokens never leaving the broker (`docs/auth-broker-gateway.md`); and an `apiKey`
starting with `!` runs a command.

## 2. How the references drive them

| | Launch | Session | A permission request | MCP |
|---|---|---|---|---|
| Orca (`src/main/acp/`) | `omp acp` with `PI_NO_PTY=1`; `opencode acp` with `OPENCODE_CLIENT=acp`, `OPENCODE_ENABLE_QUESTION_TOOL=false`; a dialect file for each | `initialize` with no file or terminal capability; `session/load`, or `session/resume` when preferred | shown to the user | passes none |
| OpenHands (`acp_agent.py`, `acp_providers.py`) | a table of providers: command, the variables for the key and the base URL; opencode's base URL only through its configuration file | keeps the session id and loads it; a new session when that fails | the first option | `McpServerStdio(name, command, args, env)` |
| Coder (`agent/x/agentacp`) | the library `github.com/coder/acp-go-sdk` (Apache-2.0) | `LoadSession`, then `ResumeSession` | the first `allow_always`, else `allow_once` | an empty list |

All three keep the connection in the process that drives the agent.

## 3. Read for Hypomnema

- One runner serves both; what differs fits a table entry: the configuration files, the setting that makes MCP tools
  plain, and how a failure is said.
- Hypomnema's workers run under a shim so that they outlive the daemon; a connection held by a library would not, so
  the few messages are written to the shim and read from its journal, in the references' order and with their answers.
- Advertising no forms keeps omp's own `ask` out, beside the tools server's (Orca's comment).

## 4. Tried, the same day

On the owner's node stc-hy-app, each CLI as its installer installs it, one turn against Hypomnema's gateway (model
`gpt-6.1-sol`, the chat route), with the node's tools server passed in `session/new`:

- **omp 18.18.6**: `initialize` answers protocol 1 with `loadSession` and the session capabilities `close`, `fork`,
  `list`, `resume`. With `tools.xdev: false` the tool `finish` was called as a plain tool; no permission was asked;
  the result carried `stopReason: end_turn` and `usage`. The session is a `.jsonl` file under `--session-dir`.
- **opencode 1.19.5**, not 2: `opencode.ai/install` installs version 1, whose `acp` command exists and answers the
  same capabilities. The configuration that worked is version 1's: `provider.<id>` with
  `npm: "@ai-sdk/openai-compatible"`, `options.baseURL`, `options.apiKey: "{env:NAME}"` and `models`, and
  `permission: {edit, bash, webfetch: "allow"}`. The tool was offered as `hypomnema_finish`, plain; no permission
  was asked. Section 1's opencode column describes version 2 as read in its source, which was not run.
