---
name: slides
description: "Slides, presentations and decks: build an animated HTML slide deck from scratch or from a PowerPoint (.pptx) and show it as an artifact. Use when the user wants a presentation, a deck, slides for a talk or pitch, or a PPT/PPTX turned into slides. Not for dashboards or single web pages."
license: MIT
---

# Frontend Slides

Create zero-dependency, animation-rich HTML presentations that run entirely in the browser, shown to the owner as an artifact (`hypomnema artifact show`).

The files named below are installed with this skill in `~/.agents/skills/slides`:

```bash
S=~/.agents/skills/slides
```

Read them with `read` (`$S/viewport-base.css`, `$S/STYLE_PRESETS.md`, ...); run its script with the `bash` tool.

## Core Principles

1. **Zero Dependencies** — Single HTML files with inline CSS/JS. No npm, no build tools.
2. **Show, Don't Tell** — Generate visual previews, not abstract choices. People discover what they want by seeing it.
3. **Distinctive Design** — No generic "AI slop." Every presentation must feel custom-crafted.
4. **Progressive Disclosure** — Read the lightweight style presets first; read the generation references only when generating.
5. **Fixed 16:9 Stage (NON-NEGOTIABLE)** — Every deck uses a 1920×1080 slide canvas scaled as a whole to the viewport. Slides must stay 16:9 on every screen, including phones. Do not reflow slide content to fit the device.
6. **Nothing From the Network (NON-NEGOTIABLE)** — The artifact runs in a locked frame with no storage and no same origin. Fonts are system font stacks, never a `<link>` or `@import` to a font service; no script, style or image is loaded from another host; nothing is kept in `localStorage`, `sessionStorage` or cookies.

## Design Aesthetics

You tend to converge toward generic, "on distribution" outputs. In frontend design, this creates what users call the "AI slop" aesthetic. Avoid this: make creative, distinctive frontends that surprise and delight.

Focus on:

- Typography: Choose type that is beautiful, unique, and interesting. Use the system font stacks in [STYLE_PRESETS.md](STYLE_PRESETS.md) ("System Font Stacks") and make them distinctive with weight, size, case, tracking and the contrast between display and body; never settle for a bare `system-ui` or Arial look.
- Color & Theme: Commit to a cohesive aesthetic. Use CSS variables for consistency. Dominant colors with sharp accents outperform timid, evenly-distributed palettes. Draw from IDE themes and cultural aesthetics for inspiration.
- Motion: Use animations for effects and micro-interactions. Prioritize CSS-only solutions for HTML. Use Motion library for React when available. Focus on high-impact moments: one well-orchestrated page load with staggered reveals (animation-delay) creates more delight than scattered micro-interactions.
- Backgrounds: Create atmosphere and depth rather than defaulting to solid colors. Layer CSS gradients, use geometric patterns, or add contextual effects that match the overall aesthetic.

Avoid generic AI-generated aesthetics:

- Overused font families (Inter, Roboto, Arial, a bare `system-ui`) as the display face
- Cliched color schemes (particularly purple gradients on white backgrounds)
- Predictable layouts and component patterns
- Cookie-cutter design that lacks context-specific character

Interpret creatively and make unexpected choices that feel genuinely designed for the context. Vary between light and dark themes, different font stacks, different aesthetics. You still tend to converge on common choices across generations. Avoid this: it is critical that you think outside the box!

## Fixed Stage Rules

These invariants apply to EVERY slide in EVERY presentation:

- Every deck has a viewport wrapper that fills the browser window.
- Every slide is authored inside a fixed 1920×1080 stage.
- The stage scales uniformly to fit the viewport. It may letterbox/pillarbox; it must not re-layout content.
- Do not use responsive breakpoints to rearrange slide content for phones.
- Use fixed internal slide measurements at the 1920×1080 design size.
- Slide visibility must be controlled by `.active` / `.visible` using `visibility`, `opacity`, and `pointer-events` from `viewport-base.css`. Do not use `display: none` / `display: block` for slide switching; later layout classes such as `.slide-content { display: flex; }` can override them and make every slide visible at once.
- Use `clamp()` only for non-slide UI outside the stage, or for small fallback previews where a full stage is impractical.
- Include `prefers-reduced-motion` support
- Never negate CSS functions directly (`-clamp()`, `-min()`, `-max()` are silently ignored) — use `calc(-1 * clamp(...))` instead

**When generating, read `$S/viewport-base.css` and include its full contents in every presentation.**

### Content Density Modes

Ask the user whether this is primarily a reading deck or a speaking deck, then design around that answer:

| Density mode | Best for | Design behavior |
| ------------- | -------- | --------------- |
| **Low density / speaker-led** | Public talks, keynote-style sharing, live explanation | One idea per slide, large type, strong visual hierarchy, generous negative space, 1-3 bullets max, more slides if needed |
| **High density / reading-first** | Reports, handouts, async review, detailed internal docs | More self-contained slides, structured grids/tables/annotations, 4-8 bullets or 4-6 cards when readable, tighter but still intentional spacing |

Baseline limits still apply: no scrolling, no overflow, no overlapping panels, and no text below comfortable reading size. If content exceeds the selected density mode, split it into more slides instead of shrinking until it becomes cramped.

---

## Phase 0: Detect Mode

Determine what the user wants:

- **Mode A: New Presentation** — Create from scratch. Go to Phase 1.
- **Mode B: PPT Conversion** — Convert a .pptx file. Go to Phase 4.
- **Mode C: Enhancement** — Improve an existing HTML presentation. Read it, understand it, enhance. **Follow Mode C modification rules below.**

### Mode C: Modification Rules

When enhancing existing presentations, fixed-stage fitting is the biggest risk:

1. **Before adding content:** Count existing elements, check against density limits
2. **Adding images:** Fit them inside the 1920×1080 slide canvas. If slide already has max content, split into two slides
3. **Adding text:** Max 4-6 bullets per slide. Exceeds limits? Split into continuation slides
4. **After ANY modification, verify:** the slide stage remains 16:9, no text overflows its card, and no panels overlap. You cannot render the page in your box: check the sizes in the HTML against the 1920×1080 stage, and ask the owner to page through the artifact on a computer and a phone
5. **Proactively reorganize:** If modifications will cause overflow, automatically split content and inform the user. Don't wait to be asked

**When adding images to existing slides:** Move image to a new slide or reduce other content first. Never add images without checking if existing content already fills the 1920×1080 slide stage.

---

## Phase 1: Content Discovery (New Presentations)

**Ask ALL questions together** so the user fills everything out at once. If the current environment provides a native structured-question UI, use it; otherwise ask in one concise message with clearly numbered choices:

**Question 1 — Purpose** (header: "Purpose"):
What is this presentation for? Options: Pitch deck / Teaching-Tutorial / Conference talk / Internal presentation

**Question 2 — Length** (header: "Length"):
Approximately how many slides? Options: Short 5-10 / Medium 10-20 / Long 20+

**Question 3 — Content** (header: "Content"):
Do you have content ready? Options: All content ready / Rough notes / Topic only

**Question 4 — Density** (header: "Density"):
How dense should the deck feel? Options:

- "Low density / speaker-led" — Big ideas, fewer words, more visual breathing room
- "High density / reading-first" — More self-contained detail for async reading

**Do not add inline editing.** The artifact's frame keeps no storage and saves no file, so edits made in it would be lost; the user asks for changes and you revise the deck.

Remember the user's density choice. It affects slide count, typography scale, amount of text per slide, layout density, and whether to favor cinematic presenter slides or self-contained reading slides.

If user has content, ask them to share it.

### Step 1.2: Image Evaluation (if images provided)

If user selected "No images" → skip to Phase 2.

If user provides an image folder:

1. **Scan** — List all image files (.png, .jpg, .svg, .webp, etc.)
2. **Inspect each image** — Open it with `read`, which shows an image. If it cannot be read, use filenames/metadata and ask the user to clarify only when needed
3. **Evaluate** — For each: what it shows, USABLE or NOT USABLE (with reason), what concept it represents, dominant colors
4. **Co-design the outline** — Curated images inform slide structure alongside text. This is NOT "plan slides then add images" — design around both from the start (e.g., 3 screenshots → 3 feature slides, 1 logo → title/closing slide)
5. **Confirm the outline** using the same structured-question mechanism when available: "Does this slide outline and image selection look right?" Options: Looks good / Adjust images / Adjust outline

**Logo in previews:** If a usable logo was identified, embed it (base64, a `data:` URL) into each style preview in Phase 2 — the user sees their brand styled three different ways. Keep it small: one HTML page is at most 256 KB.

---

## Phase 2: Style Discovery

**This is the "show, don't tell" phase.** Most people can't articulate design preferences in words.

### Step 2.0: Generate 3 Style Previews Directly

Based on purpose, audience, mood, and content density, generate 3 distinct single-slide HTML previews showing typography, colors, animation, and overall aesthetic.

Do not ask the user whether they want options or a preset picker. The default discovery experience is always visual comparison.

If the user already gave a vibe, use it. If they did not, infer the likely mood from the occasion, audience, content, and stakes. Keep the options diverse enough that the user can react visually instead of needing to articulate taste up front.

If the user explicitly names a preset, honor that as one option and generate the remaining preview slots around it.

Read [STYLE_PRESETS.md](STYLE_PRESETS.md) for preset candidates and the system font stacks.

| Mood                | Suggested Presets                                  |
| ------------------- | -------------------------------------------------- |
| Impressed/Confident | Bold Signal, Electric Studio, Dark Botanical       |
| Excited/Energized   | Creative Voltage, Neon Cyber, Split Pastel         |
| Calm/Focused        | Notebook Tabs, Paper & Ink, Swiss Modern           |
| Inspired/Moved      | Dark Botanical, Vintage Editorial, Pastel Geometry |

**Preview mix rules:**

- Generate 3 previews by default: 1 safe preset from `STYLE_PRESETS.md`, 1 bolder preset from `STYLE_PRESETS.md` that contrasts with it, and 1 wildcard.
- The wildcard may be either a third preset or a self-generated custom design. Choose whichever creates the strongest, most useful contrast for the user's occasion, audience, mood, and content.
- Do not force every expressive option to come from the presets. If the brief has a sharper, more specific design opportunity than the presets, use the wildcard slot to design freely.
- For conservative or high-stakes decks, make the safe preset especially restrained; choose a calm second preset; make the wildcard either another restrained preset or a custom design that feels authoritative rather than decorative.
- For expressive decks, keep the safe preset as a readable fallback; choose one strong second preset; make the wildcard adventurous, context-specific, and clearly different from both other previews.

**Custom wildcard design rules:**

- Follow the Design Aesthetics section above: no generic "AI slop", no default font/color/layout choices, no purple-gradient-on-white clichés, no cookie-cutter dashboard/card look.
- Match the user's stated occasion, audience, mood/vibe, and content density. The custom design should feel authored for this deck, not merely "stylish."
- Make a deliberate visual thesis: distinctive typography, a committed palette, a recognizable layout system, and one strong atmospheric or graphic device.
- Keep it feasible for a full deck. The preview must imply a design system that can expand into section, content, quote, comparison, and closing slides.
- Use fixed 1920×1080 stage rules and pass the same preview authenticity checks as every other option.
- Never render "custom", "wildcard", "AI-generated", or design-process labels on the slide itself.

**Preview authenticity rules (NON-NEGOTIABLE):**

- Every style preview must look like a real first slide from the user's deck, not a diagnostic card.
- Never render internal workflow text on a slide: no `preview`, `generated from`, `template`, `preset`, `style option`, `Option A/B/C`, file names, paths, or source-doc labels.
- Never render template names or slug names on the slide itself. Template/style names belong only in the message to the user.
- Never render user requirement notes as slide content, such as "sharp and provocative", "safe option", "bold option", "for internal sharing", or "audience: ...", unless the user explicitly wants that exact phrase to appear in the deck.
- If the slide needs chrome, use real deck chrome only: the deck title, section title, date, author, company, page number, or a genuine content phrase from the user's material.
- Before showing previews, inspect the visible text and revise if any internal metadata appears.

Save previews to `slide-previews/` in your directory (style-a.html, style-b.html, style-c.html). Each should be self-contained and compact, showing one animated title slide.

Show each preview to the user as an artifact:

```bash
hypomnema artifact show --title 'Style A: <name>' --html-file slide-previews/style-a.html
```

### Step 2.1: User Picks

Ask (header: "Style"):
Which style preview do you prefer? Options: Style A: [Name] / Style B: [Name] / Style C: [Name] / Mix elements

If "Mix elements", ask for specifics.

---

## Phase 3: Generate Presentation

Generate the full presentation using content from Phase 1 (text, or text + curated images) and style from Phase 2.

If images were provided, the slide outline already incorporates them from Step 1.2. If not, CSS-generated visuals (gradients, shapes, patterns) provide visual interest — this is a fully supported first-class path.

Apply the user's density choice throughout the deck:

- **Low density / speaker-led:** Use more slides with fewer ideas per slide. Favor large headings, short phrases, visual metaphors, section beats, quote/statement slides, and presenter-friendly pacing.
- **High density / reading-first:** Make slides more self-contained. Use structured grids, comparison tables, annotated diagrams, captions, and concise explanatory copy. Keep hierarchy strong so it feels designed, not like a document pasted onto slides.

If the user's stated needs are mixed, choose the closer of the two modes instead of inventing a middle option: live audience persuasion defaults low-density; async circulation or detailed review defaults high-density.

Never let high density become visual clutter. If a high-density slide starts to overflow, split it or redesign it into a clearer structure.

If the user selected a self-generated custom wildcard, treat that preview's CSS and layout as the design recipe:

- Preserve its fonts, palette, decorative vocabulary, spacing rhythm, grid logic, and component grammar.
- Expand the same visual system across the full deck. Do not switch to a preset after the user has chosen the custom direction.
- Design any missing slide layouts from that system rather than importing patterns from another style.
- Keep the output fixed-stage and single-file like every other deck.

**Before generating, read these supporting files:**

- [html-template.md](html-template.md) (`$S/html-template.md`) — HTML architecture and JS features
- [viewport-base.css](viewport-base.css) (`$S/viewport-base.css`) — Mandatory CSS (include in full)
- [animation-patterns.md](animation-patterns.md) (`$S/animation-patterns.md`) — Animation reference for the chosen feeling

**Key requirements:**

- Single self-contained HTML file, all CSS/JS inline, at most 256 KB; images that do not fit as small `data:` URLs go in an `assets/` folder beside it (see Phase 5)
- Include the FULL contents of viewport-base.css in the `<style>` block
- Use the system font stacks from `STYLE_PRESETS.md` — never a `<link>` or `@import` to a font service or any other host
- Nothing from the network and no `localStorage`, `sessionStorage` or cookies: the artifact's frame blocks them
- Add detailed comments explaining each section
- Every section needs a clear `/* === SECTION NAME === */` comment block

---

## Phase 4: PPT Conversion

When converting PowerPoint files:

1. **Extract content** — Run `python "$S/scripts/extract-pptx.py" <input.pptx> <deck_dir>`. `python` is the documents environment, first on PATH, which already has python-pptx (and Pillow); never `pip install` into it, it is read-only. The script writes `<deck_dir>/extracted-slides.json` and the slides' images to `<deck_dir>/assets/`
2. **Confirm with user** — Present extracted slide titles, content summaries, and image counts
3. **Style selection** — Proceed to Phase 2 for style discovery
4. **Generate HTML** — Write `<deck_dir>/index.html` in the chosen style, preserving all text, images (as relative `assets/<file>` paths), slide order, and speaker notes (as HTML comments). Delete `<deck_dir>/extracted-slides.json` and the images you did not use before showing the folder

---

## Phase 5: Delivery

1. **Clean up** — Delete `slide-previews/` if it exists
2. **Show** — Show the deck as an artifact:

   ```bash
   # one HTML file, at most 256 KB (small images as data: URLs)
   hypomnema artifact show --title '<deck title>' --html-file <name>.html
   # a folder with index.html at its top and its images in assets/, at most 5 MB
   hypomnema artifact show --title '<deck title>' --path <deck_dir>
   ```

   The command prints the artifact's id. To change the deck later, run it again with `--id <id>`: the owner sees the next revision in place; never make a second artifact for the same deck.
3. **Summarize** — Tell the user:
   - Style name, slide count
   - Navigation: click the slide, then Arrow keys, Space, Page Up/Down; swipe on a phone; the panel's Full screen button for presenting
   - Offer the natural post-draft action: ask for revisions, which you make in the file and show again with `--id`

---

## Phase 6: Share (Optional)

The artifact is the shared copy: it appears in this chat as a card the owner opens, and on Lark your next reply links to it. There is no export to PDF and no other hosting; say so if the user asks.

---

## Supporting Files

| File                                               | Purpose                                                              | When to Read              |
| -------------------------------------------------- | -------------------------------------------------------------------- | ------------------------- |
| [STYLE_PRESETS.md](STYLE_PRESETS.md)               | 12 curated visual presets with colors, font stacks, and signature elements | Phase 2 (style selection) |
| [viewport-base.css](viewport-base.css)             | Mandatory fixed-stage CSS — copy into every presentation             | Phase 3 (generation)      |
| [html-template.md](html-template.md)               | HTML structure, JS features, code quality standards                  | Phase 3 (generation)      |
| [animation-patterns.md](animation-patterns.md)     | CSS/JS animation snippets and effect-to-feeling guide                | Phase 3 (generation)      |
| [scripts/extract-pptx.py](scripts/extract-pptx.py) | Python script for PPT content extraction                             | Phase 4 (conversion)      |
