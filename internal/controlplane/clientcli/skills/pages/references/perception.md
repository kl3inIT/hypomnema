<!--
Adapted from dashboard-design-skill 2.1.0 (github.com/abdiraimov-otabek/dashboard-design-skill at 463b8fd,
skills/dashboard-design/references/perception.md), Copyright (c) 2026 Otabek Abdiraimov, MIT License (LICENSE
beside this file). Chart choice, "when not to use" and the design principles merged from Anthropic's
knowledge-work-plugins (data/skills/data-visualization and create-viz at e91a134), Apache License 2.0 (../LICENSE).
Changed: cut to a plain HTML page whose charts are inline SVG; the native-app notes are left out.
-->

# Perception: what the eye reads, and how fast

Reach for this file when choosing how to draw a number, how to colour a scale, or why a screen fails the
first-glance test. The rules come from Stephen Few, Edward Tufte, Cole Nussbaumer Knaflic, Cleveland & McGill (via
Nielsen Norman Group), Lisa Charlotte Muth (Datawrapper), Apple's chart sessions and the WCAG.

## Monitor: the reader's sequence

A dashboard is a monitoring tool. The reader moves through four steps, and the screen serves each in turn (Few):

1. **Overview**: the state of everything at once.
2. **What needs attention**: the few items that are wrong, found in a glance, not a search.
3. **Closer look**: enough context on those items to decide whether to act.
4. **Act**: the action is named beside the problem. The page cannot act from its frame; offer the action in your
   chat reply.

A screen that shows everything with equal weight passes step 1 and fails step 2.

## Choose a chart by what it shows

| What you are showing | Best form | Alternatives |
|---|---|---|
| Trend over time | Line | Area, only for a cumulative total or a composition |
| Comparison across categories | Bar | Horizontal bar for many or long labels; dot plot |
| Ranking | Horizontal bar, sorted | Ranked list with share bars; slope chart for two periods |
| Part-to-whole | Stacked bar, or a segmented share bar | 100% stacked bar when the proportion is the point |
| Composition over time | Stacked area | 100% stacked bars |
| Distribution | Histogram | Box plot to compare groups; strip plot |
| Correlation of two measures | Scatter plot | Bubble, a third measure as size |
| One measure across two dimensions | Heatmap (a matrix) | Small multiples |
| Performance against a target | Bullet graph | A target tile with a tick bar |
| Several measures at once | Small multiples, one scale | Separate charts |
| A single number | KPI tile | — |
| Many categories, exact values | Table | — |

Geographic maps, flows (Sankey) and networks need a library or a lot of drawing; on a dashboard a ranked list or a
table answers the same question in a glance.

**When not to use a form**
- Pie or donut: only one overwhelming share, or fewer than 6 slices where rough comparison is enough; people
  compare angles badly. Prefer a segmented bar or a ranked list.
- 3D: never; it distorts and adds nothing.
- Two y-axes: they suggest a correlation that is not there. Use two charts, or index both series to 100.
- Stacked bars with many parts: the middle segments cannot be compared. Use small multiples or grouped bars.
- Radial gauges and dials: angle plus wasted space. Use a bullet graph.
- Treemaps on a monitoring screen: fine for slow exploration only.

## Encoding ladder: how to draw a number

People judge some visual properties accurately and others badly (Cleveland & McGill, 1984 and 1985). Pick the
highest rung that fits:

| Rung | Encoding | Use for |
|---|---|---|
| 1 | Position on a common scale | Lines, dots, bars on one axis: trends and comparisons |
| 2 | Length | Bars, bullet graphs, share bars |
| 3 | Colour intensity (one hue, light → dark) | Matrices only: a heatmap where position is taken by the grid |
| 4 | Area, angle | Almost never: a donut only for one overwhelming share |
| — | Hue | Categories, never magnitude: people do not read hues as ordered |

### Choose with real data

Pick the mark with the real, messy data, not a neat sample: noise, zero days, one huge outlier, an empty week.
Lines between alternating zeros turn into zigzags that hide the values; bars keep zeros readable.

### Axes, gridlines and layout

- **Fixed range** when the bounds are known (0–100% for a rate), so full and empty read at a glance. **Dynamic
  range** when there is no natural maximum.
- Bars always start at zero. A line may start above zero when the axis says so.
- About four gridlines at round steps (20, 40, 60); for time, steps the reader counts in (weeks across a month,
  months across a year).
- Axis labels horizontal; shorten or wrap them rather than rotate 90 degrees. Label each axis with its unit when
  the title does not say it.
- Sort categories by value, unless they have a natural order (months, stages).
- A time series is wider than tall (3:1 to 2:1); a comparison can be squarer.
- Panels compared side by side share one scale.
- Show uncertainty when the data is uncertain: a range or a band, and say the figure is an estimate.
- A glanceable preview (a sparkline) has no gridlines and no labels.

## Preattentive attributes: what is seen before it is read

Colour, size, position, length, orientation and enclosure are processed in under a quarter of a second, before
reading starts. Spend them on the one thing per region that needs attention:

- A single saturated element in a grey field is found instantly; ten saturated elements are a search.
- Big numbers draw the eye first; the top of the screen gets the most dwell time; attention fades along a run of
  repeated elements, so the most important item goes first.
- **Salience test**: where does the eye land first (or blur the screenshot)? It must land on the thing the screen is
  for, or on the problem; never on a logo, a decoration or a healthy number.

## Colour rules

Few's nine rules, compressed:

1. Same colour looks different on different backgrounds: keep chart backgrounds flat, no gradients.
2. Data must contrast with its background.
3. Add colour only for a purpose: to highlight, to group, or to encode a value.
4. Different colours only for different meanings; bars that differ only by name share one colour.
5. Three intensities with three jobs: **pale** for non-data and de-emphasis (grids, tracks, the other series),
   **medium** for data, **bright or dark** only for what needs attention.
6. Quantity is one hue from pale (low) to dark (high); two such ramps meeting at a meaningful midpoint make a
   diverging scale.
7. Non-data ink (axes, borders, gridlines) is just visible enough to do its job.
8. Red and green alone are invisible to about 8% of men: pair them with a shape (▲ ▼), an icon, position or text,
   or use blue and orange.
9. No visual effects: no shadows on data, no gloss, no 3D.

More:
- Categories look equally important: even out saturation and lightness across their colours.
- Colours in one chart are told apart **by name** ("blue and orange", not "blue and teal"). Blue/yellow is the second
  most common confusion after red/green.
- At most 6 to 8 categorical colours; fold the rest into "Other".
- Highlight the story: one bright accent for the key point, grey for everything else.
- Colour meanings are cultural: follow the readers' convention for gains and losses and back it with ▲ ▼.
- Respect the reader's settings: dark mode, `prefers-contrast: more`, `prefers-reduced-motion`.

### Scales (Muth)

- **Sequential** (light → dark) when the data has no natural middle; readers understand "darker = more" without a key.
- **Diverging** when a meaningful middle exists: zero, a target, the average, 50%. It always needs a key.
- **Classed** (a few steps) when readers sort items into groups or meaningful thresholds exist. **Unclassed**
  (continuous) for general patterns.

### The inverted-salience trap

A sequential scale makes low values pale. When low is the problem (attendance, uptime, conversion), the worst cell
becomes the least visible one. The mirror mistake: the best values the darkest, so the healthiest cells are the
loudest. Use classed tiers whose intensity grows toward the threshold, the healthiest palest, and a warm warning
hue past it.

## Heatmaps

- One hue, intensity steps that grow toward the problem (healthy palest); three to five classes at meaningful
  thresholds; one warm tier past the threshold, which also carries a non-colour cue (underline, bold or ▲).
- Numbers inside cells only when the matrix is small (up to ~60 cells) and every number has 4.5:1 contrast;
  otherwise colour only, with the exact value in the tooltip.
- A missing value is its own state (a dashed outline), never an empty-looking cell that reads as zero.
- The latest column is marked; future cells are not drawn.

## Bullet graph (Few's specification)

The compact replacement for gauges: one measure against a target and qualitative ranges.

- A label at the left, a linear scale from zero, the measure as a bar about a third of the track's thickness, the
  target as a short perpendicular line, two to five background ranges (ideally three).
- Ranges are intensities of one hue, darker for poor and lighter for good.
- "Lower is better" measures (costs, defects) reverse the range order.
- A target in the future gets a projected segment, so "on track?" is answerable today.

## Sparklines (Tufte, Few)

Word-sized lines that give history to a current number: no axes, no gridlines, a dot on the last point. The value
sits next to them. Every sparkline in a set covers the same window as the main chart.

## Context: compared to what?

A number alone says nothing. Each headline number carries one comparison: the previous period (same elapsed time),
a target, a normal range, or its history; said in a symbol and one or two words ("▲ 2 vs Sep", "▼ 15% vs last
month"), with the exact baseline in the tooltip.

## Accessibility

- Colour is never the only carrier of meaning (WCAG 1.4.1): add a shape, icon, pattern, line style or label.
- Data marks need 3:1 contrast against their neighbours (1.4.11); text in charts 4.5:1 (1.4.3).
- Every chart has a text alternative: an `aria-label` with the range, the extremes and the latest value, and a table
  of its numbers.
- Spoken values put the context first: "June 1, 36 orders", never "6-1".
- The hit area of a chart is its whole plot height, not the thin marks.

## Few's thirteen mistakes (checklist)

1. Exceeding one screen for the monitoring view.
2. Numbers without context.
3. Excessive detail or precision.
4. A deficient measure (unclear unit, or not what the decision needs).
5. The wrong display form for the message.
6. Meaningless variety (a different chart type per panel to look interesting).
7. Poorly designed display forms (unlabelled, 3D, badly ordered).
8. Inaccurate encoding (bars not starting at zero).
9. Poor arrangement (things to compare are not side by side on one scale).
10. Important data not highlighted, or everything highlighted.
11. Useless decoration.
12. Misused or overused colour.
13. An unattractive display: a busy screen is a slow screen.
