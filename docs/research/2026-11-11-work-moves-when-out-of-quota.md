# Moving work to another engine when one is out of quota

Date: 2026-11-11. Asked by the owner after the roles were settled (issue #155): with four kinds of worker (Claude
Code, Codex, omp, opencode), what should happen to a Task whose kind is out of quota. Read by one scout in the
clones under `.tmp/`: openmausbot, t3code, openclaw, omnigent, hermes-agent, orca, omp, coder, opencode, genie and
buzz, as cloned on 2026-11-10 and 2026-11-11; Hypomnema's own handling was read at `origin/main`. Paths are in those
repositories. Inferences are marked. Nothing is decided here.

How several accounts of one provider are rotated is another question, in
[several accounts](2026-11-10-several-accounts.md). This one is about another model, CLI or vendor.

## 1. Hypomnema today

- A worker that reports a rate limit ends its Dispatch as `rate_limited`. The account cools down until the reset the
  vendor named (a default when it named none), on every node; the Task goes back to ready and is held until that
  time; the failure is not counted (`orchestration/dispatch.go`, `FailureRateLimited`).
- The next Dispatch takes the first account of the same runtime that is not cooling down
  (`orchestration/placement.go`, `pickAccount`). A long cooldown that leaves the runtime with no free account is
  escalated once (`account_cooling`).
- A worker whose model the gateway serves has no account on a node; the gateway rotates its own accounts and tries a
  model's next target (`ARCHITECTURE.md`, the model gateway).
- Nothing moves a Task to another runtime. The coordinator's guide tells the coordinator not to: "Do not move it to
  another profile or model on your own" (issue #127).

So a Task on Claude Code whose only account is limited for the week waits for the week, while Codex is free.

## 2. The references

Five move work by themselves: OpenMausBot, t3code, OpenClaw, Hermes and omp. Orca offers it as a button. Omnigent,
Coder, opencode, genie and Buzz have nothing comparable for quota.

| | What triggers it | Who sets the order | What a backup must be | When, and what happens to the conversation | Going back | What the person sees | Guards |
|---|---|---|---|---|---|---|---|
| OpenMausBot | The CLI's own limit frame (`status` rejected, not on overage), told apart from a 429, which is retried 3 times at 1, 3 and 8 s; also a sign-out. The reset is the CLI's `resetsAt`, else 1 h | The person: per bot, then the install's backup, then a ranking (Claude, ChatGPT, Gemini, Grok) (`server/host/limit-takeover.ts` L172-194) | Ready, not held, same tools and workspace, an approval level that carries over; never "another sign-in to the same subscription engine" | At the start of a turn, or for a message that never started; an accepted prompt is not moved, since "the engine might have performed actions". Only that thread switches | Before each turn, once the reset has passed; not when the person picked another model since | One notice per stretch: "Codex reached its limit until 14:00, so Kai is using Claude meanwhile." Nobody is asked; a switch turns it off | A pay-per-use engine only if the person chose it; no switch that needs a wider grant; each backup once per turn |
| t3code | A usage window at 100% with a reset in the future (`packages/shared/src/usageLimits.ts` L25-36) | The person: one fallback model ("Used for new threads when the default model's provider is out of usage") | Enabled, installed, signed in, not itself exhausted | New threads only, and never when the person chose the model. A running thread waits for the reset | Not applicable (inference): the choice is made when a thread is created | A banner, "Using {fallback}", with "use default" | One hop |
| OpenClaw | Auth failures, rate limits, overload, timeouts, billing; a weekly limit counts as a rate limit (`docs/concepts/model-failover.md` L453, L519-531) | Config: rotation within a provider first, then `agents.defaults.model.fallbacks`; an agent may set its own, `[]` for none; a model the user picked is strict | Not cooling down | Only with "evidence that replaying the original attempt is safe"; the user's turn is reused "exactly once"; not after committed tool effects | The next turn; a fallback stays first for 10 to 30 minutes | "↪️ Model Fallback: … (selected …; reason)" and its clearing | Each candidate once a turn; 16 attempts before rotating |
| Hermes | 429 or 5xx after its retries; 401, 403, 404 at once | The person: an ordered `fallback_providers`; delegated children have their own list | Not the backend that just failed; entitled | In the middle of a turn; the history is kept and "continues from exactly where it left off"; the prompt cache is lost | The next message, unless the reset is still ahead | "⚠️ Model fallback: … unavailable (…); using …", then "✅ Primary model restored" | Once per turn; never billing another balance "behind your back" |
| omp | 429, quota walls, outages; `retry.fallbackOn` may be `usage-limit` only (`docs/settings.md` L598-608) | Chains matched by specificity: the model, the provider, the role, the default; sub-agents have their own | Not cooling down | In the middle of a turn | At the cooldown's end, or never (`fallbackRevertPolicy`) | An event; `confirm` asks before the reserve is spent | A hop uses a retry; `fail-closed` refuses the reserve |
| Orca | A usage limit | The person, by hand: "Switch Account" or "Hand it off to another agent instead" | | A fresh session with a note: "The prior provider session is read-only context… Inspect the current repository state… Treat workspace files as authoritative" (`src/renderer/src/lib/agent-session-continuation.ts` L92-111) | "continue at {time}" on a schedule | Buttons | A breaker after three failed starts: "never route around the breaker" |

Omnigent's family fallbacks pick something the server can serve when routing, and "never slides down to a cheaper
tier" (`server/smart_routing.py` L1262-1313); Polly's prompt says of a worker that cannot start, "drop it from the
roster and do NOT re-dispatch". genie raises a model one step for quality, not quota (`skills/fix/SKILL.md` L31-33).

Common to the five:

- **The person orders the backups.** No product lets the coordinating model choose the backup for a quota reason.
- **A quota limit is not a short error.** It is told apart from a 429 that a retry clears, and its reset time comes
  from the vendor.
- **Work returns** when the reset has passed.
- **The switch is said**, in one line, and can be turned off by an empty list.

Where they differ is when the switch may happen: only for work that has not started (OpenMausBot, t3code), only when
a replay is safe (OpenClaw), or in the middle of a turn with the history kept (Hermes, omp, which run one process
that holds the conversation).

None checks that a backup's model is at least as strong as the one it replaces.

## 3. Read for Hypomnema

All inference, for the owner to decide.

- The unit that would move is a Task, and what it moves between are profiles; a profile is already where the owner
  sets a role's CLI and model (issue #155). An ordered list of backup profiles on a profile is OpenMausBot's per-bot
  list and OpenClaw's per-agent `fallbacks`.
- The trigger exists: `rate_limited` with a reset time, apart from a worker's error. The question a profile already
  answers since issue #139, whether a worker can start under it now and until when its accounts wait, is the
  eligibility check the references run on a backup.
- A Task that has not started is the case OpenMausBot and t3code both allow. A Task whose worker was limited in the
  middle has a branch with its work; Orca's hand-off note ("Treat workspace files as authoritative") is the
  reference for continuing it under another CLI, and is a second step.
- The coordinator's guide already tells the coordinator not to move a Task on its own; with a list the owner set,
  the scheduler would do it, and the references agree that this choice is the person's.

## 4. Work already started (added later on 2026-11-11)

Asked by the owner after the first step (issue #171) was built: how the references continue work that was under
way when the limit came. Orca and OpenMausBot were read by the session; OpenClaw, Hermes, omp and t3code by a scout.

A correction to section 2: OpenMausBot does move a turn that a usage limit refused. Its rule that an accepted
prompt is not moved ("the engine might have performed actions") is for a start that failed or went silent
(`docs/verification/automatic-recovery.md`). For a limit, `turnFailed` moves the thread to the first backup and
sends the person's own message again there, with the thread's history (`server/quota-switch.ts` L150-190,
L198-245): only the person's own request, never a routine's, a webhook's or a relayed one, and not when a question
or a card waits in the thread. So issue #171, which moves only a Task with no Conversation, is stricter than
OpenMausBot.

| | What goes to the backup | What keeps work from being done twice | What the backup is told |
|---|---|---|---|
| OpenMausBot | The thread's own history, kept by the product, and the person's message sent again | Only the person's own request is sent again; a thread that is working, or holds a waiting question or card, is switched and nothing is resent | Nothing found for the model; the person reads "continuing on {name} after hitting the usage limit" |
| OpenClaw | The saved transcript with finished tool results; ids and thinking reshaped for the provider (`replay-history.ts`) | A one-way flag per run: a tool with side effects, a spawned session or an async start makes the run unsafe to replay, and "model fallback still require[s] evidence that replaying the original attempt is safe" (`replay-state.ts` L21-51, `docs/concepts/model-failover.md` L471) | Nothing for the model |
| Hermes | The whole message list, the client swapped in place; the prompt cache is lost | No gate; a tool result already in the history is not run again | The `Model:` and `Provider:` lines of the system prompt |
| omp | The session's history, the failed assistant message dropped; signatures stripped across providers | "Safe only before committed text, images, tool calls, or server tools"; a tool batch runs again only when all of it failed (`turn-recovery.ts` L1580-1593, L209) | Nothing for the model. Per-call task workers run with fallback off (`task/executor.ts` L4935) |
| Orca (by hand) | A new session of another CLI in the same workspace, with the path of the old transcript or a bounded copy of it, the last prompt and the last assistant message | The note: "Inspect the current repository state, including git status and the relevant files. Treat workspace files as authoritative if they differ from the transcript." | "Continue work from the prior Orca session… The prior provider session is read-only context… Briefly state where the previous session stopped. If work remains, continue it." (`agent-session-continuation.ts` L92-111) |
| t3code | Nothing by itself: a thread that met a limit waits and is sent "Continue where you left off." after the reset, once (`UsageLimitRecoveryWorker.ts`). A switch by hand sends a summary within a budget and a tool to read the history; the worktree stays | The resume's command id makes it idempotent | "Context handoff (...): From thread X, runs a-b. Recover history with t3_thread_read…" (`ContextHandoffDelivery.ts` L45-53) |

Two families. OpenClaw, Hermes and omp call a model's API themselves, so they pass the message list to another
model inside one process. Orca and t3code drive whole CLIs, as Hypomnema does: a CLI's session cannot be given to
another CLI, so the next one starts fresh in the same working directory with a note and a way to read what
happened. OpenMausBot is between: it keeps the conversation itself and sends the request again.

Read for Hypomnema (inference): a worker is a CLI, so the hand-over is Orca's and t3code's, a new Conversation in
the same worktree with a note; the trigger and the resending of the Task's own spec are OpenMausBot's; the guard
against doing work twice is OpenClaw's and omp's, applied to what cannot be seen in the worktree (a pull request
already open, a write through the service proxy). A Conversation names one node and its worktree
(`orchestration/dispatch_types.go`), so the backup can only run where that worktree is.
