---
name: pages
description: "Build a fast, good-looking page: a data dashboard, or any other page (a landing page, a pricing page, a tool), shown to the owner in the chat as an artifact with the `show_artifact` tool. Use when the owner asks for a dashboard, charts or KPIs on a page, a page or mock-up to look at, or a data page to keep up to date, for example from a Superset dashboard's figures."
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

`dashboard.html` is beside this SKILL.md, in this skill's folder. It runs in the artifact's locked frame, at phone and
desktop widths, in light and dark. Copy it into your directory as `index.html` and change only what the data needs:

1. `CONFIG`: `dataFile` (a folder page's data, `data.json`), `source`, `updated` (when the figures were read, the
   `computed_at` of `superset_chart_data`, ISO time), `currency` (`VND`, `USD`, …).
2. `prepare`: map your columns to the fields the page uses. The sample has `date`, `region`, `category`, `product`,
   `orders`, `units`, `revenue`, one row a month, region and product.
3. `AGGREGATES` and the `render…` functions: your questions.
4. Headings, notes and KPI labels in the HTML, in the owner's language.
5. Replace the sample rows between the `embedded-data` tags with your data (or leave the tag empty in a folder page
   and write `data.json`).

Keep the rest: the charts drawn as SVG by `lineChart` and `barChart`, the colour tokens, `Intl` formatting, loading and
error states, aria labels and the "Show the numbers" tables. Cut cards rather than adding a fifth chart.

**The locked frame.** The page runs sandboxed, in an origin of its own. Make it self-contained: inline `<style>` and
`<script>`, the system font, images as `data:` URLs. Load nothing from the network: no CDN script or stylesheet, no
web font, no `fetch` but a folder page's own files. There are no cookies and no `localStorage` or
`sessionStorage`; keep state in variables. The page cannot call the chat or the control plane.

## Data

- **From Superset.** Find the chart with the `superset` skill (`superset_dashboards` with a `search`, then
  `superset_dashboard`), then read it with `superset_chart_data` and write its answer to `chart.json`. Its `columns`
  and `rows` are what the template reads, as they are; a date may come as milliseconds since 1970 and is read as a
  day. Say in `CONFIG.source` which dashboard and chart, and in `CONFIG.updated` its `computed_at`.
- **Aggregate first.** Ship the smallest table that answers the questions: one row per day or month and dimension,
  not raw events. A chart's rows are already its aggregate; when they are not enough, ask for another chart rather
  than raw rows.
- **Size.** One page is at most 256 KB including the template (about 30 KB), so embed at most about 200 KB of JSON. A
  folder holds 5 MB in all.
- **Embedding.** The JSON goes inside `<script type="application/json">`: write `<\/` for any `</` in a value, so a
  value cannot close the tag.
- **Types.** Dates as `YYYY-MM-DD` text or milliseconds. Numbers as plain numbers, not formatted text.
- Check one or two numbers on the page against `superset_chart_data` before you show it.

## Design

**Charts**
- One question per chart. Make the heading the question ("Which regions sell the most?") and the note under it the answer, computed from the data.
- Pick the form by the job: compare categories with a bar (horizontal and sorted when labels are long); show change over time with a line; show part-to-whole with a stacked bar. A single number is a KPI tile, not a chart. Many categories belong in a table.
- Never: 3D, two y-axes (use two charts, or index both series to 100), pies with more than 3 slices, rainbow colour scales, a number on every point.
- Bars start at zero. Don't cut an axis to make a change look bigger.
- Label directly: values at bar tips, series names at line ends. Use a legend only when direct labels collide.
- Annotate the key point (the peak, a target, the change that matters) on the chart itself.
- Emphasis beats colour: one accent for what matters, grey for the rest (the template greys every region but the selected one).

**Colour**
- One accent colour for the page. The template's 6 categorical colours (separate light and dark sets) were checked for colour-blind separation and at least 3:1 contrast; use them in order.
- Give each category a fixed colour, so filtering never repaints the survivors. Fold anything past 6 into "Other".
- Magnitude: one hue from light to dark. Above/below a baseline: two hues with a grey middle.
- Green and red mean good and bad only, always with ▲ ▼ and words, never colour alone.
- Text uses the ink colours, never a series colour.

**Layout and type**
- Order: title and filters, then 3 to 5 KPI tiles, the main chart full width, 1 or 2 supporting charts, a detail table, and the source line.
- Space on a 4, 8, 12, 16, 24, 32 px scale; align to the grid; hairline grid lines; let whitespace separate cards.
- System font. Tabular numbers in tables and axes; KPI numbers stay proportional.
- Format every number and date with `Intl`: compact for KPIs and axes (₫1.2B), full in tables and tooltips.
- Say where the data came from, the dates it covers and when it was refreshed, at the bottom.

**Interaction and access**
- One or two filters, in one row above everything; every KPI, chart and table follows them.
- Tooltips add detail but never hold the only copy of a number: each chart has its table.
- Each chart has an `aria-label` that states what it shows and the takeaway. Native `select` and `details` keep the keyboard working; keep visible focus and text contrast of at least 4.5:1.
- It must work at 360 px wide: one column, nothing scrolls sideways except a wide table inside its own box.
- Show a loading line at start, a plain error if the data fails, and dim (don't blank) the cards while filters apply.

## Where it shows

- Beside the chat the page has about half the window, often one column; full screen and on its own page it
  has the whole window. On a phone it opens over the whole screen.
- The frame draws the page on a light canvas; the template's dark colours are for a page opened in a dark browser.

## Before you hand it over

- Never chart more than about 10,000 marks; aggregate first.
- Labels must not collide or clip at 360 px and at full width: keep labels short.
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
