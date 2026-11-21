---
name: hypomnema-coordinator
description: >-
  Has Hypomnema workers do coding work on the owner's nodes through the hypomnema CLI: plans a Run,
  follows it and reports each Task's outcome with its evidence. Use when the owner asks to give coding work to
  Hypomnema or its workers, to start, follow, finish or cancel a Run, or to answer or forward a worker's question,
  and when the control plane wakes you about a Run. Otherwise do the work yourself: workers suit work that should run
  on the nodes and end in a pull request.
---

# Coordinating Hypomnema workers

In a Bot chat you coordinate a Run; in a session with the owner you hand it off to a Bot chat (Recipe, below).
The control plane starts each Task when a node and account are free. An implementer opens a pull request from its
own branch `hyp/<task_id>`; the other roles return their reports. The coordinator verifies the result.
`hypomnema runs show <run_id> --json` tells you where the Run stands after you lose context.

Bots coordinate and do day-to-day work (messages, Lark, Superset, reports and the phone). Give repository changes
to workers: the Bot reads files for planning but does not write code.

Add `--json` to every command and read the result before the next step. When a result has `next`, those are the
commands to run next. `hypomnema <command> --help` lists a command's flags; a flag it does not list does not exist.

## Recipe

A Run has one controller: the Bot of the chat it belongs to. There are two paths, by where you are:

- **In a Bot chat** you coordinate the Run to its end: steps 0 to 4.
- **In a session with the owner** (Claude Code, Codex), outside a Bot chat, you hand the Run off: steps 0 to 2,
  then tell the owner the Run, the chat that `runs create` named and the Tasks, and stop. That chat's Bot is
  woken by each event and takes it from there: it answers questions, sends reviews and fix rounds, and finishes
  the Run. Do not do step 3 or 4 yourself, and after the hand-off only observe (Waiting, below).

0. Before a Run that comes from an issue, a document or a memory, check that its premise still holds on the base
   branch: read the code it names, or run a read-only command on the system it names. When the work is already there
   or the problem does not reproduce, tell the owner with what you saw instead of creating the Run; an issue that
   rests on a stale line is closed, not built. Put what you checked in the objective, so workers start from it.
1. Write the objective to a file and create the Run:
   ```text
   hypomnema runs create --repo <owner/name> --objective-file <file> --key <new key> --json
   ```
   The Run belongs to your Bot's chat. Outside a Bot chat, the owner's command defaults to the main Bot's main
   chat; `--chat <chat_id>` picks another chat. The result names the chat the control plane will wake.
   `--repo` also takes the URL of a repository on GitLab, or a name from `hypomnema repos list --json`. When the
   create answers that a host is not known, tell the owner the command it names; only the owner declares a host.
2. Write every Task you already know to a file and create them in one call:
   ```text
   hypomnema tasks create <run_id> --file <tasks.json> --key <new key> --json
   ```
   The file is `{"tasks": [{"ref": "api", "profile": "implementer", "kind": "narrow", "spec": "...", "depends_on": []}]}`.
   The profile names a role, and the scheduler selects its model. Text
   goes through a file, or `-` for standard input, because shell quoting can change it; check the specs in the
   output.
3. In a Bot chat: handle each wake-up (Waiting, below) until every Task has ended: succeeded, failed or
   canceled. A blocked Task has not ended.
4. In a Bot chat: finish the Run with `hypomnema runs finish <run_id> --json` as soon as every Task has ended, without waiting
   for the owner to press anything, then report to the owner per Task: the outcome, its evidence, and what is left.
   While the owner has yet to decide on a fix you offered for a
   failed Task, or while its review waits for a next step, leave the Run open: a finished Run takes no new Tasks.

A key is any string unique to that create, such as `health-run-1`. When a create gets no answer (a timeout, a
dropped connection), run it again with the same `--key`: the control plane returns the first result instead of
creating a second Run or second Tasks.

## Choosing a role and kind

Read `hypomnema profiles list --json` to confirm the roles exist. A role has no fixed runtime, model or
availability: these depend on the kind and the accounts. Do not dispatch a retired vendor copy instead.

- `implementer` investigates and edits in one Task. Set `kind` on every Task and say in one line how open the
  work is. Use `broad` when unsure (owner, 2026-11-18). Effort follows the kind; planner and reviewer use high.
  - `broad` (high): "The API can take several shapes; refactor its callers and add regression tests."
  - `narrow` (medium): "Fix the reproduced duplicate retry; preserve the response shape."
  - `quick` (low): "Rename the setting in one file; the new name is fixed."
- `planner` researches and proposes a design without changing project files. Get independent review before the
  owner sees it. Load `investigate` and `review`.
- `explorer` answers standalone questions when the relevant files are not known. Load `investigate`.
- `reviewer` checks from another vendor. `quick` needs green CI and no worker review. For `narrow` and `broad`,
  load `review` and put "A review comes before the merge: do not turn on auto-merge." in the author's spec.
- For an important pull request (it adds a migration, changes security or permissions, merging or auto-merge, or is
  a large change) you may ask CodeRabbit to review it: `gh pr comment <url> --body "@coderabbitai review"`, a write
  the owner approves like any other. Nothing waits for CodeRabbit when it is rate-limited or does not answer, and it
  is never a merge condition: a pull request is ready with green checks, no conflict and a correct verdict, and the
  review answers CodeRabbit's findings (the `review` reference). Workers and reviewers do not ask it.
- Never name a `runtime` or `model` unless the owner asks. Report an unavailable entry instead of substituting
  another yourself. While accounts cool, the scheduler walks the remaining chain. A Task with the owner's
  explicit runtime or model waits instead. Report fallbacks from the Dispatch's model and `chain_entry`.
- A model is always named with its provider, `<provider>/<model>`, such as `opencode-go/deepseek-v4-pro` or
  `claude-subscription/claude-opus-5-5`; a bare name is refused (issue #1002). Take the name from
  `hypomnema models list` or the Dispatch's model.
- A started Task and its follow-ups stay on their vendor unless its accounts run out. The next chain entry
  can then take over in the same worktree through the existing handoff. It is not handed off after an approved
  service request. Do not create a fresh Task to move it yourself.

- An escalation of kind `repeated_failure` says a Task's worker failed again for the same reason as its previous
  failed attempt: its `reason` is the failure class (with the cause when the worker was lost), `count` how many
  attempts in a row failed so, and `dispatches` which. Read the Task (`hypomnema tasks show <task_id> --json`) and
  tell the owner the reason, the count and what the last attempt reported. Change nothing on your own: the Task is
  retried as before, and whether to change its spec, profile or model is the owner's choice.
- `hypomnema tasks show <task_id> --json` and `hypomnema runs show <run_id> --json` say what a Task or a Run used
  (`usage`): the gateway's requests, tokens and cost at API prices, and the tokens a runtime outside the gateway
  reported. Give it when the owner asks what work cost.

## Task specs

A worker sees its spec, the repository and its branch; it does not see this conversation or the other Tasks, and
its branch does not hold another Task's unmerged changes. So:

- Never put an explorer Task before a planner or implementer to gather its context. Those workers read what
  they need themselves. Give the planner the owner's words, earlier decisions and constraints; give the
  implementer the approved plan word for word.
- Keep steps that build on each other's changes in one Task, such as an endpoint, its test and its README section.
- Use separate Tasks for independent work, each with its own deliverable.
- `depends_on` (refs earlier in the file, or ids of Tasks already in the Run) only orders Tasks: a Task starts once
  all of them succeeded. It does not carry their changes, nor their reports.
- Work that builds on an unmerged Task's changes (stacked) sets `"start_from": "hyp/<task_id>"` and lists that Task
  in `depends_on`: its worktree starts from that branch, and its pull request targets it (`"pr_base":
  "start_from"`, the default) or the Run's base (`"pr_base": "run_base"`). `start_from` may also name another
  branch of the repository. Never write a branch to check out or merge in the spec: set the field.
- When a Task needs what an earlier Task found (fixing the items an audit listed), paste that Task's report, the
  summary in `hypomnema tasks show <task_id> --json`, into the spec word for word, under a heading that names its
  Task id, such as `## Report of task <task_id>`. Do not shorten or reword it.
- For a bug fix, ask for a test that fails without the fix, committed before the fix, so a disputed finding is
  settled by running that test at either commit.

Make each spec self-contained: the goal, decisions already made, what it may change, the deliverable and the
evidence that proves it is done.

## Waiting

```text
hypomnema runs wait <run_id> --after <after> --json
```

This is optional observation, not how a Run keeps its coordinator. The Run's chat is woken by each event even
when the session that created the Run has closed, so a Bot needs no wait: the wake-up starts its turn. The wait
returns when a Task ended or was blocked, a worker asked a question, an escalation arrived, or the Run ended. It
prints those events, the `after` for the next wait, and `next`. Start with `--after 0`.

In a Bot chat, on a wake-up:

- When the control plane says the owner assigned an existing Run to this chat, start with `runs show <run_id> --json`.
  Read the current Tasks, open questions (`questions --run <run_id> --json`) and PR evidence. Continue only what
  still needs a decision, review or follow-up. A reviewer Task that succeeded may still have found the change
  incorrect. Only the owner can assign legacy Runs with `runs adopt`; leave Runs in other chats alone.
- On a review wake, read the Run again and load the `review` reference. The wake names the verdict, the pull
  request and the command that reads the findings; the control plane sends nothing to the author by itself, so the
  fix is yours to decide and send. The verdict and PR state in the wake
  are a snapshot, not permission to merge. If a Task has already been added, do not repeat it. A Run stalled after
  review is not ready to finish merely because its Tasks ended. While another Task of the Run is unfinished, a
  review's end comes as a plain Task wake: read its verdict with `tasks show` before acting.
- When your turn began with a message from the control plane listing a Run's events, end your turn after acting;
  the control plane wakes you again.

In a session with the owner (Claude Code, Codex), after the hand-off (Recipe, step 2), a Run with a `chat_id` (in
`runs show`) is that chat's Bot's to control. Only observe it: `runs show`, `tasks show` and, when the owner wants
to watch it here, `runs wait`. Do not follow up, answer its questions to the coordinator, create Tasks or reviews,
or finish it, even after a wait returns its events: a finished Run takes no fix round. When the owner wants it
changed, pass the request to that chat with `hypomnema chat --chat <chat_id> --text-file <file>`, or tell the owner
to write there. A question to the owner stays the owner's, and the owner may still cancel the Run.

A chat with an open Run cannot be deleted, nor its Bot removed with its chats: the answer is a conflict until
the Run is finished or canceled. Tell the owner which Run holds it; an ended Run lets the chat go as before.

Keep at most one wait per Run and no status loops. To be notified when it is done, let the assigned chat receive
the next wake-up and report there.

## Questions

A worker asks you when it cannot go on without a decision, and waits. Its question reaches the chat its Run
belongs to: one to the coordinator wakes that chat, one to the owner waits above that chat's composer. Read the
question with `hypomnema questions show <question_id> --json`.

- Answer when the answer follows from the spec, the objective, the owner's words, or the repository's existing code
  (read it), and say what you based it on: the file, or the owner's words quoted:
  `hypomnema answer <question_id> --text-file <file> --json`, or `hypomnema answer <question_id> <option number> --json`.
- Forward to the owner a choice none of those settles; anything about secrets or access; and anything irreversible,
  destructive or outside the Task's repository that the owner has not decided in so many words:
  `hypomnema questions forward <question_id> --json`. Then tell the owner
  the question in your reply. It is the owner's to answer now, with `hypomnema answer <question_id> <answer>`; your
  token cannot answer it. The worker keeps waiting.
- `hypomnema questions --to coordinator --run <run_id> --json` lists the questions still open for you; without
  `--to coordinator` it lists the owner's.

## Results

A worker's report is a claim; the Task's record and its evidence are what count.

- For a succeeded planner, send the plan to an independent reviewer before showing it to the owner. A succeeded
  explorer or reviewer returns a report, not a pull request. Check it against the spec and report the findings.
- For a succeeded implementer, `hypomnema tasks show <task_id> --json` has the latest Dispatch's summary and evidence, such as
  `{"kind": "pull_request", "ref": "<url>"}`. Check them against the spec: the pull request is in the right
  repository, and the checks the spec asked for ran. When the evidence has no pull request, look for one read-only
  (`gh pr list --repo <owner/name> --head <branch>`), using the Task's `push_branch` or `hyp/<task_id>` when unset;
  when there is none, send the worker a follow-up (below)
  that names what is missing. `gh pr checks <url>` shows whether the checks ran.
- More work on a succeeded Task, such as a fix after a review or a correction the owner gives, goes to the same
  worker as a follow-up, not to a new Task:
  ```text
  hypomnema tasks follow-up <task_id> --note-file <file> --json
  ```
  The note says what to change or add. The Task is ready again; its worker continues its own conversation in the
  same worktree and branch, updates the same pull request, and finishes again; its end wakes you as before.
  `tasks show` lists the follow-ups. A follow-up is for a succeeded Task, or one blocked at a gate, whose gate it
  replaces (the gate is closed and the worker goes on with your note); a failed one is the `recovery` reference's.
- When the follow-up is refused (the worker's conversation was closed, its Run finished) or the Task failed or was
  canceled with its work on a pull request, create a Task with `"continues": "<task_id>"` and a spec that names
  what is left. It works on that Task's branch and pushes into it, so the same pull request takes the commits and
  keeps its number and reviews; the Task may be of another Run of the same repository. Do not open a new pull request
  with `Supersedes #n`, and do not write a branch to check out in the spec. It is refused while a Task that pushes
  to that branch is unfinished; its own pull request is found with `gh pr list --head <its push_branch>`, the
  `push_branch` that `tasks show` gives.
- `failed` or `blocked`: load the `recovery` reference before acting.
- Report a failed, blocked or canceled Task as exactly that, with what is left.
- Never merge a pull request, nor turn on its auto-merge: the worker that made it does, or the owner. A worker that
  could not turn auto-merge on says what the repository lacks (no required check, auto-merge off in its settings);
  report that to the owner, who changes the setting or merges.
- On a GitHub PR handoff, including one recovered after a worker failed, run `gh reconcile-replacement <new-url> <run-id>`
  through Hypomnema's GitHub launcher. If the Task spec explicitly says which PR it replaces, pass that PR's number
  after the Run id. The command also recognizes a `Replaces #n` or `Supersedes #n` line in the new body. Both
  GitHub heads must match a branch and commit the node pushed for a Dispatch of this Run in its repository,
  not a fork. Ask workers to use `publish_branch` for their commits. A branch name, a worker's result, or an
  up-to-date push cannot establish ownership. Both PRs must name the same closing issue; when GitHub supplies no
  links, visible closing keywords can name the same issue (including on a non-default base). The command
  comments with the replacement's link and closes the old PR without deleting its branch. No declaration means
  no write. If a check fails or an older push has no record, leave the old PR open and tell the owner.
  Each write uses the usual GitHub approvals. If it waits for approval, end your turn and rerun
  the same command when woken; it does not repeat a comment it already posted.

## Proof

Act only on proof. Silence or a slow worker does not mean it stopped or failed: the control plane marks a lost
worker and retries failed attempts itself, so neither is a reason to cancel. Cancel a Task when the owner asks, or
when its spec turns out wrong and a corrected Task will replace it; the `recovery` reference says how.

## References

| When | Load |
|---|---|
| A Task failed or is blocked, a Task needs cancelling or replacing, or the owner asks to retry or stop work | `hypomnema skills get hypomnema-coordinator --reference recovery` |
| The owner asks how the code works, why something fails, or for an audit or a comparison, and nothing is to change | `hypomnema skills get hypomnema-coordinator --reference investigate` |
| A pull request is to be reviewed | `hypomnema skills get hypomnema-coordinator --reference review` |
