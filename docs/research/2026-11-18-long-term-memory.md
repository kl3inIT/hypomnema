# The long-term layer of memory

Date: 2026-11-18. Question from the owner: how do the references build the long-term layer of memory? Bot VMAP's
memory reached 97% of its 2,200 characters. This note follows
[2026-11-16-memory-as-a-directory.md](2026-11-16-memory-as-a-directory.md).

## Sources

Two sub-agents read the code of six clones under `.tmp/`, all pulled on 2026-11-18:

| Clone | Commit |
|---|---|
| hermes-agent | e93b3da9a8 |
| openmausbot | c175749a8 |
| openclaw | 7ff0d902b |
| codex | 345f166d |
| coder | c1fec5f6 |
| omp | e1ae2cc998 |

The points this note's decisions rest on were then read again directly. Anthropic's memory tool was read on its page
(platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool.md).

## What each keeps beyond the always-loaded layer

| Reference | Notes read when needed | Search over past chats | Notes put in before a turn | Writing and upkeep in the background |
|---|---|---|---|---|
| Hermes | None: `MEMORY.md` (2,200 characters) and `USER.md` (1,375) only (`tools/memory_tool_store.py:26,188`) | `session_search`: FTS5 over every transcript, real messages, no model call (`tools/session_search_tool.py:1-8,818-833`) | Only an external provider's `prefetch`, appended to the user message (`agent/turn_context.py:114-135`) | A review every 10 user turns that may only add; patch and remove wait for the user (`agent/background_review.py:446-472`, `tools/memory_tool.py:179-219`). When the store is full, an add is refused and the agent consolidates |
| OpenMausBot | `memory/<topic>.md` with a header, `archive.md`, daily logs; the topic index goes in the prompt (`server/workspace.ts:618-626`) | `session_search`: FTS5 over chats, notes and the archive (`server/drivers/agents-catalog.ts:560-585`) | Up to 4 notes and 4 chat passages, 6,000 characters, in front of the user message (`server/recall.ts:28-31,202-204`) | Capture after 2 quiet minutes or 6 turns; organize; nightly tidy at 3:00; dream across chats (`server/memory-capture.ts`, `memory-upkeep.ts`, `memory-tidy.ts`, `memory-dream.ts`) |
| OpenClaw | Daily notes `memory/YYYY-MM-DD.md` (docs/concepts/memory.md:28-41) | Through `memory_search` when `rememberAcrossConversations` is on (`src/agents/memory-search.ts:132-141`) | Through the Active Memory plugin, a bounded recall sub-agent (`extensions/active-memory/index.ts:223,308-316`) | A flush before compaction; nightly dreaming promotes only notes recalled often (`extensions/memory-core/src/flush-plan.ts:13-37`, `src/memory-host-sdk/dreaming.ts:34-42`) |
| Codex | Rollout summaries and `MEMORY.md`, read with grep (`templates/memories/read_path.md:19-40`) | The rollout files, by path | A summary of 2,500 tokens as a developer message (`ext/memories/src/prompts.rs:35-49`) | Two phases at session start. **Off by default** (`features/src/lib.rs:1480-1483`) |
| Coder | Up to 200 notes of 8 KB in Postgres (`coderd/x/chatd/chattool/memory.go:24-33`) | None | The index (name, description), as a user message, with diffs later (`chatd/memory.go:67-160`) | None. At 80% full the write result asks the agent to consolidate below 70% (`memory.go:29-30,464-468`) |
| omp | `MEMORY.md` and skills per working directory | None by default | `memory_summary.md` within 5,000 tokens | Two phases like Codex. **Off by default** (`memory-backend/settings.ts:12-18`) |

## What most of them do

1. **Notes read only when needed, with an index loaded every time.** Five of six have this layer.
2. **Search over past chats.** Hermes, OpenMausBot and OpenClaw have it. The two that use keyword search use SQLite
   FTS5 and return the real messages. Hypomnema already keeps FTS5 tables for WhatsApp, Zalo and Muse
   (`internal/controlplane/messaging/zalo/store.go:63`).
3. **Recall and indexes go in a user message, not the system prompt.** Hermes, OpenMausBot and Coder do this so that
   the prompt the provider caches stays the same.
4. **Background writing and upkeep do not agree.** Three have it on by default, two have it off, and Coder has none.

## Conclusion

The owner approved this on 2026-11-18.

- **Step 1, issue #589:** the directory of notes. `MEMORY.md` is the index (Coder, OpenMausBot), worked with through
  Anthropic's six commands. The 80% nudge already exists (`internal/shared/agenttool/memory.go`, after Coder).
- **Step 2, issue #805:** a tool that searches a Bot's past chats with FTS5 and returns the real messages (Hermes
  `session_search`).
- **Left out until steps 1 and 2 run and show a need:**
  - automatic recall before a turn;
  - background capture and nightly upkeep (OpenMausBot, OpenClaw);
  - embeddings (OpenClaw);
  - summaries built in the background (Codex, omp).
