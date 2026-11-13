# Memory for agents and Bots

Date: 2026-11-13. Asked by the owner as the memory piece of issue #21 (shared infrastructure): how the references let
an agent keep notes across its sessions, who a memory belongs to, how it is written while several sessions run, and
what reaches the prompt.

The owner decided on 2026-11-13 that Hypomnema keeps memory itself instead of using MemoryOS, which
[the vision](../vision.md) named until then. MemoryOS was therefore not read.

Read in the clones under `.tmp/` by two sub-agents, with the points the design rests on read again directly:
OpenMausBot, OpenClaw, Hermes, Coder, Omnara, Omnigent, OpenHands (app and SDK), and the memory of Codex, omp, opencode
and pi. Claude Code is closed; what is said of it comes from OpenMausBot's and OpenClaw's code.

## 1. Who a memory belongs to

| Reference | Owner of a memory | A layer about the person | Source |
|---|---|---|---|
| OpenMausBot | Each bot: `MEMORY.md` in the bot's workspace, private to it. A bot has one project folder | "About me", read by every bot | `docs/memory.md:19-48, 174-179` |
| OpenClaw | Each agent: `MEMORY.md` in the agent's workspace; one agent cannot search another's | `USER.md`, never scoped to a project | `docs/concepts/memory.md:28-41`, `multi-agent.md:260-268`, `memory-architecture.md:328-329` |
| Hermes | Each profile: `MEMORY.md`, 2,200 characters | `USER.md`, 1,375 characters | `tools/memory_tool_store.py:188`, `tools/memory_tool.py:38-59` |
| Coder | Each chat project: up to 200 notes of 8 KB, shared by every chat of the project | None | `coderd/x/chatd/chattool/memory.go:23-38` |
| Omnara | Named stores of files in a project, attached to agent configs, read or read-write | None | `migrations/000047_memory_stores.sql`, `docs/agents/configuration.mdx:271-285` |
| Omnigent | A Hindsight bank per agent, through tools only | None | `omnigent/tools/builtins/hindsight.py:103-108` |
| OpenHands SDK | A user `MEMORY.md` and a project one, off by default | The user level | `openhands-sdk/.../context/memory.py:1-24` |
| Codex, omp | Per user (Codex) or per working directory (omp `local`), off by default, built by a background consolidation | None | `codex-rs/memories/README.md`, `omp/docs/memory.md:3-87` |
| opencode, pi | No memory; `AGENTS.md` only | | `opencode/.../instruction.ts:41-94`, `pi/.../resource-loader.ts:212-228` |

Three of the four agent products (OpenMausBot, OpenClaw, Hermes) give a memory to each agent and a second, smaller one
about the person to every agent. Coder and Omnara are organised around projects instead.

OpenClaw alone handles one agent working in several repositories: an entry written in a repository is annotated with
its `origin` remote, and recall prefers the current repository's entries (`memory-architecture.md:284-338`). The owner
decided this is not needed now (2026-11-13).

## 2. Writing while several sessions run

- Hermes: one tool, `memory{target: memory|user, operations: [add|patch|remove]}`; every change takes a file lock,
  reads the file again, applies and writes, because a write from an earlier snapshot "drops concurrent entries"
  (`memory_tool_store.py:262-301, 710-718`). A change over the limit is refused; the agent makes room itself.
- OpenMausBot: `memory_update{action: append|replace|remove|supersede|forget|prefer, text ≤ 1000, old_text}`, through
  its MCP bridge to the server (`server/drivers/agents-catalog.ts:552-567`); its description says other threads may
  be writing and to re-read and retry on a conflict. The owner's editor saves with the hash it opened (409 when
  stale).
- Coder: each write is a transaction behind an advisory lock per project; notes are immutable and replaced with
  `consolidate_memory` (`chattool/memory.go:58-79`).
- Omnara: compare-and-swap on a file's digest, and a write refused when the agent has not read the file
  (`internal/storage/memorystore/files.go:198-213`).

Hypomnema's Bot memory already follows Hermes (`internal/controlplane/orchestration/bots.go:187-300`): entries one per
line, 2,200 characters, `add`, `replace` and `remove` read again inside the write transaction, a change over the limit
refused.

## 3. What reaches the prompt

- Hermes freezes both files into the system prompt at session start; writes during the session change the file, not
  the prompt, to keep the provider's cached prefix (`memory_tool_store.py:176-179, 655-681`).
- OpenMausBot puts the first 200 lines or 24 KB of `MEMORY.md` in the system prompt of every turn (`docs/memory.md`,
  "What loads, and the budget").
- Coder keeps the system prompt free of memory and sends the index as a message, a full one on the first turn and
  changes at later turns' start, so the cached prefix survives every write (`chatd/memory.go:67-90`,
  `chattool/memory.go:315-325`).
- Coder's guidance says what not to keep: anything derivable from the code (architecture, paths, debugging fixes),
  what the instructions already say, and secrets (`chattool/memory.go:306-312`).

## 4. The CLIs' own memory

Claude Code keeps an auto-memory per working folder. OpenMausBot turns it off for a bot's turns with
`CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`, because a bot's working folder changes per thread and the notes were split
between them (`server/drivers/claude-own-memory.ts:1-6`). Codex's and omp's memories are off by default; opencode and
pi have none.

## 5. Left out by every simple design

Background consolidation ("dreaming", OpenClaw, OpenMausBot, Codex), capture from chats, recall by search, daily
logs and an undo journal (OpenMausBot) are the larger half of the references' memory. Hermes and Hypomnema's Bot memory
show the small half works on its own.
