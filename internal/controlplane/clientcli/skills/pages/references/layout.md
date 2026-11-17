<!--
Adapted from dashboard-design-skill 2.1.0 (github.com/abdiraimov-otabek/dashboard-design-skill at 463b8fd,
skills/dashboard-design/references/layout.md), Copyright (c) 2026 Otabek Abdiraimov, MIT License (LICENSE beside
this file). Changed: plain CSS in place of utility classes; the widths are those an artifact is seen at; site
navigation, the phone menu and the notes on a CSS framework are left out (a dashboard here is one page).
-->

# Layout

## Page order

Header row (title left; the "updated" chip, filters and the period right) → a sample-data chip when the data is a
sample → KPI tiles → the main view (the answer; for monitoring, the grid or list where problems show) → breakdowns →
detail tables → for a report, one notes section → the source line. Most important top-left: readers scan top-down
in an F, and the top gets the most attention.

## Width

- A dashboard grows with the screen: one column beside the chat, two from about 760 px, and up to about 1200 px of
  content; past 1800 px it may reach about 1640 px with four breakdown panels in a row. A narrow column floating
  between two empty bands reads as broken.
- A page that is mostly text (a form, a landing page) stays narrow: about 1024 px, centred.

## Grids

- KPI tiles: two columns at phone width, then as many as fit (`repeat(auto-fit, minmax(min(100%, 150px), 1fr))`), all
  on one row on a laptop.
- Attention row, only when a problem has no home on the screen: up to three items, noun + number + what to do,
  never sentences, never one lonely card.
- Panels: one column → two (from about 760 px) → four on very wide screens; or 7 + 5 and 6 + 6 on a 12-column grid.
- **No holes**: cards in one row hold similar amounts of content (at most about 8 rows each). When one card is half
  empty, restructure (fold it into its neighbour, pair it with another short card) instead of stretching it.

## The monitoring view fits one screen

The KPI tiles and the main view where problems show fit above the fold at 1440 × 900 (Few's first mistake is a
monitoring view that needs scrolling). Breakdowns and detail tables may scroll; the answer and the problems may not.

## Phones (360–375 px)

- No sideways page scroll: `document.documentElement.scrollWidth === innerWidth`. Only a wide table scrolls, inside
  its own box.
- Charts keep vertical swipes: `touch-action: pan-y`, never `none`.
- Segmented controls shrink and truncate (with a `title`), never scroll.
- Rows fold before they truncate: drop decorations (avatars, icon squares, chips, optional columns) and move their
  facts into a muted second line, so names and numbers stay whole.
- A heatmap keeps every column at 360 px for up to about 7 columns: a 36 px label column, 5 px gaps, 32 px cells,
  the legend hidden. A longer range aggregates to weeks.

## Density and type

- Base 14 px; labels 12–13 px muted; KPI values 28–32 px; panel titles 15–16 px semibold.
- Cards: 12–16 px corners, a 1 px hairline border, padding 16–24 px, a soft shadow at most.
- Hairlines at about 7.5% ink; muted text at least 4.5:1 on every surface it sits on.

## Dark mode

- Every colour is a token defined for both themes ([tokens.md](tokens.md)); tints are a token mixed with
  transparency, so they adapt.
- Check dark on screenshots with the browser's dark scheme and with `data-theme="dark"` on `<html>`, which the
  template also honours.
