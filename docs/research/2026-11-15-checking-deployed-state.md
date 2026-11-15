# Checking what is deployed instead of trusting a document

Date: 2026-11-15. Read-only study. Why: issue #467 ("the coordinator on zm gets lark-cli and the lark skill") was
filed from a line of `ARCHITECTURE.md`'s deployed section that had been false since the self-update of 2026-11-13,
which runs `setup-coordinator.sh` and so `hypomnema tool install` at every release. The change that made the line
false did not update it, and the issue was filed without looking at zm. It was closed with no code (b60e3ea fixed
the line).

## What the references do

| Reference | How it knows what a running install has | Path |
|---|---|---|
| Hermes | `hermes doctor` looks at the machine now: tools on PATH and the package manager's current selection, connectivity, config, state; each check is a `Finding` with issues and a fix line; `--fix` repairs what it can. The tool check says: "Old facts are diagnostic evidence, not an available runtime tool." | `.tmp/hermes-agent/hermes_cli/subcommands/doctor.py`, `doctor_tools.py` (`_pm_tool_path`), `doctor_report.py` (`Finding`, `doctor_check`, `_fail_and_issue`), `doctor_live.py`, `doctor_state.py` |
| OpenMausBot | `doctor --host` "only looks": `/proc`, `/sys`, version queries, listening sockets, one time request, and on a server the connector's own `probe --json`. Each check is `{id, status: ok/warn/FAIL, message, fix}`; the output is one line per check with `fix:` under a non-ok one; the exit code says whether something is missing. | `.tmp/openmausbot/server/headless/host-doctor.ts` (`HostCheck`, `fail`, exit at line 258) |
| Coder | A deployment health report (`/health` page, and the same as JSON on the API): sections (access URL, database, DERP, websocket, workspace proxy, provisioners), each with a severity; the report's severity is the worst section's. Each failure has a code (`EACS01` …) documented with problem and solution. | `.tmp/coder/coderd/healthcheck/healthcheck.go` (`Report`, severities), `.tmp/coder/docs/admin/monitoring/health-check.md` |
| Coder (docs from code) | Reference docs (CLI, API) are generated from code; CI runs `make gen` and fails on a diff; a weekly job checks links. | `.tmp/coder/Makefile` (`gen/mark-fresh`), `.tmp/coder/.github/workflows/ci.yaml` (`check_unstaged.sh`), `weekly-docs.yaml` |
| OpenClaw | CI validates the config examples in its docs against the real schema. | `.tmp/openclaw/scripts/check-docs-config-examples.mts` |
| Hypomnema today | `ARCHITECTURE.md`'s deployed section states, by hand, what zm and the nodes have. Nothing checks the running system; `cmd/hypomnema` has no doctor or health command. | `ARCHITECTURE.md` (Deployed), `cmd/hypomnema/` |

## Common shape

- None of them keeps "the server has X now" as a fact in prose. A command or page asks the running system, read
  only, and says for each check: ok, warning or failure, what it saw, and how to fix it. The overall answer is the
  worst check, and the exit code (or HTTP answer) says it.
- What can be generated from code is generated and checked in CI (Coder, OpenClaw). Hypomnema already does this
  for its OpenAPI contract and sqlc code; prose about a server cannot be generated.

## Decision

The owner approved (2026-11-15):

1. An agent checks the live system before filing or planning an issue that rests on what is deployed, and writes
   the check (date, version, command) into the issue. Recorded in `AGENTS.md`.
2. A `hypomnema doctor` is designed in its own issue, after Hermes's `doctor`, OpenMausBot's `doctor --host` and
   Coder's health report; the deployed section of `ARCHITECTURE.md` then keeps what was set up and when it was
   checked, and points to the command for the current state.
