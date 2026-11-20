# Every model named with its provider

Date: 2026-11-20. Issue [#1002](https://github.com/kl3inIT/hypomnema/issues/1002): a Task, profile, role route,
Bot or chat names a model with its provider, `<provider>/<model>`. Until now a bare name was resolved to the one
provider that offered it and refused once two did (`orchestration/profiles.go` `checkModel`), so adding OpenCode
Zen (#965), which lists the models Claude, Codex and Go already serve, would have changed or broken bare names Tasks
used (on zm in the week to 2026-11-20: `claude-opus-5-5` 9 times, `deepseek-v4-pro`, `deepseek-v4.1-flash`,
`claude-haiku-5-5`). The owner chose on 2026-11-20 that every model be named with its provider, built in the same
pull request as #965.

## Sources

| Source | What was read |
|---|---|
| 9router (`github.com/decolua/9router`, `1855aa4`, cloned into `.tmp/`) | `open-sse/services/model.js` |
| omniroute (`github.com/diegosouzapw/OmniRoute`, `07a790f`) | `open-sse/services/model.ts` |
| LiteLLM (`github.com/BerriAI/litellm`, `758d7d6`) | `litellm/litellm_core_utils/get_llm_provider_logic.py` |

## What the references do with a bare name

| Reference | `provider/model` | A bare name | Two providers offer it | None does |
|---|---|---|---|---|
| 9router | read at the first slash, the provider's alias resolved (`model.js:34-46`) | an alias the user mapped to `provider/model` (`:61-87,104-116`), else the provider read from the name's prefix (`:118-140`) | first prefix rule wins | falls back to `openai` |
| omniroute | read at the first slash (`model.ts:435-507`, `getModelInfoCore` `:850-859`) | an alias the user mapped, then the active catalogs (`:843-960`) | refused: "Ambiguous model … Use provider/model prefix (ex: …)" (`:784-797`) | refused: "Use a provider/model prefix (e.g. openai/…)" (`:828-836`) |
| LiteLLM | the prefix names the provider (`get_llm_provider_logic.py:296`) | looked up in its lists of known models (`:457-470`); its proxy serves a `model_name` the config maps to `provider/model` | first list wins | refused: "LLM Provider NOT provided … Pass model as E.g. … `huggingface/starcoder`" (`:584`) |

Their common default: `provider/model` is the canonical name, read at its first slash; a bare name is accepted only
as an alias the owner declared or by guessing from catalogs and prefixes, and omniroute and LiteLLM refuse one they
cannot place with the form to write it in.

## What was built, and from where

| Decision | From |
|---|---|
| A model is named with its provider wherever work names one; a Task's own model is `<provider>/<model>`, read at its first slash (`modelRef`) | the references' canonical form; 9router `parseModel` |
| A bare name is refused, never guessed from which provider offers it, with the names it could be written as | omniroute's and LiteLLM's refusals; departs from their guessing because a provider added later would change what a stored name means (owner, 2026-11-20) |
| The gateway's own table names stay: a client may call one by its name, which names its targets' providers | LiteLLM's proxy `model_name`, 9router's and omniroute's aliases |
| A migration gives each Task's bare model the provider the code before dispatched it under, read as that code read it (the role-chain entry of its last Dispatch, else the one provider offering the whole name to the Task's runtime, among the kinds before Zen), in Go right after its SQL migration; any other is left, not guessed, marked so it is read whole and refused as the code before refused it, and its Task waits with the reason without holding the others; the way back also strips the provider of Tasks made after the upgrade; a profile version, Bot or chat without a provider is left as the code before ran it, an edit leaving its model alone gives it the one provider offering it, and a profile version is restored by its id with the provider it recorded | issue #1002's plan; reviews of PR #1000 |
| `bots set --model provider/model` names both, beside `--provider` | the same form as `tasks create --model` |

## Gateway clients

Every client the gateway serves is already given prefixed names or table names, so none sends a bare one: a
Dispatch's model is `<provider>/<model>` (`orchestration` `GatewayModelName`); Claude Code gets it as
`ANTHROPIC_MODEL` and as its background model `ANTHROPIC_DEFAULT_HAIKU_MODEL` (`node/claude/runner.go:213`,
`shared/agentconfig/files.go:82`); Codex picks from the gateway's catalog of its own names
(`modelgateway/codex_catalog.go`); omp, opencode and pi are given the gateway's list (`agentconfig`). The gateway
already serves a subscription's model only as `<provider>/<model>` (`modelgateway/gateway.go` `GetModel`), and a test
shows a bare name reaches no provider on chat, Messages and Responses. 9router notes that the Codex CLI sends the bare
`codex-auto-review` for its auto-review (`model.js:127-128`); the gateway does not serve that name today either.
