---
name: github
description: >-
  Reads GitHub with the gh command: issues, pull requests and their diffs, CI runs, checks and failed logs, and
  files of a repository. Use when the owner asks about something on GitHub (an issue, a pull request, why CI
  failed) and you were given a github.com account.
metadata:
  hypomnema:
    requires:
      integration: github.com
---

# GitHub through Hypomnema

Read GitHub with the `gh` command (GitHub's own command-line tool). Hypomnema installed `gh` for you and signs it in
on every call: you never see a token, and you never log in. Written for Hypomnema (issue #685) from Onyx's github
skill (skills/builtin/github/SKILL.md.template, MIT, Copyright (c) 2023-present DanswerAI, Inc.) and OpenClaw's
(skills/github/SKILL.md, MIT, Copyright (c) 2026 OpenClaw Foundation); their login, install and merge steps are
left out.

## How it is signed in

- Run `gh` as you would anywhere. A small launcher runs the real tool and sends its requests through the control
  plane, which adds the token of the github.com account the owner gave you. The tool itself holds a placeholder.
- **Never run `gh auth login`, `gh auth logout`, `gh auth setup-git`, `gh auth token` or `gh config set`**, and never
  set `GH_TOKEN` or `GITHUB_TOKEN` yourself. If a command says you were given no account, say so: only the owner
  connects one (`hosts token set github.com`) and allows it for chats.
- Only github.com's API is reached: `git clone` and `gh repo clone` do not work here. Read files with
  `gh api repos/OWNER/REPO/contents/PATH` instead. Giving coding work to a worker is the `hypomnema-coordinator`
  skill's.

## Reading

```bash
gh issue view 42 --repo OWNER/REPO --comments
gh issue list --repo OWNER/REPO --state open --limit 20 --json number,title,labels,url
gh pr view 55 --repo OWNER/REPO --json title,body,author,state,files,reviews,reviewDecision
gh pr checks 55 --repo OWNER/REPO
gh pr diff 55 --repo OWNER/REPO
gh run list --repo OWNER/REPO --limit 10
gh run view <run-id> --repo OWNER/REPO --log-failed
gh search issues "repo:OWNER/REPO is:open label:bug" --limit 10
gh api repos/OWNER/REPO/contents/README.md --jq '.content' | base64 -d
```

- A link works in place of a number: `gh pr view https://github.com/OWNER/REPO/pull/55`.
- Prefer `--json` with the fields you need and `--jq` to cut it down; cap lists with `--limit`. Long logs: read the
  failed step's last lines, not the whole log.
- `gh api` reaches any REST endpoint and `gh api graphql -f query='…'` a GraphQL query; pass values as `-f`/`-F`
  fields, never pasted into the path. `hypomnema tool run github.com -- curl https://api.github.com/…` runs another
  command through the same account.

## Writing waits for the owner

- Opening an issue, commenting, opening a pull request, writing a file or a branch, and anything the control plane
  does not name (merge, close, labels, rerun, workflow dispatch) waits for the owner's approval: the command answers
  that it waits and was not run. Say so in one line and end your turn; run the same command again, unchanged, once
  he approves.
- Write only what the owner asked for in this chat. Use `--body-file` for a body with backticks or code.
- A `403` or `Resource not accessible` means the account's token does not reach that repository or action: report
  it, do not look for another way.
