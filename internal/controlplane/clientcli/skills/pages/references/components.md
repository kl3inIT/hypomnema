<!--
Adapted from dashboard-design-skill 2.1.0 (github.com/abdiraimov-otabek/dashboard-design-skill at 463b8fd,
skills/dashboard-design/references/components.md), Copyright (c) 2026 Otabek Abdiraimov, MIT License (LICENSE
beside this file). Changed: plain CSS on the template's tokens in place of utility classes; filters are the
template's selects of preset periods (the custom range calendar, the grouped filter menu and the realtime parts are
left out); the page cannot act from its frame, so an action is offered in the chat reply.
-->

# Components

Each pattern: what it is for, the spec, and the trap that shipped once and had to be fixed. Written on the tokens in
[tokens.md](tokens.md); code for the fiddly parts is in [snippets.md](snippets.md).

## KPI tiles

For: the 3–6 headline numbers.

- Tile content: label (13–14 px, ink-2) · value (28–32 px semibold, letter-spacing -0.03em, tabular) · the delta with a
  one- or two-word baseline ("▲ 13% vs Oct") · optional sparkline. The explanation ("3% of visitors", "after tax")
  is the tile's tooltip, never a sentence on a second line: a row of tiles with sub-lines reads as a paragraph.
- Tiles that only report: separate cards with a 12–16 px gap. Tiles that pick what the main chart shows: one card
  divided by hairlines, each tile a `button` with `aria-pressed`.
- **Selected** = a soft inner fill with a faint ring: `background: color-mix(in srgb, var(--accent) 6%,
  transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 20%, transparent)`. Unselected: the
  value in ink-2, the sparkline grey.
- The exact value in the tooltip when the tile shows a compact number (₫1.2B).
- Trap: an underline or left edge to mark selection reads as a heavy marker. Use the fill.
- Trap: a partial period compared with a full one as a red delta. Show the comparison value neutrally ("94%
  yesterday") until the period closes.

## Target tiles: value, goal and what is left

For: a metric with a goal ("₫48B of ₫60B"), where pacing matters more than history.

- The number (30–36 px) with the goal as a muted 13 px suffix on the same baseline ("of ₫60B").
- A tick bar in place of a solid track: 50–70 thin ticks (2 px wide, 3 px gap, about 28 px tall), filled ticks in
  the metric's colour, the rest hairline grey. Under it: what is left in words ("₫11.8B to go by Dec 31") at the
  left, the percent at the right.
- A bad-is-high metric (churn, cost): ticks along good → warn → bad and one dark marker tick at the warning
  threshold; the right label says "Healthy" or "High".
- Build the ticks from a row of spans, or `repeating-linear-gradient`; never an image.
- Trap: a target tile with no target set. Hide the bar and show the plain value, never a fake 0%.

## Stat strip

For: 4–6 secondary numbers under the KPI tiles.

- No cards: one row between two hairlines, columns divided by hairlines.
- Per column: label 13 px ink-2 · value 24–28 px medium with a small delta beside it · one muted definition ("MRR ×
  12"). The definition is what makes it a stat, not a tile.

## Movement bars

For: how a total changed (new, expansion, contraction, churn). The net figure large in the header.

- One row each: label left, signed amount right (`+₫2.6B` good, `−₫310M` bad, tabular), a 14–16 px rounded track with
  a fill sized to its share of the largest row.
- The fill is a diagonal hatch in the row's colour (`repeating-linear-gradient(45deg, c 0 2px, transparent 2px 5px)`
  over a 10% tint): hatch marks a movement, solid marks a level. On this component only.
- Gains first, then losses. Colour is the sign, never the rank.

## Delta badge

- `▲ 13%` good / `▼ 9%` bad, 12 px medium, tabular; `±0%` muted. Colour by meaning, not direction: a lower-is-better
  metric (cost, churn, complaints) turns bad when it rises; a neutral one (spend, volume) stays muted both ways.
- A base below about 5 (counts) → `was 1` in muted text. A red −100% for 1 → 0 is noise.
- Over ±999% → `10×+` / `−99%`.
- Never repeat a tile's delta anywhere else on the screen.

## Sparkline

- 24–40 px tall, a 1.5 px line, a faint fill, no axes, a dot on the last point. The selected tile's in the metric
  colour, the others grey.
- Trap: it draws **the same window as the main chart**, or every tile becomes a flat line with a spike at the end.

## Main chart

- The card's title carries the takeaway as a label and a number: a small muted label over the large value
  ("Revenue · Peak ₫1.6B, Aug 2026"), not a bare noun and not a paragraph. The interpretive sentence ("up 12% on last
  month, best day Sunday") is the chart's tooltip and its `aria-label`.
- Summary chips under the chart may give label + number ("Daily avg 41", "Best day Sun").
- Familiar forms (bars, lines) for supporting charts; one novel form (a heatmap) only when it is the screen's centre,
  with its legend. When two charts differ (time vs category), show it: vertical bars or a line over time,
  horizontal bars for categories.
- One metric at a time in its colour, a 1.5–2 px line, a fill from about 15% to 0%. No point markers but the one
  that matters (the peak, the latest).
- Y axis: 4–5 round ticks (0, 1k, 2k), labels 11–12 px muted, baseline 0. Odd ticks like 2,103 look broken
  (`niceTicks` in snippets.md).
- X axis: 5–7 labels, never every day. Daily up to about 120 days, monthly beyond.
- The previous period: a grey or dashed line, no fill, behind the current series.
- An unfinished period: a dashed tail or a dashed outline bar, never a drop.
- Tooltip: the period, then each series with its value; for a comparison, the previous value and the change.
- Bars on their own: a pale fill (about 20% of the hue) with a solid 6–7 px rounded cap, or a solid medium fill.

## Ranked list

For: sources, regions, products: "what is the top of this, and by how much".

- Row: name (truncates last) · value (tabular, right) · share % (muted, fixed width).
- Bar: a 3 px rounded track along the row's bottom edge (`--grid`), the share filled in the entity colour at about
  75% opacity. No tinted block behind the text.
- Header row: column name left, metric right, 12 px muted.
- Shares below 0.1% read `<0.1%`; a footnote says what the shares add up to.
- Trap: one taxonomy per question. Two names for one thing ("Referral" here, "Threads" there) are two answers.

## Filters

- One row above everything: one or two native `select`s styled on the tokens (region, period presets); every tile,
  chart and table follows them.
- A filter change dims the old numbers until the new ones are drawn (the template's `is-updating`).
- Period presets ("Last 3 months", "Last 12 months", "All time") rather than typed dates.

## Attention

For: the Monitor step "what needs attention", answered by shape where the data already is.

- Flag the problem in place: a warm heatmap cell, a bad chip on a row, a dashed "no data yet" cell. The flag is the
  most salient mark in its card.
- Name the action beside the flag ("Call", "Check stock") as a chip; the page cannot act, so offer the action in
  your chat reply.
- A separate attention row is the last resort, for a problem with no home on the screen: noun + number + action
  ("3 debtors over 30 days"), at most three, never sentences, never repeating a tile.

## Heatmap (matrix)

For: one measure across two dimensions where the grid is the point: regions × weeks, weekday × hour.

- Rounded cells (8–10 px radius, 32–36 px tall, 5–8 px gap), row labels left, column headers above, the latest
  column marked (accent number and a dot).
- Columns are a fixed trailing window (the last 7 days, the last 12 weeks); past about 14 columns, aggregate.
- Classed tiers of one hue at meaningful thresholds, intensity growing toward the problem, and one warm tier past it
  (tokens.md). Never a ramp where the worst cell is the palest, nor one where the best cell is the loudest.
- The warm tier also carries a non-colour cue (an underlined or bold number, or ▲).
- Text in every cell is ink or ink-2, never the accent.
- Numbers in cells only on small grids (up to about 60 cells); the exact value and its parts in the tooltip.
- Missing data: a dashed outline cell.
- Legend in the header: the tier swatches with their thresholds and the missing swatch, one or two words each.

## Bullet graph

For: a measure against a target in a small space, in place of gauges and dials (perception.md).

- A horizontal bar about a third of the track's height; the target as a short perpendicular line; two to five
  background ranges in intensities of one hue, darker for poor.
- Rows of bullet graphs share one layout and stack like a ranked list: label, bullet, value.

## Segmented share bar

For: a total split into a few ordered parts (debt by age, tickets by priority).

- One row of rounded pills whose widths are the parts' shares, 2–6 px apart, 8–10 px tall; ordered parts use one
  hue's intensities ending in `warn` or `bad`.
- Legend under it: swatch, short label, amount; share and exact amount in each segment's tooltip.

## Tables

- Hairline row separators, a muted date column, right-aligned tabular numbers, at most 100–200 rows on the page
  (aggregate or show the top N).
- Every chart has its table of numbers (the template's "Show the numbers"), so a tooltip never holds the only copy.
- On phones drop the optional columns and fold their facts into a muted second line before any name truncates.

## Words that remain

- Labels and actions name what they are: "Revenue", "Call parent"; never "Value" or "OK".
- One word per concept across the page: a "customer" is never also a "client".
- Empty states say what will appear here and how it gets there ("Orders appear after the first sale"), not whimsy.
- An error says what went wrong and what to do; no "Oops", no bare error codes.
- Text grows in translation: size for the longest language, and take month and weekday names from `Intl`, not a
  hard-coded list.

## Tooltip

- One tooltip for the whole page: dark surface (`--tip`), white text, 12 px, 8–10 px corners, above the element and
  inside the viewport. It shows on hover and on keyboard focus, and hides on scroll and Escape.
- A chart's tooltip leads with the exact value, then a muted line for the period.

## States

- **Loading**: skeleton blocks the size of the final content, and one status line.
- **Stale** (a filter applying, a refresh): keep the old numbers visible but dimmed; never flash zeros.
- **Error**: one page-level note, shown even when old data is on screen; not the same message in every card.
- **Empty**: no data is not good news: never an all-clear on a selection with no rows. Keep the structure, `—` in the
  tiles, one short line in each chart ("No orders in this selection").
- **Tiny data**: muted "was N" deltas, no percentages on bases under about 5.
- **Sample data**: a clear "Sample data" chip in the header, never mixed with real numbers.

## Trust cues

- Say once what a money figure means: "net of tax, in VND".
- Label estimates as estimates, in the tile's tooltip.
- "Updated 09:00" in the header from `CONFIG.updated`; the source line at the bottom names the dashboard and chart
  the figures came from.
