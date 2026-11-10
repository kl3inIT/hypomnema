---
name: hypomnema-coordinator
description: >-
  Plans a Hypomnema Run, has Hypomnema workers do its coding on the owner's nodes through the hypomnema CLI, and
  reports each Task's outcome with its evidence. Use when the owner asks to give coding work to Hypomnema or its
  workers, to start, follow, finish or cancel a Run, or to answer or forward a Hypomnema worker's question, and when
  the control plane wakes you about a Run. Otherwise do the work yourself or with your own subagents: Hypomnema
  workers suit work that should run on the nodes and end in a pull request.
---

# Coordinating Hypomnema workers

You coordinate a Run. You split the owner's objective into Tasks; the control plane starts each Task on a node as
soon as a node and an account are free; a worker (Claude Code or Codex) does it on its own branch `hyp/<task_id>`,
cut from the Run's base branch, and opens a pull request; you check the result and report. The control plane keeps
every record, so `hypomnema runs show <run_id> --json` tells you where a Run stands, also after you lose context.

Changes to the Run's repository are Tasks, unless the owner gives a change to you: workers have the repository, the
branch and the checks, and the owner reviews their pull requests. A change the owner gives you goes on your own
branch and into a pull request, as a worker's would. Read files and run read-only commands as much as
planning needs.

Add `--json` to every command and read the result before the next step. When a result has `next`, those are the
commands to run next. `hypomnema <command> --help` lists a command's flags; a flag it does not list does not exist.

## Recipe

1. Write the objective to a file and create the Run:
   ```text
   hypomnema runs create --repo <owner/name> --objective-file <file> --key <new key> --json
   ```
   `--repo` also takes the URL of a repository on GitLab, or a name from `hypomnema repos list --json`. When the
   create answers that a host is not known, tell the owner the command it names; only the owner declares a host.
2. Write every Task you already know to a file and create them in one call:
   ```text
   hypomnema tasks create <run_id> --file <tasks.json> --key <new key> --json
   ```
   The file is `{"tasks": [{"ref": "api", "profile": "implementer", "spec": "...", "depends_on": []}]}`. A profile
   sets the runtime, model and instructions a worker runs with; `implementer` is the default, and "Choosing a
   profile" below says how to pick another. Text
   goes through a file, or `-` for standard input, because shell quoting can change it; check the specs in the
   output.
3. Wait (below), handle what the wait returns, and wait again until every Task has ended: succeeded, failed or
   canceled. A blocked Task has not ended.
4. Finish the Run with `hypomnema runs finish <run_id> --json`, then report to the owner per Task: the outcome, the
   evidence (the pull request), and what is left. While the owner has yet to decide on a fix you offered for a
   failed Task, leave the Run open: a finished Run takes no new Tasks.

A key is any string unique to that create, such as `health-run-1`. When a create gets no answer (a timeout, a
dropped connection), run it again with the same `--key`: the control plane returns the first result instead of
creating a second Run or second Tasks.

## Choosing a profile

`hypomnema profiles list --json` shows each profile with its `description`, its `runtime` (`claude` is Claude Code,
`codex` is Codex), its model, and whether a worker can start under it now (`available`). Read it once before you
create Tasks.

- Give work only to a profile with `available: true`. One at `false` has `unavailable_reason`: `no_node` (no
  connected node has its CLI), `no_account` (its runtime has no account), or `accounts_cooling` (every account
  waits for a limit to reset, at `available_at`). Tell the owner which profiles cannot run and why, when the work
  needs one of them; say nothing about it when everything the work needs can run. A profile with no `available`
  is not a worker's (the coordinator's own).

- Choose by the description first: it says what the profile is for.
- When two profiles fit a change and differ in runtime, give work across many files, a refactor or writing tests to
  the one on Claude Code, and a narrow, well-scoped change to the one on Codex.
- A question about the code that changes nothing is for `explorer` Tasks: load the `investigate` reference.
- Leave the model to the profile. Name a `model` on a Task only when the owner asks for one;
  `hypomnema models list --json` shows what it may name.
- When a create is refused because a profile or a model cannot be served, or an escalation says a Task has no node
  or no account to run on, tell the owner what is missing and stop there for that Task. Do not move it to another
  profile or model on your own: which worker and which model do the owner's work is the owner's choice.

## Task specs

A worker sees its spec, the repository and its branch; it does not see this conversation or the other Tasks, and
its branch does not hold another Task's unmerged changes. So:

- Keep steps that build on each other's changes in one Task, such as an endpoint, its test and its README section.
- Use separate Tasks for work that can go on in parallel on different files, each ending in its own pull request.
- `depends_on` (refs earlier in the file, or ids of Tasks already in the Run) only orders Tasks: a Task starts once
  all of them succeeded. It does not carry their changes.

Make each spec self-contained: the goal and the change in the repository's terms; the decisions already made
(names, formats, interfaces), so parallel Tasks agree; what it may change; and the evidence that proves it is done:
the tests or commands to run, and the pull request.

## Waiting

```text
hypomnema runs wait <run_id> --after <after> --json
```

It returns when something needs you: a Task ended or was blocked, a worker asked you a question, an escalation
arrived, or the Run ended. It prints those events, the `after` for the next wait, and `next`. Start with `--after 0`.

- When your turn began with a message from the control plane listing a Run's events, end your turn after acting;
  the control plane wakes you again.
- In a session with the owner (Claude Code, Codex), start the wait as a background command if your runtime tells you
  when one exits, and end your turn; otherwise run it in the foreground. Each time a wait returns, act on it, then
  start the next one with the `after` it printed.

Keep one wait per Run and no status loops: a loop sends a request every few seconds and sees nothing the wait does
not. "Tell me when it's done" also means: start the wait and end your turn.

## Questions

A worker asks you when it cannot go on without a decision, and waits. Read the question with
`hypomnema questions show <question_id> --json`.

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

- `succeeded`: `hypomnema tasks show <task_id> --json` has the latest Dispatch's summary and evidence, such as
  `{"kind": "pull_request", "ref": "<url>"}`. Check them against the spec: the pull request is in the right
  repository, and the checks the spec asked for ran. When the evidence has no pull request, look for one read-only
  (`gh pr list --repo <owner/name> --head hyp/<task_id>`); when there is none, create a follow-up Task whose spec
  says to start from the branch `hyp/<task_id>` (fetch it and merge it into its own branch) and names what is
  missing. `gh pr checks <url>` shows whether the checks ran.
- `failed` or `blocked`: load the `recovery` reference before acting.
- Report a failed, blocked or canceled Task as exactly that, with what is left.
- Never merge a pull request: it is what the worker delivers, and the owner reviews and merges it.

## Proof

Act only on proof. Silence or a slow worker does not mean it stopped or failed: the control plane marks a lost
worker and retries failed attempts itself, so neither is a reason to cancel. Cancel a Task when the owner asks, or
when its spec turns out wrong and a corrected Task will replace it; the `recovery` reference says how.

## References

| When | Load |
|---|---|
| A Task failed or is blocked, a Task needs cancelling or replacing, or the owner asks to retry or stop work | `hypomnema skills get hypomnema-coordinator --reference recovery` |
| The owner asks how the code works, why something fails, or for an audit or a comparison, and nothing is to change | `hypomnema skills get hypomnema-coordinator --reference investigate` |
