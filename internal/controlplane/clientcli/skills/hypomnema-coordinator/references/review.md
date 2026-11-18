# Review

<!-- Omnigent examples/polly/config.yaml:182-199; no reference for the quick exception (owner, 2026-11-18) -->
`quick` work (for example a README rename) needs green CI and no worker review. `narrow` and `broad` work (for
example a queue fix or a refactor) needs a reviewer from another vendor. The author's spec says "A review comes
before the merge: do not turn on auto-merge." Hold the PR if no independent reviewer can run.

<!-- oh-my-opencode packages/senpi-task/src/agents/builtin/plan-reviewer.ts:120-151; no reference for approval and copying specs (owner, 2026-11-18) -->
Review a plan before showing it to the owner: use `profile: reviewer`, `kind: broad` and `review_of` set to the
succeeded planner Task's id. For example, "Check whether this retry design is executable; verify the cited paths
and the acceptance checks", followed by the plan verbatim. Ask the planner to address blockers. Once the owner
approves, put the plan on the issue and pass it verbatim to the implementer.

1. Start from a Task that succeeded with a pull request in its evidence (`hypomnema tasks show <task_id> --json`).
   Look at its checks first, read-only (`gh pr checks <url>`): while they are red there is nothing to review yet.
   For an important pull request you may first ask CodeRabbit to review it: `gh pr comment <url> --body
   "@coderabbitai review"`, a write the owner approves like any other. Important means it adds a migration,
   changes security or permissions, merging or auto-merge, or is a large change. When CodeRabbit is rate-limited or
   does not answer, nothing waits for it: go on with the review. Workers and reviewers never ask it (owner,
   2026-11-17, issue #766).
<!-- Omnigent examples/polly/config.yaml:182-199; no reference for review_of (owner, 2026-11-18) -->
2. Set `profile: reviewer`, `kind` to the work's kind and `review_of` to the succeeded author Task's id. Use
   the same Run so the scheduler excludes the vendor that actually did the work. Never specify a model yourself.
3. Create the review as a Task in the same Run:
   ```text
   hypomnema tasks create <run_id> --file <tasks.json> --key <new key> --json
   ```
   Start every review spec with `Review pull request: <canonical PR URL>` on its own line. In the first round,
   give the reviewer the spec the change was made from, but not the author's report or transcript, so the
   reviewer judges the change itself.

   Ask the reviewer to include these four lines in the `finish` summary, before its findings:
   `Verdict: correct` (or `incorrect`), `Merge state: <mergeStateStatus>`, `Mergeable: <mergeable>` and
   `Checks: <PASSING|FAILING|PENDING|UNKNOWN>`, one field per line. Just before reporting, use `gh pr view <url>
   --json mergeable,mergeStateStatus` and `gh pr checks <url> --required --json bucket`. Failing or canceled checks
   take precedence over pending ones; a nonempty set of successful or skipped required checks is PASSING. Use
   UNKNOWN for an unavailable read or when there are no required checks. For GitLab, report the equivalent
   conflict and check states, and UNKNOWN for any field you cannot verify. Do not change the PR while reviewing it.

   A review first reads what CodeRabbit (`coderabbitai[bot]`) already left on the pull request: its reviews and
   their bodies, which hold the findings outside the diff (`gh api repos/<repo>/pulls/<n>/reviews`), its inline
   comments (`gh api repos/<repo>/pulls/<n>/comments --paginate`) and its comments on the pull request
   (`gh api repos/<repo>/issues/<n>/comments`). It checks each Critical or Major finding against the code at the
   head commit and reports it as fixed, still open (a P1 finding of its own, so the verdict is not correct) or
   wrong, with the reason; Minor findings are listed as follow-ups. A notice that CodeRabbit was rate-limited or
   skipped the pull request is not a review; with no CodeRabbit review the review goes on without it.
4. Wait as for any Run. When woken, read `hypomnema runs show <run_id> --json` and the review Task's summary and
   findings (`hypomnema tasks show <task_id> --json`). If the Run is closed or the next Task is already queued,
   do not dispatch it again. When the review left `review_stall`, act on the branch below before ending your
   turn. If the verdict is unknown, get the actual verdict; never treat it as correct.
5. A verdict of correct: read the PR's current merge state, head and required checks, since the wake is a snapshot.
   If the PR already merged, report it and finish the Run. If it conflicts or must be brought up to date, send
   the author a follow-up to merge the base, resolve any conflict, rerun checks, and report the new head; have the
   resulting change reviewed again.
   For red checks, have the author fix the failure and report the updated head; for pending checks, have the author
   watch them. For an unmet policy or permission the author cannot fix, tell the owner what is blocking the PR and
   leave the Run open. A correct verdict with green checks on a mergeable PR: send the author a follow-up that
   the review passed and asks it to turn on auto-merge only if the spec permits it, as its rules say for its
   repository:
   ```text
   hypomnema tasks follow-up <task_id> --note-file <file> --json
   ```
   Report the pull request as reviewed, then report what the author says: merged, auto-merge on, or what the
   repository lacks for it, which the owner settles. You never merge nor turn on auto-merge yourself.
   CodeRabbit is never a condition of its own: its findings count through the verdict, as in step 3.
6. Findings that make the change incorrect: if `review_stall.waiting_for_owner` is true, use the stop rule below.
   Otherwise send them to the worker that made the change at once, without asking
   the owner (owner, 2026-11-18, issue #871), as a follow-up on that Task, the findings pasted word for word:
   ```text
   hypomnema tasks follow-up <task_id> --note-file <file> --json
   ```
   The worker fixes them on its own branch and pull request, which keeps what it knew of the change. When the
   follow-up is refused, create a fix Task whose spec starts from the branch `hyp/<task_id>` and lists them.
7. Have the updated pull request reviewed by a new review Task. Unlike the first round, its spec carries what the
   rounds before found, so it can tell what was fixed: the pull request's URL and the spec as before, then
   - `## Earlier review (Task <review_task_id>)`: that review's verdict and findings, pasted word for word;
   - `## The author's answer (Task <task_id>)`: the report the worker gave after the follow-up, word for word;
   - `## Still disputed`: each finding the author did not fix and said why, or none.

   Ask the reviewer to say, for each earlier finding, whether it is fixed, still open or disputed, and to report
   new findings as before. After round 3, if any P0 or P1 remains, give the owner a one-line report with the verdict
   and remaining blockers, then stop without waiting for an answer (issue #871). Leave the Run and pull request open
   with auto-merge off and add no Task. The "Waiting for you" label may stay until work is added or the Run ends.

Other findings go into your report as follow-ups; they do not hold the pull request back.
