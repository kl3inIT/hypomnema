---
name: pages
description: "Builds a fast, good-looking page, a data dashboard, a report with charts or any other page (a landing page, a pricing page, a tool), and shows it to the owner in the chat as an artifact with the `show_artifact` tool. Use when the owner asks for a dashboard, a report, charts or KPIs on a page, a page or mock-up to look at, or a data page to keep up to date, for example from a Superset dashboard's figures, and when he asks whether a dashboard looks right."
---

# Data dashboards and pages

A page shown with the `show_artifact` tool appears in the chat as one card. The owner opens it beside the chat,
full screen or on its own page, and steps back through its revisions; on Lark your next reply links to it. To change
it, call the tool again with its `id` (the id the first call returned): it becomes the card's next revision. Never
make a second artifact for the same page.

| Call `show_artifact` with | Shows |
|---|---|
| `title`, `path: "index.html"` | one page, at most 256 KB |
| `title`, `path: "dashboard"` | a folder: `index.html` and its data, at most 5 MB |
| `id`, `path: "dashboard"` | the next revision of the same page |

## Pick the right home

| The owner wants | Build |
|---|---|
| A quick answer or one chart | No page. Read the figures (the `superset_chart_data` tool, see the `superset` skill) and answer with the number or a markdown table. |
| A small summary to look at | One page (a `path` to one HTML file) with the aggregated rows embedded as JSON. |
| Bigger data | A folder (a `path` to the folder): `index.html` and `data.json` beside it, the template's `CONFIG.dataFile`. Aggregate first all the same. |
| "Keep it up to date", "every morning" | A folder page and a Routine (see your instructions on Routines) whose script reads the figures again into its data, `hypomnema superset chart <dashboard> <chart> > dashboard/data.json`, and runs `hypomnema artifact show --id <id> --path dashboard`: a script runs commands, not tools. The card shows the new revision. |

The page is seen by the owner in Hypomnema only: there is no public link. Still, put in it only the rows the owner
asked for; a list of people is personal data (the `superset` skill).

## Start from the template

`dashboard.html` is beside this SKILL.md, in this skill's folder, with the design references in `references/`
beside it. It runs in the artifact's locked frame, at phone and desktop widths, in light and dark. Copy it into your
directory as `index.html` and change only what the data needs:

1. `CONFIG`: `dataFile` (a folder page's data, `data.json`), `source`, `updated` (when the figures were read, the
   `computed_at` of `superset_chart_data`, ISO time), `currency` (`VND`, `USD`, …), `sample` (`false` for real
   data), `notes` (a report's notes, or empty).
2. `prepare`: map your columns to the fields the page uses. The sample has `date`, `region`, `category`, `product`,
   `orders`, `units`, `revenue`, one row a month, region and product.
3. `AGGREGATES`, the `render…` functions and `check`: your questions, and the sums that must agree.
4. Headings, labels and tooltips in the HTML and the script, in the owner's language.
5. Replace the sample rows between the `embedded-data` tags with your data (or leave the tag empty in a folder page
   and write `data.json`).

Keep the rest: the colour tokens with their dark values, the KPI row and its deltas, the charts drawn as SVG by
`lineChart` and `barChart`, `Intl` formatting, the one tooltip, the loading, empty and error states, aria labels and
the "Show the numbers" tables. Cut cards rather than adding a fifth chart.

**The locked frame.** The page runs sandboxed, in an origin of its own. Make it self-contained: inline `<style>` and
`<script>`, the system font, images as `data:` URLs, charts as inline SVG or CSS. Load nothing from the network: no
CDN script or stylesheet, no chart library, no web font, no `fetch` but a folder page's own files. There are no
cookies and no `localStorage` or `sessionStorage`; keep state in variables. The page cannot call the chat or the
control plane. It can hand the viewer a file on a click: build it in the page (a CSV of the filtered table) or
`fetch` a file of its folder (a report the Routine wrote beside `index.html`), make a `Blob`, and click an `<a>`
whose `href` is `URL.createObjectURL(blob)` and whose `download` is the file's name. A plain link to a folder file
does not keep its name: the page's origin is not the files'.

## Data

- **From Superset.** Find the chart with the `superset` skill (`superset_dashboards` with a `search`, then
  `superset_dashboard`), then read it with `superset_chart_data` and write its answer to `chart.json`. Its `columns`
  and `rows` are what the template reads, as they are; a date may come as milliseconds since 1970 and is read as a
  day. Say in `CONFIG.source` which dashboard and chart, and in `CONFIG.updated` its `computed_at`.
- **Aggregate first.** Ship the smallest table that answers the questions: one row per day or month and dimension,
  not raw events. A chart's rows are already its aggregate; when they are not enough, ask for another chart rather
  than raw rows.
- **Size.** One page is at most 256 KB including the template (about 35 KB), so embed at most about 200 KB of JSON. A
  folder holds 5 MB in all.
- **Embedding.** The JSON goes inside `<script type="application/json">`: write `<\/` for any `</` in a value, so a
  value cannot close the tag.
- **Types.** Dates as `YYYY-MM-DD` text or milliseconds. Numbers as plain numbers, not formatted text.

## Build a dashboard

Each step is done before the next starts. The references are files in `references/` beside this SKILL.md; read the
one a step names when you reach it.

1. **Name the question.** Write the page's one question ("Are sales growing, and where?"), the 3–6 numbers that
   answer it, the problems as rules with numbers ("a region down more than 10% on the period before"), and what the
   owner would do about each. Work out the expected answers from the rows now, before any HTML (`references/qa.md`,
   Known totals).
2. **Pick a form for each number** (table below; `references/perception.md` for the encoding ladder, colour scales,
   heatmaps and bullet graphs), with a one-line reason each.
3. **Lay out the hierarchy**: header (title, "updated", filters) → KPI tiles → the main view → breakdowns → detail
   tables → notes (a report only) → source line. Most important top-left; cards in one row hold similar amounts;
   on a monitoring page the tiles and the main view fit above the fold (`references/layout.md`).
4. **Set the tokens** before any component: every colour a CSS variable with a light and a dark value
   (`references/tokens.md`; the template has them).
5. **Build each component from its pattern** (`references/components.md`), with the code in
   `references/snippets.md` where it fits: KPI and target tiles, delta, sparkline, ranked list, heatmap, bullet
   graph, share bar, tooltip.
6. **Design every state**: loading, filters applying (dimmed, never zeros), error (one page-level note), empty (`—`
   and one short line, never an all-clear), tiny bases, sample data (a chip).
7. **Run the QA pass** (`references/qa.md`): the numbers first, then the words audit and the salience test, then
   screenshots at phone, beside-the-chat and full width, light and dark.

**Choose a chart by the question** (more in `references/perception.md`):

| The question | Form |
|---|---|
| How is it changing over time? | Line; area only for a running total |
| Which is biggest? Rank them. | Horizontal bars, sorted, values at the tips |
| What is it made of? | Stacked bar or a segmented share bar; a donut only for one overwhelming share |
| How are values spread? | Histogram |
| Do two measures move together? | Scatter plot |
| One measure across two dimensions | Heatmap with classed tiers |
| Are we on target? | Bullet graph or a target tile |
| One number | KPI tile, not a chart |
| Many categories, exact values | Table |

Never: 3D, two y-axes (two charts, or index both series to 100), gauges and dials, pies with more than a few slices,
rainbow scales, a number on every point.

## Rules that apply everywhere

- **First glance.** What a reader gets in 3 seconds without hovering is designed first: the answer, then what needs
  attention.
- **Colour follows meaning.** One hue for the page's main entity (the accent), grey for the rest: unselected bars,
  previous periods, secondary series. A warm hue (`--warn`) or `--bad` only on a problem; intensity grows toward the
  problem, never toward "good". The six series colours (separate light and dark sets) are for categories, in
  order, each keeping its colour when filters change; fold anything past 6 into "Other". Green and red never carry
  meaning alone: ▲ ▼ and a word with them. Text is ink, never a series colour.
- **Context.** Every headline number answers "compared to what?" with one baseline: the previous period of the same
  length, a target, a normal range or its history, in a symbol and one or two words ("▲ 12% vs prev 3 mo").
- **Numbers.** Every number and date formatted with `Intl`; tabular figures, right-aligned in columns; as precise as
  the decision needs (₫5.2B on a tile, exact in the tooltip and the table); compact on axes; `<0.1%` below 0.1%; `—`
  for no data, never a fake 0.
- **Filters.** One or two, in one row above everything; every tile, chart and table follows them.
- **Comparison.** A previous period is a grey or dashed line; an unfinished period is dashed. A base under about 5
  reads "was 1" in grey, never a red −100%. Deltas are coloured by meaning: a lower-is-better metric (cost,
  complaints, churn) turns bad when it rises; a neutral one (spend, volume) stays grey.
- **Say it once.** A number shown twice agrees, and is a claim in one place only.
- **Attention lives on the data.** Flag the problem where it shows (a warm cell, a bad chip on a row), not in a
  sentence elsewhere. The page cannot act; say the action in your chat reply.
- **Axes.** Bars start at zero; about four round ticks; labels horizontal and short. Label directly (values at bar
  tips, series names at line ends); a legend only when direct labels collide.
- **Density.** 14 px base text, labels 12–13 px muted, KPI values 28–32 px, spacing on 4, 8, 12, 16, 24, 32 px, the
  system font, hairline borders.
- **Width grows with the screen**: more columns on a wide window, one column at 360 px with nothing scrolling
  sideways but a wide table in its own box.
- **Access.** Each chart has an `aria-label` with its takeaway and a table of its numbers; tooltips show on focus
  too; visible focus; text contrast at least 4.5:1, marks 3:1.

## Words

The main view has no sentences: names, numbers, units, one- or two-word baselines ("vs Oct"), legend labels and
chips. A chart's title is a label and a number ("Revenue · Peak ₫1.6B Aug 2026"); its interpretation goes in the
tooltip and the `aria-label`. Sentences live in tooltips, empty states, the error note and, for a report read by
managers, one notes section at the end of the page ("Nhận xét" for a Vietnamese reader): at most 3–5 short points,
each about a number the page shows (owner, 2026-11-17). A dashboard looked at every day has no notes section.

## Check the numbers

Before you show a page, and after each change to its data:

- Work out the expected totals from the rows you read, outside the page's code (a short script over `chart.json`),
  and compare each tile, each chart's sum and peak, and each table's total and top row with them.
- The template's `check()` compares the tiles with their charts and tables at every render and shows one error note
  when they disagree; extend it for each component you add. `data-check="ok"` on `<html>` means they agree.
- Check one or two numbers on the page against `superset_chart_data` itself.

## Critique ("is this dashboard good?")

Look at the page (a screenshot, or open it), then answer in this order:

1. **Verdict** in one plain line ("Better, not good yet").
2. **What works**: 2–4 specifics.
3. **Issues ranked by harm**: honesty (numbers that disagree, misleading deltas or axes) → salience (does the eye
   land on what needs action?) → hierarchy (what leads, what competes) → words (sentences that should be a shape or a
   tooltip) → component smells (`references/qa.md`, Smell list) → polish. Each names the element, what the reader
   sees, and the fix.
4. Offer to fix them in one revision.

Honesty problems come first: a pretty page with two answers to one question is not good.

## Where it shows

- Beside the chat the page has about half the window, often one column; full screen and on its own page it
  has the whole window. On a phone it opens over the whole screen.
- The frame draws the page on a light canvas; the template's dark colours are for a page opened in a dark browser.

## Before you hand it over

- Never chart more than about 10,000 marks: a line about 500 points a series, a bar chart about 50 categories, a
  table 100–200 rows; aggregate or show the top N first.
- Labels must not collide or clip at 360 px and at full width: keep labels short.
- The numbers agree (above), the main view has no sentences, and the source line says where the data came from, the
  dates it covers and when it was read.
- Say in your reply what the page shows, as of when, and how it stays current (the Routine, if any).

## Make a page

**One HTML file**, with its style and script inline, for a landing or pricing page, a form-like tool or a mock-up
to iterate on, as for a dashboard. No framework or icon library is loaded: write plain HTML, CSS and a small script,
and draw an icon as inline SVG.

**Design**
- Space on 4, 8, 12, 16, 24, 32 and 48 px. One container, at most about 1024 px wide, centred, with 16 px at the sides.
- Type: one family, the system font; sizes 12, 14, 16, 20, 24, 30, 36; headings semibold with slightly tight letter spacing, body with relaxed line height.
- Colour: neutral greys for text and surfaces, one accent for actions and what matters. Define colours as CSS
  variables and give each a dark partner under `@media (prefers-color-scheme: dark)`.
- Cards: 16 px corners, a 1 px border, a soft shadow at most. Buttons at least 36 px tall.
- It must work at 360 px wide: stack columns (a grid that becomes one column on a narrow screen), nothing scrolls sideways.
- Real words, not lorem ipsum.
- Keep it under 256 KB: embed data, not libraries.

## Sources

The artifact and Routine parts and the template are adapted from an open-source data-dashboards skill (Apache-2.0, `LICENSE`). The
dashboard design (build steps, rules, critique, `references/`) is adapted from dashboard-design-skill by Otabek
Abdiraimov (MIT, `references/LICENSE`); chart choice from Anthropic's data plugin (Apache-2.0, `LICENSE`).
