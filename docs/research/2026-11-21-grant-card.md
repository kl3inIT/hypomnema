# The grant card: a Bot asks the owner for a Connection it was not given

Issue [#471](https://github.com/kl3inIT/hypomnema/issues/471), part D. Design drafted in
[#471 comment 7056646200](https://github.com/kl3inIT/hypomnema/issues/471#issuecomment-7056646200), reviewed by an
independent Codex worker (Run `run_4bfxjayyqi66gzxbbkr5kq7e2u`, Task `tsk_z6okm4mwbejqebfytr6gfxep6e`, verdict
"ready after changes", no P0) and approved by the owner as revised in
[#471 comment 7056846217](https://github.com/kl3inIT/hypomnema/issues/471#issuecomment-7056846217) (2026-11-21).

## The need

From the owner's chats (read-only, 142 chats, 9–20 Nov): he turns Connections on for a Bot by hand and then writes
to it again: "Chủ đã cấp lark:report cho bạn" (Bot Nhân sự, 13/11), "lark:community đã được bật cho bot này" (Bot
Cộng đồng, 16/11). On main before this change a Bot used a Connection only when the owner switched it on for chats
and the Bot's list allowed it (`orchestration/botaccess.go` `MayUse`), and its pi was offered only those
(`agentapi/agentapi.go`, `offered: bot.MayUse`): it could not see, so could not ask for, one it lacked.

## What the references do

Code read in `.tmp/` (OpenMausBot 35d0442 and 7a3d80c, OpenClaw 16c28ff6a, t3code 8cf97b5 and 80871270, LibreChat
d4e85d2, Open WebUI, Onyx).

| Ref | Model sees what it lacks | User sees | After the answer | Who |
|---|---|---|---|---|
| OpenMausBot | yes: the prompt lists assigned-not-connected apps, "call COMPOSIO_MANAGE_CONNECTIONS … and end the turn" (`server/cloud-connections-prompt.ts:73`); an app outside the bot's scope is refused "ask the user to allow it under the bot's Access settings, and do not retry" (`server/connector-verdict.ts:193`), its card auto-denied (`server/host/connector-cards.ts:39-44`) | Connect securely / Not now (`src/components/ConnectorCard.tsx:178-205`) | turn ends, resumed automatically when connected (`server/connector-proxy.ts:331`, `server/index.ts:11433-11444`) | admin only (`index.ts:14922`); checked every call (`:14287`, `:14387`) |
| OpenClaw | yes: a synthetic `connect` tool per unconnected server (`src/agents/agent-bundle-mcp-requester-connect.ts:55-96`) | Connect {server} button (`src/auto-reply/reply/mcp-channel-actions.ts:54-75`) | next message (`requester-connect.ts:34`) | shared: owner, direct chat, untainted turn (`agent-bundle-mcp-operator-connect.ts:37-43`) |
| t3code | yes: the tool returns `pending`, "do not wait, poll, or call it again" (`apps/server/src/mcp/toolkits/orchestrator/tools.ts:337-349`) | link card: access level or decline (`apps/web/src/components/chat/LinkRequestCard.tsx`) | the answer arrives as a message to the agent (`apps/server/src/peer/PeerLinkRequests.ts:453-494`) | the user in the thread |
| LibreChat | no: the tool hidden or the turn waits (`api/server/services/ToolService.js:1378-1440`) | Sign-in to {domain} (`packages/chat/src/components/parts/ToolCall.tsx:509-528`) | the same turn waits about 2 min | each user |
| Open WebUI, Onyx | no: hidden (`backend/open_webui/utils/tools.py:241`, `onyx/tools/tool_constructor.py:428`) | connect from a menu beforehand | the user sends again | each user |
| Generic approval cards (Codex `protocol.rs:4590`, t3code `ComposerPendingApprovalActions.tsx:27`, Hermes `base_exec_approval.py:56`, OpenMausBot `PendingApproval.tsx:309`, LibreChat `ToolApproval.tsx:376`, OpenClaw `permission-relay.ts:27`) | | once / session / always / deny | resumes by itself | the owner or the requester |

How a repeated ask is answered, read for the build (OpenMausBot 7a3d80c, t3code 80871270):

- OpenMausBot finds the card a request already made by the turn's `resumeKey` and the app's slug, and makes no
  second one (`server/host/connector-cards.ts:57-64`); a new turn has a new key.
- t3code answers a retry of the same `clientRequestId` with that card's outcome, declined included
  (`apps/server/src/peer/PeerLinkRequests.ts:244-249`, `resultOf` at `:428-447`); a fresh ask makes a new card.

## The review's points (all taken)

- P1-1: disclosing the names of Connections a Bot was not given is the owner's choice: he agreed (2026-11-21).
- P1-2: one tool, listed through the agent API's tools and the guide, so `hypomnema mcp` and `bots prompt` agree;
  it returns at once with the state; idempotent.
- P1-3: a record of its own, not an Approval: the Approval table and its scopes stay as they are. Only the owner
  answers, through an owner-only operation; first answer wins.
- P1-4: absent and refused for a Routine, a worker, the coordinator token, a Lark chat and any chat that is not the
  Bot's main chat.
- P1-5, P1-7: Always re-reads the Connection, the Bot and the main chat in one transaction and appends exactly that
  use, by a method of its own; a failed check makes the request stale. Revoked on the Bot's page; no setup change,
  no Undo on the card; `MayUse` read at every use.
- P1-6: a concurrent or repeated call makes one pending record.
- P1-8: a pending request counts as Needs you on the Bot's row and its department's count.
- Where to put the wake: a system line in the chat marked for the coordinator, delivered once through the chat's
  undelivered messages, not `WakeChats`, which is for Run events.

## Decided

The table in #471 comment 7056846217, with each row's source, and one point it left open, settled by the coordinator
on 2026-11-21 from the two references above: a call in the same chat turn as the latest request for the same (Bot,
chat, Connection), or in a turn the owner did not start (the turn his answer's line wakes), is answered with the
latest request's state and makes no card; only a later turn the owner started asks again. The two choices on the
card, Always for this Bot and Decline, depart from the references' once/session/always/deny (owner, 2026-11-21): a
Connection is used by many calls in one task, and a grant that joins the Bot's list is what the owner did by hand.

Left out: a sign-in to a new account from the card (Connections are made in Settings); grants for workers; the
request in the `report` tool preset.
