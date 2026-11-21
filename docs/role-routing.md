# Worker role routing

The [default mapping](../internal/controlplane/orchestration/role_routing.yaml) selects ordered runtime and model
entries for a worker role and Task kind. `hypomnema profiles routing` prints the effective YAML. The owner can
replace it with `hypomnema profiles routing-apply <file>`: every entry is validated against the provider's model
catalog before the whole mapping is stored. The built-in mapping can refer to models that a server has not yet
discovered; an unavailable model is reported on the Task, not silently substituted.

While trying omp, Sonnet leads the `quick` implementer and explorer chains. For the owner's trial, run three to
five explicitly pinned omp/DeepSeek Tasks with these roles before moving it to the head. If the provider no longer
offers a model, or no worker advertises the runtime, leave that entry unavailable and report the reason. The
model catalog, not the mapping, says which models a provider can serve.
The current OpenCode Go adapter cools accounts across all its models, so both of its entries wait when the
shared account pool cools.

`hypomnema profiles metrics --json` reports two weeks of settled attempts, successes, failures and priced cost by
role, kind and chain position (with model and runtime, so a changed order does not conflate different models).
Use `--since` for a different period. Costs use the existing Dispatch usage ledger (#856), at API prices; entries
with unpriced usage are counted separately. A Dispatch without a chain entry ran with a legacy profile.

## Owner-confirmed cutover

No migration modifies the existing worker profiles, Bots or stored memories. Legacy profiles with a runtime
continue to use that configuration. In a separate owner-approved step, check active Tasks and the model catalog,
export and reconcile the old profiles' memories into the corresponding roles, and apply the four YAML files in
[`deploy/profiles`](../deploy/profiles). These new versions omit runtime, model and profile fallbacks. The role's
memory is shared across its chain; previously started Dispatches keep their original profile versions. Update the
Bots' worker lists before retiring the old profiles. Keep the old profile versions for their historical Tasks.

The Bot model changes also belong to that step. Use the existing `hypomnema bots set <bot> --provider <provider>
--model <model> --fallbacks <provider/model,...>` from #877, not the worker map:

| Bot | Primary | Ordered fallbacks |
| --- | --- | --- |
| Coordinator, VMAP, BeyondPilot | `claude-subscription/claude-opus-5-5` | `codex-subscription/gpt-6.2-sol`, `opencode-go/deepseek-v4.1-flash` |
| Both Đóng góp VMAP Bots | `codex-subscription/gpt-6.1-terra` | `claude-subscription/claude-sonnet-5-5`, `opencode-go/deepseek-v4.1-flash` |

The control plane names a chat from the first owner's message or file, without a model
(`internal/controlplane/orchestration/chats.go`, `titleOf`). The Bot uses its session model for compaction,
not a separate utility model: `internal/controlplane/coordinator/usage.go` sends pi's `compact` command, and pi
1.5.2 takes `this.model` in `packages/coding-agent/src/core/agent-session.ts:2923-2936,2987-2994`.

## Prompt sources

Source comments in the profile samples, and the table below for the coordinator's skill, mark the rules adapted
from omp, Codex and Omnigent and the behavior learned from oh-my-opencode. The latter is cited for its approach, with original wording here, not redistributed prompt text.

- omp (`can1357/oh-my-pi` at `08cb444`), MIT: [license and copyright](../third_party/omp/LICENSE).
- Codex (`openai/codex` at `daa59eb`), Apache 2.0: OpenAI Codex, Copyright 2025 OpenAI.
- Omnigent (`omnigent-ai/omnigent` at `4cd6dc5`), Apache 2.0: Copyright 2026 Databricks, Inc. This software
  includes software developed at Databricks and its use is subject to the included
  [Apache 2.0 license](../third_party/openmausbot/LICENSE).
- oh-my-opencode (`code-yeongyu/oh-my-opencode` at `be84607`), Sustainable Use License 1.0: references to its
  plan-consultant, plan-reviewer and category rules identify the source of those ideas.
- Cursor's pstack and orchestrate plugins (`cursor/plugins` at `e58dd04`), MIT: pstack Copyright (c) 2026 Lauren
  Tan, orchestrate Copyright (c) 2026 Cursor. The reviewer's proof, evidence level and plan rules (issue #952) and what blocks
  (issue #1051) cite their verifier and review playbooks for the approach, in original wording here.

### Coordinator skill

The coordinator's skill files (`internal/controlplane/clientcli/skills/hypomnema-coordinator/`) are printed into
every coordinator turn that loads them, so they hold no source comments (issue #1051). Each rule's source is here,
keyed by the file, its section and the rule's first words. The reviewer's own rules and their sources are in
[`deploy/profiles/reviewer.yaml`](../deploy/profiles/reviewer.yaml).

| File | Section | Rule | From |
|---|---|---|---|
| `guide.md` | Coordinating Hypomnema workers | "In a Bot chat you coordinate a Run …" | Omnigent examples/polly/config.yaml:174-192; no reference for Hypomnema's Run, Task and branch names (owner, 2026-11-18) |
| `guide.md` | Coordinating Hypomnema workers | "Bots coordinate and do day-to-day work (messages, Lark …" | Omnigent examples/polly/config.yaml:174-192; no reference for the Bot's daily work (owner, 2026-11-18) |
| `guide.md` | Recipe | "A Run has one controller: the Bot of …" | One controller per Run: Orca orchestration-legacy-coordinator-authority.ts:39-91 (a5c191cc); OpenMausBot server/coordinator.ts:63-64 (71ada2b5); the hand-off path for the owner's session is inference the owner accepted (owner, 2026-11-20, issue #969) |
| `guide.md` | Recipe | "The file is `{"tasks": [{"ref": "api", "profile": "implementer" …" | omp packages/coding-agent/src/prompts/tools/task.md:28-57; no reference for Task JSON (owner, 2026-11-18) |
| `guide.md` | Recipe | "A Task whose summary says "auto-merge on at …" | pstack skills/poteto-mode/playbooks/shipping.md:8 ("a requested auto-merge is not a merge"; read the PR back for the result) (issue #1053) |
| `guide.md` | Recipe | "While the owner has yet to decide on …" | Hermes hermes_cli/kanban_db_graph.py:156 (the root wakes when all children complete) and kanban_db_workspace.py:419; Orca src/main/runtime/orchestration/db/worker-terminal/worker-terminal-user-keep.ts:140 (a finished orchestration worker closes by itself); t3code apps/server/src/cli/thread.ts:654 finishRun; owner, 2026-11-19, issue #904 |
| `guide.md` | Choosing a role and kind | "Read `hypomnema profiles list --json` to confirm the …" | omp packages/coding-agent/src/prompts/tools/task.md:28-33; no reference for the profile list (owner, 2026-11-18) |
| `guide.md` | Choosing a role and kind | "`implementer` investigates and edits in one Task. Set …" | omp packages/coding-agent/src/prompts/tools/task.md:31-33,45-57; Omnigent examples/polly/config.yaml:174-180; oh-my-opencode docs/guide/agent-model-matching.md:149-157 |
| `guide.md` | Choosing a role and kind | "`planner` researches and proposes a design without changing …" | oh-my-opencode packages/senpi-task/src/agents/builtin/plan-consultant.ts:10-16,65-82; plan-reviewer.ts:120-151 |
| `guide.md` | Choosing a role and kind | "`explorer` answers standalone questions when the relevant files …" | omp packages/coding-agent/src/prompts/tools/task.md:31-33; prompts/agents/scout.md:38-47 |
| `guide.md` | Choosing a role and kind | "`reviewer` checks from another vendor. `quick` needs green …" | Omnigent examples/polly/config.yaml:182-199; no reference for the quick exception (owner, 2026-11-18) |
| `guide.md` | Choosing a role and kind | "For an important pull request (it adds a …" | No reference for CodeRabbit handling (owner, 2026-11-18) |
| `guide.md` | Choosing a role and kind | "Never name a `runtime` or `model` unless the …" | omp packages/coding-agent/src/prompts/tools/task.md:60-78; no reference for same-worktree handoff (owner, 2026-11-18) |
| `guide.md` | Task specs | "Never put an explorer Task before a planner …" | omp packages/coding-agent/src/prompts/tools/task.md:33-46; no reference for the approved-plan copy (owner, 2026-11-18) |
| `guide.md` | Task specs | "Work that builds on an unmerged Task's changes …" | t3code docs/orchestration-v2/orchestrator-mcp-server.md:354-355 and orchestrationInstructions.ts:10,14 (4d5bf2d5); Orca create-github-pull-request.ts:70-110 (issue #690) |
| `guide.md` | Task specs | "For a bug fix, ask for a test …" | pstack skills/poteto-mode/playbooks/bug-fix.md:11; skills/tdd/SKILL.md:9 (issue #952) |
| `guide.md` | Task specs | "Make each spec self-contained: the goal, decisions already …" | omp packages/coding-agent/src/prompts/tools/task.md:38-46 |
| `guide.md` | Waiting | "On a review wake, read the Run again …" | OpenMausBot server/runtime/envelope.ts:184,255 (71ada2b5; the requester checks the report and sends rework itself); Orca src/main/bots/bot-pr-events.ts:1-13 (a5c191cc; nothing wakes a worker's author on its PR); owner, 2026-11-20, issues #967 and #969 |
| `guide.md` | Waiting | "In a session with the owner (Claude Code, Codex), after …" | One controller per Run: Orca src/main/runtime/rpc/orchestration/compatibility/orchestration-legacy-coordinator-authority.ts:39-91 (a5c191cc; only the bound principal writes, another caller is read-only); OpenMausBot server/coordinator.ts:63-64 (71ada2b5; the requester is woken). Applying it to the owner's CLI session is inference the owner accepted (owner, 2026-11-20, issue #969). |
| `guide.md` | Waiting | "A chat with an open Run cannot be …" | Orca orchestration-legacy-coordinator-authority.ts:63-91 (a5c191cc; the stored binding holds); OpenHands SDK openhands-agent-server/openhands/agent_server/conversation_lease.py:167-209 (fb565cfe; a live owner is not displaced); issue #969 |
| `guide.md` | Questions | "A worker asks you when it cannot go …" | Orca mobile/src/session/native-chat/input/prompts/use-mobile-worker-approvals.ts:60 (a5c191cc; a coordinator chat shows its workers' open prompts); OpenMausBot server/coordinator.ts:63-64 (71ada2b5); issue #969 |
| `guide.md` | Results | "For a succeeded planner, send the plan to …" | Omnigent examples/polly/config.yaml:185-192; oh-my-opencode packages/senpi-task/src/agents/builtin/plan-reviewer.ts:120-151; no reference for withholding the plan before owner approval (owner, 2026-11-18) |
| `guide.md` | Results | "A worker that turned auto-merge on finishes …" | pstack skills/poteto-mode/playbooks/shipping.md:8-9 (arm `--auto` on a verified head; a watcher follows the run until it reads merged); scripts/watch-pr/policy.ts:59-140 (merged, closed, a conflict, failed checks); babysit.md:8 (conflicts before CI); the follow-up path of orchestration/followups.go (issue #1053) |
| `guide.md` | Results | "When the follow-up is refused (the worker's conversation …" | Orca pr-start-point.ts:226-245 (38aa9119); t3code GitManager.ts:1013-1069, 3361-3390 (4d5bf2d5) (issue #690) |
| `review.md` | Review | "`quick` work (for example a README rename) needs …" | Omnigent examples/polly/config.yaml:182-199; no reference for the quick exception (owner, 2026-11-18) |
| `review.md` | Review | "Review a plan before showing it to the …" | oh-my-opencode packages/senpi-task/src/agents/builtin/plan-reviewer.ts:120-151; no reference for approval and copying specs (owner, 2026-11-18) |
| `review.md` | Review | "A red check gets one fresh build before …" | pstack skills/poteto-mode/playbooks/babysit.md:8 ("Flake earns one fresh build. Only the diff's own failures get a commit"); playbooks/autonomous-run.md:9 (flaky verifiers fixed in their own PR) (issue #952) |
| `review.md` | Review | "Set `profile: reviewer`, `kind` to the work's kind …" | Omnigent examples/polly/config.yaml:182-199; no reference for review_of (owner, 2026-11-18) |
| `review.md` | Review | "Start every review spec with `Review pull request …" | pstack skills/poteto-mode/playbooks/shipping.md:7 (a verdict covers a patch); skills/swarm/SKILL.md:37 (a result without its SHAs is dropped) (issue #953) |
| `review.md` | Review | "Do not restate the reviewer's rules in …" | AGENTS.md "One fact has one home"; the rules live in `deploy/profiles/reviewer.yaml` with their sources (issue #1051). What `review.md` stated until then moved there with its sources: the summary's lines and the Execution list (orchestrate skills/orchestrate/prompts/verifier.md:38-61, references/handoffs.md:130-140; pstack skills/poteto-mode/playbooks/shipping.md:7, a verdict covers a patch; issue #952), and the bar and the proof rule (pstack skills/poteto-mode/worker.md:4, the bar is the whole bar; playbooks/autopilot-full.md:16 and skills/interrogate/SKILL.md:19, block only for what a run or a doc line shows; orchestrate skills/orchestrate/prompts/verifier.md:23, "Run the code"; issue #952) |
| `review.md` | Review | "Copy an owner's acceptance condition into the spec …" | pstack skills/poteto-mode/worker.md:4 ("The bar in your kickoff is the whole bar"); Codex codex-rs/prompts/templates/review/rubric.md:14 (criterion 3, no rigor above the rest) (issue #1051) |
| `review.md` | Review | "For red checks, rerun once as in step …" | pstack skills/poteto-mode/playbooks/babysit.md:8 ("Flake earns one fresh build") (issue #952) |
| `review.md` | Review | "Only a finding that blocks by the reviewer …" | pstack skills/poteto-mode/playbooks/autopilot-full.md:16 (issues #952, #1051) |
| `review.md` | Review | "A verdict of `blocked` is not a pass …" | orchestrate skills/orchestrate/references/handoffs.md:137 ("Re-spawn the verifier once the env is healthy, or escalate. Don't count as verified"); prompts/verifier.md:27 (issue #952) |
| `review.md` | Review | "Size the review to the change. Round 1 …" | pstack skills/poteto-mode/playbooks/autopilot-full.md:5,14 (one full swarm at the code-ready head; a rebase, a conflict resolution or a minor fix gets a single verifier, a wording-only change none); playbooks/shipping.md:7 (a verdict covers a patch, and an older one holds only after its `git patch-id` is compared) (issue #952). pstack supports no review for wording only. That a clean merge of the base needs green CI only, with no verifier, departs from pstack's single verifier for a rebase: issue #952 chose it, and the #1051 plan kept it (owner, 2026-11-22). That a `make migration-fix` rename needs none either is the #1051 plan's choice too, an inference no reference has (owner, 2026-11-22) |
| `review.md` | Review | "After round 3, if any blocking finding …" | Three rounds: Codex codex-rs/ext/guardian-reviewer/src/lib.rs:87 (MAX_REVIEW_ATTEMPTS = 3) and oh-my-opencode packages/memory-core/src/reflection/completion-validation.ts:127 (MAX_REPAIR_ROUNDS = 3, then stop); both run their rounds with no human approval in between. Hermes agent/background_review.py:249 caps the iterations inside one review, not the rounds. No reference has a one-line report to the owner after the last round (owner, 2026-11-18, issue #871). |
| `recovery.md` | A failed Task | "Offer a retry that fits the last cause …" | orchestrate skills/orchestrate/references/handoffs.md:61-67: retry by failure mode, and after two retries abandon rather than make a third attempt (issue #952) |
| `investigate.md` | Investigate and plan | "Use `explorer` for a standalone answer when the …" | omp packages/coding-agent/src/prompts/tools/task.md:28-46; prompts/agents/scout.md:38-65 |
| `investigate.md` | Investigate and plan | "Use `planner` to research and recommend one design …" | oh-my-opencode packages/senpi-task/src/agents/builtin/plan-consultant.ts:20-37,65-82; omp packages/coding-agent/src/prompts/tools/task.md:33-46; no reference for approval flow (owner, 2026-11-18) |
| `investigate.md` | Investigate and plan | "Never chain `explorer → planner` or `explorer → …" | omp packages/coding-agent/src/prompts/tools/task.md:31-46; Codex codex-rs/core/src/tools/handlers/multi_agents_spec.rs:738-745; no reference for local design and CLI boundary (owner, 2026-11-18) |
