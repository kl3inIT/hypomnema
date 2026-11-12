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
only, and what happens to anything that writes; its sign-in, QR code, configuration and update steps are left out.

## How it is signed in

- Run `lark-cli` as you would any command. A small launcher runs the real tool and sends its requests through the
  proxy, which signs them in. The tool itself holds no token. A call hours into your work is signed in like the
  first one.
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

## What runs at once, and what waits for the owner

Every request `lark-cli` makes passes Hypomnema's service proxy, which adds the app's credential and decides what
runs at once and what waits for the owner. He sets it service by service (messages, mail, calendar, tasks, drive,
wiki, sheets and the rest), by the risk `lark-cli` itself gives a command; you do not see the list.

- **A read** runs at once.
- **A write** (sending a message, creating or changing a document, a record, a file, an event or a task) and **a
  high-risk write** (deleting, transferring, changing who may see something) are held for the owner's approval,
  unless he set them otherwise. He is shown exactly what you sent.
- **What he set to never** is refused.

Run the command you mean. Do not ask the owner first in words: the approval he is shown is the question. What the
command answers tells you what happened:

| The command says | What it means, and what to do |
|---|---|
| Its normal answer | It ran. Read back what was written (the message id, the document's link) and report it. |
| `Waiting for the owner's approval (apr_…): … was NOT run` | He did not answer in time; nothing was done. **In a chat**: tell him in one line what waits for his approval, and end your turn; you are woken when he answers. **As a worker**: ask him with your question tool, as a plain question without options that names what you want approved, and wait. |
| You are told it was **approved** | Run the same command again, with the same arguments and the same content. It passes once. A changed command is a new request and asks again. |
| `Rejected by the owner`, or you are told it was **rejected** | Do not run it, and do not look for another way to do the same thing. Say so in your answer. |
| `Refused: the owner set … to never` | The same: say so, do not work around it. |

- Each command's `--help` names its risk: `read`, `write` or `high-risk-write`. Prefer `--dry-run`, which shows the
  request and sends nothing.
- **Exit code 10** means the command is a high-risk write and `lark-cli` stopped before sending it, asking for the
  flag its message names (`--yes`). Run it again with that flag: the proxy then holds the request for the owner's
  approval, which is the confirmation `lark-cli` asks for. Never treat the flag as the owner's yes.
- A request to read or summarize is not a request to send or change anything: do not start a write the owner did
  not ask for.

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
