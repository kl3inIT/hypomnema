---
name: google-workspace
description: >-
  Reads, searches and changes the owner's Gmail, Google Calendar and Google Drive with the gws command, which
  Hypomnema installs and signs in. Use when the owner asks about their mail, calendar or files: what arrived today,
  what needs an answer, today's or tomorrow's schedule, a morning brief, finding a message or a file, drafting or
  sending a reply, creating an event, uploading a file. Not for a Google account Hypomnema was not given.
---

# Google Workspace through Hypomnema

Read, search and change the owner's Gmail, Google Calendar and Google Drive with the `gws` command (the Google
Workspace CLI). Hypomnema installed `gws` for you and signs it in on every call: you never see a password or a
token, and you never log in. Every request `gws` makes passes Hypomnema's service proxy, which adds the account's
credential and decides what runs at once and what waits for the owner.

Adapted from the Google Workspace CLI's `gws-shared` skill (Apache-2.0, https://github.com/googleworkspace/cli).
Changed for Hypomnema: how the command is signed in, which account it acts as, and what happens to anything that
writes; its request to star the repository is left out.

## How it is signed in

- Run `gws` as you would any command. A small launcher runs the real tool and sends its requests through the
  proxy, which signs them in. The tool itself holds no token. A call hours into your work is signed in like the
  first one.
- **Never run `gws auth ...`**, and never set or print a credential yourself. If a command says it is not
  signed in, or that you were given no account, say so in your report: only the owner can connect an account, in
  Settings, Integrations.
- You can use only the accounts you were given. A command for anything else is refused, and that is not something to
  work around.

## Which account

- When you were given one Google account, every command acts as it. Nothing to add.
- When you were given several, name one on each command: `gws --account work gmail +triage`. The label is the
  owner's name for the account (`work`, `personal`). A command that names none is refused with the list.
- An account may be read-only for a service. A command that changes something there is refused by Google; report it,
  do not retry.

## What runs at once, and what waits for the owner

The owner decides, action by action, what you may do with an account. You do not see the list; the proxy applies
it to each request:

- **Reading, and what is easy to undo**, runs at once: listing and reading mail, events and files, writing a
  draft, uploading a new file.
- **What reaches other people or cannot be taken back** is held for the owner's approval, unless he set it
  otherwise: sending mail, changing or deleting an event, changing, sharing or deleting a file, labelling or
  trashing mail. He is shown exactly what you sent: the recipients, the subject, the text, the file.
- **What he set to never** is refused.

Run the command you mean. Do not ask the owner first in words: the approval he is shown is the question, and it
shows more than you can say. What the command answers tells you what happened:

| The command says | What it means, and what to do |
|---|---|
| The service's normal answer | It ran. Read back what was written (the message id, the event link, the file id) and report it. |
| `Waiting for the owner's approval (apr_…): … was NOT run` | The command waited for the owner and he did not answer in time. Nothing was sent. **In a chat**: tell him in one line what waits for his approval, and end your turn; you are woken when he answers. **As a worker**: ask him with your question tool, as a plain question without options that names what you want approved, and wait for the answer. |
| You are told it was **approved** | Run the same command again, with the same arguments and the same content. It passes once, without another question. If you change anything, it is a new request and asks again. |
| `Rejected by the owner` or you are told it was **rejected** | Do not run it, and do not look for another way to do the same thing. Say so in your answer. |
| `Refused: the owner set … to never` | The same: say so, do not work around it. |

- Prefer `--dry-run` to see what a command would send before you send it.
- A request for a summary, a search or a brief is not a request to send or change anything: do not start a write
  the owner did not ask for.

## Never

- Never print a token, a cookie or a credential, and never write one into a file, a commit or a message.
- Never forward or quote mail outside the task you were given.

## Command shape

```bash
gws <service> <resource> [sub-resource] <method> [flags]
gws <service> +<helper> [flags]        # the helpers below, shorter for common work
```

| Flag | What it does |
|------|--------------|
| `--params '{"key": "val"}'` | URL and query parameters |
| `--json '{"key": "val"}'` | The request body |
| `--format <FORMAT>` | `json` (default), `table`, `yaml`, `csv` |
| `--dry-run` | Check the call locally without sending it |
| `-o, --output <PATH>` | Save a binary answer to a file |
| `--upload <PATH>` | Upload a file's content |
| `--page-all` | Follow every page (one JSON object a line) |
| `--page-limit <N>` | The most pages with `--page-all` (default 10) |

Wrap `--params` and `--json` in single quotes so the shell leaves the inner double quotes alone:
`gws drive files list --params '{"pageSize": 5}'`.

`gws <service> --help` lists a service's resources; `gws schema <service>.<resource>.<method>` shows a method's
parameters before you call it.

## References

Load only the one the work needs. Each is a file in `references/` beside this guide; when this guide came from
`hypomnema skills get google-workspace`, load one with
`hypomnema skills get google-workspace --reference <name>`.

| Work | Reference |
|------|-----------|
| Mail: every Gmail command | `gws-gmail` |
| The unread inbox at a glance | `gws-gmail-triage` |
| Read one message | `gws-gmail-read` |
| Search mail well (operators) | `gmail-search-syntax` |
| Send, reply, reply to all, forward | `gws-gmail-send`, `gws-gmail-reply`, `gws-gmail-reply-all`, `gws-gmail-forward` |
| Calendar: every command | `gws-calendar` |
| Today's or a day's agenda | `gws-calendar-agenda` |
| Create an event | `gws-calendar-insert` |
| Drive: every command | `gws-drive` |
| Upload a file | `gws-drive-upload` |
| A morning or next-day brief from mail and calendar | `daily-brief` |
