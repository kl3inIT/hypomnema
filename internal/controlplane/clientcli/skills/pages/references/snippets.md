<!--
Adapted from dashboard-design-skill 2.1.0 (github.com/abdiraimov-otabek/dashboard-design-skill at 463b8fd,
skills/dashboard-design/references/snippets.md), Copyright (c) 2026 Otabek Abdiraimov, MIT License (LICENSE beside
this file). Changed: the component and utility-class snippets are plain JavaScript and CSS on the template's tokens, as the
upstream file says to do on plain HTML; numbers use Intl with the reader's locale; the range calendar, the
same-elapsed-time window and the stale-response guard are left out (a page here has its data in one file).
-->

# Snippets

Small, dependency-free pieces for the parts that are easy to get subtly wrong, for a plain HTML page on the tokens
in [tokens.md](tokens.md). The template (`dashboard.html`) already has `ticks`, the delta, the tooltip and the
check; copy the others when the page needs them.

## Numbers

```js
const CURRENCY = "VND";
const fmt = {
  money: new Intl.NumberFormat(undefined, { style: "currency", currency: CURRENCY, maximumFractionDigits: 0 }),
  moneyShort: new Intl.NumberFormat(undefined, { style: "currency", currency: CURRENCY, notation: "compact", maximumFractionDigits: 1 }),
  int: new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }),
  short: new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }),
};
/** A share in percent: never "0.0%" for a real but tiny share. */
const share = (v) => (v === 0 ? "0%" : v < 0.1 ? "<0.1%" : v < 1 ? v.toFixed(1) + "%" : Math.round(v) + "%");
/** Seconds as a duration; round first, so 119.6 is "2m 0s", not "1m 60s". */
const duration = (s) => { const t = Math.round(s); return t >= 60 ? `${Math.floor(t / 60)}m ${t % 60}s` : `${t}s`; };
/** No data is "—", never a fake 0. */
const orDash = (v, f) => (v == null || !Number.isFinite(v) ? "—" : f(v));
```

## Round axis ticks

```js
/** 0 and evenly spaced round ticks covering max: 0, 500, 1k, 1.5k… (steps of 1, 2, 2.5 or 5 × 10^n). */
function niceTicks(max, count = 4) {
  if (!(max > 0)) return [0];
  const raw = max / count, pow = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * pow).find((s) => s >= raw);
  return Array.from({ length: Math.ceil(max / step) + 1 }, (_, i) => +(i * step).toFixed(10));
}
// the chart's top is the last tick; label each with fmt.short.format(tick)
```

## Delta badge

```js
/** Change against the previous period, as DOM. Tiny bases say what they were instead of shouting a percentage.
 *  invert: lower is better (cost, churn); neutral: neither direction is good (spend, volume). */
function delta(el, cur, prev, { format = fmt.int.format, min = 5, invert = false, neutral = false, vs = "" } = {}) {
  el.replaceChildren();
  if (!prev || !Number.isFinite(cur)) return;
  el.title = `${vs || "Previous period"}: ${format(prev)}`;
  if (prev < min) { el.append(Object.assign(document.createElement("span"), { className: "muted", textContent: `was ${format(prev)}` })); return; }
  const d = ((cur - prev) / prev) * 100, r = Math.round(d);
  const good = invert ? d < 0 : d > 0;
  const text = r === 0 ? "±0%" : Math.abs(d) > 999 ? (d > 0 ? "10×+" : "−99%") : `${d > 0 ? "▲" : "▼"} ${Math.abs(r)}%`;
  const cls = r === 0 || neutral ? "muted" : good ? "up" : "down";
  el.append(Object.assign(document.createElement("span"), { className: cls, textContent: text }), vs ? ` ${vs}` : "");
}
```

```css
.up { color: var(--good); font-weight: 600; }
.down { color: var(--bad); font-weight: 600; }
.muted { color: var(--muted); }
```

## KPI tile that picks the chart

```html
<div class="kpi-strip" role="group" aria-label="Key figures">
  <button class="kpi-pick" aria-pressed="true" data-tip="Revenue net of tax"><span class="kpi-in">
    <span class="kpi-label">Revenue</span><span class="kpi-value">₫9.4B</span><span class="kpi-delta"></span></span></button>
  <button class="kpi-pick" aria-pressed="false"><span class="kpi-in">…</span></button>
</div>
```

```css
.kpi-strip { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 150px), 1fr));
  background: var(--surface); border: 1px solid var(--border); border-radius: 12px; }
.kpi-pick { all: unset; cursor: pointer; padding: 8px; border-right: 1px solid var(--grid); }
.kpi-pick:last-child { border-right: 0; }
.kpi-in { display: grid; gap: 4px; padding: 12px 16px; border-radius: 10px; }
.kpi-pick:hover .kpi-in { background: var(--soft); }
.kpi-pick[aria-pressed="true"] .kpi-in { background: color-mix(in srgb, var(--accent) 6%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent) 20%, transparent); }
.kpi-pick:focus-visible .kpi-in { outline: 2px solid var(--accent); }
```

## Ranked row with a slim share bar

```html
<li class="rank"><span class="rank-name">TP.HCM</span><span class="rank-value">₫3.1B</span><span class="rank-share">38%</span>
  <span class="rank-bar" aria-hidden="true"><span style="width: 38%"></span></span></li>
```

```css
.rank { position: relative; display: flex; align-items: center; gap: 12px; height: 44px; padding: 0 12px 6px; }
.rank-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rank-value { font-variant-numeric: tabular-nums; }
.rank-share { width: 44px; text-align: right; font-size: 13px; color: var(--muted); font-variant-numeric: tabular-nums; }
.rank-bar { position: absolute; left: 12px; right: 12px; bottom: 5px; height: 3px; border-radius: 3px; background: var(--grid); overflow: hidden; }
.rank-bar > span { display: block; height: 100%; min-width: 1.5%; border-radius: 3px; background: var(--accent); opacity: .75; }
.rank:hover .rank-bar > span { opacity: 1; }
```

## Heatmap tiers and cells

```js
/** Classed tiers at meaningful thresholds; the problem tier is warm, so the worst cell is the most visible. */
const tier = (rate) => (rate >= 95 ? "t1" : rate >= 90 ? "t2" : rate >= 85 ? "t3" : "warn");
```

```css
.heat { display: grid; gap: 6px; grid-template-columns: 52px repeat(var(--cols), minmax(0, 1fr)); }
.cell { display: grid; place-items: center; height: 34px; border-radius: 10px; font-size: 13px; font-weight: 600;
  font-variant-numeric: tabular-nums; color: var(--ink); }
.cell.t1 { background: color-mix(in srgb, var(--accent) 7%, transparent); color: var(--ink-2); }
.cell.t2 { background: color-mix(in srgb, var(--accent) 15%, transparent); }
.cell.t3 { background: color-mix(in srgb, var(--accent) 26%, transparent); }
.cell.warn { background: color-mix(in srgb, var(--warn) 30%, transparent); text-decoration: underline 2px; text-underline-offset: 2px; }
.cell.none { border: 1.5px dashed color-mix(in srgb, var(--warn) 75%, transparent); }
@media (max-width: 400px) { .heat { grid-template-columns: 36px repeat(var(--cols), minmax(0, 1fr)); gap: 5px; } .cell { height: 32px; } }
```

Each cell has `tabindex="0"` and a `data-tip` with its row, column and exact value ("Đà Nẵng · Week 44 · 82%").

## Bullet graph

```html
<div class="bullet" role="img" aria-label="Revenue: ₫48B of target ₫60B"
  style="--poor: 50%; --fair: 75%; --value: 80%; --target: 100%">
  <span class="range r3"></span><span class="range r2"></span><span class="range r1"></span>
  <span class="measure"></span><span class="target"></span>
</div>
```

```css
/* Few's spec: bar a third of the track, target as a perpendicular line, ranges darker for poor. Widths are
   percentages of the scale's maximum. */
.bullet { position: relative; height: 24px; }
.bullet .range { position: absolute; inset: 0 auto 0 0; }
.bullet .r3 { width: 100%; background: color-mix(in srgb, var(--ink) 10%, transparent); }
.bullet .r2 { width: var(--fair); background: color-mix(in srgb, var(--ink) 25%, transparent); }
.bullet .r1 { width: var(--poor); background: color-mix(in srgb, var(--ink) 40%, transparent); }
.bullet .measure { position: absolute; left: 0; top: 33%; height: 33%; width: var(--value); background: var(--ink); border-radius: 2px; }
.bullet .target { position: absolute; top: 4px; bottom: 4px; left: var(--target); width: 2px; background: var(--ink); }
```

## Segmented share bar

```js
function shareBar(el, parts, total) {
  el.className = "segs";
  el.setAttribute("role", "img");
  el.setAttribute("aria-label", parts.map((p) => `${p.label} ${fmt.short.format(p.value)}`).join(", "));
  el.replaceChildren(...parts.filter((p) => p.value > 0).map((p) => {
    const s = document.createElement("span");
    s.style.flex = String(p.value);
    s.style.background = p.color; // a token: "var(--accent)", or a tier mixed from it
    s.dataset.tip = `${p.label}: ${fmt.money.format(p.value)} · ${share((100 * p.value) / total)}`;
    return s;
  }));
}
```

```css
.segs { display: flex; gap: 6px; }
.segs > span { height: 10px; min-width: 10px; border-radius: 999px; }
```

## One tooltip for the whole page

```html
<div class="tip" id="tip" role="tooltip"></div>
```

```css
.tip { position: fixed; z-index: 10; max-width: 260px; padding: 6px 10px; border-radius: 8px; background: var(--tip);
  color: #fff; font-size: 12px; line-height: 1.4; white-space: pre-line; pointer-events: none; opacity: 0; transition: opacity .1s; }
.tip.show { opacity: 1; }
```

```js
/** Any element with data-tip gets the same dark tooltip, on hover and on keyboard focus. SVG elements too. */
const tip = document.getElementById("tip");
function showTip(el) {
  tip.textContent = el.dataset.tip;
  tip.classList.add("show");
  const r = el.getBoundingClientRect(), w = tip.offsetWidth, h = tip.offsetHeight;
  tip.style.left = `${Math.max(8, Math.min(innerWidth - w - 8, r.left + r.width / 2 - w / 2))}px`;
  tip.style.top = `${r.top - h - 8 < 8 ? r.bottom + 8 : r.top - h - 8}px`; // flip below near the top edge
}
const hideTip = () => tip.classList.remove("show");
for (const type of ["pointerover", "focusin"])
  document.addEventListener(type, (e) => { const el = e.target.closest?.("[data-tip]"); el ? showTip(el) : hideTip(); });
document.addEventListener("scroll", hideTip, true);
document.addEventListener("keydown", (e) => { if (e.key === "Escape") hideTip(); });
```
