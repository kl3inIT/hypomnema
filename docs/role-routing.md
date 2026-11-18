# Worker role routing

The [default mapping](../internal/controlplane/orchestration/role_routing.yaml) selects ordered runtime and model
entries for a worker role and Task kind. `hypomnema profiles routing` prints the effective YAML. The owner can
replace it with `hypomnema profiles routing-apply <file>`: every entry is validated against the provider's model
catalog before the whole mapping is stored. The built-in mapping can refer to models that a server has not yet
discovered; an unavailable model is reported on the Task, not silently substituted.

While trying omp, Sonnet leads the `quick` implementer and explorer chains. For the owner's trial, run three to
five explicitly pinned omp/DeepSeek Tasks with these roles before moving it to the head. If the provider no longer
offers a model, or no worker advertises the runtime, leave that entry unavailable and report the reason. The
model catalog, not the mapping, says which models a provider can serve.
The current OpenCode Go adapter cools accounts across all its models, so both of its entries wait when the
shared account pool cools.

`hypomnema profiles metrics --json` reports two weeks of settled attempts, successes, failures and priced cost by
role, kind and chain position (with model and runtime, so a changed order does not conflate different models).
Use `--since` for a different period. Costs use the existing Dispatch usage ledger (#856), at API prices; entries
with unpriced usage are counted separately. A Dispatch without a chain entry ran with a legacy profile.

## Owner-confirmed cutover

No migration modifies the existing worker profiles, Bots or stored memories. Legacy profiles with a runtime
continue to use that configuration. In a separate owner-approved step, check active Tasks and the model catalog,
export and reconcile the old profiles' memories into the corresponding roles, and apply the four YAML files in
[`deploy/profiles`](../deploy/profiles). These new versions omit runtime, model and profile fallbacks. The role's
memory is shared across its chain; previously started Dispatches keep their original profile versions. Update the
Bots' worker lists before retiring the old profiles. Keep the old profile versions for their historical Tasks.

The Bot model changes also belong to that step. Use the existing `hypomnema bots set <bot> --provider <provider>
--model <model> --fallbacks <provider/model,...>` from #877, not the worker map:

| Bot | Primary | Ordered fallbacks |
| --- | --- | --- |
| Coordinator, VMAP, BeyondPilot | `claude-subscription/claude-opus-5-5` | `codex-subscription/gpt-6.2-sol`, `opencode-go/deepseek-v4.1-flash` |
| Both Đóng góp VMAP Bots | `codex-subscription/gpt-6.1-terra` | `claude-subscription/claude-sonnet-5-5`, `opencode-go/deepseek-v4.1-flash` |

The control plane names a chat from the first owner's message or file, without a model
(`internal/controlplane/orchestration/chats.go`, `titleOf`). The Bot uses its session model for compaction,
not a separate utility model: `internal/controlplane/coordinator/usage.go` sends pi's `compact` command, and pi
1.5.2 takes `this.model` in `packages/coding-agent/src/core/agent-session.ts:2923-2936,2987-2994`.

## Prompt sources

Inline source comments mark the rules adapted from omp, Codex and Omnigent and the behavior learned from
oh-my-opencode. The latter is cited for its approach, with original wording here, not redistributed prompt text.

- omp (`can1357/oh-my-pi` at `08cb444`), MIT: [license and copyright](../third_party/omp/LICENSE).
- Codex (`openai/codex` at `daa59eb`), Apache 2.0: OpenAI Codex, Copyright 2025 OpenAI.
- Omnigent (`omnigent-ai/omnigent` at `4cd6dc5`), Apache 2.0: Copyright 2026 Databricks, Inc. This software
  includes software developed at Databricks and its use is subject to the included
  [Apache 2.0 license](../third_party/openmausbot/LICENSE).
- oh-my-opencode (`code-yeongyu/oh-my-opencode` at `be84607`), Sustainable Use License 1.0: references to its
  plan-consultant, plan-reviewer and category rules identify the source of those ideas.
