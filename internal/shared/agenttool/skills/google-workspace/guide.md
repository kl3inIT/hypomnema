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
token, and you never log in.

Adapted from the Google Workspace CLI's `gws-shared` skill (Apache-2.0, https://github.com/googleworkspace/cli).
Changed for Hypomnema: how the command is signed in, which account it acts as, and the rule for anything that
writes; its request to star the repository is left out.

## How it is signed in

- Run `gws` as you would any command. A small launcher gets a fresh, short-lived token for the call and then runs
  the real tool. A call hours into your work is signed in like the first one.
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

## Before anything that writes

Sending or replying to mail, deleting or archiving, creating or changing an event, uploading, sharing or deleting a
file: **say exactly what you are about to do and get the owner's yes first.**

- In a chat, ask the owner in the chat. A request for a summary, a search or a brief is not a yes to send or change
  anything.
- As a worker, ask with your question tool and wait for the answer, unless the task you were given already names
  that exact action and its target.
- Prefer `--dry-run` to see what a command would send before it is sent.
- After a write, read back what was written (the message id, the event link, the file id) and report it.

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
