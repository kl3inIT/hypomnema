# Review

How a pull request is reviewed by another worker. Whether to review is the owner's to say; this is how. The
author's spec says "A review comes before the merge: do not turn on auto-merge.", so its worker leaves the pull
request open for the review.

1. Start from a Task that succeeded with a pull request in its evidence (`hypomnema tasks show <task_id> --json`).
   Look at its checks first, read-only (`gh pr checks <url>`): while they are red there is nothing to review yet.
2. Pick the reviewer from `hypomnema profiles list --json`: a profile whose description says it reviews, and says
   it is for this kind of change. The descriptions say which reviewer goes with which author, so that the worker
   that reviews is not the kind that wrote the change; go by them. When the only reviewer that can run is of the
   author's own kind, use it and say in your report that the review was not independent.
3. Create the review as a Task in the same Run:
   ```text
   hypomnema tasks create <run_id> --file <tasks.json> --key <new key> --json
   ```
   In the first round its spec has the pull request's URL and the spec the change was made from, and nothing
   else: not the author's report and not its transcript, so the reviewer judges the change itself. How the
   reviewer reports is in its own profile.
4. Wait as for any Run. The review is the summary in `hypomnema tasks show <task_id> --json`: a verdict and
   findings with a priority each.
5. A verdict of correct, with green checks: send the author a follow-up that the review passed and asks it to turn
   on auto-merge, as its rules say for its repository:
   ```text
   hypomnema tasks follow-up <task_id> --note-file <file> --json
   ```
   Report the pull request as reviewed, then report what the author says: merged, auto-merge on, or what the
   repository lacks for it, which the owner settles. You never merge nor turn on auto-merge yourself.
6. Findings that make the change incorrect: report them to the owner as the reviewer gave them, and offer to send
   them to the worker that made the change. Once the owner agrees, send them as a follow-up on that Task, the
   findings pasted word for word:
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
   new findings as before. After two rounds that still leave findings that make the change incorrect, stop and tell
   the owner what does not converge.

Other findings go into your report as follow-ups; they do not hold the pull request back.
