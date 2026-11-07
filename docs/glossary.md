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
| **Coordinator** | The pi process that plans, creates Tasks, dispatches them, opens Gates and summarises results; it does not do the work itself. Holds no state. Called Brain until 2026-11-07, as in older research notes; the role is Orca's coordinator | A worker |
| **Orchestration** | The control plane module that keeps Runs, Tasks, Dispatches, Gates, leases, mailboxes and the event log, and is the only writer of that state. It runs nothing itself. Called execution until 2026-11-07; the name is Orca's (`src/main/runtime/orchestration/`) | The Coordinator, which decides; orchestration keeps the record |
| **Worker** | A CLI agent process (Claude Code, Codex, or pi) carrying out one Dispatch | The Node it runs on |
| **Runtime** | The kind of worker and how it is driven: `claude-cli`, `codex`, `pi` | A model or a provider |
| **Node** | A machine paired with the control plane that runs Workers: a VM, a server, a sandbox | Omnara's "machine", same meaning |
| **Account** | One credential a Runtime runs under: a Claude subscription, a Codex subscription or an API key. Managed by the control plane; a Dispatch records which one it used | A user account of the owner |
| **Model gateway** | The control plane module every model request goes through: forwards subscription traffic, injects API keys, records usage per Dispatch | A Node's egress proxy |
| **Session** | A vault credential session opened for one Dispatch and revoked when it closes | A chat session or a CLI session; call those "conversation" and "CLI session" |
| **Announce** | A Worker's final report to the Coordinator, with outcome and evidence | A chat message to the owner |
| **Policy** | A rule that answers a tool call or request with ALLOW, DENY or ASK | A Gate |
| **Epoch** | A counter on a Dispatch; a write from a stale epoch is refused | A timestamp |
