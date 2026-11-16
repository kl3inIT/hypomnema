# Messaging accounts as the owner: WhatsApp and Zalo

Date: 2026-11-16. Question from the owner: how can the main Bot read the WhatsApp (then Zalo) group chats he is in,
with his rights, as the Lark user Connection lets it act as him in Lark (issue #468)? Who has done it, and how? The
owner chose to link his main number (owner, 2026-11-16). Design: issue #612 (WhatsApp), issue #613 (Zalo).

## What Hypomnema has (main, 2026-11-16)

Only Lark: a Channel (`internal/controlplane/channels/`), and a user Connection that acts as the owner
(`internal/controlplane/integration/larkuser.go`). A Channel lets people talk to a Bot and refuses strangers
(`orchestration/channelstore/channelstore.go:47`, modes owner, allowlist, everyone); it is not how a Bot acts as the
owner. Nothing exists for WhatsApp or Zalo.

## Two ways into WhatsApp

| Way | What it gives | Who uses it |
|---|---|---|
| Meta's Cloud API (official) | A business number; customers write to it through a webhook; replies free within 24 hours, templates (paid) after | Chatwoot `app/services/whatsapp/` (with coexistence, `app/jobs/webhooks/whatsapp_events_job.rb:38`), Hermes `plugins/platforms/whatsapp_cloud/`, Evolution API `src/api/integrations/channel/meta/` |
| WhatsApp Web, a linked device | A person's own account, his chats and groups, by QR; unofficial, the number can be banned | OpenClaw `extensions/whatsapp/` (Baileys; personal number "fully supported", `docs/channels/whatsapp.md:103`), Hermes `plugins/platforms/whatsapp/` (default mode `self-chat`, `adapter.py:832`), MemoryOS `memoryos-integration-whatsapp/bridge/` (whatsmeow, ADR 0031), Evolution API, WAHA |

Only the second can act as the owner in his groups.

## MemoryOS, the owner's own

whatsmeow in a Go bridge process the JVM starts (the JVM has no maintained library), JSON lines over stdio, sessions
in whatsmeow's tables in PostgreSQL, a lease per bot for several processes; failures logged out, banned, replaced
(`docs/decisions/0031-whatsapp-bridge-and-bot-leases.md`, `docs/runbooks/whatsapp-bot.md`). Its runbook says never a
person's own number; the owner departs from it on purpose for this feature. Zalo in MemoryOS is the official Zalo Bot
API (`memoryos-integration-zalo/.../ZaloBotApi.java`), which acts as a bot, not as a person.

## whatsmeow (Go, MPL-2.0)

QR and phone-code linking (`qrchan.go:221` `GetQRChannel`, `pair-code.go:92` `PairPhone`), `SendMessage`,
`GetJoinedGroups` (`group.go:538`), `Download`, `BuildHistorySyncRequest` (`send.go:588`); events `Message`,
`HistorySync`, `LoggedOut`, `TemporaryBan`, `StreamReplaced` (`types/events/events.go`); `sqlstore.NewWithDB`. There
is no server-side query of past messages: messages are kept as they arrive, plus the history sync sent at link time.
Hypomnema is Go, so it can run in the control plane's process, with no bridge.

## Zalo

Official Zalo Bot and Official Account APIs act as a bot or a page (OpenClaw `docs/channels/zalo.md`). A personal
account is reached only through `zca-js` 2.2.0, unofficial, as OpenClaw's `zalouser` does in process, QR login,
groups behind an allowlist (`docs/channels/zalouser.md:85,102`). It is JavaScript: a Go control plane would start a
bridge process, as MemoryOS and Hermes do, unless a maintained Go library exists (not researched yet).

## Other omnichannel products

Chatwoot also connects Messenger, Instagram, Telegram, LINE, TikTok, SMS and email (`app/models/channel/`); every
stranger becomes a contact in an inbox, a bot answers first and hands off to a person (`conversation.rb:184`
`bot_handoff!`). That is the model for answering customers, a different need from acting as the owner.

Clones in `.tmp/`: openclaw, hermes-agent, memoryos, chatwoot, evolution-api, waha, whatsmeow.
