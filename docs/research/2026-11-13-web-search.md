# Web search and fetch for every Bot and worker

Date: 2026-11-13. For issue #354. Read in the clones under `.tmp/` by two scouts: the gateways OmniRoute
(`diegosouzapw/OmniRoute` at `bf8a11d`, cloned for this note), 9router, 9router-go, CLIProxyAPI, LiteLLM, new-api,
Envoy ai-gateway, Coder's aibridge and Bifrost; the agent products OpenClaw, Hermes, omp, opencode, Codex and
OpenMausBot; pi. Prices and free tiers came from web searches (2026-11), not from vendors' own pages, and conflict;
they are marked as such.

## 1. What Hypomnema had

- A Bot (pi) had no web tool; pi's core has none (`pi/packages/coding-agent/examples/extensions/plan-mode/index.ts:223`
  only says "use brave-search skill via bash").
- A Claude Code worker has Anthropic's WebSearch and WebFetch; a Codex worker has OpenAI's hosted `web_search`, which
  the gateway's `/v1/responses` passthrough keeps (`modelgateway/codex/passthrough.go:146-172`). Codex has no fetch
  tool (`codex/core/src/tools/hosted_spec.rs`).
- omp and opencode reach the gateway as chat completions (`internal/node/acp/agents.go`), whose Codex adapter does not
  carry hosted tools, so a Codex subscription gives them Codex's model and not its search. omp's own `web_search`
  starts with Parallel's keyless MCP from the node; its `web/hosted` step searches through the session's own model and
  credential (`omp/packages/coding-agent/src/web/search/index.ts:164-181`), which is the gateway's model there.
  opencode's `websearch` asks the user to pick a provider in a form Hypomnema does not show (inference); its
  `webfetch` works.

## 2. Agent products

| | Runs in | Search provider | Fetch | Cap | Cache | Private addresses |
|---|---|---|---|---|---|---|
| OpenClaw | the gateway server | the first provider with a key (Brave, Gemini, … SearXNG), or a pinned one | markdown first, Readability, then Firecrawl | 20,000 chars | 15 min | refused, every redirect checked |
| Hermes | the agent | the first key, then a keyless ring (Exa, Parallel, Firecrawl…) | the vendor's extract | 15,000 chars, head and tail | 20 min | refused, every redirect checked |
| omp | the agent | Parallel keyless, the model's own search, Exa, SearXNG, scrapers | in-process HTML to markdown, trafilatura, Jina | 300 lines | none found | none found |
| opencode | the agent | chosen in a form (Exa keyless) | htmlparser2 to markdown | 5 MB | none found | URL permission rules |
| Codex, OpenMausBot | the model's vendor | the vendor's own | none, or the vendor's | — | — | — |

OpenClaw keeps Claude Code's WebSearch by default and turns it off (`--disallowedTools WebSearch`) only when it serves
its own (`src/agents/cli-runner/prepare.web-search.test.ts`). Files: OpenClaw `src/agents/tools/web-fetch.ts`,
`web-search.ts`, `src/infra/net/fetch-guard.ts`; Hermes `tools/web_tools.py`, `url_safety.py`; omp
`src/web/search/index.ts`, `providers/*.ts`, `src/tools/fetch.ts`; opencode `packages/core/src/tool/plugin/websearch.ts`.

## 3. Gateways

Four have their own search: OmniRoute, 9router, 9router-go and LiteLLM; CLIProxyAPI and new-api pass Codex's own
search through; ai-gateway, aibridge and Bifrost only carry native search tools.

- **OmniRoute**, the most complete: `POST /v1/search` and `POST /v1/web/fetch` with normalized results
  (`open-sse/handlers/search.ts`, `src/app/api/v1/web/fetch/route.ts`); about 25 providers
  (`open-sse/config/searchRegistry.ts`); with none named, the keyed providers cheapest first, SearXNG when its address
  is set, Codex's search when opted in, DuckDuckGo last; a named provider has no fallback; a 429 on a borrowed key locks
  only its search (`borrowedKeyLock.ts`); cache 60 s to 5 min, coalesced (`services/searchCache.ts`); redirects by hand,
  at most 5, checked each hop, credentials dropped across origins (`handlers/search/redirectGuard.ts`); cost per key as
  model `<provider>/search`; REST, MCP tools and chat-tool interception.
- **9router**: the same endpoints; the provider is the model, combos chain them; backoff 2 s·2ⁿ up to 5 min
  (`open-sse/config/errorConfig.js:32`); a subscription's own search as a provider (`handlers/search/chatSearch.js`);
  taught to agents by skills (`skills/9router-web-search/SKILL.md`).
- **9router-go**: `/v1/search`, `/v1/scrape`, `/v1/web/fetch` forwarded raw (`internal/handlers/media/media.go`).
- **LiteLLM**: `/v1/search/{tool}` over named search tools, cost per query, interception of native tools.

## 4. Providers, as the web said in 2026-11 (unverified against vendors)

| Provider | Free | Without signing up | Quality in published comparisons |
|---|---|---|---|
| Exa | keyless public MCP; with a key about $10 a month of credit | yes | among the best; one November test had it answer 30 of 30 |
| Parallel | keyless public MCP; about $1 per 1,000 paid | yes | among the best |
| SearXNG | free, self-hosted | after it is run | depends on the engines it scrapes |
| Tavily, Firecrawl | 1,000 credits a month | no | middle; Firecrawl is strongest at reading pages |
| Brave | no true free tier now: $5 per 1,000 with $5 a month of credit, card required | no | varies widely |
| Anthropic's own search | $10 per 1,000 on the API; in a Claude Code subscription | — | good; for that model only |

Exa does not publish its keyless limit; a 429 says to add a key (omp `providers/exa.ts:348`).

## 5. What the owner chose (2026-11-13)

The gateway's own `/v1/search` and `/v1/web/fetch` (OmniRoute); Exa with a key, SearXNG when set, Exa and Parallel
keyless, in that order; a 429 rests the provider (9router's backoff, OmniRoute's search-only lock); fetch with markdown
first and a readable extract, 20,000 characters (OpenClaw); cache 5 minutes (OmniRoute). Claude Code keeps its own
search and fetch; Codex keeps its search and gets fetch; Bots, omp and opencode get both, with omp's and opencode's own
web tools turned off. A subscription's search as a provider (OmniRoute, 9router) is left for later: it spends the
subscription's quota. Design: issue #354.
