# Workers talking to each other

Date: 2026-11-09. Read from the clones in `.tmp/`; paths are relative to each clone. Inferences are marked.

The question: today a Hypomnema worker talks only to the control plane (`ask`, `finish`), and its questions go to
the coordinator or the owner. Should a worker be able to message the worker of another Task, and if so, through
what?

## 1. What the references do

| Reference | Can one agent message another? | Path | Delivery | Limits |
|---|---|---|---|---|
| Orca | Yes. Any agent can `reference find` another agent and message it (`skill-guides/orca-cli.md` L178-185) | A supervised worker is reached only through its orchestration mailbox (`orchestration send --to dispatch:<id>`), never by typing into its terminal; `run:<id>` reaches a coordinator | A working worker gets the message when its current turn ends; a finished one gets it as a follow-up Task and reports with `worker_done` (`skill-guides/orchestration.md` L48-54) | A finished worker on a paired server refuses `send` |
| OpenClaw | Yes, on by default (`docs/concepts/multi-agent.md` L225) | `sessions_send` runs another session on the same Gateway and can wait for it (`docs/concepts/session-tool.md` L43); steering a running session is `sessions_send(mode="steer")` | Admitted inputs are durable (`session-tool.md` L151) | `tools.agentToAgent.enabled`, an `allow` list of agent pairs, `tools.sessions.visibility` |
| OpenMausBot | Yes, any bot reaches any bot on a team (`docs/team-cloud.md`, `canReach`) | One tool, `coordinate_bots`, through the harness (`docs/runtime-contract.md` §9) | `work` wakes the requester once on the result; `consult` answers and acts on nothing; `notify` is one-way; a message to a running thread is steered into its next step | Depth and budgets per root; refusal of work handed back to an ancestor; dedupe of the same brief |
| Omnara | People and other agents post input to the same agent, with an `actor` attribution (`docs/events/sending-input.mdx`, from [the client API note](2026-11-09-coordinator-and-client-api.md)) | The agent's event log | Appended to the log | — |

Every reference that allows it sends the message through the orchestrator's own record (a mailbox, a session's
admitted inputs, an assignment row, an event log). None lets two workers open a channel of their own.

OpenMausBot shows the cost of growing this without one record: it replaced "two delegation systems, six wake lanes,
a dozen clocks and ten trust wordings" with one `Assignment` record and one lifecycle (`docs/runtime-contract.md`).

## 2. When it helps (inference)

- Two Tasks running in parallel agree on one thing, such as an API shape between a backend and a frontend.
- A reviewer and an implementer go back and forth.
- A worker asks the worker of an earlier Task why something was done.

## 3. Risks (inference)

- Two workers ping-pong and spend the subscription's quota.
- Two workers settle a decision that the coordinator and the owner never see.
- A waits on B while B waits on A: a deadlock the reaper only sees as two stalls.
- A message to a worker that has already finished needs a new Dispatch, which is work the coordinator did not plan.

## 4. Consequence for Hypomnema

Allow it the way Orca and OpenClaw do: through the control plane's record, scoped to one Run, consult only by
default, with a limit per pair. The design is in issue
[#55](https://github.com/kl3inIT/hypomnema/issues/55) (inference: not approved).
