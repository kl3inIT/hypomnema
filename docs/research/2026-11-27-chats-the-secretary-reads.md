# Which chats the secretary reads

Date: 2026-11-27, for issue #1270. Read in the clones under `.tmp/`: OpenClaw `032a2b336`, Hermes `e93b3da9a8`,
OpenMausBot `c175749a8`, Evolution API `fa09d37`, Chatwoot `ba1cb4f`, inbox-zero `c868856` (cloned for this),
waha `c42f68a`, wacli `83869f1`, whatsapp-mcp `7d6a06d`. Inferences are marked.

## Hypomnema before

`deploy/thu-ky/new-messages.mjs` drops chats whose name or id matches a regex of `dropChats` (default: official
accounts, ads, newsletters, broadcasts), overridable in `thu-ky/filters.json` in the Bot's folder, where a field
replaces the default list. Groups are read, ranked after direct chats. Dropped chats are still synced and stored; a
Bot reads them when asked. No screen.

## The references

| Project | Chosen by | Default for groups | Skipped chats kept? | Setting lives | Screen |
|---|---|---|---|---|---|
| OpenClaw | group id, sender id, `*`; `groupPolicy` open / allowlist / disabled (`docs/channels/groups.md:20-41`) | blocked until allowed | no: dropped before routing (`docs/channels/whatsapp.md:661`); an allowed group not mentioned keeps 50 messages as context (`historyLimit`) | `openclaw.json` per channel and account | a generic config form (`ui/src/pages/channels/view.config.tsx`); no picker found |
| Hermes | Telegram chat id, `group_allowed_chats` (`gateway/authz_mixin.py:754`) | not authorized until listed | only allowlisted chats in observe mode | env or `config.yaml` | none found |
| inbox-zero | sender, domain or subject pattern with an `exclude` flag (`apps/web/prisma/schema.prisma` `GroupItem`) | n/a | yes: mail stays in Gmail; excluded rules are skipped (`utils/ai/choose-rule/match-rules.ts:348-360`) | Postgres per email account | a learned-patterns page |
| Evolution API | one `groupsIgnore` boolean per instance (`src/api/dto/settings.dto.ts`) | read | no: group messages skipped in `messages.upsert`, not stored | database, settings API | not in this repository |
| waha | `ignore.{status,groups,channels,broadcast}` (`src/structures/sessions.dto.ts:200-225`) | read | no: events dropped | session config | none |
| wacli | no skip setting; `chats list --query`, mute (`docs/chats.md`) | — | — | — | a CLI list with search |
| OpenMausBot | Gmail only; up to 20 dismissed senders as a soft hint to the model (`server/decider/inbox-triage.ts`) | — | — | `brief.json` | Dismiss on a card, pause |
| Chatwoot | no source filter found | — | — | — | — |

Common default: the agents that act on chats (OpenClaw, Hermes) block groups until chosen; the others read them. No
reference keeps a skipped chat readable while only the automatic reading skips it, and none ships a per-chat list
with search and switches (inference from what was read).

## Decision (owner, 2026-11-27)

A per-chat flag "the secretary reads it", stored per Connection and chat id (inbox-zero's per-account exclude items);
a switched-off chat is still stored and read on request (the owner's requirement); the same defaults for every
source, Telegram included: direct chats and groups on, bots and official accounts off, configured later by the owner;
the screen opens from a gear on the Inbox page, as Meetings' settings did (#1121). Details in issue #1270.
