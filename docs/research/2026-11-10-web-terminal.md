# A terminal on a machine, from the web

Date: 2026-11-10. Question from the owner: a machine created at a provider has no SSH key and no inbound port, so
there is no way onto it. How do the references open a shell on a machine from the web, where in their UI, and what
goes with it.

Read in `.tmp/`: Coder (clone on disk, AGPL v3) and Nezha with its agent (WTFPL v2), in their code; which libraries
Omnara, Omnigent, Orca and T3 Code use, from their manifests. Coder's code is read for behaviour only: its license
does not allow copying it here. "Inferred" marks what no reference states.

## 1. Where it is in the UI and what it is for

Coder (`docs/workspaces/workspace-access/web-terminal.md`): a Terminal button on the workspace's page opens a page of
its own, `/@user/workspace/terminal`, that is only the terminal (`site/src/pages/TerminalPage/TerminalPage.tsx`). It is
"automatically enabled for all workspaces". The page keeps the session's id in its URL (`?reconnect=<uuid>`), so a
reload or a dropped network comes back to the same shell. A lost connection is shown as an alert above the terminal
(`site/src/modules/terminal/WorkspaceTerminalAlerts.tsx`). A link that carries a command to run asks first: "A link is
requesting to run a command in your terminal" (`TerminalCommandConsentDialog.tsx`).

Beside it, over the same connection to the agent, Coder offers desktop IDEs, port forwarding, a file browser and
remote desktops (`docs/workspaces/workspace-access/`), counts an open terminal as activity that keeps a workspace from
stopping (`docs/workspaces/workspace-scheduling.md` L59), and lets its own AI agents run tools in the workspace
(`docs/coder-agents/index.md` L55, L258).

Nezha: a terminal per server from the dashboard; beside it scheduled and batch commands (`controller/cron.go`), a file
manager (`controller/fm.go`) and MCP tools that execute on a server (`controller/mcp_tools_exec.go`).

## 2. How the bytes travel

| Question | Coder | Nezha |
|---|---|---|
| Who starts it | The browser opens a WebSocket, `GET /api/v2/workspaceagents/{agent}/pty` (`coderd/workspaceapps/proxy.go` L181, L767-893) | `POST /terminal {server_id}` makes a stream id and tells the agent to start (`controller/terminal.go` L31-87); the browser then opens `GET /ws/terminal/:id` (L96-138) |
| How the agent joins | The server dials the agent over its tailnet | The agent is sent a task on the stream it already holds, then opens a second stream of its own and names the id in its first frame, after four magic bytes (`agent/cmd/agent/terminal_session.go` L135-136; `service/rpc/io_stream.go` L56-57) |
| Both ends must meet within | not found | 10 s, else "stream endpoints not established" (`io_stream.go` L60-99) |
| Browser to server | JSON, `{data}` for keys and `{height,width}` for a resize (`site/src/modules/terminal/WorkspaceTerminal.tsx` L73-74, L497-503) | A type byte, then raw input (0) or JSON `{cols,rows}` (1) (`terminal_session.go` L191-212) |
| Server to browser | Raw bytes in binary frames (`proxy.go` L845); the server only copies both ways (L891) | Raw bytes copied both ways (`io_stream.go` L113-119) |
| Resize limits | not found | 2 to 1000 columns, 2 to 500 rows (`terminal_session.go` L17-20) |
| Keepalive | WebSocket | WebSocket ping every 10 s; agent heartbeat every 30 s |

## 3. The shell and the session

| Question | Coder | Nezha |
|---|---|---|
| PTY library | `github.com/creack/pty` v1.1.24 (`go.mod`) | the same (`agent/pkg/pty/pty.go`); Omnara uses it too |
| Shell | The user's shell, as the agent's OS user, in the agent's working directory (`agent/agentssh/agentssh.go` L1036-1095) | The first of zsh, fish, bash, sh, as the agent's user, with `TERM=xterm-256color` (`pty.go` L17, L39-70) |
| First size | From the request, 80 by 80 when absent | 80 by 24 |
| Session id | A UUID the client chooses and keeps (`agent/reconnectingpty/server.go` L221) | A UUID the dashboard makes, used once |
| The browser goes away | The shell lives on. Output goes to a 64 KiB ring that a returning client is sent whole (`buffered.go` L52, L222-223). With no client for 5 minutes the shell is ended (`reconnectingpty.go` L93-95, L125-126); a session nobody attaches to is ended after 30 s (L28) | The shell is ended after 2 s (`terminal_session.go` L16, L118-131). No reconnect, no buffer |
| Several viewers of one shell | Yes (`activeConns`) | No |

## 4. Who may, the switch, and the record

| Question | Coder | Nezha |
|---|---|---|
| Who may open one | Whoever may SSH to the workspace: the same permission (`coderd/workspaceapps/db.go` L358-362) | An admin or the server's owner (`model/common.go` L63); the WebSocket is attached only by the session's creator or an admin (`controller/permissions.go` L18-30) |
| Origin of the WebSocket | Only the dashboard's own hosts (`proxy.go` L828-833) | not found |
| On by default | Yes; no setting turns it off | Yes; `disable_command_execute` in the agent's own configuration turns it off, with every other remote execution (`agent/model/config.go` L38; checked in `terminal_session.go` L67) |
| Record | A connection event with the method, the client's address and a session id (`agent/reconnectingpty/server.go` L89-95), and the session's start and end | "terminal init" and "terminal exit" in the agent's output |
| Keystrokes or output recorded | No | No |

## 5. The page

Coder renders with `@xterm/xterm` and its fit, web-links, unicode and WebGL add-ons (`site/package.json`); Omnigent
(`web/package.json`) and Orca (`package.json`) render their terminals with `@xterm/xterm` too. xterm.js is MIT.

## 6. What this means for Hypomnema

- Both references run a shell as the agent's own user and copy bytes; the differences are in the session. Coder
  keeps the shell across a reload; Nezha ends it. The owner asked for the first.
- Nezha's way for the agent to join fits a node that already holds one connection to the control plane: a message
  on that connection, then a second connection for the bytes, named by the session's id.
- Both are on by default. The switch that exists is Nezha's, on the machine itself, which the control plane cannot
  flip.
- Neither records what is typed.
- Not read: how either behaves on Windows beyond Nezha using ConPTY; the node is released for Linux and macOS only.
