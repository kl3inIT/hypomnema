<!--
Adapted from dashboard-design-skill 2.1.0 (github.com/abdiraimov-otabek/dashboard-design-skill at 463b8fd,
skills/dashboard-design/references/tokens.md), Copyright (c) 2026 Otabek Abdiraimov, MIT License (LICENSE beside
this file). Changed: plain CSS variables named as in the template (dashboard.html) in place of the upstream's utility-class theme;
tints written with color-mix(); the six series colours are the template's.
-->

# Tokens

Define every colour once, for light and dark, then mix tints from the tokens, so a tint adapts to the theme by
itself. The template's set (`dashboard.html`):

```css
:root {
  color-scheme: light;
  --page: #f5f6f8;     /* page background */
  --surface: #ffffff;  /* cards */
  --soft: #eff1f4;     /* hover, segmented track, skeletons */
  --border: #e3e5ea;   /* card borders */
  --grid: #eceef1;     /* hairlines, gridlines, bar tracks */
  --axis: #c9cdd4;     /* the zero line, input borders on hover */
  --ink: #16181d;      /* primary text */
  --ink-2: #4b5260;    /* secondary text */
  --muted: #646b78;    /* labels, axes; >= 4.5:1 on every light surface */
  --deemph: #c4c9d1;   /* the other bars when one is selected */
  --accent: #0b6bcb;   /* the page's one entity hue, selection, focus */
  --good: #1a7f37;     /* a good change */
  --bad: #c62a2f;      /* a bad change on a meaningful base */
  --warn: #9a6700;     /* below threshold: the warm heatmap tier, an overdue chip */
  --tip: #1f2530;      /* the tooltip's surface */
  --series-1: #0b6bcb; --series-2: #c76a00; --series-3: #00866b;
  --series-4: #d1452b; --series-5: #8a5cc7; --series-6: #c2407e;
}
/* dark: same names, new values. Follows the system unless the page pins a theme with data-theme. */
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --page: #0f1115; --surface: #17191e; --soft: #1f2228; --border: #2a2e36; --grid: #23272e; --axis: #3a3f48;
    --ink: #eceef2; --ink-2: #b4bac5; --muted: #8e96a3; --deemph: #3d434d;
    --accent: #3a8ae8; --good: #3fb950; --bad: #f47067; --warn: #e3a21a; --tip: #2a3548;
    --series-1: #3a8ae8; --series-2: #c97a00; --series-3: #109c7c;
    --series-4: #e0573a; --series-5: #9b78e0; --series-6: #d4568f;
  }
}
:root[data-theme="dark"] { /* the same dark values again, for a pinned dark theme */ }
```

Copy the dark values into the pinned rule too: one name, two values. A tint is the token mixed with transparency:
`background: color-mix(in srgb, var(--accent) 8%, transparent)`.

The charts are SVG drawn by the script, which cannot read `var()` in an attribute it sets by hand: the template's
`theme()` reads each token as the browser resolved it and draws again when the scheme changes.

## Rules

- One hue carries the page's main entity (`accent`), plus `good`, `bad` and `warn` for meaning. Everything else is
  ink, muted or a hairline. Another hue needs a reason (a second entity, such as money beside people), not
  decoration.
- Series colours are for categories in one chart, in order, each category keeping its colour when filters change.
- Three intensities, three jobs: **pale** (a token at 7–20%) for non-data and de-emphasis: tracks, healthy heatmap
  tiers, the selected fill; **medium** (the token) for data marks; **bright or dark** only for what needs attention.
  A screen of medium and bright everywhere has nothing left to highlight with.
- Heatmap tiers are the accent at 7 / 15 / 26% over the card for the healthy steps, `warn` at 30% with ink text for
  the problem tier.
- Text is ink, ink-2 or muted, never a series colour; accent text on an accent tint fails 4.5:1.
- Check muted text contrast on every surface it sits on (card, soft, page), in both themes.

## Density scale

Base **14 px** text (13 px for dense tables and second lines), **16 px** icons drawn as inline SVG, strokes **1 /
1.2 / 1.5**, weights regular and medium, semibold only for the page title or a headline number.
