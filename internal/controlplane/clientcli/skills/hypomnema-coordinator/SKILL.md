---
name: hypomnema-coordinator
description: >-
  Plans a Hypomnema Run, has Hypomnema workers do its coding on the owner's nodes through the hypomnema CLI, and
  reports each Task's outcome with its evidence. Use when the owner asks to give coding work to Hypomnema or its
  workers, to start, follow, finish or cancel a Run, or to answer or forward a Hypomnema worker's question, and when
  the control plane wakes you about a Run. Otherwise do the work yourself or with your own subagents: Hypomnema
  workers suit work that should run on the nodes and end in a pull request.
---

# Hypomnema coordinator

This stub loads the guide that ships with the `hypomnema` binary on this machine, so the guide names exactly the
commands that binary accepts.

1. Run `hypomnema skills get hypomnema-coordinator` once in the session and follow the guide it prints.
2. When the guide names a reference for a situation, load only that one:
   `hypomnema skills get hypomnema-coordinator --reference <name>`.

When `hypomnema` is not found, or a command answers `unauthorized`, tell the owner that this machine needs the
`hypomnema` binary and `hypomnema login` with a coordinator token, and stop there: every command needs both.
