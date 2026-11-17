# Meta Muse for marketing, and a Growth studio pipeline

Date: 2026-11-17. The owner wants a marketing capability for VMAP (the map app): a "studio" that turns a phone into a
video factory, finds user insights, and can use Meta's Muse to research Facebook/Instagram. This note records what was
studied before any design. Reference clones are under `.tmp/muse-refs/` and `.tmp/growth-refs/` (git-ignored). "Inferred"
marks what no source states. The decision is not made; this is the comparison the owner asked to keep before closing the
session.

## 1. The question and the shape in Hypomnema's terms

"Muse là một worker đặc biệt đi tìm insight" is the owner's framing. In the glossary a Worker is a coding CLI that runs
one Dispatch and ends; that is the wrong term. The insight-finder is a **Bot** (long-lived, persona, memory, Routines),
provisionally **Growth Bot**. Muse is not a worker or a Bot; it is a **tool the Bot calls**, delivered as a new
**Integration** named Muse, the same way Zalo (`bridges/zalo`) and the `superset_*`/`whatsapp_*`/`zalo_*` tools are
exposed through `hypomnema mcp` (`ARCHITECTURE.md` line 96).

## 2. Meta Muse: what is official, what is not

- **Official API (OpenAI-compatible)**: `api.meta.ai/v1`, model `muse-spark-1.3` (and `sam-3.1`), used by Meta's own
  workshop `.tmp/muse-refs/legal-agent-workshop/HANDBOOK.md` (lines 47–48) via the OpenAI Python SDK. There is also a
  CLI, **Muse Code** (`dev.meta.ai/docs/muse-code`). This is the only supported programmatic path.
- **No official API for the Muse agent** (the thing that browses and uses Facebook/Instagram), and **no official video
  model** as of Oct 2026 (MoonReel README). The agent and video exist only inside the Muse app / muse.ai.
- **Built-in connectors**: Meta says Facebook, Instagram, Threads connect automatically when the accounts are in the
  same Accounts Centre (`.tmp/muse-refs/awesome-muse-connectors/connectors/README.md` line 11). Meta does not state the
  agent can read *other people's* groups or public posts, only the user's own connected accounts. **Inferred**: group
  research of the kind the owner wants is not a documented Muse capability.
- **Availability**: Muse launched in the US only, 18+, via the Muse app and WhatsApp. **Region block is per account,
  not per IP** (MoonReel README line 387). A VPN/proxy is **not** a fix and raises the odds of a verification challenge
  or account lock. **Inferred**: a Vietnam-registered Facebook account is likely blocked regardless of VPN.

### Controlling the Muse agent from code (unofficial)

All of these drive a logged-in muse.ai session, because the chat is an encrypted Noise WebSocket with no public API:

| Repo (`.tmp/muse-refs/`) | How | Risk |
|---|---|---|
| `Muse-Chat-MCP` | Playwright drives your own logged-in Chrome; exposes 7 MCP tools + an OpenAI shim + CLI | Fragile; your account, your ToS risk. No Facebook-group reading — generation/chat only (`manual/MUSE_MANUAL.md`) |
| `useapi/muse-api` | Hosted third-party REST + MCP (37 tools) on your Muse account | Paid middleman; hand your Meta account to a vendor |
| `czg86389-hub/muse2api` (1.4k★), `www222fff/muse2api` | Headless CDP → OpenAI-compatible API; multi-account rotation, 48h session renew, per-account proxy | Account rotation = ToS violation |
| `lichao199208/muse2api` (read in full) | **Protocol-level** gateway with a built-in **auto-registration machine**: email material in → auto-OTP (IMAP) → age verification paid from a **card pool** → TLS fingerprint spoofing (`curl_cffi`, `SIGNUP_TLS_IMPERSONATE=chrome`) → fake identities (`app/signup/identity.py`) → account pool. `docs/no-card-probe.md` probes 0-dollar order creation | **Do not use.** Fabricated identities, fingerprint spoofing, payment-bypass probing. Its own LICENSE forbids bulk registration and unauthorized access. Borders on payment fraud |

**Decision support**: Hypomnema's shared browser (`ARCHITECTURE.md` lines 414–420) is one headless Chrome on the
control plane with a persistent profile; a Bot already has open/snapshot/click/fill/type tools and the owner can take
over a tab in the web chat. So the *safe* way to test Muse is: owner signs a **secondary** Facebook account into
muse.ai in that shared browser once, by hand, then a skill types a research prompt and reads the answer — the
Muse-Chat-MCP pattern, but on infrastructure Hypomnema already has, no new reverse-engineering code. **Open question
(must test):** does muse.ai even load and let a VN account research Facebook from the control plane's IP.

## 3. Collect → insight → report (for the Growth Bot)

Read in `.tmp/growth-refs/`. None of these use Muse, and **none collect Facebook groups** (the closest is the cn fork's
logged-in Playwright bridge, which carries ban risk).

| Piece | Repo | License | Take |
|---|---|---|---|
| App-store reviews | `JoMingyu/google-play-scraper` (Python) | MIT | `reviews('com.google.android.apps.maps', lang='vi', country='vn', sort=NEWEST)` — keyless, cheapest reliable VN source for Google Maps / VietMap / VMAP |
| Multi-platform 30-day research | `mvanhorn/last30days-skill` | MIT | Reddit + YouTube (yt-dlp) + web are **free/keyless**; Instagram/Threads/TikTok via **ScrapeCreators** (paid, 10k free calls); Facebook = **Ad Library only** (`lib/meta_ads.py`). Packaged as a SKILL.md + Go MCP. Dedup (0.7 n-gram), URL rank-fusion, cluster uncertainty tags, synthesis prompt `references/synthesis.md`. Grok-Bot path shows the pattern: prefer a host's native platform tool first — **inferred** we can slot Muse in that way for Meta |
| Per-item LLM scoring | `obris-dev/openmagpie` | Apache-2.0 | Score each post `{score 0–1, reason}` with untrusted text fenced (`engines/openai_compat/prompts.py`); `source:external_id` identity; webhook digest; cron |
| Insight framework | `coreyhaines31/marketingskills` | MIT | `customer-research` skill: JTBD/pains/money-quotes, frequency×intensity, **3/2/1-source confidence table**, per-quote evidence table. Use as the report template |
| cn variant | `Jesseovo/last30days-skill-cn` | MIT | Logged-in Playwright bridge per source (`crawler_bridge.py`), union-find clustering, daily GitHub-Action hot board. The template for any Facebook-via-login attempt |

**Vietnamese gap (inferred)**: every dedup/cluster here special-cases only CJK; Vietnamese (diacritics, segmentation)
likely needs embedding-based clustering instead of lexical overlap.

## 4. Video studio

| Repo | License | Fit |
|---|---|---|
| `siri2moon/moonreel` (`.tmp/muse-refs/`) | MIT | Best structure model: topic → per-scene JSON script (`scriptSchema`) → `script_ready` approval → per-scene retry → ffmpeg concat + SRT → Shorts/caption generation; VI+EN UI, VN-aware prompts; `GenerationProvider` interface. But video source is Muse only |
| `harry0703/MoneyPrinterTurbo` | MIT | Run as the render service: **takes user-supplied local footage** (`video_source=local`), VN Azure/Edge voices (`vi-VN-HoaiMyNeural`, `vi-VN-NamMinhNeural`), Be Vietnam Pro font, subtitle realign to script, REST API + CLI + agent skill |
| `OpenMontageInc/OpenMontage` | AGPL-3.0 | Best human-in-the-loop model: per-stage `checkpoint_required` + `human_approval_default`, scene_plan with `screen_recording`/`talking_head` types and shot language. AGPL binds only if we modify and serve it; internal use is fine (inferred) |
| `RayVentura/ShortGPT` | MIT | Stale (Feb 2025); take only its timed search-query schema |

**What no repo does**: (1) a **filming brief / shot list for a human to shoot on a phone** — OpenMontage's scene_plan
has the fields but uses them to drive generation, not to brief a person; (2) matching uploaded phone clips back to
planned shots; (3) **Android emulator screen capture** (OpenMontage captures a desktop) — Hypomnema's own `adb`
path (`ARCHITECTURE.md` lines 633–649) plus `adb screenrecord` is the route (inferred). These are owner-specific and
must be built, kept because the owner asked for them (owner, 2026-11-17).

## 5. Posting + read-back (later)

`gitroomhq/postiz-app` (AGPL) has per-platform provider code worth reading as endpoint documentation: Facebook Page
(Graph v25.0, `pages_manage_posts`…), Instagram Reels (`instagram_content_publish`), TikTok Content Posting API v2
(`DIRECT_POST` vs inbox `UPLOAD`), YouTube Data API v3 + Analytics. Each `analytics()`/`postAnalytics()` shows the
weekly read-back metrics. **Every one needs our own developer app + platform App Review/audit** (inferred for Meta;
code only states TikTok's). `postiz-agent` CLI defaults to **publish**, has **no approval gate** — so the rule is: the
Bot only ever writes a draft, a human promotes it. **Recommendation**: thin per-platform adapters calling the APIs
directly (AGPL makes copying postiz code unwise; treat it as docs), unless the team later wants postiz's UI.

## 6. What to do first (proposed, not approved)

1. Owner manually registers / signs a **secondary** Facebook account into muse.ai (no VPN) and reports whether it loads
   and can research Facebook. This gates whether Muse is worth any integration work for insights.
2. Owner confirms whether VMAP has a Facebook Page + Instagram and who is admin (needed for official read-back later).
3. First build = **weekly insight report** on the free sources only (google-play-scraper + last30days Reddit/YouTube/web
   + Superset downloads + the Lark community chat), rendered as an artifact posted to the Lark community group. Facebook
   and video come after step 1's result.

## Sources

- Meta Muse: https://ai.meta.com/muse/ · https://about.fb.com/news/2026/09/introducing-muse · Muse Code:
  `dev.meta.ai/docs/muse-code` (not opened)
- Clones read: `.tmp/muse-refs/{legal-agent-workshop, awesome-muse-connectors, Muse-Chat-MCP, muse-api, moonreel,
  czg-muse2api, www-muse2api, lichao-muse2api}`;
  `.tmp/growth-refs/{last30days-skill, last30days-skill-cn, openmagpie, marketingskills, google-play-scraper,
  MoneyPrinterTurbo, OpenMontage, ShortGPT, postiz-app, postiz-agent}`

## 7. Result of the manual test, and the decision (later on 2026-11-17)

The owner signed in to muse.ai and asked about VMAP. Muse read the public posts of `facebook.com/groups/tasco.vmap`
(Cộng đồng Bản đồ VMAP Việt Nam) and answered with dated facts, without the owner's Facebook being connected: so the
agent does read public Facebook groups, which no API or repository above does. The owner approved the roadmap and step 1
as an Integration (not a skill on the shared browser, whose session every agent would share): issues
[#781](https://github.com/kl3inIT/hypomnema/issues/781) and [#782](https://github.com/kl3inIT/hypomnema/issues/782).
