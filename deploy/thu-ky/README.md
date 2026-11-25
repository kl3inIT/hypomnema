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
| `triage.md` | The prompt of the `triage` Routine, every 15 minutes, read only: triage per conversation, drafts, meetings, accounts to log in again, and what it learns of the owner. |
| `new-messages.mjs` | The check of `triage`: counts the new messages, wakes the Bot only when some are left. |
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

Each run gets a batch the Bot finishes (issue #1101): the check names at most `perRun` conversations (15), and keeps
the rest as `carry` in its state. Direct chats and conversations that tag the owner (Lark) or name him come first,
then groups, then mail, then meetings. What was carried is named first at the next check, and wakes the Bot even
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
