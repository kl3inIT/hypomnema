# Glossary

One meaning per term. Use these words in code, documents, issues and messages. A term from a reference project that
means something else is named here once and not used.

| Term | Meaning | Not to be confused with |
|---|---|---|
| **Owner** | The one person who assigns work, approves gates and owns every resource | A user of a multi-tenant product |
| **Run** | One objective from the owner, such as "add login with Google to second-bite". Holds Tasks | A model "run" or a CI run |
| **Task** | One unit of work inside a Run, with dependencies on other Tasks of the same Run (a DAG) | Orca's task, which has the same meaning |
| **Dispatch** | One attempt to carry out a Task: placed on one Node with one Runtime, with an epoch for fencing. A retry is a new Dispatch | The Task itself; a Task may have many Dispatches |
| **Gate** | A decision a Task waits on, answered by the owner (plan approval, merge approval, a question) | A per-tool-call permission prompt, which is a Policy verdict |
| **Coordinator** | The agent that plans a Run, creates its Tasks, answers or forwards its workers' questions and summarises results; it does not do the work itself and holds no state. It is pi on the server, or the owner's own agent (Claude Code, Codex) calling the Client API; each Run has one, recorded by its token. Called Brain until 2026-11-07, as in older research notes; the role is Orca's coordinator | A worker |
| **Chat** | A conversation between the owner and pi, the coordinator on the server, kept by the control plane; a Run pi creates belongs to its chat, which its events wake | pi's own session file, which is pi's memory, not the record |
| **Orchestration** | The control plane module that keeps Runs, Tasks, Dispatches, Gates, leases, mailboxes and the event log, and is the only writer of that state. It runs nothing itself. Called execution until 2026-11-07; the name is Orca's (`src/main/runtime/orchestration/`) | The Coordinator, which decides; orchestration keeps the record |
| **Worker** | A CLI agent process (Claude Code, Codex, or pi) carrying out one Dispatch | The Node it runs on |
| **Conversation** | A Worker's CLI conversation (a Claude Code session id, a Codex thread id) on one Node and worktree, recorded by orchestration. A later Dispatch of the same Task continues it when the previous Dispatch settled cleanly; a crash or an unknown outcome starts a new one with a recovery note | A Dispatch, which is one attempt and ends; a vault Session |
| **Runtime** | The kind of worker and how it is driven: `claude-cli`, `codex`, `pi` | A model or a provider |
| **Profile** | A named, versioned choice of runtime, provider, model, effort and instruction that an agent Hypomnema starts runs with: a worker through its Task, pi as `coordinator`. A change is a new version; a Dispatch records the version it ran with. Called Role until 2026-11-09, when roles were defaults read from the environment | A Runtime, which is code on the node; the owner's own agent, whose model the owner picks |
| **Node** | A machine paired with the control plane that runs Workers: a VM, a server, a sandbox | Omnara's "machine", same meaning |
| **Account** | One credential for a model provider: a Claude, Codex or Antigravity subscription, or an API key. Kept by the control plane; a Runtime runs under it directly, the Model gateway serves it, or both; a Dispatch records which one it used | A user account of the owner |
| **Client API** | The control plane's one API, `/api/v1`, described by `api/openapi.yaml`, which the web client, the `hypomnema` CLI and coordinators call with a Client token | The node API (nodes only); the Model gateway |
| **Client token** | A bearer token for the Client API, of kind `owner` (everything) or `coordinator` (its own Runs, and reading the rest); shown once, stored as a hash | A node's token (`hyn_`); an Account's credential |
| **User** | Someone who signs in to the web client with a name and a password, made on the server; acts as the owner | An Account (a subscription a CLI or the gateway runs under); a Client token |
| **Web session** | A signed-in browser: a cookie whose secret the control plane keeps as a hash, good for 30 days after its last use | A CLI's session (`cli_session_ref`); a Conversation |
| **Model gateway** | The control plane module that serves models to pi, to workers that are not a vendor's CLI and to the owner's tools: an OpenAI-compatible API over the Model table. It picks the Account and records usage. A vendor's CLI (Claude Code, Codex) does not go through it | A Node's egress proxy |
| **Model table** | The models the Model gateway serves, declared by the owner: a public name, what it can do (chat, images, speech), and its targets (provider and upstream model) in fallback order. A model that is not in it is refused | A provider's own model list |
| **Gateway key** | A bearer key for the Model gateway: one for the coordinator, one per tool or machine of the owner, one per Dispatch of a worker that uses the gateway (revoked when it ends), each with the models it may use | A Client token; an Account's credential |
| **Git host** | Where a repository lives and its pull requests are opened: `github.com`, `gitlab.com`, or a company's server the owner declared, each of a kind (`github`, `gitlab`). A request to merge is a pull request on every host | A Node, which is a machine that runs Workers |
| **Transcript** | What a Worker said and did during one Dispatch, kept by the control plane as items in order: messages, reasoning, tool calls, their results and notices. The Node sends it from the Worker's journal | A Conversation, which is the CLI's own record on the Node; a Session |
| **Secret** | A named value kept by the vault, encrypted: an API key or a subscription's token set. It has versions; another record names it as `vault:<name>`. Its value is never shown | An Account, which uses a Secret; a Client token or Gateway key, which are stored as hashes |
| **Integration** | A service Hypomnema works with on the owner's behalf: a git host (named by its host), connected with a token, or a service that signs in with an account, of which Google is the first (issue #103) | A connector, a plugin, an app |
| **Connection** | One account of an Integration that the owner authorized, under a label he chose (`work`). Its credential is a vault secret. A git host's single token of before 2026-11-10 is its Connection `default` | An account (that is a runtime's subscription), a login |
| **Session** | A vault credential session opened for one Dispatch and revoked when it closes | A CLI session or a chat; call those Conversation |
| **Announce** | A Worker's final report to the Coordinator, with outcome and evidence | A chat message to the owner |
| **Policy** | A rule that answers a tool call or request with ALLOW, DENY or ASK | A Gate |
| **Epoch** | A counter on a Dispatch; a write from a stale epoch is refused | A timestamp |
