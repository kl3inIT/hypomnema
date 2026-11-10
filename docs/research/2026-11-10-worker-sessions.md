# A worker's session on the server and on the web (2026-11-10)

Read for issue #43 (worker sessions on the web): how the references store an agent's activity, send it from the
machine that runs the agent, and show it. Revisions read: Omnigent `cf1021e`, Omnara `0ef1711`, OpenClaw
`d622a2ba4`, Coder `b798061`, t3code `b8c2cfad`, Orca `4c13ae46`. Three agents read the clones; the constants
quoted below were checked against the files afterwards, the rest is as the agents reported it.

## Hypomnema today

- The shim writes the worker's stdout to `run/<dispatch>/journal.jsonl`: Claude Code's raw stream-JSON, one line up
  to 16 MiB (`internal/node/shim/shim.go`). Claude runs without `--include-partial-messages`, so the journal holds
  whole messages, not text deltas (`internal/node/claude/runner.go`).
- The runner reads the journal from a byte offset kept on disk and turns it into a start, progress, questions and
  the final report (`runner.go` `tail`, `handle`). Only Claude has a runner.
- Durable reports go through the SQLite outbox, are all sent again on reconnect, and a full send queue drops
  messages (`internal/node/daemon/daemon.go`). A message between node and control plane is at most 1 MiB
  (`internal/shared/nodeproto`).

## Storing a session

| Project | Table, order | Payload | Bound on one item | Kept |
|---|---|---|---|---|
| Omnara | `agent_events`, `sequence` per agent (`migrations/000004_events_messages_turns.sql`) | Normalized: content blocks `text`, `reasoning`, `tool_call`, `error`, …; tool results in their own table | A tool result over 50 KiB goes to an artifact, an 8 KiB preview stays (`internal/storage/executionstore/tool_result_overflow.go`) | Forever; rows cannot be updated or deleted; idle agents are only marked archived |
| OpenClaw | `transcript_events`, `(session_id, seq)` (`src/state/openclaw-agent-schema.sql`) | The agent's transcript entries as JSON, zstd | 4 MiB an event; a tool result 16 000 to 64 000 characters (`src/agents/tool-result-limits.ts`) | `pruneAfter` 30 days, 5000 sessions, 10 GB an agent (`docs/reference/session-management-compaction/maintenance.md`); sub-agent sessions are disposable |
| Omnigent | `conversation_items`, `position` per conversation (`omnigent/db/db_models.py`) | Normalized items, 11 kinds: `message`, `function_call`, `function_call_output`, `reasoning`, `error`, … | The sender truncates the longest string fields of an oversized item and marks it (`harnesses/claude_native/forwarder.py`) | Forever; an archive flag hides a session |
| t3code | `orchestration_events` plus a projection of turn items (`apps/server/src/persistence/Migrations`) | Normalized items, about 18 kinds | On the wire 32 KiB a string, the whole item on demand (`orchestration-v2/WireProjection.ts`) | Event log one day, items forever |

Common to all four: whole, normalized items are stored, not the provider's raw stream; text and reasoning deltas
are sent live and never stored; one item is bounded; none caps the number of items in a session.

## Sending it from the machine

| Project | Unit | Buffer on the machine | Resume, duplicates | Slow server |
|---|---|---|---|---|
| Omnigent | Batches of at most 32 events or 256 KiB over the runner's WebSocket (`runner/transports/ws_tunnel/event_delivery.py`); 100 events or 10 MiB over HTTP (`session_event_batch.py`) | The agent's own transcript file with a byte cursor kept on disk (`harnesses/claude_native/forwarder.py`) | The cursor moves only on the server's ack; every item has a stable `source_id` | The producer waits (34 batches pending at most); oversized items are truncated |
| Coder | Batches each second, at most 1 MiB (`codersdk/agentsdk/logs.go`) | Memory only | A failed batch is sent again whole; no key | New logs are dropped past 1 MiB queued; the server stops an agent at 1 MiB of logs |
| Omnara | The agent loop runs on the server; the daemon runs tools. Reports go through a SQLite outbox; process output stays in a 100 MiB file the server reads by cursor (`internal/machinedaemon`) | Outbox and output file | Report id; byte cursor | The file drops its oldest bytes |
| OpenClaw | One 16 KiB chunk a request (`src/node-host/node-invoke-progress.ts`) | None | None: a disconnect cancels the invoke | Output capped at 200 KB |

Omnigent is the only reference that sends a coding CLI's whole session from another machine, and it does so the
way the node already reads its journal: a file and a byte cursor. Omnara, named in the first proposal of #43, does
not send a session from the machine at all.

## Showing it

Read: t3code `apps/web/src/components/chat`, Omnigent `web/src/components/blocks` and `chat`, Omnara
`frontend/apps/web/src/components/agents`, Orca `src/renderer/src/components/native-chat`.

- All four fold consecutive tool calls into one line that counts them in words ("Read 2 files, ran 1 command");
  opening it shows one row a call (icon, verb, target). Omnigent keeps the last 3 calls of a live run open
  (`BlockRenderer.tsx` `STREAMING_TAIL`). t3code and Omnigent fold a finished turn into "Worked for X" and leave
  its last message.
- A call's result is hidden until opened, then shown in a box of limited height: Omnigent 80 lines or 12 000
  characters with Expand (`ToolCodePanel.tsx`), Orca 4000 characters with "Show full output", t3code and Omnara by
  height only. Omnigent and Orca show an edit as a diff in place.
- All four follow the end until the reader scrolls away, then offer a button back to the newest (Omnigent uses
  `use-stick-to-bottom`).
- Older history loads in pages: Omnara 100 with a button, Omnigent 20, t3code by turn, Orca on scroll.
- t3code, Omnigent and Orca render only the rows in view (`@legendapp/list`, `@tanstack/react-virtual`); Omnara
  uses CSS `content-visibility`.

## Proposed for Hypomnema

Recorded in issue #43; not decided until the owner approves it there.

- The node turns each journal line into session items of one shape for every runtime, bounds each item, and sends
  them in batches from a byte cursor that moves on the control plane's ack (Omnigent).
- The control plane stores the items per Dispatch by sequence number and serves them in pages after a cursor.
- The web shows them in the Task panel as the references do; a card on the board shows the newest activity.

Inferred, with no reference: the size of one item (64 KiB), removing a Dispatch's items 30 days after it ended
(OpenClaw's age, applied to items), and `content-visibility` with pages of 100 instead of a virtual list.
