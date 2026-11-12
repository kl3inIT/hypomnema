# The tools a repository needs, on the machine of a worker

Date: 2026-11-12. Asked after the check of issue #157 on 2026-11-11: a Codex, an omp and an opencode worker each
answered `go: command not found`, so a worker could not run this repository's checks on its node.

Read in the clones under `.tmp/`: Orca (`orca.yaml`, `src/main/workspace-hooks/hooks.ts`,
`setup-hook-env-vars.ts`), t3code (`t3.json`, `apps/server/src/project/ProjectSetupScriptRunner.ts`), Coder
(`examples/templates/docker/main.tf`) and OpenMausBot (`Dockerfile`, `docs/self-hosting.md`; no setup script of a
repository was found in `server/` or `docs/`).

## 1. What each does

| | Who installs what a repository needs | How | Where |
|---|---|---|---|
| Orca | The system, from a script the repository declares | `scripts.setup` in `orca.yaml`, run in a shell when a worktree is created, 120 s at most, with `ORCA_ROOT_PATH` and `ORCA_WORKTREE_PATH` | `hooks.ts` `runHook`, `HOOK_TIMEOUT` |
| t3code | The system, from a script the repository declares | A script in `t3.json` with `runOnWorktreeCreate: true` | `t3.json`, `ProjectSetupScriptRunner.ts` |
| Coder | The system, from the workspace's template | `startup_script` of the agent, at each start of the workspace | `examples/templates/docker/main.tf` L34 |
| OpenMausBot | The agent, in its own shell | The image carries the general tools (git, curl, Python, pnpm, uv): "pnpm, so a bot's shell can install what its project asks for" | `Dockerfile`, the `cloud-home` stage |

## 2. Tried on a node, 2026-11-11

Run `run_ixlirddnrwnah6xde6odkhpf44`: a Codex worker on `aioffice-serving`, as the node's user without sudo, was
told to install Go when it is missing. By its report it downloaded Go 1.27.4 from go.dev to `$HOME/.local/go` in
about 10 seconds, checked the SHA-256, and ran `go test ./internal/node/acp/` with it. The worker's report was not
checked on the machine by hand.

## 3. Read for Hypomnema

- Three of four have the system run a script the repository, or its template, declares when the workspace is made;
  in all four the agent may install more in its own shell.
- Orca and t3code start the agent while the script still runs by default, and both can hold it until the script
  ends (`setupAgentStartupPolicy: wait-for-setup`; t3code's `t3ProjectFile.ts`). In Orca a script that fails does
  not undo the worktree (`runtime-local-worktree-setup.ts`).

## 4. Decided (owner, 2026-11-12)

Learn from all four. The node runs a setup script the repository declares when it makes a Task's worktree, after
Orca and t3code; the design is in issue #186, built in pull request #188 and released as node `v0.9.2`. Beside it
a worker installs what is missing itself, as OpenMausBot leaves it to the bot's shell: `AGENTS.md` says which tools
the checks need and where an agent puts them. What exists is in `ARCHITECTURE.md`; the check on a node is tracked
in issue #186.

This section said until later the same day that no setup script would be built; that was a misreading of the
owner's "openmausbot đi", which asked for OpenMausBot to be read as one more reference.
