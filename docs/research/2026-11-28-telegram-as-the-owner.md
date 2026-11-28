# Telegram as the owner: his personal account behind the device Integration

Date: 2026-11-28, for issue #1184. Question: how does Hypomnema read and write the owner's own Telegram (his direct chats
and groups, as a person, not a bot), in the shape WhatsApp (#612) and Zalo (#613) already have? Read in code at these
revisions: gotd/td `570af5c7` (MIT, `.tmp/gotd-td`), mautrix/telegram `d0869244` (AGPL-3.0, `.tmp/mautrix-telegram`),
Hermes `e93b3da9a8`, OpenClaw `032a2b336`, and Telegram's own pages (core.telegram.org, fetched 2026-11-28).
Inferences are marked "(inference)". Earlier notes: [messaging accounts](2026-11-16-messaging-accounts-as-the-owner.md),
[which chats the secretary reads](2026-11-27-chats-the-secretary-reads.md).

## What a bot cannot do (why not the Bot API)

A Telegram bot is a separate account. It sees only chats where it was added. With privacy mode on (the default) it gets
commands, replies to itself and service messages in a group (Hermes `website/docs/user-guide/messaging/telegram.md:257-263`,
OpenClaw `docs/channels/telegram/setup.md:99-107`); even with privacy off it never sees other bots' messages
(core.telegram.org/bots/faq). It cannot read the owner's direct chats with anh Trung at all. Hermes
(`plugins/platforms/telegram/adapter.py:2`, python-telegram-bot) and OpenClaw (`docs/channels/telegram.md:10`, grammY)
both use the Bot API, so neither reads a person's Telegram. Only an MTProto user client does.

## What Hypomnema has (main, 2026-11-28; read in code)

- `messaging.Service` / `messaging.Account` (`internal/controlplane/messaging/messaging.go`): `Link` (steps with a QR
  code or image, `LinkStep`), `Open`, and per account `Chats`, `Messages`, `Media`, `Backfill`, `Send`; `State` with
  `StatusLoggedOut`, `Banned`, `Replaced`; `Chat.Kind` is `dm`, `group` or `broadcast`.
- One package per service, a `<service>.db` beside the control plane's database: `messaging/whatsapp/` (whatsmeow in
  process, `store.go` mirror tables `wa_*` with FTS5, `backfill.go`, `media.go`, `send.go`, `members.go`) and
  `messaging/zalo/` (a Node bridge, `bridge.go`). Registered in `internal/controlplane/controlplane.go:359-377`
  (`whatsapp.Open(... "whatsapp.db")`, `zalo.Open(... "zalo.db")`, `integrations.WithDevices`).
- `integration/catalog.go:382-403` defines `WhatsApp()` and `Zalo()` as `KindDevice`; `integration/device.go` runs the
  link: `beginLink`, a followed QR with `Code`, `Image`, `Expires`.
- Bot tools: the table in `agentapi/messaging_tools.go:53-72` (`chats`, `messages`, `media`, `history`, `send` per
  integration), tool names in `shared/agenttool/` (`whatsapp.go`, `switches.go`) and `shared/toolsmcp/whatsapp.go`;
  the send asks approval through `serviceproxy/whatsapp.go` (catalog) and `inbox/send.go`, `inbox/sent.go`.
- Chat-selection: `inbox_chat_reads` has no CHECK on the integration "so that Telegram (#1184) takes these rows"
  (`migrations/00184_inbox_chat_reads.sql:5`); `inboxstore/chatreads.go` `ChatKind(serviceKind, name, id)` returns
  `bot` for service kind `broadcast` or a name matching `BotPatterns`; `inbox/chats.go` `List` switches on
  `"zalo", "whatsapp"` and `"lark"` and refuses anything else.

## Mechanisms compared

| Question | gotd/td (library) | mautrix/telegram (bridge, user login) | Hermes, OpenClaw (Bot API) | Telegram docs |
|---|---|---|---|---|
| Acts as | a user or a bot, by login | a user ("puppeting"), `pkg/connector/loginphone.go`, `loginqr.go` | a bot only | user login needs `api_id` + `api_hash` |
| Login | phone, code, 2FA: `telegram/auth/flow.go:51-107` (`Flow.Run`: `SendCode`, `SignIn`, on `ErrPasswordAuthNeeded` the password); QR: `auth/qrlogin/qrlogin.go:60-160` (`Export`, `Accept`, `Import`, and `Options.Password` callback on `SESSION_PASSWORD_NEEDED`, line 146) | both: `PhoneLogin` (number, code, `passwordLoginStep` at `login.go:231`) and `QRLogin`; `api_id`/`api_hash` required in config (`config.go:66,230`) | bot token from BotFather | QR token lasts about 30 s and is re-exported (core.telegram.org/api/qr-login); 2FA is not described on that page (inference: as for phone login) |
| Session storage | `session.Storage` interface, `LoadSession`/`StoreSession` bytes (`session/session.go:136`); file, memory, or your own; `session.Data` holds the auth key, DC, user id (`:94-118`) | the session is a field of the login's metadata in the bridge database (`client.go:266`, `CustomSessionStorage`) | none | one `api_id` per phone number |
| Updates and gaps | `telegram/updates` manager: pts/qts/seq, getDifference with backoff, FLOOD_WAIT waited out (`updates/doc.go`), `StateStorage` interface with 10 methods (`updates/storage.go:30-41`); `OnChannelGap` for a channel gap it cannot recover (`updates/config.go:53-65`), `OnTooLong` | gotd's manager with the bridge database as `Storage` and `AccessHasher` (`client.go:255-260`) | long polling or webhook | n/a |
| History | `messages.getHistory` iterator `telegram/query/messages/queries.gen.go:275` (`GetHistoryQueryBuilder.Iter`), search `:1529`, dialogs `query/dialogs/queries.gen.go:110`; takeout wrapper `telegram/takeout/takeout.go:120,192` | backfill per chat (`backfill.go`); dialog sync by recency: `sync.update_limit 100`, `create_limit 15` (`example-config.yaml`); takeout optional, default off (`takeout.dialog_sync: false`) | bots get no past | takeout lists private chats, groups, channels (core.telegram.org/api/takeout); the page does not say how the user confirms (not verified) |
| Media | `Client.Download(location)` builder with CDN, per-DC scheduler (`telegram/download.go:234`) | `directdownload.go`, `media/` | file API with size caps | n/a |
| Flood wait | `tgerr.AsFloodWait` (`tgerr/flood_wait.go:20`) returns the wait; the retry middleware is `gotd/contrib/middleware/floodwait`, used by `examples/userbot/main.go` (imports; contrib not read) | its fork of gotd | `telegram_flood_cooldown.py` for bot limits | bots: 1 message/s per chat, 20/min per group (bots/faq) |
| Lost login | `auth_lost.go:44-60` `IsAuthLost`, `account_status.go` `AuthLostKindOf`: banned, deleted, revoked, unregistered, expired, key_invalid (`AUTH_KEY_DUPLICATED` is the same key used elsewhere); `FreezeState` for a frozen account (`:147`) | `OnAuthError`, `OnDead` (`client.go:270-272`) | token revoked | other sessions get `updateNewAuthorization`; nothing says a new login ends them (core.telegram.org/api/auth) |
| Kinds of chat | `tg.User.Bot` (`tg/tl_user_gen.go:216`), `Verified :222`, `Support :233`, `Scam :235`, `Fake :241`, `Deleted :209`; `tg.Channel.Broadcast` (`tg/tl_chat_gen.go:1042`) vs `Megagroup :1051`, `Gigagroup :1109`, `Forum :1144`; `tg.Chat` is a basic group | uses `user.Bot` (`userinfo.go:294`), `chat.Broadcast`, `chat.Megagroup` (`chatinfo.go:163-166,199-204`); no bot filter; a member cap `max_member_count` (`chatinfo.go:204`) | `ChatType` private, group, channel; `is_bot` of a sender (`adapter.py:743`) | n/a |
| License | MIT | AGPL-3.0 (`LICENSE`): read, never copy | MIT (Hermes, OpenClaw) | n/a |

How each tells "junk" chats apart: none of them has a sales-bot or spam filter. gotd gives only the account flags
(`Bot`, `Verified`, `Scam`, `Fake`, `Support`) and the channel flags (`Broadcast` vs `Megagroup`). mautrix bridges by
recency and a member cap, not by kind. Hermes and OpenClaw block groups until allowlisted (the 2026-11-27 note). So the
default "bots and official accounts skipped" is built from flags; nothing reads a message to decide (inference).

## Telegram's rules for a user client (core.telegram.org, fetched 2026-11-28)

- `api_id` and `api_hash` come from my.telegram.org, one `api_id` per phone number. The shared sample id is limited
  and gives `API_ID_PUBLISHED_FLOOD`. All client apps must follow the API Terms (`/api/obtaining_api_id`).
- Flooding or spamming means a ban. "Accounts that log in with unofficial clients are automatically put under
  observation." Appeals go to recover@telegram.org (same page).
- API Terms (`/api/terms`): section 1.1 guard users' privacy; 1.4 no actions on behalf of the user without his
  knowledge and consent, and no tampering with read status ("ghost mode"); 1.5 no using Telegram data to train or
  fine-tune AI models; 2.2 users must know the app uses the Telegram API; 4 violations end API access after 10 days.
  What we do fits 1.4 (sending waits for his approval; we never call `messages.readHistory`, so chats are not marked
  read) and, as inference, 1.5 (messages go to a model for answering, not for training; the owner should confirm that
  reading).
- A new login does not end the owner's other sessions (the page says other sessions get `updateNewAuthorization` and the
  user may reject an unconfirmed one; a new device appears in his device list). Unlike WhatsApp's one linked device
  and Zalo's one web listener, several Telegram clients live together.

## Recommendation (one)

**gotd/td in the control plane's process, linked by QR with a password step, one `telegram.db`, tools mirrored from
WhatsApp.**

- **Library: `github.com/gotd/td`.** From: whatsmeow in process for WhatsApp (2026-11-16 note) and mautrix, the one
  maintained Telegram user bridge in Go, built on gotd (`pkg/gotd` is its vendored fork; why it forked is not read).
  Pure Go, MIT, go 1.26 against our 1.27.4: no bridge process, no Node, unlike Zalo. Departs from nothing.
- **Login on the Integrations page.** QR first, as WhatsApp and Zalo: `qrlogin.Export` loop, each token about 30 s
  (`qrlogin.go`), shown through the existing `Code`/`Expires` of `LinkStep`. The account's two-step password is asked
  with one more step: `qrlogin.Options.Password` is a blocking callback, so it waits for the page's answer. Phone
  number plus code is the fallback when the QR cannot be scanned (`auth.Flow` with a `UserAuthenticator`,
  `flow.go:168`). From: gotd `examples/userbot` has a `-qr` flag and a phone path; mautrix offers both. This needs a
  change the other devices did not: a link step that asks for typed input (phone, code, password) and a call to
  answer it (inference: `messaging.Service` and `integration/device.go` grow `Ask` and `Answer`; not in a reference's
  shape, it is the minimum for 2FA).
- **Where the session lives.** `telegram.db` beside `whatsapp.db` and `zalo.db` (`controlplane.go:359-377`), SQLite
  (`modernc.org/sqlite`, already in `go.mod`): our implementation of `session.Storage` (one blob per account), of
  `updates.StateStorage` and the access-hash storages, and the mirror tables `tg_chats`, `tg_messages` with FTS5 shaped
  as `wa_*` (`whatsapp/store.go`). From: mautrix keeps session, update state and access hashes in its database
  (`client.go:255-266`). The auth key is the full account, as whatsmeow's keys in `whatsapp.db`; encrypting it with
  the vault is an open question. The owner's `api_id`/`api_hash` live in the vault (never in Git).
- **Updates and past.** `updates.Manager` keeps every chat as it arrives and recovers gaps itself;
  `OnChannelGap` and `OnTooLong` trigger a backfill of the chat. Unlike WhatsApp (the phone sends history) and Zalo
  (a phone sync), Telegram's servers answer `messages.getHistory`, so `telegram_history` is a real server fetch of
  messages older than the oldest kept, with `offset_id`. Takeout is left out: mautrix keeps it off by default and the
  docs page does not show how it is confirmed (a worker may add it later for a bulk first import).
- **Rate and bans.** Wrap the client with the floodwait middleware (the same one gotd's userbot example uses; read its
  code when building); fetch history by pages at a modest pace; never send without approval.
- **Tools.** `telegram_chats`, `telegram_messages`, `telegram_media`, `telegram_history`, `telegram_send` as the
  WhatsApp row of `agentapi/messaging_tools.go`; `telegram.send` asks approval through a `serviceproxy` catalog like
  `whatsappCatalog` (`serviceproxy/whatsapp.go:9`); text, replies and files, no mentions in step one.
- **State.** `State.Status` from `AuthLostKindOf`: banned, revoked or unregistered to `StatusLoggedOut` (banned keeps
  `StatusBanned`), `AUTH_KEY_DUPLICATED` to `StatusReplaced` (inference), frozen account (`FreezeState`) shown with its
  `Until` as a new reason.
- **Chat kinds for the chat-selection page** (default reads: direct and group yes, bot and channel no, owner 2026-11-27).
  Chat id is the peer type plus id, `user:123`, `chat:456`, `channel:789` (inference: peers are not unique across types).

| Telegram | `Chat.Kind` | Secretary default | From |
|---|---|---|---|
| `tg.User`, not bot, not verified | `dm` (Direct) | read | `User.Bot` |
| `tg.User` with `Bot` | `bot` (new service kind, `ChatKind` takes it as Bot) | skipped | gotd `User.Bot`; mautrix `userinfo.go:294` |
| `tg.User` with `Verified`, `Support`, `Scam` or `Fake` (Telegram's own service chat 777000, official accounts) | `bot` | skipped | gotd flags (inference: our "official accounts" row; no reference maps them) |
| `tg.User` with `Deleted` | not listed | n/a | gotd `Deleted` |
| `tg.Chat` (basic group), `tg.Channel` with `Megagroup` | `group` | read | gotd; mautrix `chat.Megagroup` |
| `tg.Channel` with `Broadcast` (also `Gigagroup`) | `broadcast` (already maps to Bot) | skipped | mautrix `IsBroadcast`; the existing `ChatKind` rule |
| `ChatForbidden`, `ChannelForbidden`, left chats | not listed | n/a | gotd |

  Not covered by flags: a human who cold-messages him to sell, and big public supergroups full of promotion. A later
  switch could use "not in his contacts" (`User.Contact` / `MutualContact`, gotd `tl_user_gen.go:196-202`) or a member
  cap like mautrix's `max_member_count`; neither is a default now (inference, owner to decide).

## Files a Telegram integration mirrors (from the code read above)

- New `internal/controlplane/messaging/telegram/`: `telegram.go` (Service, Account, updates handler), `store.go` and
  `store_media.go` (mirror, FTS5; shape of `whatsapp/store.go`), `session.go` (`session.Storage`, `StateStorage`),
  `backfill.go` (`getHistory`), `media.go`, `send.go`, `members.go`, `convert.go` (tg types to `messaging.Message`).
- Edits: `messaging/messaging.go` (link step that asks for input; `Chat.Kind` adds `bot`); `integration/catalog.go`
  (`TelegramID`, `Telegram()`), `integration/device.go` (ask/answer steps), `controlplane.go` (`telegram.Open`,
  `WithDevices`); `agentapi/messaging_tools.go`, `agentapi/tools.go`, `agentapi/signins.go`, `shared/agenttool/`
  (`telegram.go`, `switches.go`), `shared/toolsmcp/`; `serviceproxy/telegram.go`, `serviceproxy/catalog.go`;
  `inbox/send.go`, `inbox/sent.go`, `inbox/inbox.go`, `inbox/chats.go` (add `"telegram"`), `orchestration/inboxstore/`
  (`FromTelegram`, `ChatKind` takes `bot`; no migration: 00184 has no CHECK), `clientapi/inboxchats.go`,
  `clientapi/toolaccess.go`, `clientcli/integrations.go`; `api/openapi.yaml` and the generated files; the web
  Integrations page (QR plus a password field) and the chat-selection page (no change if kinds map as above).
- No new migration for chats; `telegram.db` makes its own tables as `whatsapp.db` does.

## Risks

- Account limits: unofficial clients are "under observation"; a flood or spam means a ban (obtaining_api_id). Keep to
  reading plus approved sends; no bulk history pulls at login (page it).
- The auth key in `telegram.db` is the whole account; anyone with the file is him. Same posture as `whatsapp.db`.
- Telegram's AI-training ban (terms 1.5) and consent rules (1.4): reading to answer is not training (inference); say it
  to the owner once.
- gotd is a large generated package (`tg/`): long builds and a bigger binary (inference, not measured).
- Channel gap recovery is partial: `ChannelDifferenceTooLong` is not fully recovered (`updates/doc.go` limit 3), so a
  backfill after `OnChannelGap` is required.
- Not measured live: login, flood-wait rates and takeout confirmation. A spike on a server (never on the owner's own
  machine login, see memory) should test one login, a getHistory of one chat, and a session survive restart.

## Open questions for the owner

1. Does he already have an `api_id`/`api_hash` for his number (my.telegram.org, one per number)? If not, he makes it
   there; the control plane never makes it for him.
2. Accept the ban/observation risk of an unofficial client on his main account (as for WhatsApp and Zalo, owner
   2026-11-16)?
3. Link by QR plus password (recommended), or phone number plus code?
4. Encrypt the session in the vault, or keep it in `telegram.db` like `whatsapp.db`?
5. Chats to skip by default beyond flags: channels yes (broadcast), and do you want "not in my contacts" or a big-group
   member cap later?
6. Forum groups (topics): read as one group chat first?
