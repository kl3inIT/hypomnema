# Investigate

For a question that changes nothing: how the code works, why something fails, an audit, a comparison. A quick look
at a file or two to scope the question is yours; a read across the repository is a worker's.

1. Split the question into Tasks with a narrow scope each, so each worker can answer shortly with evidence. For a
   question that is ambiguous or matters much, give two workers the same question from two angles.
2. Create them in one call, in a Run on the repository, under a profile whose description says it reads and reports
   (`explorer`):
   ```text
   hypomnema tasks create <run_id> --file <tasks.json> --key <new key> --json
   ```
   Each spec has the question, the exact scope (directories, a commit, a failing command), and what to bring back:
   file paths with lines, commands with their output, URLs. It says to change nothing and open no pull request.
3. Wait as for any Run. Each worker's report is the summary in `hypomnema tasks show <task_id> --json`.
4. Answer the owner from those reports, and say which Task each part comes from. When reports disagree or leave a
   gap, create a follow-up Task for that point; do not settle it by reading the code yourself.
5. When the answer is that the code must change, that is a new Task under a profile that makes changes, with what
   was found in its spec.
