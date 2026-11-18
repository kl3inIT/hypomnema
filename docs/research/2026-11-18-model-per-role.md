# A model for each role

Date: 2026-11-18. Decision: owner, issue [#870](https://github.com/kl3inIT/hypomnema/issues/870).
The issue records the observations and recommendations from the two model-routing reports. The snapshot below
is what the issue reported on `zm` (v0.9.101), not a fresh check or a deployment. The second table is the target.

## Reported setup on zm

| Role | Primary | Fallback |
| --- | --- | --- |
| `implementer` | `claude-subscription/claude-opus-5-5` | `implementer-codex` |
| `implementer-codex` | `codex-subscription/gpt-6.2-sol` | — |
| `reviewer` | `codex-subscription/gpt-6.2-sol`, high | — (`reviewer-claude` only when named) |
| `explorer` | `claude-subscription/claude-sonnet-5-5` | — |
| `explorer-codex` | `codex-subscription/gpt-6.2-sol` | — |
| Main coordinator, VMAP, BeyondPilot | `claude-subscription/claude-opus-5-5` | — |
| Two Đóng góp VMAP Bots | `codex-subscription/gpt-6.1-sol` | — |

## Approved target

| Role | Primary | Fallbacks in order | From |
| --- | --- | --- | --- |
| Main coordinator Bot | `claude-subscription/claude-opus-5-5` | `codex-subscription/gpt-6.2-sol`, then `opencode-go/deepseek-v4.1-flash` | Owner; ordered backups as OpenMausBot and OpenClaw |
| Domain Bots: VMAP, BeyondPilot, Đóng góp VMAP | `codex-subscription/gpt-6.1-terra` | `claude-subscription/claude-sonnet-5-5`, then `opencode-go/deepseek-v4.1-flash` | Owner; same ordered backup rule |
| `implementer` | `claude-subscription/claude-opus-5-5` | `implementer-codex` | Polly's strong implementer |
| `implementer-codex` | `codex-subscription/gpt-6.2-sol` | — | Unchanged from the issue's snapshot |
| `implementer-quick` | omp + `opencode-go/deepseek-v4.1-flash` | `implementer` | oh-my-opencode `quick`; Gemini CLI's Flash for bounded tasks |
| `reviewer` (Claude author) | Codex + `codex-subscription/gpt-6.2-sol`, high | `reviewer-deepseek` | Polly's cross-vendor review; omp `@slow` reviewer |
| `reviewer-claude` (GPT author) | `claude-subscription/claude-opus-5-5`, high | `reviewer-deepseek` | Polly's cross-vendor review; omp `@slow` reviewer |
| `reviewer-deepseek` (last rung only) | omp + `opencode-go/deepseek-v4.1-flash`, high | — | Owner; another vendor when the independent first choice cannot run |
| `explorer` | `opencode-go/deepseek-v4.1-flash` | `claude-sonnet-5-5` | omp `@smol` scout; Hermes delegation; oh-my-opencode explore |
| `explorer-codex` | `codex-subscription/gpt-6.2-sol` | — | No change specified in the issue |

`implementer-quick` starts with 3–5 Tasks: docs, renames, a one-file fix, or a test after a pattern already in the
repository. Use `implementer` for everything else. A DeepSeek author's reviewer still needs to be GPT or Claude.
The owner allows VMAP and BeyondPilot to use DeepSeek after enabling **Global** under Console → Settings → Privacy
→ Regions. Free models are excluded by the owner; OpenCode Zen also requires its client for free-tier chat models.
The model IDs and tier decisions are the owner's, not claims about today's availability or price. The issue favors
V4.1 Flash over V4 Pro for image input, lower cost and a higher Go monthly limit. Revisit the choices once issue
#856 shows success and cost per profile. Applying this table on `zm` is a separate step.

The Bot page's ordered model picker with test data: [desktop](images/870/bot-fallback-models-desktop.png)
and [phone](images/870/bot-fallback-models-phone.png).

## References checked in source

| Mechanism | Source |
| --- | --- |
| Strongest model for hard changes and final review; a different vendor checks the author | [Omnigent Polly, `examples/polly/config.yaml:98,182`](https://github.com/omnigent-ai/omnigent/blob/a9061f1/examples/polly/config.yaml#L98) |
| Ordered Bot backups, with a limit of five and awareness of held models | [OpenMausBot, `src/components/bot-panel/BackupAis.tsx:24`](https://github.com/milind-soni/OpenMausBot/blob/5e7c989/src/components/bot-panel/BackupAis.tsx#L24), `server/host/limit-takeover.ts:128` |
| Ordered model candidates and skipping a resting provider | [OpenClaw, `src/agents/model-fallback-candidates.ts:371`](https://github.com/openclaw/openclaw/blob/52b0f4d/src/agents/model-fallback-candidates.ts#L371), `model-fallback-runner.ts:442` |
| Cheap scout, strong reviewer | omp, `packages/coding-agent/src/prompts/agents/scout.md:5`, `reviewer.md:6` |
| Inexpensive delegation and its own fallback chain | Hermes, `cli-config.yaml.example:1918,1960`, `agent/agent_init_fallback.py:12` |
| Cheap quick tier and DeepSeek Flash in explore | oh-my-opencode, `packages/model-core/src/category-model-requirements.ts:108`, `agent-model-requirements.ts:78` |
| Flash for simple, bounded tasks | Gemini CLI, `packages/core/src/routing/strategies/classifierStrategy.ts:36` |
| DeepSeek requires Global; free-tier Zen chat models require OpenCode | OpenCode, `services/site/src/docs/content/console/go.mdx:46`, `models.mdx:31`; see the [gateway research](2026-11-18-opencode-go-provider.md) |
