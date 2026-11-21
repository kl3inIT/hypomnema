# The Thư ký Bot

The Bot that reads the owner's messages and meetings and keeps his Inbox (issue #1023, part 4 of #838). What it
does is its prompt (`persona.md`), what it may do is its tool switches and its read-only Routine; nothing here is
built into the server. The server code it relies on is described in `ARCHITECTURE.md` (Routines, "Access"; the
Inbox's "log in again" items; the meetings' tools).

| File | What it is |
|---|---|
| `setup.sh` | Makes or updates the Bot and its Routines with `hypomnema` commands, as the owner. |
| `persona.md` | The Bot's instructions: triage per conversation, drafts, meetings, the brief. |
| `triage.md` | The prompt of the `triage` Routine, every 15 minutes, read only. |
| `new-messages.mjs` | The check of `triage`: counts the new messages, wakes the Bot only when some are left. |
| `brief.md` | The prompt of the `brief` Routine, at 08:00 and 17:00, told on Lark. |

## Set it up (the owner, on the machine where `hypomnema` is logged in as him)

1. Check the accounts the Bot will read: `hypomnema integrations list`. The defaults are `lark:me` and `zalo:main`;
   add WhatsApp and Google by their labels.
2. Run `deploy/thu-ky/setup.sh --connections lark:me,zalo:main,whatsapp:<label>,google:<label>`. It sets the Bot
   (Claude Sonnet 5.5, GPT-6.1 Sol as its fallback, its tools) and the `triage` Routine, read only.
3. The brief is told in a Lark chat with the Bot. Give the Bot a Lark app of its own:
   `hypomnema channels lark set thu-ky --bot "Thư ký" --app-file - --owner <your Lark email> --mode owner --on`
   (the app's `app_id` and `app_secret` as JSON on standard input; never in a file kept in Git). Write to the app
   once on Lark, find that chat with `hypomnema chats --bot <the Bot's id>`, then run `setup.sh` again with `--brief-chat <chat id>`.

The filters of the check (no-reply senders, official accounts, newsletters, how many Lark chats) can be changed
without a release: write the fields to change into `thu-ky/filters.json` in the Bot's folder (the names are those of
`defaults` in `new-messages.mjs`). Its state, when each source was last read, is `thu-ky/new-messages.json` there;
removing it makes the next check look back one hour.

## The two-day trial (owner, 2026-11-21)

Run it on zm for two days before relying on it. Each day, look at the items it added (the Inbox page, or
`hypomnema inbox list --state proposed,open --stale`) and at its drafts (Needs you), and at what each run read and did (the
`triage` Routine's runs, each with its chat).

## Turn it off

- `hypomnema routines list --bot "Thư ký"` gives the Routines' ids.
- `hypomnema routines pause <id>` stops one; `hypomnema routines resume <id>` starts it again.
- `hypomnema routines rm <id>` removes one with its runs.

## What it can and cannot do

- In `triage`'s runs, and in its check, it reads the owner's accounts it was given and writes only to the Inbox:
  every send and every write outside the control plane is refused at once, never asked (read-only Routines, #956).
- It only reads the owner's own accounts (`bots set --owner-accounts read`, which only the owner changes): in every
  chat and Routine of the Bot, his own Lark chat with it included, each write and send through his Lark sign-in,
  his Google, his Zalo and his WhatsApp is refused at once, whatever his approvals or Allow settings say; reads
  stay. It has no send tools and gives work to no worker.
- A broken account is one "Log in again: <account>" item, kept and closed by the server from the Bot's reads.
