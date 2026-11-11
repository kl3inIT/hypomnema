# Review

How a pull request is reviewed by another worker. Whether to review is the owner's to say; this is how.

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
   Its spec has the pull request's URL and the spec the change was made from, and nothing else: not the author's
   report and not its transcript. How the reviewer reports is in its own profile.
4. Wait as for any Run. The review is the summary in `hypomnema tasks show <task_id> --json`: a verdict and
   findings with a priority each.
5. A verdict of correct, with green checks: report the pull request as reviewed and ready for the owner. The owner
   merges; you never do.
6. Findings that make the change incorrect: report them to the owner as the reviewer gave them, and offer a fix
   Task whose spec starts from the branch `hyp/<task_id>` of the change and lists them. Create it once the owner
   agrees, and have its pull request reviewed the same way. After two rounds that still leave such findings, stop
   and tell the owner what does not converge.

Other findings go into your report as follow-ups; they do not hold the pull request back.
