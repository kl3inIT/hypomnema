# Social listening: collecting what people say, for Bots (2026-11-27)

The owner asked (2026-11-27) for a standard way for Bots to find what real people say about a topic, across
Facebook groups, Instagram, Threads, TikTok, X, Reddit, YouTube, LinkedIn, app stores and Vietnamese forums.
It should have one logic and one UI, not an ad-hoc tool for each source. The case that raised it is a strategy
report about Tasco, Cốc Cốc and VETC, which needs the public's view and not only the press. This note widens step 2
of [#781](https://github.com/kl3inIT/hypomnema/issues/781) (Growth Bot) and builds on
[2026-11-17-muse-and-growth-studio.md](2026-11-17-muse-and-growth-studio.md).

Read in code on 2026-11-27, cloned in `.tmp/growth-refs/`:

- last30days-skill (MIT) and last30days-skill-cn (MIT)
- Agent-Reach (MIT)
- MediaCrawler ("Non-Commercial Learning License 1.1": read only, never copied)
- stickerdaniel/linkedin-mcp-server (Apache-2.0)
- obsei (Apache-2.0)
- harken (MIT)
- openmagpie
- google-play-scraper (MIT)
- BettaFish (GPL-2.0, architecture only)
- trend-opportunity-radar (MIT)
- marketingskills `customer-research` (MIT)
- postiz-app (AGPL, read as documentation only)

The vendor and official pages were read the same day (links in section 2). Paths below are relative to
`.tmp/growth-refs/`. *Inferred* marks a reading that is not written in a reference.

## 1. What the references are

| Reference | Layer it teaches | What it is |
|---|---|---|
| last30days | Whole pipeline, one-shot | The host model plans the query. A Python engine fetches each (subquery, source) stream, normalizes it, scores it, fuses streams with RRF, removes duplicates and clusters. The host model then writes the synthesis under strict rules (`skills/last30days/SKILL.md`, `references/synthesis.md`). |
| last30days-cn | Source registry, login sessions | A declarative `SourceSpec` registry (`scripts/lib/sources.py:15-70`), `LOGIN_SPECS` per platform (`crawler_bridge.py:77-112`), ordered fallback ladders, and a `via` label on every item. |
| Agent-Reach | Access layer | `Channel` → `Action` → ordered `backends` (`agent_reach/channels/base.py:30-118`). One envelope `{ok, items, error{code,hint}, meta.attempts}` (`runtime/result.py:85-142`), closed error codes, cooldowns, doctor and `live_test`, and one stealth browser per user. |
| MediaCrawler | Logged-in crawling of 7 Chinese platforms | Abstract crawler, login and store classes (`base/base_crawler.py:26-111`). The browser supplies cookies, and private APIs are called with signed headers (`media_platform/xhs/client.py`). It pseudonymizes users (`tools/user_hash.py`). None of its platforms is one we need. |
| linkedin-mcp-server | One logged-in source done carefully | Reads the rendered page only, with no private API (`docs/decisions/2026-09-16-rendered-page.md`). Throttling is detected from the HTTP status and barriers from URL patterns (`core/auth.py:28-35`, `core/throttle.py`). 12 typed error codes, and one call at a time (`sequential_tool_middleware.py`). Users report no bans in its issues, only rate limits and CAPTCHAs (#136, #1393). |
| obsei | Pipeline and storage for feedback | `Source.fetch(cursor)` (`core/protocols.py:50`). `Record` with a hashed identity (`core/record.py`). The cursor moves only after the store writes. PII is redacted before the store. Embedding themes with stable ids (`themes.py:164-191`). `ask` gives answers with citations. An MCP server and a voice-of-customer skill. |
| harken | Keyword monitor | `fetch_page(query, cursor, since)` and the `Mention` schema. Scan state per (query, source) with incremental and backfill cursors, plus `last_error` (`store.py:65-77`). Projects group queries. |
| openmagpie | Continuous feeds | `Connector.poll(spec, since)`, with a typed spec union stored as JSON and a watermark per source row. Item uniqueness is (feed, kind, external_id). Retention is set in days per feed. An LLM relevance gate. |
| BettaFish | Analysis and report | Parallel agents plus a moderator note every 5 speeches (`ForumEngine/monitor.py:50`). It is not a debate and has no stop rule. A report IR with 16 block types, a validator, and a monitoring template. |
| trend-opportunity-radar | Evidence rules | Each claim is labelled platform fact, machine extraction or model judgment. Counter-evidence is required. Partial sampling caps confidence. A single snapshot never shows a trend (`SKILL.md:110-138`). |
| marketingskills | Report shape | Confidence High, Medium or Low for 3, 2 or 1 independent sources. Quote tags. Themes ranked by frequency × intensity (`skills/customer-research/SKILL.md`). |
| postiz | Account UI | A provider tile grid. Connect by OAuth, by a form of fields, or by browser cookies. Card flags `refreshNeeded`, `inBetweenSteps` and `disabled`. Reconnect. The refresh failure is flagged atomically and notified once (`prisma/schema.prisma:370-413`). |

## 2. The best route for each platform (read 2026-11-27)

| Platform | Best | Then | Notes |
|---|---|---|---|
| Facebook groups, Pages, comments | Apify Facebook Groups and Comments scrapers, run by Apify | ScrapeCreators | About $2.6-5 per 1,000 posts and $1.4 per 1,000 comments. Each item has a link, date, author and reactions. Public groups only. Comments are top-level only. The Graph API reads only Pages you manage. The Meta Content Library is for researchers. `facebook-scraper` is dead (no push since 2024-06). |
| Instagram, TikTok | Apify actors | ScrapeCreators | Paid per result. TikTok's Research API is limited to non-profit researchers in a few regions. |
| Threads | Official keyword search | ScrapeCreators | Free. It needs the app-reviewed `threads_keyword_search` permission. 2,200 queries a day. No reply text. |
| X | Official pay-per-use API ($0.005 per post read) | twscrape | twscrape accounts were suspended from 2026-11-21 (its issues). |
| Reddit | Keyless JSON, the last30days default | Official API, approval-gated since 2025-11 | Datacenter IPs often get 403 (last30days-cn `SKILL.md` §4). Low volume for Vietnam (inferred). |
| YouTube comments | Data API v3 (10,000 units a day) | yt-dlp `--write-comments` | Free. |
| LinkedIn | linkedin-mcp-server pattern on a signed-in session | `site:linkedin.com/posts` web search, ScrapeCreators | The official API has no keyword search of others' posts. |
| Google Play | google-play-scraper | — | Per app, not by keyword. `at` is naive local time. It swallows errors as end of data (`features/reviews.py:113-125`). |
| Voz, Tinhte, Otofun; VnExpress comments | Web search, then a fetch of the thread | A small per-site reader | voz.vn is behind Cloudflare and its robots.txt reserves rights and refuses AI use. tinhte.vn's robots.txt blocks ClaudeBot by name. No reference covers these sites. |
| Meta, via Muse | Lead finding | — | Muse answers with a model's summary of public Facebook, Instagram and Threads content (Meta AI Mode, 2026-06). It gives no item list with links and counts, so its claims cannot be checked one by one. Use it to choose groups and keywords, then collect the evidence with a route above. |

No reference reads Facebook groups, Threads, TikTok or Vietnamese forums. Agent-Reach's Facebook channel is
experimental and covers search and profile only (`channels/facebook.py:16-36`). MediaCrawler covers Chinese
platforms only.

**Owner, 2026-11-27: no paid route is used.** Apify, ScrapeCreators and X's paid API are out. What is left:

| Platform | Free routes, in order |
|---|---|
| Facebook | Muse with a fixed template (each post with its link, date and a verbatim quote; asked again per group for depth), then `site:facebook.com` web search |
| Instagram | Muse |
| Threads | The official keyword search, then Muse |
| TikTok | davidteather/TikTok-Api (MIT, 6.8k stars, pushed 2026-11-14; unofficial, a browser and an `ms_token` cookie), then yt-dlp, then `site:tiktok.com` web search |
| X | A signed-in X account's cookies (`auth_token`, `ct0`), then the owner's API key when he has one, then the signed-in Grok CLI (`grok_x.py`), then `site:x.com` web search. The cookie route is the common default: it heads last30days' auto chain `bird → xai → xurl → xquik` (`env.py:1575`, `bird_x.py`: X's GraphQL with the account's cookies), and openmagpie (twikit), Agent-Reach (opencli) and twscrape read X the same way. Only harken uses the official API v2 (`sources/x.py`). Use a secondary account: twscrape users report suspensions from 2026-11-21. |
| YouTube | Data API, then yt-dlp |
| Reddit | Keyless JSON, then the official API |
| LinkedIn | A signed-in account, then `site:linkedin.com/posts` web search |
| Google Play | google-play-scraper |
| Forums | Web search, then a fetch of the thread |

Muse returns links when asked (owner, 2026-11-27), so Facebook and Instagram get item-level evidence through it.
Items found by a model (Muse, Grok) are checked before they count: the link is opened and must hold the quote, and
the date is checked (grok_x validates each post's date from its snowflake id, `_validate_items`, because "its output
can be confidently wrong"). An item that fails stays as "unverified". Engagement counts are missing on that route.

## 3. What the references agree on

| Concern | Common default | From |
|---|---|---|
| Source contract | One adapter per source with a typed config. It returns normalized items. A failure stays inside the adapter. | All of obsei, harken, openmagpie, last30days-cn, Agent-Reach |
| Several ways to reach one platform | An ordered chain of backends. Each has a side-effect-free probe and the flags `paid` and `opt_in`. The first usable backend wins. The backend that served is named. | Agent-Reach `backends`, last30days `BackendSpec`/`ChainDescriptor` (`backends.py:104-160`), last30days-cn `via` |
| Failure classes | A closed set: need_login, rate_limited, blocked or restricted, payment_required, not_found, timeout, broken. A login or block stops that account until a person acts. A rate limit means a cooldown, not a retry. | Agent-Reach `FINAL_CODES` (`result.py:33`), linkedin `ErrorCode` (`error_handler.py:65-84`), last30days `health.py:28-55` |
| Partial coverage | A failed source is never reported as "nobody talks about it". Only a clean zero is a zero. | last30days `synthesis.md:180`, last30days-cn §5, radar `sampling-contract.md` |
| Item | source, backend, native id, url, author, container (group or subreddit), published_at plus date confidence, text, engagement (a per-source dict), parent (for comments), query, fetched_at | last30days `SourceItem` (`schema.py:81-103`), obsei `Record`, harken `Mention`, Agent-Reach item |
| Identity and dedup | A hash of (source, native id) under a unique key. Then a normalized URL. Then near-duplicates by Jaccard similarity of at least 0.7 within a source. | obsei `record.py:74-76`, harken `models.py:72-74`, last30days `fusion.py:31-50`, `dedupe.py:75-151` |
| Ranking and clusters | RRF across streams, engagement normalized per source, clusters with `single-source` and `thin-evidence` tags. | last30days `fusion.py:499`, `signals.py:303-410`, `cluster.py:61-248` |
| Who plans and writes | The agent's own model plans and writes. Code fetches, scores and clusters. | last30days LAW 7, obsei (LLM only to enrich and ask), radar |
| Report check | One synthesis pass under hard evidence gates, then one reviewer pass. Not a standing debate. | last30days, radar. BettaFish's forum has no stop rule and no evaluation (`monitor.py`). |
| Saved topics | A cursor or watermark per (topic, source). It moves only after the store writes, and the boundary item may come again. | obsei `pipeline.py:202`, harken `store.py:65-77`, openmagpie `polling.py:56-90` |
| Health on screen | Per source: state, last success, last error, items collected. A failure is flagged once and notified once. | postiz `refreshNeeded`, harken `source_metrics`, BettaFish `crawling_tasks`, Agent-Reach doctor |
| Pacing for a logged-in account | One call at a time per account and a minimum gap per site. Stop for 24 hours after a challenge. | linkedin `AGENTS.md:209-223`, Agent-Reach `browser_binding.py:12-17`, MediaCrawler `MAX_CONCURRENCY_NUM=1` |

Where they differ:

- **Raw authors versus pseudonyms.** obsei and MediaCrawler hash authors; harken and openmagpie store them raw.
- **Stable theme ids.** Only obsei keeps them.
- **Vietnamese text.** No reference handles it. last30days tokenizes Latin text with English stopwords and does no
  diacritic folding. A lexical cluster will be weak for Vietnamese; embeddings, as obsei uses, are the likely fix
  (inferred).

## 3b. More to learn than the sources

**Threads, free.** Muse first: whether Meta AI reads Threads is reported both ways, so test it once Muse is signed in
again. Then `site:threads.com` web search. Then the official keyword search once Meta grants `threads_keyword_search`;
without that grant it finds only the account's own posts. last30days reads Threads through ScrapeCreators
(`scripts/lib/threads.py`), which is paid.

**Probes on 2026-11-27 (owner's Muse account `1`, signed in that day):**

- **Muse on Threads.** Asked for Threads posts about Cốc Cốc, VETC and VMAP from the last 30 days. The first answer
  stopped mid-work ("sent 10 links to open…"). A follow-up in the same thread returned 10 posts. Each post had an
  author, a date, a verbatim quote (marked as caption, image text or comment) and likes and replies where seen. Two
  links were opened with curl, and the `og:description` matched the quotes (`@minhtiensg/post/DfUomw4iepN`,
  `@coccoc.official/post/Dfe40MVAZjj`). Links come wrapped as `l.meta.ai/?u=…`, so the `u` parameter must be
  unwrapped. Muse also declined to pad: it said that no VMAP post was found.
- **Grok through the gateway.** `opencode-go/grok-4.7` on `/v1/chat/completions` answered "I can't search X live". X
  search is a server-side tool of xAI's own API and of the Grok CLI. OpenCode Go does not pass it on. X therefore
  needs the owner's X API key, an xAI key, or a signed-in Grok CLI.

**Setup falls into three kinds; the logic is the same for all:**

| Kind | Sources | Setup, after |
|---|---|---|
| None | Reddit JSON, yt-dlp, Google Play, App Store, HN, Bluesky, web search, forum fetch, Telegram preview | On/off and a daily live test (web search `source: public`; Agent-Reach `check`) |
| API key | YouTube Data API, Threads API, X API, xAI | Add a key to the vault and rest on a daily limit (the web-search keyed providers) |
| Signed-in account | Muse, X, LinkedIn, Grok CLI, TikTok's `ms_token` cookie | Sign in through the owner's tab of the shared Chrome. Cookies go to the vault, accounts are kept in order, and the card shows "sign in again" (the Muse screen; postiz `refreshNeeded`) |

**Other sources the references read** (last30days `scripts/lib/`). Each is listed with the free route where there is
one:

| Source | What it shows | Where it is read |
|---|---|---|
| Meta Ad Library | What a brand pays to say, with video scripts. The public Ad Library page is readable in a browser (inferred). | `meta_ads.py`, through ScrapeCreators |
| Job posts | What a competitor hires for, which hints at what it builds | `jobs.py`, `hiring_signals.py` |
| Patents | Long-term technology direction | `patents.py` |
| News timeline | Events, and who reported them | radar-intelligence (Google News, GDELT); `techmeme.py` |
| Telegram public channels | Channel posts. The public preview at `t.me/s/<channel>` is readable (inferred). | `telegram.py`, through ScrapeCreators |
| App Store and Trustpilot reviews | App and company reviews | `trustpilot.py`; obsei |
| Hacker News, Bluesky, GitHub | Developer discussion, keyless | last30days, harken, obsei |

**Logic worth taking:**

| Idea | Learned from |
|---|---|
| A pre-flight check for keyword traps: rewrite the query or ask one question before running | runbook step 0.45 |
| Resolve where people talk before searching: handles, communities, channels, competitors, found with 2-3 web searches | runbook 0.5-0.55, `competitors.py` |
| Snowball: mine new handles and hashtags from the first results and search again | `entity_extract.py` |
| Query intent sets the subqueries, the clustering and the supplements | `planner.py`; radar's five decision profiles |
| Retry thin sources with a simpler query | `_retry_thin_sources` |
| 2-3 capped web-search supplements after collection | runbook step 2 |
| Count before filtering; cap confidence on partial sampling | radar `sampling-contract.md` |
| Quote tags: pain, trigger, outcome, objection, competitor | marketingskills `source-guides.md` |
| History across runs before any trend claim; a cross-platform hot board | `--store`; cn `--hot`; radar |
| Ask over the stored items with citations, plus a judge model | obsei `ask.py` |
| A daily live test per backend, and a "regressed" flag | Agent-Reach `check`, `regressed()` |

## 4. How it maps onto Hypomnema

Hypomnema already has three gateway capabilities backed by pools:

| Pool | Code | Screen |
|---|---|---|
| web search | `internal/controlplane/websearch/chain.go` | `web/src/features/models/web-search-screen.tsx` |
| images | — | `features/models/images-screen.tsx` |
| Muse | `internal/controlplane/muse/pool.go` | `features/muse/muse-screen.tsx` |

Each has an order of providers or accounts, an on/off switch, a state (`ready`, `resting`, `not_set_up`, `refused`,
`needs_sign_in`), secrets in the vault, and a tool answer that names who served and who failed
(`websearch/websearch.go:144-152`).

How each new route fits:

- **A keyed source** (Apify, ScrapeCreators, YouTube, X): the keyed-provider pattern of web search.
- **A signed-in source** (LinkedIn, later others): the Muse pattern. That is a cookie jar in the vault and its own
  context of the shared Chrome (`browser/contexts.go`), with sign-in through the owner's tab (`OpenOwnerTab`). Muse's
  code is specific to muse.ai, so the cookie and context part becomes shared (inferred).
- **A keyless source** (Reddit JSON, Google Play, yt-dlp): `source: public` links, as web search has.

Two gaps today:

- Bots cannot reach an outside MCP server (`ARCHITECTURE.md:1931`), so linkedin-mcp-server's behaviour is learned,
  not plugged in.
- A skill can require only `integration` or `device` (`ARCHITECTURE.md:2026-2036`), so it cannot say "needs a
  listening source".

## 5. Recommended architecture

```
Bot ── skill "social-listening" (plan, synthesis rules, report shape)
  │
  └─ tool listen_search(query plan) ─► control plane: Listening capability
        ├─ source registry: platform → ordered backends {keyless | key | signed-in account | Muse}
        ├─ each backend: probe, paid/opt-in, pacing, typed failure → state + cooldown
        ├─ normalize → Item; dedup; engagement per source; RRF; clusters + uncertainty tags
        └─ answer: clusters + items (link, date, author, quote, engagement) + coverage per source
  (later) saved Topics: cursor per (topic, source), weekly Routine, Postgres items, retention days
UI: Settings ▸ Listening — one card per platform, its backends in order, state badge,
    reconnect / add key / sign in, last success, last error, items this week
```

| Decision | Choice | From |
|---|---|---|
| Where it lives | A fourth gateway capability beside web search, images and Muse, with one tool for every Bot and worker. It is not an Integration with a Connection per Bot. | Hypomnema's own pools (#782 moved Muse the same way, owner 2026-11-18) |
| Source and backend model | Platform → ordered backends with probe, paid, opt_in and pacing. The answer names the backend that served and lists each tried one with its failure code. | Agent-Reach, last30days `BackendSpec`, websearch `Errors` |
| Failure codes | need_login, refused or restricted, rate_limited (cooldown), payment_required, not_found, timeout, broken, no_results | Agent-Reach, linkedin, last30days |
| Item and dedup | The item in §3. Unique (source, native id). Then a URL. Then near-duplicates at 0.7. | last30days, obsei, harken |
| Ranking and clusters | RRF plus normalized engagement. Clusters tagged `single-source` and `thin-evidence`. Embedding similarity for Vietnamese. | last30days; obsei (embeddings) |
| Planning and writing | The Bot's model plans the subqueries and writes the report. The tool does no prose. | last30days LAW 7 |
| Report | Answer first with at most 3 actions. Then what was read and what is missing. Then 1 to 3 findings, each with support and counter-evidence, a High/Medium/Low confidence and what it cannot show. Then verbatim quotes with link and date. Then risks and next focus. Each claim is labelled fact, extraction or judgment. One reviewer pass. | radar, marketingskills, BettaFish template, last30days synthesis |
| Muse and Grok | Asked with a fixed template for items (link, date, verbatim quote). Each item is checked by opening its link and its date; one that fails stays "unverified". | #781 step 2 (the Muse template); last30days `grok_x.py` |
| Signed-in accounts | A cookie jar in the vault and its own Chrome context. One call at a time, a minimum gap per site, and a 24-hour stop after a challenge. Sign-in through the owner's tab. | Muse pool, linkedin `AGENTS.md:209-223`, Agent-Reach |
| Screen | A card per platform, after the Muse and web-search screens, with postiz's states (needs sign-in, reconnect, off). Adds last success, last error and items this week. | postiz, harken, BettaFish `crawling_tasks`, the Machines page cards |
| Saved topics (later) | A cursor per (topic, source), moved after the store writes. Items in Postgres, retention in days, a weekly Routine. | obsei, harken, openmagpie |
| Left out | A multi-agent forum, PII redaction before store, alerts and spikes | BettaFish shows no gain. Redaction and spike alerts appear in one reference each (obsei), so they wait for a need. |

## 6. Steps

1. **Now, without code.** The Bot that writes the Tasco report uses `web_search` (including `site:linkedin.com/posts`
   and `site:voz.vn`), Muse once it is signed in again, and Google Play reviews. It applies the report rules in §5.
2. **The capability and its free sources.** Web, YouTube (Data API or yt-dlp), Reddit keyless, Google Play, and Muse as
   a backend. Includes the `listen_search` tool, the skill and the screen.
3. **Sources that need approval or a sign-in.** The Threads API (Meta's review of `threads_keyword_search`) and a
   LinkedIn signed-in account. No paid route (owner, 2026-11-27).
4. **Saved topics.** A weekly Routine and the stored items.

Open for the owner:

- Whether to read Facebook groups through a signed-in account, which no reference does.
- Which LinkedIn account to sign in.
- Whether Vietnamese forums are read beyond web search, given voz.vn's reservation.
