# Worker survival and long tool calls: spikes on zm (2026-11-08)

Two questions that shape the node daemon (issue #19, next step), run on zm with Claude Code 2.1.319, `--model haiku`
(about 0.002 USD a turn), from scratch Python programs outside the repository (`~/hypomnema-spike/probe-*`). Claude
was driven as in [the headless driver spike](2026-11-06-headless-cli-drivers.md): bidirectional stream-JSON with
`--permission-prompt-tool stdio`.

## 1. Does a worker survive its daemon?

A parent process owned claude's stdin and stdout, started a turn whose Bash tool ran a 40 s script, and was killed
with SIGKILL during the tool call.

| Case | Claude | The tool's work | Transcript and resume |
|---|---|---|---|
| Only the parent killed, claude in its process group | Kept running, finished the turn, exited | Done | Ends with the answer; `--resume` succeeds, same session id |
| Only the parent killed, claude in its own session (`setsid`) | Same | Done | Same |
| The whole process group killed (`kill -9 -- -PGID`, as systemd stops a unit by default) | Died at once | The tool's own child still finished | Ends at the `tool_use`; on resume claude reports the command's outcome as unknown |

- Every event written after the parent died is lost to a new daemon, which sees the outcome only through the
  transcript (`~/.claude/projects/<cwd with / as ->/<session_id>.jsonl`) or a resume.
- **Shim:** a small process started with `setsid` owns claude's pipes. It appends every stdout line to a journal
  (fsync per line) and reads input from a FIFO opened read-write, so a dead writer never gives it end of file. A
  `{"type":"shim_close"}` line closes claude's stdin. This was about 100 lines of Python in all. The daemon was killed
  mid-turn; a new daemon read the journal from its saved byte offset, found the turn's `result`, sent a second turn
  through the FIFO and got its answer in the same session.
- Bash refuses `sleep N && …` ("use run_in_background"). When the model runs a command in the background, the turn's
  `result` arrives early, and claude runs another turn when the background task finishes. A `result` is therefore
  not always the end of the work.

Consequences (inferred):
- The node daemon runs each worker under a shim, in its own session. Its systemd unit uses `KillMode=process`, so a
  daemon restart does not kill workers. Reattach (PR 2) then holds.
- Still to design: a permission prompt that arrives while no daemon runs (it waits in the journal for the next one),
  journal rotation, and the shim itself dying.

## 2. Can a worker block on an MCP tool call for hours?

A stdio MCP server with one tool, `ask`, blocked until an answer file appeared. Claude Code sends a `progressToken`
with every tool call.

Read from the Claude Code binary:
- **Hard limit:** the server's `timeout`, else `MCP_TOOL_TIMEOUT`, else 1e8 ms (about 27.8 h). Progress
  notifications do not extend it.
- **Idle limit:** `CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT`, by default 30 min for stdio servers and 5 min for remote ones; 0
  turns it off. A response or a progress notification resets it, and it is checked every 30 s.
- `MCP_TIMEOUT` bounds connecting to a server, not calls.

| Run | Settings | Result |
|---|---|---|
| A | Default | Blocked 6 min 40 s, then returned the answer |
| B | `MCP_TOOL_TIMEOUT=3600000` | Same |
| C | Idle limit 60 s, no progress | Aborted at 60 s ("sent no response or progress for 60s") |
| D | Idle limit 60 s, progress every 15 s | Blocked 3 min, then returned the answer |
| E | `MCP_TOOL_TIMEOUT=90000`, progress every 15 s | Timed out at 90 s; the server got `notifications/cancelled` |
| F | Interrupt at 31 s | The tool result is an error, then `result error_during_execution`, exit 1; the server got `notifications/cancelled` (`AbortError: remote-cancel`) |

Consequences (inferred):
- The question tool sends `notifications/progress` about every 60 s while it waits, and its server sets a hard limit
  longer than the longest wait. A `wait` question (PR 3) can then last hours with the worker alive.
- A cancellation reaches the tool server, so the node can withdraw the question.
