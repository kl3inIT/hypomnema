# Review

`quick` work (for example a README rename) needs green CI and no worker review. `narrow` and `broad` work (for
example a queue fix or a refactor) needs a reviewer from another vendor. The author's spec says "A review comes
before the merge: do not turn on auto-merge." Hold the PR if no independent reviewer can run.

The control plane posts the commit status `review` on a PR's head: success where a verdict on that head is
`correct`, failure with the first finding where it is `incorrect` or `blocked`, and success "no review needed" on
each head of a `quick` Task's PR. A new head has none until it is reviewed; where `review` is a required check,
the PR merges only at a head a reviewer judged correct.

Review a plan before showing it to the owner: use `profile: reviewer`, `kind: broad` and `review_of` set to the
succeeded planner Task's id. For example, "Check whether this retry design is executable; verify the cited paths
and the acceptance checks", followed by the plan verbatim. Ask the planner to address blockers. Once the owner
approves, put the plan on the issue and pass it verbatim to the implementer.

If GitHub reports a rate limit, run `gh api rate_limit` on the affected host.
This call does not count against the primary limit.
Wait until the exhausted quota resets (`X-RateLimit-Reset`) before resuming.
For a secondary limit, honor `Retry-After` if present. If neither delay is given for a secondary limit,
wait at least one minute before resuming. Report the limit and the wait in your review report.

1. Start from a Task that succeeded with a pull request in its evidence (`hypomnema tasks show <task_id> --json`).
   Look at its checks first (`gh pr checks <url>`).
   Do not rerun a failing test to get a green check. Hold the merge until the failure is fixed or, for a flaky
   test, the test is quarantined with an issue and a note visible to the owner (issue #1281).
   When a review or fix Task investigates a flaky test or asks for repeated test runs, load the `recovery`
   reference and copy its [test repeat rules](recovery.md#flaky-tests-and-repeat-runs) into the Task's spec.
   For an important pull request you may first ask CodeRabbit to review it: `gh pr comment <url> --body
   "@coderabbitai review"`, a write the owner approves like any other. Important means it adds a migration,
   changes security or permissions, merging or auto-merge, or is a large change. When CodeRabbit is rate-limited or
   does not answer, nothing waits for it: go on with the review. Workers and reviewers never ask it (owner,
   2026-11-17, issue #766).
2. Set `profile: reviewer`, `kind` to the work's kind and `review_of` to the succeeded author Task's id. Use
   the same Run so the scheduler excludes the vendor that actually did the work. Never specify a model yourself.
3. Create the review as a Task in the same Run:
   ```text
   hypomnema tasks create <run_id> --file <tasks.json> --key <new key> --json
   ```
   Start every review spec with `Review pull request: <canonical PR URL>` on its own line, then `Head: <sha>`, the
   pull request's head now (`gh pr view <url> --json headRefOid`), on the next. A head gets one verdict: the
   control plane refuses a review of a head that already has a `correct` or `incorrect` verdict, from any Run, and
   names that Task and verdict; read that Task instead of asking again. A `blocked` verdict does not count. A
   review of a pull request that has a verdict gets a `## Reviewed head` section with the head that verdict covers,
   for step 7's `git diff <reviewed_sha>..<head>`. In the first round,
   give the reviewer the spec the change was made from, but not the author's report or transcript, so the
   reviewer judges the change itself.

   Do not restate the reviewer's rules in the spec: the `reviewer` profile holds how it reviews, which findings
   block, what it runs, how it answers CodeRabbit and the lines its summary starts with
   (`hypomnema profiles show reviewer --json`). The control plane records a verdict only when the summary has both
   its `Verdict:` and `Head:` lines.

   Copy an owner's acceptance condition into the spec word for word, as one line. Never restate it as a general
   property for the reviewer to hunt: "only delete what is safe to delete" stays that line, not "check every
   deletion this change could make".
4. Wait as for any Run. When woken, read `hypomnema runs show <run_id> --json` and the review Task's summary and
   findings (`hypomnema tasks show <task_id> --json`). If the Run is closed or the next Task is already queued,
   do not dispatch it again. When the review left `review_stall`, act on the branch below before ending your
   turn. If the verdict is unknown, get the actual verdict from the summary's `Verdict:` line (`review_stall`
   shows `blocked` as `unknown`); never treat it as correct.
5. A verdict of correct: read the PR's current merge state, head and required checks, since the wake is a snapshot.
   If the PR already merged, report it and finish the Run. If it conflicts or must be brought up to date, send
   the author a follow-up to merge the base, resolve any conflict, rerun checks, and report the new head. A head
   past the verdict's `Head:` gets what step 7 says for its change.
   For red checks, follow step 1 and have the author report the updated head once the failure is resolved.
   For pending checks, have the author use `gh pr checks <url> --required --watch --interval 60`.
   For an unmet policy or permission the author cannot fix, tell the owner what is blocking the PR and
   leave the Run open. A correct verdict with green checks on a mergeable PR: send the author a follow-up that
   the review passed and asks it to turn on auto-merge only if the spec permits it, as its rules say for its
   repository:
   ```text
   hypomnema tasks follow-up <task_id> --note-file <file> --json
   ```
   Report the pull request as reviewed, then report what the author says: merged, auto-merge on, or what the
   repository lacks for it, which the owner settles. You never merge nor turn on auto-merge yourself.
   Auto-merge on is not merged: the control plane follows it and wakes you, as the guide's Results say (issue #1053).
   CodeRabbit is never a condition of its own: its findings count through the verdict, which the reviewer
   profile says how to reach.
6. Findings that make the change incorrect: if `review_stall.waiting_for_owner` is true, use the stop rule below.
   Only a finding that blocks by the reviewer profile's rule makes it incorrect; an `incorrect` verdict where
   none does is handled as `blocked` (below), never as a pass.
   Otherwise send the proven ones to the worker that made the change at once, without asking
   the owner (owner, 2026-11-18, issue #871), as a follow-up on that Task, the findings pasted word for word:
   ```text
   hypomnema tasks follow-up <task_id> --note-file <file> --json
   ```
   The worker fixes them on its own branch and pull request, which keeps what it knew of the change. On GitHub it
   uses `publish_branch` so the node records the updated head for the Run. When the
   follow-up is refused, create a fix Task with `"continues": "<task_id>"` whose spec lists them: it pushes into the
   same branch and pull request (issue #690).

   A verdict of `blocked` is not a pass and not a fix round. When its cause is gone, create one new review Task
   with the same spec and `## Blocked review (Task <review_task_id>)`, that review's summary word for word. When
   the cause needs the owner (an account, a login), or the new review is blocked too, tell the owner in one line
   what stops it and leave the Run and pull request open with auto-merge off.
7. Have the updated pull request reviewed by a new review Task. Unlike the first round, its spec carries what the
   rounds before found, so it can tell what was fixed: the pull request's URL, `Head:` set to the updated head, and
   the spec as before, then
   - `## Earlier review (Task <review_task_id>)`: that review's verdict, its `Head:` and its findings, pasted word
     for word;
   - `## The author's answer (Task <task_id>)`: the report the worker gave after the follow-up, word for word;
   - `## Still disputed`: each finding the author did not fix and said why, or none.

   Size the review to the change. Round 1 was the full review at the code-ready head. From round 2, ask the
   reviewer to check each earlier finding (fixed, still open or disputed) and only `git diff <reviewed_sha>..<head>`,
   where `<reviewed_sha>` is the earlier `Head:`, and set `"effort": "medium"` on the Task. Review the whole change
   again, at the usual effort, only for new behavior, a redesign or a large new diff. A head whose only change
   since the reviewed head is a clean merge of the base, a `make migration-fix` rename of the change's migrations
   or wording needs only a review of that diff, at `"effort": "medium"`, for its `review` status. Every verdict
   names the head it covers.
   After round 3, if any blocking finding remains, give the owner a one-line report with the verdict
   and remaining blockers, then stop without waiting for an answer (issue #871). Leave the Run and pull request open
   with auto-merge off and add no Task. The "Waiting for you" label may stay until work is added or the Run ends.

Other findings go into your report as follow-ups; they do not hold the pull request back.
