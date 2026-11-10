---
name: lark
description: >-
  Reads and changes what the owner's Lark app may reach in Lark (documents, sheets, Base, wiki, drive, messages,
  calendars, tasks) with the lark-cli command, which Hypomnema installs and signs in. Use when the owner asks about
  something in Lark: reading or writing a document or a sheet, a Base table, a wiki page, a file, sending a message
  as the bot, a calendar the app can see. Not for a person's own mail, calendar or drive: the command acts as the
  app, not as the owner.
---

# Lark through Hypomnema

Work in Lark with the `lark-cli` command (Lark's own command-line tool). Hypomnema installed `lark-cli` for you and
signs it in on every call: you never see a secret or a token, and you never log in.

Adapted from `lark-cli`'s `lark-shared` skill (MIT, Copyright (c) 2026 Lark Technologies Pte. Ltd.,
https://github.com/larksuite/cli). Changed for Hypomnema: how the command is signed in, that it acts as the app
only, and the rule for anything that writes; its sign-in, QR code, configuration and update steps are left out.

## How it is signed in

- Run `lark-cli` as you would any command. A small launcher gets a fresh, short-lived token for the call and then
  runs the real tool. A call hours into your work is signed in like the first one.
- **Never run `lark-cli auth ...`, `lark-cli config ...`, `lark-cli profile ...` or `lark-cli update`**, and never
  set or print a credential yourself. The tool's own skills and hints sometimes say to log in, to run
  `config init`, to show a QR code or to switch `strict-mode`: do none of it here. If a command says it is not
  configured, or that you were given no account, say so in your report: only the owner can connect the app, in
  Settings, Integrations.
- You can use only the Lark app you were given. A command for anything else is refused, and that is not something
  to work around.

## Who you act as: the app, never a person

- Every command acts as the owner's Lark app (the bot). `lark-cli whoami` shows it: `"identity": "bot"`.
- `--as user` is refused ("strict mode is bot"). Do not try to change that.
- The app sees only what it was given: documents, folders, Bases and wikis shared with the app, chats the bot is a
  member of, calendars it was invited to. **A person's own things come back empty, not as an error**: an empty
  list does not mean there is nothing, it may mean the app was not given it. Say that in your report instead of
  concluding the thing does not exist.
- What the app may do is also bounded by its scopes in Lark's developer console. A call that answers with a missing
  scope is reported with the scope's name; the owner adds it there. Do not open or follow a console link yourself.

## Which account

- When you were given one Lark app, every command acts as it. Nothing to add.
- When you were given several, name one on each command: `lark-cli --account work docs --help`. The label is the
  owner's name for it (`work`). A command that names none is refused with the list.

## Before anything that writes

Sending a message, creating, changing or deleting a document, a record, a file, an event or a task, changing who
may see something: **say exactly what you are about to do and get the owner's yes first.**

- In a chat, ask the owner in the chat. A request to read or summarize is not a yes to send or change anything.
- As a worker, ask with your question tool and wait for the answer, unless the task you were given already names
  that exact action and its target.
- Each command's `--help` names its risk: `read`, `write` or `high-risk-write`. Prefer `--dry-run`, which shows the
  request and sends nothing.
- **Exit code 10 is a gate, not an error**: the command is a high-risk write and stopped before doing it. Show the
  owner the action and its main parameters, get an explicit yes, and only then run it again with the flag its
  `hint` names added at the end. Never add that flag on your own.
- After a write, read back what was written (the document's link, the record's id, the message's id) and report it.

## Never

- Never print a token, a secret or a credential, and never write one into a file, a commit or a message.
- Never send to a chat or share a document outside the task you were given.

## Command shape

```bash
lark-cli <domain> --help                         # a domain's +shortcuts and its API resources
lark-cli <domain> +<shortcut> [flags]            # a high-level task; prefer these
lark-cli <domain> <resource> <method> [flags]    # one API method
lark-cli schema <service>.<resource>.<method>    # a method's parameters, types and scopes, before calling it
lark-cli api GET /open-apis/...                  # any endpoint by its path, when nothing typed exists
```

- `--jq <expr>` filters the JSON that comes back; `--format json|table|csv` chooses its form; `--page-all` follows
  every page.
- **Success is `"ok": true`** (or exit code 0). There is no top-level `code` on success; `code` is only inside
  `error` when a call failed. Do not test `code == 0`.
- File paths (`--file`, `--output`, `@file`) must be relative to the directory you run in; an absolute path is
  refused as unsafe. Pass large JSON on standard input (`--data -`).
- Wrap `--params` and `--data` in single quotes so the shell leaves the inner double quotes alone.

## The tool's own guides

`lark-cli` carries a guide for each domain, matching its version. Read the one for the work at hand before the
first call in that domain, and only that one:

```bash
lark-cli skills list                    # every guide with what it is for
lark-cli skills read lark-doc           # a guide
lark-cli skills read lark-doc references/<file>.md    # one of its references
```

| Work | Guide |
|------|-------|
| Documents (docx): read, write, comment | `lark-doc`, `lark-markdown` |
| Sheets | `lark-sheets` |
| Base: tables, records, views | `lark-base` |
| Wiki spaces and pages | `lark-wiki` |
| Drive: files, folders, permissions | `lark-drive` |
| Messages and group chats, as the bot | `lark-im` |
| Calendars and events the app can see | `lark-calendar` |
| Tasks | `lark-task` |
| People and departments | `lark-contact` |
| An endpoint with no typed command | `lark-openapi-explorer` |

These guides are written for a person's own `lark-cli`. Where one disagrees with this guide (signing in, acting as
a user, configuration, updating), this guide wins.
