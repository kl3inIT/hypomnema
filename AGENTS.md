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
- "Choosing between designs" above is the owner's choice of a design, not the options an approved issue leaves
  open: a worker decides those itself, by the worker rules in `internal/node/runner/prompt.go` (issue #1069).

## Working rules

- Research before design: study mature open-source projects and the owner's own repositories from primary sources
  (code, official docs), compare, recommend one, then wait ([reference-based design](docs/conventions.md#reference-based-design-and-scope-control)).
- Before proposing a default or a rule (security posture, tool access, spawn policy, isolation, layout), read how the
  references do it and start from their common default; show the comparison with paths and depart only with a stated
  reason. Mark what no reference does as inference.
- Do not invent logic. Every decision, in a design, an issue or a reply to the owner, says in its own column or line
  which reference it is learned from ("From", "Học từ"). A piece of logic that no reference has is left out, not
  kept with a label, unless the owner asked for it; then the line names the owner and the date (owner, 2026-11-10).
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
- Documents are leads, not proof: `ARCHITECTURE.md`, research notes, an issue's text, memory, pasted output.
  Before designing, fixing or filing on what one says, check it against the code on `main` or the running system
  and cite what you saw (a path and line, a command and its output). A fix that cannot point to the line where the
  problem shows rests on an unverified premise; when unsure, say so instead of guessing (OpenClaw and Hermes
  `AGENTS.md`, Coder's "state uncertainty instead of guessing"; owner, 2026-11-15).
- Before starting an issue, check that its premise still holds on `main`. When the work is already there or the
  problem does not reproduce, close the issue with the evidence instead of building it (Hermes closes as
  `implemented_on_main` or `cannot_reproduce`; owner, 2026-11-15).
- A document's line about a running system (a server has a tool, a node runs a version) is evidence of the past.
  Before filing or planning an issue that rests on what is deployed, check the live system read-only and write the
  check (date, version, command) into the issue; fix the document if it was wrong
  ([research](docs/research/2026-11-15-checking-deployed-state.md); owner, 2026-11-15).
- A reference whose code cannot be read (a closed product, a paid component) is studied on its real pages: open
  them in a browser and take pictures of each screen and state, kept in `.tmp/`. Text fetched from a page is not
  the screen (owner, 2026-11-11).
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
- Before pushing, once, after the last edit or merge: `make verify-static` and the unit tests of what the change
  touched (`go test` on the changed packages; for the web client, `pnpm --dir web exec vitest related --run` on the
  changed files, and `pnpm --dir web typecheck` and `lint`). Do not run the whole suite locally: CI's Unit, Race and
  browser jobs are the full proof (t3code `AGENTS.md:102,111`, OpenClaw `AGENTS.md:86`, Orca `AGENTS.md:8`, Coder
  `AGENTS.md:46`; owner, 2026-11-19).
- Do not merge main into a branch only because main advanced. Merge it for a conflict, for a red check whose cause
  is on main, or for a migration. After a merge, rerun only the checks it affects (OpenClaw `AGENTS.md:46`).
- A migration is made with `make migration-create NAME=what_it_does`, which names it by its time, so branches open
  side by side never take the same version (#954; goose `create`, Omnara's `migration-create`). Its pull request
  stays green with that name while open; right before it is merged (for a worker, before turning on auto-merge) it
  merges main, runs `make migration-fix`, which numbers it after main's last (goose `fix`), and `make
  migration-check`, then pushes and merges once green. The `migrations-current` status checks PR heads against
  main and is refreshed for open PRs that add migrations when migrations change on main. If main takes a number
  while the PR is open, merge main and run `make migration-fix` again (#1263). A push to main refuses a migration
  named by its time or one whose number main took meanwhile (#334, #954).
- A merge conflict in generated code (`internal/controlplane/clientapi/openapi/api.gen.go`,
  `web/src/api/generated/*.gen.ts`) is never edited by hand: resolve `api/openapi.yaml` first, take either side of
  the generated files, then run `make openapi-generate`, `make web-api-generate` and `make openapi-check` (#754).
- The checks need Go at the version `go.mod` names (the linters and generators come through `go tool`), and the
  web client's need Node 24 and pnpm (`web/package.json`). On a node, the repository's setup script
  (`deploy/worker/setup.sh`) has installed what was missing for the worker's user, without sudo, and linked it into
  `$HOME/.local/bin`, which the node puts first on the worker's PATH; elsewhere, run that script yourself (it is
  idempotent) and put `$HOME/.local/bin` first on your PATH. The web client's packages and Playwright's Chromium
  come after: `pnpm --dir web install --frozen-lockfile`, then `pnpm --dir web exec playwright install chromium`;
  `pnpm --dir web exec playwright install-deps --dry-run chromium` names the system libraries it lacks, which take
  sudo. Without `make` (it takes sudo too), run the recipes of the Makefile's targets by hand: `make verify` is
  `verify-static` (`fmt-check`, `tidy-check`, `lint`, `deps-check`, `sqlc-check`, `openapi-check`,
  `migration-check`, `goose-version-check`), `unit` and `race`, with `go` for `$(GO)`. What needs sudo, such as gcc for `make race` and
  Chromium's libraries, is left to CI and said in the pull request
  ([research](docs/research/2026-11-12-a-repository-tools-for-workers.md); owner, 2026-11-12; issue #718).
- Work in its own `git worktree` when another session may use this checkout; never switch the branch of a checkout
  you did not start.
- Before an action that reaches many things at once (many PRs, branches, machines, messages, or money), count them
  with a read-only query, check each is meant, and give the owner the number first.
- Never write a secret value to Git, documents, issues, logs or command history; record only where it is managed.
- Write repository documents in English and update them in the change that makes them true. One fact has one home;
  link instead of repeating.
- Once a week, or when asked: close stale issues and branches, remove merged worktrees, and fix docs that no longer
  match the code.

## Rules and what enforces them

A rule that something checks cannot be forgotten; a rule that only this file states can. Each row pairs a rule with
what fails when it is broken. A new rule gets its row; a rule that nothing enforces and that is broken twice gets a
check in the same change, and a rule whose mistake can no longer happen is dropped (pstack
`skills/correct/SKILL.md:15-30`; #954).

| Rule | Enforced by |
|---|---|
| Go files are gofmt-formatted; `go.mod` and `go.sum` of each module are tidy | `make fmt-check`, `make tidy-check` (CI Static) |
| Import boundaries, no wall clock in the orchestration core, the banned packages ([conventions](docs/conventions.md#layout-and-boundaries)) | `make lint`, `.golangci.yml` depguard and forbidigo (CI Static) |
| The node links no control plane code, and the control plane no node code | `make deps-check` (CI Static) |
| Generated code is what its source generates, never edited by hand | `make sqlc-check`, `make openapi-check`, `make web-api-check` (CI Static, Web) |
| A numbered migration on main never changes; no two share a number; a new one comes after main's last | `make migration-check` against main, or on a push the commit before (CI Static); `migrations-current` on PR heads (`migrations-current.yml`) |
| A migration named by its time never reaches main or a release | `make migration-check MIGRATION_TIMESTAMPS=refuse` on a push to main (`ci.yml`); `release.yml` releases only a green commit |
| The goose that makes migration files is the goose the binary migrates with | `make goose-version-check` (CI Static) |
| A pull request merges only with its checks green | the `Required` check on main's branch protection |
| Reference-based design, asking first, a "From" for each decision, the merge steps above, docs in the change | nothing: the reviewer and the owner |

## Skills

Vendored skills (Go, shadcn, prompts) live in `.claude/skills/` with their sources in `SOURCES.md`; never install two
skills for one topic ([conventions](docs/conventions.md#agent-skills)). The owner's own skills are installed for the
owner's user, not in the repository, and may be missing on a node. Use the matching skill when its trigger appears:
`research-then-decide` (how others do it, design choices), `ship-branch` (commit, PR, CI, merge), `test-audit`
(writing or reviewing tests), `code-review`, `chat-reply` (drafting a message to someone else).
