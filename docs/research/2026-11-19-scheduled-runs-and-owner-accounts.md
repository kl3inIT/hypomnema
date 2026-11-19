# Scheduled runs and the owner's own accounts

Date: 2026-11-19. Question: may a Routine, which runs while the owner is away, read his own accounts (his Lark
sign-in `lark:me`, Zalo, WhatsApp) to write him a morning brief? Today it may not: a user Connection and the
messaging tools are given only on the owner's turn in a chat (`ARCHITECTURE.md`, "Who may use it" under Lark and
"A Bot reads it with tools" under WhatsApp; `agentapi/agentapi.go` `botMay`, `orchestration/chats.go`
`OwnerTurn`). A first morning-brief Routine on 2026-11-19 could read only Google for that reason.

## What the references do

| Reference (commit) | Does a scheduled run get the person's own accounts? | Where |
|---|---|---|
| OpenMausBot (`c175749a8`) | Yes, through one explicit mode. Its built-in routine "Keep an eye on my day" reads Gmail and Google Calendar on a schedule and writes a brief. Such a run is `access: "read-and-draft"`: allow-only. Every request in the run is refused outright (never a card); it reaches only conversation reads, its own results, the engine's hooks and the connected-apps relay; through the relay it may only call read tools of Gmail and Calendar (a read verb on another app is refused, as it can carry text to a URL) and write one reply draft per thread its checks handed it, to that thread's sender only. No browser, computer, phone, web search or memory; its prompt has no memory, recent work, team notes or skills; app answers reach it as records (untrusted data). | `docs/proactive-routines.md:75-100`, `server/read-and-draft.ts:1-80` |
| OpenMausBot, the rule we cited | `server/cloud-team.ts:64` "Personal connections never enter" a **team**: a member's own connection has no path into a team shared with other people. It is about sharing between people, not about schedules. | `server/cloud-team.ts:63-67` |
| OpenMausBot, whose turn | Work that traces to outside text stays confined, whatever it wakes or schedules. | `server/host/outside-lineage.ts:1-4` |
| Hermes (`e93b3da9a8`) | Yes. A cron-spawned agent gets the configured toolsets except `messaging` and `clarify` (always) and `cronjob` (by default); a job's `enabled_toolsets` may narrow it but never widen past the config's denylist. | `cron/scheduler.py:419-440` |
| OpenClaw (`7ff0d902b`) | Yes. A job runs with the owner session's tool policy (`*`) or a finite `--tools` allowlist capped to the tools of the turn that created it. A wake is never an owner turn (`senderIsOwner`), so owner-only tools are not given to it. | `docs/automation/cron-jobs/payloads.md:62-82`, `docs/plugins/tool-plugins.md:273-281` |

## Findings

- No reference forbids a scheduled run from reading the person's own accounts. All three give scheduled work
  the person's tools; all three keep a wake from counting as the owner speaking.
- The closest to this need is OpenMausBot's read-and-draft run: same purpose (a daily brief from the person's
  own mail and calendar), and the narrowest posture: allow-only, reads of the named apps, no outbound path.
- Our `ARCHITECTURE.md` cited `cloud-team.ts` for keeping a Routine off a user Connection. That file is about
  teams. The owner's-turn rule itself (`OwnerTurn`) rests on OpenMausBot's `outside-lineage.ts` and OpenClaw's
  `senderIsOwner`, and both references still let a scheduled run read through an explicit, narrower path.

## Recommendation

Follow OpenMausBot: a Routine the owner marks read-only may read his user Connections and messaging mirrors, and
nothing else that could act or carry data out; everything else in that run is refused. Kept: a wake is not his
turn, so sends, writes and every other owner-turn tool stay refused. Design and plan: issue for the Inbox
morning brief (#838).
