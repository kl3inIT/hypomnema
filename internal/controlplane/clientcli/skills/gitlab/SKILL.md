---
name: gitlab
description: >-
  Reads GitLab with the glab command: issues, merge requests and their diffs, pipelines, jobs and their logs, and
  files of a project, on gitlab.com or a company's GitLab server. Use when the owner asks about something on GitLab
  (an issue, a merge request, why a pipeline failed) and you were given an account of a GitLab host.
metadata:
  hypomnema:
    requires:
      integration: gitlab
---

# GitLab through Hypomnema

Read GitLab with the `glab` command (GitLab's own command-line tool). Hypomnema installed `glab` for you and signs it
in on every call: you never see a token, and you never log in. Written for Hypomnema (issue #740) after the `github`
skill; the commands are glab 1.130.0's.

## How it is signed in

- Run `glab` as you would anywhere. A small launcher runs the real tool and sends its requests through the control
  plane, which adds the token of the GitLab account the owner gave you. The tool itself holds a placeholder.
- Each account is on one host, `gitlab.com` or a company's server such as `git.example.com`, and glab acts on that
  host: name a project as `GROUP/PROJECT` (or `GROUP/SUBGROUP/PROJECT`) with `-R`. With accounts on several hosts,
  name one on each call: `glab --account git.example.com:default mr list -R GROUP/PROJECT`.
- **Never run `glab auth login`, `glab auth logout`, `glab config set`**, and never set `GITLAB_TOKEN`,
  `GITLAB_HOST` or `GITLAB_ACCESS_TOKEN` yourself. If a command says you were given no account, say so: only the
  owner connects one (`hypomnema integrations connect <host>`) and allows it for chats.
- Only the host's API is reached: `git clone` and `glab repo clone` do not work here. Read files with
  `glab api` instead. Giving coding work to a worker, or cloning a large repository, is the
  `hypomnema-coordinator` skill's.

## Reading

```bash
glab issue view 42 -R GROUP/PROJECT --comments
glab issue list -R GROUP/PROJECT --per-page 20 --output json
glab mr view 55 -R GROUP/PROJECT --comments
glab mr list -R GROUP/PROJECT --per-page 20 --output json
glab mr diff 55 -R GROUP/PROJECT --raw
glab ci list -R GROUP/PROJECT --per-page 10
glab ci get -R GROUP/PROJECT --merge-request 55 --with-job-details --output json
glab ci trace <job-id> -R GROUP/PROJECT
glab api "projects/GROUP%2FPROJECT/repository/files/README.md/raw?ref=main"
```

- A merge request's or an issue's URL works in place of its number.
- Prefer `--output json` and cut it down with `jq`; cap lists with `--per-page`. Long logs: read the failed job's
  last lines, not the whole log.
- `glab api` reaches any REST endpoint under `/api/v4/`; a project in its path is its full path with each `/`
  written `%2F`. `glab api graphql -f query='…'` sends a GraphQL query. `hypomnema tool run gitlab --account
  <host>:<label> -- curl https://<host>/api/v4/…` runs another command through the same account.

## Writing waits for the owner

- Reading runs. Anything that changes or deletes (opening an issue or a merge request, a note, approving, merging,
  turning auto-merge on or off, a label, closing, retrying or running a pipeline, writing a file or a branch) waits
  for the owner's approval unless he allowed it: the command answers that it waits and was not run. Say so in one
  line and end your turn; run the same command again, unchanged, once he approves.
- Merging and auto-merge are two answers of their own: `glab mr merge` asks for auto-merge when the merge request
  has a pipeline, and merges at once when it has none. Name which one you mean, and merge only when the owner asked.
- Write only what the owner asked for in this chat. Pass a long body with `--description` from a file's content,
  never pasted into the path.
- A `403` or `404 Project Not Found` means the account's token does not reach that project or action: report it, do
  not look for another way.
