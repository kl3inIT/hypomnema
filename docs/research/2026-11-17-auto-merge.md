# Auto-merge: a worker turns on the host's auto-merge

Studied on 2026-11-17 for issue #714 (the owner, 2026-11-17: "auto merge nếu hết conflict và ci xanh cho các dự án ở
github"). Question: when a worker may have its pull request merged without asking, with which commands, and what keeps
a red or conflicting change out. Also: the merge gate's answer carrying text, a follow-up on a Task blocked at a gate,
and a Bot's merge through the service proxy (issue #713).

## Revisions read

Cloned into `.tmp/` with `--depth 1` on 2026-11-17.

| Reference | Revision | What was read |
|---|---|---|
| t3code (`pingdotgg/t3code`) | `42a5aade8485` | pull-request panel and its logic, the GitHub and GitLab providers |
| GitHub CLI (`cli/cli`) | `0536d5e52884` | `pkg/cmd/pr/merge`, `pkg/cmd/pr/checks` |
| glab (`gitlab-org/cli`) | `206ebab4fc3f` | `internal/commands/mr/merge/mr_merge.go` |
| github-mcp-server | `67d4dc489700` | `pkg/github/pullrequests.go` |
| Renovate | `4fa30be541c8` | GitHub and GitLab platform automerge, branch reuse, `docs/usage/configuration-options.md` |
| Kodiak (`chdsbd/kodiak`) | `0f720a50725d` | `bot/kodiak/evaluation.py` |
| bors-ng | `ca725797e53a` | `lib/worker/batcher/bors_toml.ex`, `message.ex` |
| Dependabot (`dependabot/fetch-metadata`) | `f6f48e4820a3` | `README.md` (GitHub's documented auto-merge workflow) |
| OpenClaw | `7f07edeba9ea` | `scripts/pr-lib/merge.sh`, `.agents/skills/openclaw-pr-maintainer/references/landing.md` |
| Orca (`stablyai/orca`) | `c026da2c5f02` | auto-merge availability, coordinator messages, gate RPC |
| Omnigent | `b1576b38a14e` | `web/src/shell/PullRequestMerge.tsx`, `omnigent/runner/github_resource.py`, `.github/scripts/dependabot_automerge.py` |
| Coder | `61ea77f8f617` | `.agents/skills/pull-requests/SKILL.md`, `.github/workflows/dependabot.yaml` |
| OpenHands | `5ed4ffa5ac25` | `src/utils/suggestions/repo-suggestions.ts` |
| Hermes (`NousResearch/hermes-agent`) | `3e9d42b87608` | searched, nothing on merging |
| Omnara | `42bdc8cc4d0d` | `internal/harness/tools/interaction_tools.go`, `internal/storage/internal/agentexecution/commands_interactions.go` |
| Onyx | `cce02672acb4` | `backend/onyx/external_apps/providers/github.py`, `actions.py`, `matching/engine.py` |

Mergify was not studied: its engine is no longer open source, and its pages were not opened in a browser.

## What Hypomnema does on main 1c2eef1

- The worker's rules put merging under "Ask first" (`internal/node/runner/prompt.go:31`) and suggest a `gate` to
  `finish` "such as whether to merge" (`prompt.go:57-58`).
- The coordinator never merges (`internal/controlplane/clientcli/skills/hypomnema-coordinator/guide.md:167`,
  `references/review.md:20-21`); a review's verdict of correct with green checks is reported to the owner, who merges.
- A question's option can carry text (`internal/shared/nodeproto/nodeproto.go:681-685`, `AllowsText`), and the web form
  shows a text box for such an option (`web/src/features/questions/question-form.tsx:40`). But the worker's `ask` and
  `finish` tools take options as plain strings (`internal/node/workertools/workertools.go:115-117`, `:148`) and turn
  each into an `Option` with a label only (`workertools.go:210-219`). So no worker's gate has ever offered a text
  answer: this is why "Change first" on PR #701 carried no text. Only a test builds such an option
  (`internal/controlplane/orchestration/questions_test.go:44-50`).
- A follow-up is refused unless the Task succeeded (`internal/controlplane/orchestration/followups.go:84-85`). A Task
  that finished with a gate is `blocked` with reason `gate` (`dispatch.go:668-686`) until the owner answers, so the
  coordinator could not send the review's findings to PR #709's worker. The cancel path already closes a Task's open
  gates (`service.go:610`, `CancelOpenGatesOfTask`).
- The service proxy's GitHub catalog has no merge action (`internal/controlplane/serviceproxy/github.go:135-160`, the
  five writes); `gh pr merge` from a Bot is `github.unlisted` and always asks (`catalog.go:70-75`).
- `kl3inIT/hypomnema`: `allow_auto_merge: false`, no branch protection (`gh api repos/kl3inIT/hypomnema/branches/main/protection`
  → 404 "Branch not protected") and no ruleset (`.../rules/branches/main` → `[]`). Its CI already has one job meant
  to be the required check: `Required`, which passes when every job passed or was skipped
  (`.github/workflows/ci.yml:183-195`), and runs on every pull request (PR #709's checks list it).

## How the references merge by themselves

### The host's auto-merge, and the trap of an immediate merge

- **GitHub CLI.** `gh pr merge --auto` enables auto-merge only when the pull request is not already mergeable:
  `autoMerge: opts.AutoMergeEnable && !isImmediatelyMergeable(pr.MergeStateStatus)` (`pkg/cmd/pr/merge/merge.go:593`),
  and `isImmediatelyMergeable` is `CLEAN`, `HAS_HOOKS` **or `UNSTABLE`** (`merge.go:826-832`). `UNSTABLE` means checks
  that are not required are failing or pending. Otherwise it sends GraphQL `mergePullRequest` at once
  (`pkg/cmd/pr/merge/http.go:89-103`). So on a repository with no required checks, `gh pr merge --auto` merges
  immediately, before CI or with CI red. `--match-head-commit <sha>` pins the head (`merge.go:184`); auto-merge itself
  is the `enablePullRequestAutoMerge` mutation (`http.go:89-95`).
- **t3code** does the same on purpose: "A PR that became ready since detail loaded merges directly, matching
  `gh pr merge --auto`" (`packages/source-control-github/src/server/GitHubPullRequestApi.ts:2833-2838`). It offers
  auto-merge only when the repository allows it and the pull request cannot merge now
  (`GitHubPullRequestProvider.ts:85-90`), and never on a draft or a conflicting one
  (`apps/web/src/components/pullRequest/PullRequestDetailPanel.tsx:1303-1312`). A conflict makes the primary control
  "resolve" (`pullRequestDetail.logic.ts:107`). The default method is a merge commit (`GitHubPullRequestApi.ts:2841`).
  On GitLab it runs `glab mr merge --auto-merge=true --yes` (`packages/source-control-gitlab/src/server/GitLabPullRequestCli.ts:1018-1023`).
- **Orca** shows the auto-merge control only when a requirement is unmet (`BLOCKED`, `BEHIND`, a review required),
  never on `UNSTABLE`, a conflict, or a repository that does not allow it
  (`src/shared/github/pull-requests/merge/pull-request-auto-merge-availability.ts:13-34`).
- **Omnigent** names the same states: `BLOCKED` "Required checks or reviews haven't passed", `DIRTY` "Resolve
  conflicts first", and `UNSTABLE` as a state "GitHub still merges" (`web/src/shell/PullRequestMerge.tsx:55-63`). Its
  merge uses `gh pr merge --<method> --match-head-commit <sha>` (`omnigent/runner/github_resource.py:2112-2136`).
- **glab.** `--auto-merge` defaults to true, but it is sent only when the merge request has a pipeline
  (`internal/commands/mr/merge/mr_merge.go:223-230`); without one, glab merges at once and only warns "No pipeline has
  been run" (`mr_merge.go:286-289`). A failed or canceled pipeline stops it (`mr_merge.go:224-227`). `--sha` pins the
  head (`mr_merge.go:91`).
- **Renovate** uses the platform's auto-merge only when the repository allows it (`lib/modules/platform/github/index.ts:1957-1963`)
  and sends `enablePullRequestAutoMerge` directly (`index.ts:1990-2002`), with MERGE as the default method
  (`index.ts:1966-1969`). Its documentation warns: "If you do not select any status check, and you use platform
  automerge, then GitHub might automerge PRs with failing tests!" (`docs/usage/configuration-options.md:265-273`, again
  at `:4278-4283`). On GitLab it waits until the merge request has a pipeline before setting it
  (`lib/modules/platform/gitlab/index.ts:641-700`).
- **Kodiak** refuses to work on a base branch without branch protection or a ruleset that requires checks or reviews:
  "This was to prevent merging pull requests that didn't require any status checks or code review"
  (`bot/kodiak/evaluation.py:513-528`, enforced at `:718-723`).
- **bors-ng** refuses a `bors.toml` that names nothing to gate on: "does not specify anything to gate on"
  (`lib/worker/batcher/bors_toml.ex:157-158`, `message.ex:270-272`).
- **Dependabot.** GitHub's documented way is a workflow step `gh pr merge --auto --merge "$PR_URL"`
  (`dependabot/fetch-metadata` `README.md:167-170`); Coder runs it for Dependabot's pull requests
  (`.github/workflows/dependabot.yaml:49-53`). Omnigent's own Dependabot merger waits for its `Merge Ready` status
  (`.github/scripts/dependabot_automerge.py:223-228`).
- **OpenClaw** is the one agent that lands pull requests itself: `scripts/pr merge-run <pr> --auto-merge` submits "one
  pinned squash/auto-merge", and "GitHub waits for `openclaw/ci-gate` (CI + applicable security review) + required
  reviews" (`.agents/skills/openclaw-pr-maintainer/references/landing.md:73`, `:81`). It merges only on a scoped
  instruction to land: "A readiness question alone is not land authority" (`landing.md:107`). The agent stays until
  the merge is verified (`landing.md:82`). Its script prints "No required checks configured for this PR"
  (`scripts/pr-lib/merge.sh:390-393`).

All of them leave the decision to merge to the host's rules: the checks the base branch requires. Where those rules
are missing, Renovate warns, Kodiak and bors refuse, and gh and glab merge at once.

### Conflicts

- t3code turns a conflict into an agent's task: bring the branch "up to date with `<base>` using this repository's
  convention, resolve every conflict while preserving the intent of both sides, and verify the project still builds
  before pushing" (`apps/web/src/components/pullRequest/pullRequestDetail.logic.ts:1172-1189`).
- OpenClaw repairs a conflict and re-runs the affected checks: "Older run ≠ current-head CI" (`landing.md:20`); a push
  by a collaborator keeps auto-merge armed (`landing.md:84`).
- Renovate rebases its own branch when it is conflicted and unmodified, and only warns when someone else changed it
  (`lib/workers/repository/update/branch/reuse.ts:163-180`).
- Kodiak blocks, removes its label and comments "This PR currently has a merge conflict. Please resolve this and then
  re-add the label" (`bot/kodiak/evaluation.py:941-963`); bors reports "Merge conflict" (`message.ex:30-31`, `:98-99`).

### Waiting for a review

- Kodiak and GitHub's auto-merge wait for required reviews set in branch protection (`evaluation.py:513-528`; OpenClaw
  `landing.md:81`, "+ required reviews"). Hypomnema's reviewer works with the owner's own GitHub account, which cannot
  approve its own pull request, so a required approval is not available here (inference from GitHub's rule that an
  author cannot approve their own pull request; not tested).
- OpenClaw orders it: review artifacts first, then `prepare-run`, then `merge-run --auto-merge` (`landing.md:64-74`).
- Hypomnema's coordinator runs a review as a Task after the author's Task succeeded and reports "a verdict of correct,
  with green checks" (`references/review.md:20-21`), and sends fixes to the author as a follow-up (`review.md:22-29`).

### Asking instead

- Coder's agent skill: "Never auto-merge. Do not merge or mark ready for review unless the user explicitly asks"
  (`.agents/skills/pull-requests/SKILL.md:55-56`).
- github-mcp-server has `merge_pull_request`, a write (`ReadOnlyHint: false`), merging at once with an optional
  `expectedHeadSha` (`pkg/github/pullrequests.go:1670-1766`); it has no auto-merge tool (searched `pkg/` for
  `auto_merge`, `automerge`, `enablePullRequestAutoMerge`: none).
- Onyx's GitHub catalog has no merge action (`backend/onyx/external_apps/providers/github.py:27-44`); a write defaults
  to ASK (`actions.py:118-122`), and a request no action matches is "Perform action", ASK
  (`external_apps/matching/engine.py:150-158`).
- OpenHands has only a canned prompt asking the agent to add a GitHub Action that merges Dependabot's pull requests
  "so long as the tests are passing" (`src/utils/suggestions/repo-suggestions.ts:12-13`).
- Hermes: nothing on merging (searched for `pr merge`, `merge_pull`, `automerge`, `auto-merge`; only docs listing merged
  pull requests).

Correction to issue #714's table: Orca (the availability rule above, and a `merge_ready` message its coordinator
ignores, `src/main/runtime/orchestration/coordinator/coordinator.ts:186-190`), OpenClaw (its landing flow) and
Omnigent (a merge button, Dependabot auto-merge) do have code on merging. Coder and OpenHands have only Dependabot
workflows or prompts; Hermes has nothing.

### A gate's answer with text, and new input while a question is open

- Omnara's `ask_question` adds an "Other" option with `AllowsText` to every question an agent asks
  (`internal/harness/tools/interaction_tools.go:66-72`), so the agent never has to declare it.
- Orca's gate is resolved with a free-text `resolution` (`src/shared/rpc-contract/orchestration/orchestration-gates-params.ts:24`,
  `src/main/runtime/rpc/methods/orchestration/gates/gates.ts:113`).
- Omnara cancels the open questions of a turn when new steering input arrives, reason `superseded_by_input`
  (`internal/storage/internal/agentexecution/commands_interactions.go:340-362`).

### A Bot's merge as an action

- t3code lists `merge`, `enable-auto-merge` and `disable-auto-merge` as separate actions of a pull request
  (`packages/source-control-github/src/shared/definition.ts:113-125`).
- gh sends GraphQL `mergePullRequest` for a merge and `enablePullRequestAutoMerge` for auto-merge, and
  `disablePullRequestAutoMerge` to turn it off (`pkg/cmd/pr/merge/http.go:89-118`); github-mcp-server merges with
  REST `PUT /repos/{owner}/{repo}/pulls/{number}/merge` (`pullrequests.go:1766`, go-github's `PullRequests.Merge`).
- Onyx and Hypomnema start a write at "ask" (above).

## The owner's GitHub repositories

Counted read-only on 2026-11-17 with `gh repo list kl3inIT --no-archived --limit 200`, then for each repository
`gh api repos/<r>` (`allow_auto_merge`), `gh api repos/<r>/contents/.github/workflows` (workflow files),
`gh api repos/<r>/commits/<default branch>` and its `check-runs` and `status` (checks on the default branch's head),
`gh api repos/<r>/branches/<b>/protection` (404 "Branch not protected" = none) and `gh api repos/<r>/rules/branches/<b>`
(rulesets). The account is on the Pro plan (`gh api user --jq .plan.name` → `pro`), so rulesets and auto-merge are
available on private repositories too. No setting was changed.

- **53** repositories are not archived (issue #714 said 50); 5 are forks.
- **20** have workflow files; 3 more (`vnr_spst`, `shoes-shopping-online-system-fe`, `my-portfolio`) have only a commit
  status on the default branch (a deployment status, not CI, inferred from no workflow). 9router (a fork) has
  workflows but no check run on its head.
- **None** has classic branch protection on its default branch.
- **1** requires a check: BeyondPilot, through its active ruleset `main` (required check `CI result`). MemoryOS has a
  ruleset without a required check (deletion, non-fast-forward, Copilot review).
- **0** has `allow_auto_merge` on.
- **Ready for auto-merge today: none.** BeyondPilot needs only `allow_auto_merge`. hypomnema needs a ruleset requiring
  `Required` and `allow_auto_merge`. Every other repository with CI needs both a required check and the setting.


## Recommendation

The design is in issue #714's `## Design` comment; in short:

1. **The worker arms the host's auto-merge instead of asking, only where the host will wait.** GitHub: the base
   branch requires at least one status check (branch protection or a ruleset) and the repository has
   `allow_auto_merge`; then `gh pr merge <url> --auto --merge --match-head-commit <sha>`. GitLab: the project has
   "Pipelines must succeed" (the project field `only_allow_merge_if_pipeline_succeeds`, seen in glab's `internal/commands/ci/get/testdata/ci_get.result`) and the merge request has a pipeline; then
   `glab mr merge <iid> --auto-merge --yes --sha <sha>`. From Renovate (allowed setting, its warning), Kodiak and
   bors (refuse without required checks), gh and glab's code (they merge at once otherwise), t3code and Orca (never on a
   draft or a conflict).
2. **Otherwise the worker leaves the pull request open and says so**, with what the repository lacks; it never merges
   directly. From Kodiak (`evaluation.py:718-723`), bors (`bors_toml.ex:157-158`), Coder (`SKILL.md:55-56`).
3. **A conflict is the worker's to fix** before arming, by merging the base into its branch and re-running the checks;
   a conflict it cannot resolve without guessing the intent is a question. From t3code and OpenClaw.
4. **The worker waits for the required checks, fixes red ones, and reports whether the pull request merged**, then
   finishes; it does not stay to watch other pull requests. From OpenClaw (`landing.md:82`) and Hypomnema's own rule
   that the agent watches CI and fixes red runs (`AGENTS.md`).
5. **A review, when asked, comes before auto-merge**: the spec says so, the author does not arm, and after a verdict of
   correct the coordinator sends a follow-up asking the author to arm it. The coordinator still never merges. From
   OpenClaw's order (review, prepare, merge) and the coordinator's review reference.
6. **Every option of a worker's question and gate can carry text**: the tools add an "Other" option with text, as
   Omnara does; the merge gate's "change first" carries what to change.
7. **A follow-up to a Task blocked at a gate supersedes the gate**: the gate closes as superseded and the Task
   continues with the note, as Omnara cancels open questions on new input.
8. **A Bot's merge is two catalog actions**, `github.pulls.merge` and `github.pulls.automerge` (the names of #713's design; GitLab: `gitlab.merge_requests.merge` and `.automerge`), both "needs care"
   (ask by default) until the owner allows them; they belong to issue #713's catalog work.

Not found in any reference: a check that the base branch's required checks include the repository's CI job by name
(OpenClaw names its own `openclaw/ci-gate`; nobody else checks which check is required). Left out.

## Repositories (2026-11-17)

Columns: workflow files in `.github/workflows` on the default branch; check runs / commit statuses on the default
branch's head commit; classic branch protection; required status checks from rulesets
(`/rules/branches/<b>`); `allow_auto_merge`.

| Repository | Private | Fork | Last push | Workflow files | Check runs / statuses on the default branch's head | Branch protection | Ruleset required checks | allow_auto_merge |
|---|---|---|---|---|---|---|---|---|
| hypomnema (`main`) | no | no | 2026-11-17 | 2 | 5/0 | none (404) | none | false |
| BeyondPilot (`main`) | yes | no | 2026-11-17 | 3 | 9/0 | none (404) | `CI result` (ruleset `main`, active; also a pull request rule with 0 approvals) | false |
| MemoryOS (`main`) | no | no | 2026-11-16 | 4 | 23/0 | none (404) | none (a ruleset with deletion, non-fast-forward and Copilot review only) | false |
| second-bite (`main`) | no | no | 2026-11-17 | 9 | 45/0 | none (404) | none | false |
| elitex-web (`master`) | yes | no | 2026-11-02 | 0 | 0/0 | none (404) | none | false |
| assistant-ui (`main`) | no | yes | 2026-10-28 | 27 | 0/0 | none (404) | none | false |
| northstar (`main`) | no | no | 2026-11-05 | 4 | 101/0 | none (404) | none | false |
| visual-paradigm-mcp (`main`) | yes | no | 2026-10-08 | 0 | 0/0 | none (404) | none | false |
| 9router (`master`) | no | yes | 2026-10-14 | 3 | 0/0 | none (404) | none | false |
| zero-mail (`main`) | no | no | 2026-11-16 | 8 | 276/0 | none (404) | none | false |
| petclinic-ms (`main`) | no | no | 2026-09-21 | 0 | 0/0 | none (404) | none | false |
| vnr_spst (`master`) | no | yes | 2026-08-20 | 0 | 0/1 | none (404) | none | false |
| OrgMemory (`main`) | no | no | 2026-10-25 | 9 | 155/0 | none (404) | none | false |
| kdlstc (`main`) | no | no | 2026-08-24 | 1 | 0/0 | none (404) | none | false |
| dth-crm (`main`) | no | no | 2026-08-10 | 3 | 29/0 | none (404) | none | false |
| myfschoolse1913 (`main`) | no | no | 2026-10-05 | 1 | 74/0 | none (404) | none | false |
| ai-agent-core (`main`) | no | no | 2026-08-31 | 3 | 34/0 | none (404) | none | false |
| orgmemory-edge (`main`) | no | no | 2026-07-18 | 1 | 2/0 | none (404) | none | false |
| ai-agent-core-v2 (`main`) | no | no | 2026-07-09 | 0 | 0/0 | none (404) | none | false |
| kl3inIT (`main`) | no | no | 2026-06-23 | 0 | 0/0 | none (404) | none | false |
| spring-ai-elements-chat (`main`) | no | no | 2026-06-22 | 0 | 0/0 | none (404) | none | false |
| jhipster-sec (`main`) | no | no | 2026-05-02 | 4 | 4/0 | none (404) | none | false |
| traffic-law-chatbot (`main`) | no | no | 2026-04-18 | 0 | 0/0 | none (404) | none | false |
| sandulieu (`main`) | no | no | 2026-04-12 | 0 | 0/0 | none (404) | none | false |
| react-zustand-form (`main`) | no | no | 2026-04-06 | 0 | 0/0 | none (404) | none | false |
| angapp-role-security (`master`) | no | no | 2026-03-20 | 0 | 0/0 | none (404) | none | false |
| jhipster-angular-sakai (`master`) | no | no | 2026-03-16 | 0 | 0/0 | none (404) | none | false |
| shoes-shopping-online-system-be (`main`) | no | no | 2026-03-16 | 1 | 1/0 | none (404) | none | false |
| shoes-shopping-online-system-fe (`main`) | no | no | 2026-03-16 | 0 | 0/1 | none (404) | none | false |
| inbox-zero (`main`) | no | yes | 2026-03-15 | 9 | 381/0 | none (404) | none | false |
| jmix-k8s (`main`) | no | no | 2026-03-13 | 0 | 0/0 | none (404) | none | false |
| airbyte-etl (`main`) | yes | no | 2026-02-11 | 1 | 2/0 | none (404) | none | false |
| jmix-react (`main`) | no | no | 2026-01-17 | 0 | 0/0 | none (404) | none | false |
| react-route-v7 (`main`) | no | no | 2026-01-09 | 0 | 0/0 | none (404) | none | false |
| spring-microservice-gradle-demo (`main`) | no | no | 2026-01-08 | 0 | 0/0 | none (404) | none | false |
| spring-petclinic-microservices (`main`) | no | yes | 2025-12-23 | 1 | 0/0 | none (404) | none | false |
| my-portfolio (`main`) | no | no | 2025-12-17 | 0 | 0/1 | none (404) | none | false |
| fast-api-demo (`main`) | no | no | 2025-10-18 | 0 | 0/0 | none (404) | none | false |
| airbyte-iframe (`main`) | no | no | 2025-09-19 | 0 | 0/0 | none (404) | none | false |
| airbyte-webapp (`main`) | no | no | 2025-09-18 | 0 | 0/0 | none (404) | none | false |
| airbyte-demo-new (`main`) | no | no | 2025-09-09 | 0 | 0/0 | none (404) | none | false |
| airbyte-demo (`main`) | no | no | 2025-08-28 | 0 | 0/0 | none (404) | none | false |
| jmix-example (`main`) | no | no | 2025-08-14 | 0 | 0/0 | none (404) | none | false |
| jmix-map-demo (`main`) | no | no | 2025-08-08 | 0 | 0/0 | none (404) | none | false |
| course-hub-frontend (`main`) | no | no | 2025-07-26 | 0 | 0/0 | none (404) | none | false |
| coursehub-backend (`main`) | no | no | 2025-07-25 | 1 | 0/0 | none (404) | none | false |
| House-Rental-System (`main`) | no | no | 2025-07-20 | 0 | 0/0 | none (404) | none | false |
| my-cv (`main`) | no | no | 2025-06-24 | 0 | 0/0 | none (404) | none | false |
| spring-boot-practical (`main`) | no | no | 2025-05-15 | 0 | 0/0 | none (404) | none | false |
| Golden-Finger (`main`) | no | no | 2025-03-30 | 0 | 0/0 | none (404) | none | false |
| LAB211 (`main`) | no | no | 2025-01-16 | 0 | 0/0 | none (404) | none | false |
| CourseManagementSystem (`main`) | no | no | 2024-10-07 | 0 | 0/0 | none (404) | none | false |
| math-util-live (`main`) | no | no | 2024-10-02 | 0 | 0/0 | none (404) | none | false |