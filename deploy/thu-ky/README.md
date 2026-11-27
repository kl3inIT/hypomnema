# The secretary Routines

Two Routines that make the main Bot the owner's secretary: `triage` reads his messages and meetings and keeps his
Inbox, `brief` tells him what waits on him (issue #1023, part 4 of #838). The owner talks to one Bot: the Routines run
on the main Bot, which keeps its own persona, and their prompts carry the secretary's rules (owner, 2026-11-23,
issue #1107; claude-chief-of-staff, Hermes and OpenMausBot each have one main assistant whose scheduled job does the
triage). Nothing here is built into the server; the server code it relies on is described in `ARCHITECTURE.md`
(Routines, "Access"; the Inbox's "log in again" items; the meetings' tools).

| File | What it is |
|---|---|
| `setup.sh` | Makes or updates the two Routines of a Bot (the main Bot by default) with `hypomnema` commands, as the owner. It makes no Bot. |
| `triage.md` | The prompt of the `triage` Routine, every 15 minutes, read only: triage per conversation, drafts, meetings, accounts to log in again, a priority and a project on its items that have none, notes that say the need and the plan with every later update a comment (issue #1186), and what it learns of the owner. |
| `triage.test.mjs`, `triage-world.mjs`, `triage-run.mjs` | A run of `triage.md` with a real Claude Code against the Bot's tools as an MCP server: a new message on a known item is a comment, not a line of its notes (issue #1186). Only with `HYPOMNEMA_TEST_CLAUDE=claude node --test deploy/thu-ky/triage.test.mjs`; CI skips it. |
| `triage-cases.json`, `triage.eval.mjs` | The cases of the owner's short replies to asks ("dạ oki chị" to a request for work, "ok" to information, a sticker, a question back; issue #1182), each a thread and what the triage must add, close or leave, run with `triage.md` and a real model. Only with `THU_KY_EVAL=1 node --test deploy/thu-ky/triage.eval.mjs` on a node with the gateway; CI never runs it. Run it after a change to `triage.md`. |
| `new-messages.mjs` | The check of `triage`: counts the new messages, others' and the owner's own apart, and wakes the Bot only when some are left. |
| `brief.md` | The prompt of the `brief` Routine, at 08:00 and 17:00, told on Lark. |

## Set it up (the owner, on the machine where `hypomnema` is logged in as him)

1. Check that the main Bot reads the accounts the triage needs, and has the tools for it: `hypomnema bots show
   <the main Bot>` names its Connections and tools; `hypomnema integrations list` names the accounts (such as
   `lark:me`, `zalo:main`, `whatsapp:<label>`, `google:<label>`). It needs the `inbox`, `meetings`, `zalo` and
   `whatsapp` tools; `bots set <the main Bot> --connections …` adds an account.
2. Run `deploy/thu-ky/setup.sh`. It sets the `triage` Routine of the main Bot, read only (`--bot <name or id>` for
   another Bot).
3. The brief is told in the owner's Lark chat with the main Bot, through its Lark Channel. Find that chat with
   `hypomnema chats --bot <the main Bot's id>`, then run `setup.sh` again with `--brief-chat <chat id>`.

The filters of the check (no-reply senders, official accounts, newsletters, how many Lark chats) can be changed
without a release: write the fields to change into `thu-ky/filters.json` in the Bot's folder (the names are those of
`defaults` in `new-messages.mjs`). Its state, when each source was last read, is `thu-ky/new-messages.json` there;
removing it makes the next check look back one hour.

The owner's own new messages wake the triage too (issue #1182): his "dạ oki chị" to a request for work is work he
took, and is read even when no one writes after it. So his chats where only he writes would wake it for nothing: his
Lark chat with the main Bot, where he answers the brief, and his chat with himself (Zalo's "My Documents", his own
number on WhatsApp). Put their ids into `dropChats` of `filters.json`. A field there replaces the default, so copy the
default list of `new-messages.mjs` and add the ids, each a regular expression:

```json
{ "dropChats": ["^Zalo ", "\\bOA\\b", "official account", "quảng cáo", "newsletter", "@broadcast$",
  "^oc_<the brief's Lark chat>$", "^<his Zalo self-chat id>$", "^<his number>@s\\.whatsapp\\.net$"] }
```

The brief's Lark chat id is the `oc_…` of the main Bot's Lark Channel chat with him (the brief Routine's chat).

Each run gets a batch the Bot finishes (issue #1101): the check names at most `perRun` conversations (15), and keeps
the rest as `carry` in its state. Direct chats and conversations that tag the owner (Lark) or name him come first,
then groups, then chats with only his own new messages, then mail, then meetings. What was carried is named first at the next check, and wakes the Bot even
when nothing is new, so a backlog such as a first look of a week goes through in turns of 15 minutes. For a group
message that names him to count as a direct ask, write his names into `ownerNames` of `filters.json` (regular
expressions, such as `["Đạt", "anh Dat"]`); by default none is looked for.

## The owner's context (issue #1119)

The Bot keeps what it learns of the owner in one note of its memory, `/memories/context/owner.md`: his people (role,
relation, language, how he addresses them), his recurring processes ("when ThanhTV sends a CV, it goes to cô Laura's
candidate sheet"), his links, projects and teams, each entry with where it was learned.

- Every run of the Bot's Routines, `triage` and `brief` included, begins with the note as it was when the run began
  (as Hermes gives its `USER.md` at a session's start).
- `triage` enriches it itself: a fact an item needs and the note lacks is looked up with its read tools; what the
  owner changed in his Inbox since the last run (an item he added, dropped, edited or reopened) is read as a rule it
  missed, the check naming that moment (`woke` in its state); and at its end the run writes what it learned. It is
  the one part of the Bot's memory a read run may change.
- It holds at most 8,192 bytes, a note's limit: a change past it is refused, so the Bot merges and prunes.
- The owner reads and edits it on the Bot's page, under Notes (a read run cannot write the Bot's folder, so it is
  not a file of the Files panel), or asks the Bot in a chat to change it. Nothing is to be made for it: the first
  run that learns something creates it.

## Turn the Thư ký Bot off

Until 2026-11-23 these Routines ran on a Bot of their own, "Thư ký", made by an earlier `setup.sh`. Once the main
Bot's `triage` runs:

- `hypomnema routines list --bot "Thư ký"` gives its Routines' ids; `hypomnema routines pause <id>` stops each, and
  `hypomnema routines rm <id>` removes one with its runs.
- Its Lark Channel, if one was set: `hypomnema channels lark set thu-ky --off`.
- The Bot itself, once nothing of it is wanted: `hypomnema bots rm "Thư ký"` (refused while Routines or Lark Channels
  use it).

Its Inbox items stay: the Inbox is the owner's, not the Bot's.

## Once: the temporary morning-brief's list

Before these Routines, a temporary `morning-brief` Routine of the main Bot kept its own list,
`files/morning/open.md` in the main Bot's folder. Before removing it, move its open items into the Inbox, once:

1. In a chat with the main Bot, ask it to read `files/morning/open.md` and, for each line still open, look in the
   Inbox (`inbox_list`) and add what is not there yet with `inbox_add`, from that chat, with the source and the
   quote the line names; a line it cannot place is proposed.
2. Check the items it added (the Inbox page, or `hypomnema inbox list --state proposed,open`).
3. Remove the Routine: `hypomnema routines list` gives the id of `morning-brief`, `hypomnema routines rm <id>`
   removes it. The file may stay as it is or be removed by hand.

## The two-day trial (owner, 2026-11-21)

Run it on zm for two days before relying on it. Each day, look at the items it added (the Inbox page, or
`hypomnema inbox list --state proposed,open --stale`) and at its drafts (Needs you), and at what each run read and
did (the `triage` Routine's runs, each with its chat).

## Turn it off

- `hypomnema routines list --bot <the Bot>` gives the Routines' ids.
- `hypomnema routines pause <id>` stops one; `hypomnema routines resume <id>` starts it again.
- `hypomnema routines rm <id>` removes one with its runs.

## What it can and cannot do

- In `triage`'s runs, and in its check, the Bot reads the owner's accounts it was given and writes only to the
  Inbox and to its note of the owner's context: every send and every write outside the control plane is refused at
  once, never asked (read-only Routines, #956). This is what keeps the triage from sending, now that the Bot also
  has send tools for its chats.
- In the Bot's chats, its Lark chat with the owner included, a send waits for the owner's approval, as for any of
  its work there.
- A broken account is one "Log in again: <account>" item, kept and closed by the server from the Bot's reads.
