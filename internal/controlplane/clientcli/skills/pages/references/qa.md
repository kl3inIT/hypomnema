<!--
Adapted from dashboard-design-skill 2.1.0 (github.com/abdiraimov-otabek/dashboard-design-skill at 463b8fd,
skills/dashboard-design/references/qa.md), Copyright (c) 2026 Otabek Abdiraimov, MIT License (LICENSE beside this
file). Changed: the known totals are worked out from the figures you read (a Superset chart), the page checks its
own components; the words audit allows one notes section at the end of a report (owner, 2026-11-17); the screenshot
widths are those an artifact is seen at; sign-ups, site switching and the notes on a CSS framework are left out.
-->

# QA pass

Run it before you show a page, and again after each change to its data or layout. Look at every screenshot you
take; reading the code is not seeing the screen.

## Known totals (numbers before pixels)

Screenshots catch broken layouts, not three components agreeing on the same wrong number.

1. **Work out the expected answers outside the page's code**, from the rows you read (the `superset_chart_data`
   answer, or `chart.json`): the period total, the previous period's total and the change, the best month or day,
   the top row of each ranked list and its share. A short script over the JSON does it; the page's own functions
   do not count.
2. **Check each component on its own** against those answers, reading what it shows:
   - a KPI tile: the number and the delta as displayed;
   - a chart: its series summed over the range equals the tile's total; its peak is the best period;
   - a table or ranked list: the rows (and any "Other") sum to the total; the top row and its share match;
   - every tooltip, chip or caption that repeats a number.
3. **Keep the check in the page.** The template's `check()` compares the tiles with the sums of their charts and
   tables at every render and shows one page-level error when they differ; extend it for each component you add.
   It sets `data-check="ok"` on `<html>` when everything agrees.

Done when every component matches the expected answers. Then the words audit and the salience test, then
screenshots.

## Words audit (wordless)

Count the sentences on the rendered main view, outside tooltips, empty states and the notes section. The target
is zero.

- A tile's second line that explains the number → the tile's tooltip.
- A row of insight sentences → the flag on the data, with its action beside it (components.md → Attention).
- A chart note ("the axis starts at 60%", "October is not finished") → the axis labels and the dashed style say it;
  the rest goes in the tooltip and the `aria-label`.
- Allowed on the main view: names, numbers, units, a one- or two-word baseline ("vs Sep"), legend labels, chips of
  one or two words, the source line.
- **A report read by managers** may end with one notes section ("Nhận xét" for a Vietnamese reader, "Notes"): at
  most 3–5 short points, each naming a number that is on the page above it, nothing the page does not show. It is
  the only place for sentences; a dashboard that is looked at every day has none.

Done when the count is zero, or each remaining sentence has a reason.

## Salience test

Look at the full screenshot for one second (or blur it) and note where the eye lands first, second, third.

- First on the screen's answer or on the problem; never on a logo, a decoration or a healthy number.
- Every problem from the first build step is visible without hovering, and is the most salient mark in its card.
- Healthy data is calm: if more than a few marks per card are bright, nothing is.

## Real data and perception checks

- Run the page on the real, messy rows as well as a neat sample: zero periods, one huge outlier, an empty week, the
  longest names. The marks still read and nothing overflows.
- Pick a filter combination with no rows: the empty state shows, with `—`, not zeros.
- Colour blindness: every problem stays findable in greyscale; series stay distinct by position, label or line
  style.
- Keyboard: Tab reaches the filters, each tooltip target and each "Show the numbers"; focus is visible.

## Screenshots

If you have a browser tool, open the page and take a screenshot at each width it is seen at:

- **360–375 px** (a phone, the full page): one column, no sideways scroll
  (`document.documentElement.scrollWidth === innerWidth`), no label clipped.
- **about 700 px** (beside the chat): one column or two, tiles still in rows.
- **1440 px** (full screen): the monitoring view above the fold.

In light and in dark (the browser's dark scheme, or `data-theme="dark"` on `<html>`), with a tooltip open, and in
the empty and error states once. Crop to a component to judge details; the full page to judge balance.

## Consistency checks (honest)

- The same number agrees everywhere: tile and tooltip, chart peak and the title's number, list total and bar total.
- Sparklines and the main chart cover the same periods.
- No red delta on a base under about 5; the comparison window has the same length and elapsed time.
- No `NaN`, `Infinity`, `-₫0`, `−0%`, `1m 60s`, `1 orders`, `0.0%`, `Invalid Date`.
- A filter change dims and redraws; nothing flashes to zero.
- The source line and the "updated" time say what is true.

## Smell list (spot it → do this instead)

| Smell | Instead |
|---|---|
| Tinted block behind each list row | 3 px bar along the row's bottom edge |
| Underline or left edge marking the selected tile | Soft tinted fill + faint ring |
| Narrow centred column on a wide screen | More columns |
| Sideways-scrolling tabs | A segmented control that shrinks |
| Red −100% on 1 → 0 | Muted "was 1" |
| A card repeating a tile's number | Delete the card |
| Wide cards holding one small number each | Fold the number into a KPI tile or a tooltip |
| Random y-axis ticks (2,103) | Round ticks (1k, 2k), 0 baseline |
| Flat sparkline with a spike at the end | Trim to the chart's window |
| Two names for one thing | One word per concept |
| A 2–3 slice donut as the main view | Ranked list with share bars |
| Gradients, glass, 3D icons, a date chip on every card | Quiet surfaces, one accent, one period for the page |
| A sentence under every KPI number | Delta with a one-word baseline; the explanation in the tooltip |
| A row of insight sentences | Flag the problem on the data; for a report, the notes section at the end |
| Heatmap where the worst cell is the palest | Classed tiers plus a warm "below target" tier |
| Every heatmap cell saturated | Pale-to-medium tiers for healthy values; colour for problems |
| Radial gauge or dial | Bullet graph |
| A rainbow of icon tiles | Monochrome icons; colour only where it means something |
| Red and green as the only signal | Add ▲ ▼, an icon or a label |
| A card stretched half empty beside a tall neighbour | Restructure the row |
| A partial period shown as a red drop | Neutral value until the period closes; dashed partial bar |
| "All good" on a selection with no data | The empty state: no data is not good news |
| Names truncated on phones while optional columns stay | Drop the optional columns first |
| The same error text in every card | One page-level note |
