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

## Zalo, studied again (later on 2026-11-16)

The owner asked which library and which repositories do it best, and named `DTHSoftwareCompany/zlog`. Read in
`.tmp/` at these revisions: `zca-js` 1e1c503, `zcago` d4ff65b, `zlog` e4d2b82f, `deplao-builder` 515f1dc,
`hermes-zalo-plugin` f00cf56, `softtyos/zca-js` e1c09a9; GitHub searched for Zalo personal-account projects.

| Project | What it is | Read in code |
|---|---|---|
| `RFS-ADRENO/zca-js` (MIT, 740 stars) | The library almost every project builds on | Pure JavaScript dependencies (`ws`, `crypto-js`, `pako`, `tough-cookie`…), Node ≥ 18. A session is `{imei, cookie, userAgent}` (`src/zalo.ts:34`), from a QR login (`loginQR`). Zalo removed `/api/group/history` (`getGroupChatHistory.ts`, deprecated); a group's past is paged with `getCloudMessage` (`hasMore`, `isOld`, filtered at the owner's join, `tsJoinGroup`); a direct chat's with `listener.requestOldMessages` (command 510). One web listener per account: opening Zalo Web stops it (README:113). Fixed the day before for Zalo's API version (`src/apiVersion.ts`, after v2.3.0) |
| `amrakk/zcago` (MIT, Go) | A Go rewrite of `zca-js` | Last commit 2026-09-08; a fixed API version (`config.DefaultAPIVersion`); no `getCloudMessage`; sending attachments still a TODO |
| `zlog` (MIT) | A product that logs many Zalo accounts for companies | A Go backend and a NestJS service holding the sessions with a private fork of `zca-js` (`"zca-js": "file:../zca-core"`, not in the repository). Reconnects (`listener.start({retryOnClose: true})`), refreshes cookies on a schedule (`cookie-refresh.service.ts`), and pulls the whole history from the phone after the owner confirms on it (`zalo-client.ts:3147`, `pullMobileMessage`), which only its fork has |
| `softtyos/zca-js` (MIT, one author, 1 star) | A fork of `zca-js` 2.1.1 that follows Zalo PC 26.7.10 | Has the phone sync in the open: `requestSyncFromPhone` sends `pull_mobile_msg` (command 12000) with an RSA key and a sequence range, no chat filter: the phone sends its whole store, decrypted by `getSyncData` (ZDB 4.0) |
| OpenClaw `zalouser`, `deplao-builder` (272 stars), `zalo-agent`, `zaloclaw` | Agents and apps on a personal account | All on `zca-js` 2.x, in a Node process |
| `hitechcloudvn/hermes-zalo-plugin` | Zalo for Hermes, which is Python | `zca-js` in a Node sidecar the plugin talks to over HTTP and WebSocket (`sidecar/server.js`) |

Decided by the owner (2026-11-16): `zca-js` itself, pinned, in a small Node bridge the control plane starts and talks
to, as Hermes, MemoryOS and `zlog` keep Zalo in a process of its language; the past of groups with `getCloudMessage`
and of direct chats with `requestOldMessages`; then, as a second step, the phone's whole store, ported from
`softtyos/zca-js` with its notice rather than depending on a one-author package that would hold the owner's cookies;
everything kept, as WhatsApp's mirror. Not `zcago`: two months behind Zalo and without a group's past. Design in issue
#613.
