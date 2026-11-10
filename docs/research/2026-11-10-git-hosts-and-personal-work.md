# More than one git host, and work that is not code (2026-11-10)

Two questions the owner asked after the first real Task ran: does Hypomnema depend on GitHub, and what of a Task
that has nothing to do with git, such as checking mail. On the same day the owner widened the goal: Hypomnema also
serves the owner's personal work ([vision](../vision.md)).

Revisions read: Orca `4c13ae46`, t3code `b8c2cfad`, Coder `b798061`, Omnigent `cf1021e`, OpenHands `91c2e38` and
its SDK `e4160d1`, Omnara `0ef1711`, OpenClaw `d622a2ba4`, Hermes `022a1443`, OpenMausBot `1de1164`, Companion
`c950f6f`, Buzz `4e28176`. Two agents read the clones; the lines quoted in this note were checked against the files
afterwards, the rest is as the agents reported it. OpenMausBot's own model is in
[its note](2026-11-09-openmausbot-and-personal-agent-vms.md).

## Hypomnema today

- GitHub is assumed in four places: the clone URL (`internal/node/worktree/worktree.go`, `GitHubURL`), the
  worker's instruction to run `gh pr create` (`internal/node/claude/prompt.go`), a Run's `repo` as `owner/name`
  with no host, and the web's link for a pull request given as `owner/repo#N`.
- The orchestration core does not know the host: a result's evidence is a kind and a ref.
- A Run without a repository is already allowed: the node makes an empty directory and the worker's first turn
  says only "Work only in this directory" (`prompt.go`). No such Task has been run yet.
- A worker has no way to reach mail or a calendar: no connected accounts, no secrets per Dispatch (issues #21,
  #66), and no schedule that starts a Task.

## 1. More than one git host

| Project | Hosts | How a host is known | Reading a pull request's state | The worktree after a merge | Credentials |
|---|---|---|---|---|---|
| Orca | GitHub, GitLab, Bitbucket, Azure DevOps, Gitea (`src/shared/source-control/hosted-review/hosted-review-contracts.ts`) | Probed from the repository's remote; no host is stored | A `ForgeProvider` each (`getReviewForBranch`, `getReviewByNumber`, `createReview`; `src/main/source-control/forge-provider.ts`), through `gh`, `glab` or REST; polled each 60 s, 15 min when there is none (`hosted-review-refresh-pacing.ts`) | A cleanup dialog the user opens, filtered by review state and idle days | The machine's own `gh` and `glab` logins |
| t3code | GitHub, GitLab, Forgejo, Bitbucket, Azure DevOps, more as packages | A kind and a base URL, probed from the remote (`packages/contracts/src/sourceControl.ts`) | A `SourceControlProvider` each; a sync each minute | A setting, off by default (`worktreeOnMerge`), that removes a merged, clean worktree; age rules, off by default (`packages/contracts/src/settings.ts`) | A stored token, `GH_TOKEN`, or the host's CLI |
| Coder | GitHub and GitLab for pull request state | A provider record an admin configures, with its API URL | A `gitprovider.Provider` each; a server worker re-reads a row after 120 s | None | OAuth per user |
| Omnigent | GitHub, GitLab, Azure DevOps, more as Python plugins (`omnigent/git_providers/__init__.py`) | Parsed from the remote and from pull request URLs; self-hosted hosts from the environment | A facet per host through `gh`, `glab`, `az`; an open pull request is re-read when older than 5 min | Removed only when the session is deleted | The host's CLI login |
| OpenHands | GitHub, GitLab, Bitbucket, Azure DevOps, Forgejo | An enum beside `owner/repo` | Not read; the number is kept | None | A token per host as a sandbox secret |
| Omnara | GitHub | An integration record | A webhook for "opened" only | None | Short-lived tokens of a GitHub App |

What they share: the host is found from the git remote, not typed in; each host is one small adapter, or the CLI
the user already logged in to; a pull request's state is `open`, `closed`, `merged` or `draft` with checks and the
review decision (Orca `HostedReviewState`, t3code, Coder), read by polling each 1 to 5 minutes, not by webhooks; and
no project removes a worktree on its own by default. None tells the agent which command to use in its prompt: Orca,
t3code and Omnigent watch what the agent ran or printed and record the pull request from that.

## 2. Work that is not code

| Project | For | Where an agent works with no repository | The result | Mail, calendar, browser | Work on a schedule |
|---|---|---|---|---|---|
| OpenClaw | "A personal agent first" (`docs/concepts/main-session.md`); coding is one group of sessions | The agent's one kept workspace, which also holds its memory; or an empty directory per session | A message to a channel; a sub-agent announces to whoever asked | Skills that wrap CLIs (Gmail, IMAP); a browser profile of the agent's own; tools allowed and denied per agent (the mail reader is denied files and the shell, `docs/automation/imap.md`) | Cron jobs in the gateway's database; triggers from webhooks, Gmail and IMAP |
| Hermes | Both | The home directory; a kanban task gets a scratch directory deleted when it completes, its declared artifacts kept (`website/docs/user-guide/features/kanban.md`) | A message with files attached; a task's summary and artifacts | MCP servers; a credential vault the model never reads | A jobs file; cron fired by webhooks |
| OpenMausBot | Personal and team bots | A kept folder per bot, its memory too | Cards and notifications | Connected apps (Composio), granted per bot | Routines with checks that run before the model |
| Companion | A general collaborator | One kept workspace per companion | Task results and an outbox | A plugin catalog with OAuth, chosen per companion | Cron routines and signed webhooks |
| Omnigent | Coding first | The host's home directory, "which is what makes chat-only, research, and MCP-only automations possible" (`docs/AUTOMATIONS.md`), or a fresh sandbox | The session's messages; artifacts | MCP tools in the agent's file; policies cap tools and spend | Scheduled tasks in the database |
| Omnara | Any agent | A sandbox, released after | Events and artifacts | Only the tools in the agent's config; MCP asks each time by default | Cron triggers |
| Buzz | Developer teams | A kept home per agent; a scratch folder | A message; an outbox | MCP | Workflows on a schedule, a webhook or a message |

What they share: personal work is the same kind of session or task as coding, with another directory and other
tools (only Hermes's kanban is apart); with no repository the agent works in a kept directory of its own, and
scratch that is deleted is the exception; the result is a message with files, and none has a structured result like
a pull request; access to mail and the like is granted per agent, as a skill, an MCP server or a connected app,
with approval where the project has it; and a schedule that starts work lives on the server.

## What follows for Hypomnema

Not decided. Each needs its own design, approved by the owner in its issue.

- A Task stays one kind. With no repository it ends in a report and files instead of a pull request.
- The git host becomes part of a Run's repository, found from its URL, with one adapter a host for the clone URL,
  the command that opens the request, its link and its state; GitHub is the first.
- A pull request's state is polled, as the references do. Removing a worktree on a merge is something only t3code
  does, and only when asked to.

Inferred, with no reference: what a kept directory for the owner's personal Tasks should be on a node that many
Tasks share, and when a Task with no repository is cleaned up.
