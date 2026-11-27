# Templates of a Studio video: title and end cards, loudness, and a library of use cases

Issue #1230 (owner, 2026-11-27: "a library of templates for the common cases"). The references are three MIT
projects, cloned into `.tmp/screenrec/`: aidemo (tandryukha/aidemo, commit b67235a), agent-demo-video
(OrionArchitekton/agent-demo-video, de61695) and autodemo (mdayan8/autodemo, 73e5408). This note keeps the issue's
comparison, the design the owner approved on 2026-11-27, and what each built-in template is learned from.

## What the references do

| | aidemo | agent-demo-video | autodemo |
|---|---|---|---|
| Script skeleton | `aidemo init` writes a starter storyboard: search, results, act, confirm; 45-60 s; 4-6 scenes at about 2.5 words a second (`src/starter.ts:10-141`) | `DEMO_SCRIPT.md` of `### SHOT` blocks, with samples in `demos/` (`docs/guide/script-format.md:3-25`); no init found | one scenes JSON, with samples in `examples/` |
| Use-case skeletons | blog prose only: problem, walkthrough, proof, CTA with word budgets (`docs/blog/demo-video-script-template/index.md:20-45`); 30 s feature announcement (`feature-announcement-video/index.md:25-29`); lengths per surface (`how-long-should-a-demo-video-be/index.md:29-33`) | none | none |
| Title and end cards | `intro`/`outro {title, subtitle, durationMs 2600, fadeMs 350, accent, background}`; narration and captions shift by the intro; music runs under the cards (`src/types.ts:769-792`, `docs/POLISH.md:25-28`) | `brand {title, subtitle, url, hook, titleSec 2.2, endSec 3.0, accent}`; the end card shows the title, then the URL (`src/types.ts:269-290`) | `title`/`subtitle` make the intro (2.2 s); `cta {text, url}` makes the outro (2.6 s), 0.5 s fade (`src/render.js:9-10,199-222`) |
| Hook on the first frame | none | `brand.hook`, 1-60 characters; a Shorts render with no hook gives a warning (`docs/guide/quality-checks.md:28,38`) | none |
| Output presets | `youtube` 1920x1080, `short` 1080x1920, `readme-gif`, `x` (`src/types.ts:826-838`); store presets, `app-store-preview` 886x1920 (`src/store/presets.ts:44-50`) | `platform: landscape \| shorts` (`docs/guide/config.md:10`) | `output.vertical` |
| Loudness | with music, the master is normalized to -16 LUFS / -1.5 dBTP (`docs/AUTHORING.md:400-406`) | none found | none found |
| Narration rate | 2.5 words a second for English, measured or interpolated for 17 other languages, none for Vietnamese (`src/lint.ts:63-80`); lint estimates a scene's length from its words (`src/lint.ts:199-215`) | none found | none found |

What all three share: a title card and an end card of about 2-3 s, each with a title, a subtitle and one URL or
call to action, in an accent colour; a vertical preset given by name; narration at about 2.5 words a second.

None of them ships a library of named, selectable use-case templates. aidemo has one starter in code and gives its
use-case skeletons as prose in its blog.

## The approved design

| # | Decision | From |
|---|---|---|
| 1 | A video can have a title card and an end card: `{title, subtitle?, cta_url?, duration_ms}`, 2.6 s and 3.0 s by default, 350 ms fade, on the look's background in its accent; narration and captions start after the title card, the music plays under both. | aidemo `intro`/`outro`; agent-demo-video `brand`; autodemo `cta` |
| 2 | With music, the mix is normalized to -16 LUFS integrated / -1.5 dBTP; the warning stays for a mix that still misses it. | aidemo `AUTHORING.md:400-406` |
| 3 | A template is a stored document: name, use case, target length, aspect, sections each with a purpose and a word budget at 2.5 words a second, card defaults and a look to start from. Applying it fills a new video's look and cards and gives the agent its skeleton. | aidemo `starter.ts`, kept as data; word budgets from its script template |
| 4 | The first templates are built in, one per row below; more are added as data. | aidemo's blog; owner, 2026-11-27, for having a library at all |
| 5 | The agent tools list templates and `video_make` takes an optional `template`; the Studio's make dialog gets a template picker. | inferred: no reference has a picker |

Left out: the first-frame hook line (agent-demo-video only), animated HTML cards (agent-demo-video `motion`),
cross-fades and multi-language variants (aidemo only).

## The built-in templates

Each is a JSON file of `internal/controlplane/videos/templates/`; its `from` field names the lines below.

| Template | Length | Aspect | Sections (words) | Cards | From |
|---|---|---|---|---|---|
| `app-ad` | 15-30 s | 9:16 | hook 10, step 1-3 15 each, proof 10 | end card: `Tải ứng dụng ngay` over the video's title | `social-cuts-for-product-launch/index.md:31-34,64` (9:16 at 1080x1920, 15-30 s, "no logo card"); `demo-video-script-template/index.md:20-45` |
| `feature-announcement` | 30 s | 9:16 or 16:9 | what shipped 18, why it matters 30, one click to try 25 | none | `feature-announcement-video/index.md:21-29` (three beats, "no logo card"; the ask is spoken) |
| `product-walkthrough` | 60-90 s | 16:9 | problem 30, promise 20, step 1-3 35 each, proof 25, call to action 15 | title card: the video's title; end card: `Dùng thử ngay` over it | `demo-video-script-template/index.md:20-45`; `demo-video-call-to-action/index.md:12` (the ask spoken, then on the end card) |
| `onboarding` | 20-60 s | 16:9 | goal 10, step 1-3 15 each, result 10 | none | `product-onboarding-video/index.md:25-30` (lengths); the steps' and the result's words are app-ad's (inferred: the post gives only the goal's) |
| `app-store-preview` | 15-30 s | 886x1920 | step 1-3 15 each | none; frame full, no captions | `app-preview-video/index.md:9,25-29,62` (Apple's 886x1920, 15-30 s, screen captures of the app only); aidemo `src/store/presets.ts:44-50`; the steps' words are app-ad's (inferred) |

Choices made while building, each inferred where no reference has it:

- A template's look holds only the sections its use case needs (the App Store preview's full frame) and its cards;
  the rest is the owner's saved default, so a template keeps the owner's caption style and background. The
  references' looks are per project, not per use case, so none says which style an ad should have.
- A template's cards carry their words, `{title}` in them the video's title (the take's on its first make). The
  title card is the video's title, as aidemo's starter titles its intro with the project's name
  (`src/starter.ts:68-72`); the end card is a call to action over the video's title, as aidemo's outro puts the call
  to action over the domain (`src/starter.ts:73-76`) and agent-demo-video's end card shows the brand's title
  (`src/types.ts:269-290`). The words are Vietnamese, as the videos are spoken: `Tải ứng dụng ngay` ("download the
  app now") for `app-ad`, `Dùng thử ngay` ("try it now") for `product-walkthrough` (inferred: no reference says
  them). Words longer than a card holds (80 characters for a title, 120 for a subtitle) are cut at a space in their
  second half (inferred). Until review round 1 of #1246 the end card held the video's title alone, with no call to
  action.
- `video_make` takes only `template` beside the take, as the issue says; an agent cannot write a card's words, which
  the owner changes in Change look. Its answer counts the take's words at 2.5 a second with the cards against the
  template's length, as aidemo's lint estimates a storyboard (`src/lint.ts:199-215`).
- 2.5 words a second is aidemo's English figure; its lint has no Vietnamese rate, so the issue's rate is kept until a
  made video measures one.
- The App Store's size is a third aspect, `886x1920`, beside 9:16 (720x1280) and 16:9 (1280x720), at Apple's exact
  pixels as aidemo's preset exports them.
