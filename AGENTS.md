# Hypomnema repository guide

## Where to look

- What exists today: [ARCHITECTURE.md](ARCHITECTURE.md). Facts only, updated in the change that makes them true.
- Goal and target architecture: [docs/vision.md](docs/vision.md). Intent, not fact.
- One meaning per term: [docs/glossary.md](docs/glossary.md).
- Engineering rules: [docs/conventions.md](docs/conventions.md).
- What was studied, with sources: [docs/research/](docs/research/README.md). Reference clones live in `.tmp/` (git-ignored).
- Why a decision was made: [docs/decisions/](docs/decisions/README.md).
- Work and progress: GitHub issues and milestones on `kl3inIT/hypomnema`. There is no roadmap file.

## Talking to the owner

- Answer in Vietnamese, short, in plain words. Lead with the answer. No walls of options.
- Never use a bare internal identifier: say what it is ("issue #3, the repository skeleton").
- Give one recommendation with its reason, not a survey. Ask one question at a time.
- Separate what was read in code or docs from what is inferred; mark the second as such.

## Act or ask

- **Ask first** before: writing code for a design that has not been approved; anything outward-facing, shared or
  hard to undo (pushing code to `main`, changing a server, creating accounts or repositories, deleting, spending money);
  choosing between designs; widening the scope of the issue.
- **Act without asking** on: anything the owner approved in this conversation, and its obvious follow-ups (tests,
  fixing CI, updating docs, the PR); reading, searching and cloning references into `.tmp/`.
- When in doubt about scope, do the smaller thing and say what was left out.

## Working rules

- Research before design: study mature open-source projects and the owner's own repositories from primary sources
  (code, official docs), compare, recommend one, then wait ([reference-based design](docs/conventions.md#reference-based-design-and-scope-control)).
- Before proposing a default or a rule (security posture, tool access, spawn policy, isolation, layout), read how the
  references do it and start from their common default; show the comparison with paths and depart only with a stated
  reason. Mark what no reference does as inference.
- When comparing options, the amount of code to write is not a cost: agents write it. Weigh complexity instead:
  moving parts, failure modes, state that must stay consistent, what has to be operated, and how hard the result is
  to understand and change. "Less code" alone never justifies departing from what the references do (owner,
  2026-11-08).
- An HTML prototype is only a prototype: it shows a direction to choose, drawn before the references' code is
  read closely. What is built follows the references' components and behaviour, read in their code; a component
  whose license allows it is copied or adapted, with its notice, rather than written again from the prototype.
  Where the prototype and the references differ, the references win (owner, 2026-11-10).
- Before a spike or prototype, search the cloned references for the same mechanism. If a reference already does it,
  read its code as the specification and test only what is specific to this environment (accounts, versions,
  servers).
- Changing an earlier decision or name is normal as understanding grows. Make the change everywhere it is stated, in
  one change, with its reason: a new ADR that supersedes the old one for a decision, a "called X until <date>"
  note in the glossary for a term. Dated research notes stay as written.
- Every code change has a GitHub issue. Its body holds the design and plan for small changes. A multi-day change or
  a new module also gets a design section approved in the issue before code.
- Code: branch `<issue>-<slug>`; commit subjects `type(scope): summary (#<issue>)`; one pull request per issue. The
  agent watches CI, fixes red runs at the cause, and merges when green and approved.
- Docs-only changes (`docs/`, `AGENTS.md`, `README`s) are committed and pushed straight to `main`, subject
  `docs: summary`: no issue, branch or pull request (owner, 2026-11-07). If the checkout is on another session's
  branch, commit from a temporary worktree on `origin/main`. A design change that affects an open issue also gets a
  comment on that issue.
- Run `make verify` before pushing once it exists.
- Work in its own `git worktree` when another session may use this checkout; never switch the branch of a checkout
  you did not start.
- Before an action that reaches many things at once (many PRs, branches, machines, messages, or money), count them
  with a read-only query, check each is meant, and give the owner the number first.
- Never write a secret value to Git, documents, issues, logs or command history; record only where it is managed.
- Write repository documents in English and update them in the change that makes them true. One fact has one home;
  link instead of repeating.
- Once a week, or when asked: close stale issues and branches, remove merged worktrees, and fix docs that no longer
  match the code.

## Skills

Vendored skills live in `.claude/skills/` with their sources in `SOURCES.md`; never install two skills for one topic
([conventions](docs/conventions.md#agent-skills)). Use the matching skill when its trigger appears:
`research-then-decide` (how others do it, design choices), `ship-branch` (commit, PR, CI, merge), `test-audit`
(writing or reviewing tests), `code-review`, `chat-reply` (drafting a message to someone else).
