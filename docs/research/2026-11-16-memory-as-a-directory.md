# Memory as a directory of notes

Date: 2026-11-16. Question from the owner: is a memory of 2,200 characters enough, should a memory be a directory of
files rather than lines in a database, and do others build a wiki? This note follows
[2026-11-13-agent-memory.md](2026-11-13-agent-memory.md), which took the always-loaded half of the references' memory
and left the rest out (its section 5).

## What Hypomnema has (main, 2026-11-16)

One layer. An agent, a Bot and the owner each have one text of entries, one per line, all of it in the prompt:
2,200 characters for an agent or a Bot, 1,375 for the owner (`internal/controlplane/orchestration/memorystore/memorystore.go:38-45`,
`migrations/00057_memories.sql`, `bots.memory`). The `memory` tool adds, replaces or removes one entry. Nothing is
kept that is read only when needed, and there is no search.

## Two layers everywhere else

Every reference has a small layer loaded at the start and a larger one the agent reads when it needs it.

| Reference | Loaded at the start | Read when needed | Source |
|---|---|---|---|
| Hermes | `MEMORY.md` 2,200 and `USER.md` 1,375 characters | `session_search` over every past session (full-text). "Memory is for critical facts that should always be in context" | `hermes-agent/website/docs/user-guide/features/memory.md:176-262` |
| OpenMausBot | `MEMORY.md`, first 200 lines or 24 KB; an index of topic files (name, description; 40 topics or 2,000 characters) | `memory/<topic>.md`, read with the file tools; `archive.md` for entries moved out; recall searches topics before a turn | `openmausbot/docs/memory.md:17-110` |
| Coder | An index of memories (name, description of at most 150 characters), sent as a message | Up to 200 memories of at most 8 KB each, read with `read_memory`; kept in the database | `coder/coderd/x/chatd/chattool/memory.go:23-33, 311-316` |
| OpenClaw | `MEMORY.md` (20,000 characters a file), `USER.md` (4,000) | `memory/YYYY-MM-DD.md` daily notes through `memory_search` and `memory_get`; dreaming distills them into `MEMORY.md` | `openclaw/docs/concepts/memory.md`, `docs/concepts/context.md:170`, `docs/concepts/user-model.md:533` |
| Claude Code (auto memory) | `MEMORY.md`, an index of one line per note | One file per fact, read when its line is relevant | the harness this repository's sessions run in |

## A directory is what the field converged on

- **Anthropic's memory tool** gives Claude a directory, `/memories`, with six commands: `view` (a directory two levels
  deep with sizes, or a file with line numbers, cut at 16,000 characters), `create`, `str_replace`, `insert`,
  `delete`, `rename`. Where it is stored is the application's: "files on disk, a database, cloud storage", "a
  per-user directory or keys in a database"; the SDKs take a custom backend (`BetaAbstractMemoryTool`,
  `betaMemoryTool`). The application validates every path against traversal, caps file sizes and what `view`
  returns, and deletes files not read for a long time. It says nothing about concurrent writers.
  ([docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool.md), read 2026-11-16)
- **Letta** (vendor's own measurement, 2025-08-12): an agent on GPT-4o mini given only file tools (`grep`,
  `search_files`, `open`, `close`) over the conversations scored 74.0% on LoCoMo, against Mem0's reported 68.5%.
  Their reading: models use file operations well because coding data is full of them. LoCoMo measures retrieval, not
  keeping memory up to date; no independent reproduction was found.
  ([post](https://www.letta.com/blog/benchmarking-ai-agent-memory))
- **Karpathy's "LLM Wiki"** (April 2026): raw sources the agent never changes, a wiki of Markdown pages the agent
  owns, and a schema (`AGENTS.md`) saying how it is kept. Operations: ingest (one source touches 10-15 pages), query
  (answers can be filed back as pages), lint (contradictions, stale claims, orphan pages). `index.md` is the catalog
  read first; `log.md` is append-only. "At small scale the index file is enough"; it works "at moderate scale (~100
  sources, ~hundreds of pages)", then search (qmd) helps. Obsidian is the viewer; "the wiki is just a git repo of
  markdown files". ([gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f), revision ac46de1)
- **OpenClaw's `memory-wiki`** is the one reference with a wiki: an optional plugin, separate from memory, that
  compiles a Markdown vault with provenance and backlinks, shared by all agents or one per agent
  (`openclaw/extensions/memory-wiki/README.md`).

## Directory or database

The three single-machine references keep files in the agent's workspace. Hypomnema's agent memory is shared by its
workers on every node (`ARCHITECTURE.md`, "An agent ... has a memory ... shared by all its workers on every
machine"), and the owner reads it on the web; a Bot's directory belongs to the `coordinator` user, which the control
plane cannot read. Coder, the multi-machine reference, keeps memory in its database. Anthropic's tool removes the
choice: the agent sees a directory, the application stores it where it wants.

## Conclusion

Keep the 2,200-character text as the always-loaded index, and add the read-when-needed layer as a directory of notes
the agent works with through Anthropic's six commands, stored by the control plane. A wiki is the same directory
grown with links and a periodic lint (Karpathy); search, background consolidation and session search stay out until
a directory outgrows its index. Design and plan: the issue filed from this note.
