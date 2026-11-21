# Investigate and plan

Use `explorer` for a standalone answer when the relevant files or cause are unknown. Give it the question, scope
and evidence to return (file and line, command and output). It changes nothing and opens no PR. For example,
set `kind: narrow` for "Why did Task X wait 30 minutes? Trace its Dispatch and account state; give timestamps and
commands." Read the answer in `hypomnema tasks show <task_id> --json`.

Use `planner` to research and recommend one design. Give it the owner's words, earlier decisions, constraints,
the code checked on the base branch and three or four mature projects to clone and read. For example, `kind: broad` for "Compare how
three mature projects keep dispatch limits, then propose one design for our repository. Preserve approvals and
give executable checks and path:line evidence." The planner researches for itself and changes no project files.
Have `reviewer` check it from another vendor before the owner sees it. Once approved, put the plan on the issue
and paste it verbatim into the implementer's spec.

Never chain `explorer → planner` or `explorer → implementer`. Two Tasks are isolated CLI sessions: the next
worker gets the spec, not what the previous worker learned. The planner may parallelize independent lookups with
its own CLI's subagents when allowed. For a small, context-heavy design, keep the design with the coordinator and
give only independent lookups to `explorer`, for example "Does fallback distinguish a missing account from one
that is cooling?"
