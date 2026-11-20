// Draws the goldens of internal/controlplane/videos/compose (issue #908, PR 2, check 2) with Chromium, from
// spec.json beside it: each frame from aidemo's frame HTML fed our layout, on a solid ground, and each background
// with autodemo's background(). See README.md for how to run it.
//
// The frame's HTML and CSS are aidemo's renderFramePng (https://github.com/tandryukha/aidemo at commit b67235a,
// src/frame.ts:128-190), with its CSS pixels replaced by the layout's video pixels at a device scale of 1, the
// background by the solid ground, and no browser bar. MIT License, Copyright (c) 2026 Andrii Taran.
//
// The background is autodemo's (https://github.com/mdayan8/autodemo at commit 73e5408, src/compositor.html): TEMPLATES,
// blob and background() copied as they are (lines 21-29, 53-77), and the canvas set up as setup() does (line 71-73).
// MIT License, Copyright (c) 2026 Md Ayan.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
// documentation files (the "Software"), to deal in the Software without restriction, including without limitation
// the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all copies or substantial portions of
// the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO
// THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF
// CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
// IN THE SOFTWARE.

import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Playwright is the web client's: run from web/ (README.md).
const { chromium } = createRequire(join(process.cwd(), "package.json"))("@playwright/test");

const here = dirname(fileURLToPath(import.meta.url));
const spec = JSON.parse(readFileSync(join(here, "spec.json"), "utf8"));

function frameHtml(L, solid) {
  const W = L.size.width;
  const H = L.size.height;
  const device = L.cutout != null;
  const radius = L.hole_radius;
  const rTop = radius;
  const { x: winX, y: winY, w: winW, h: winH } = L.window;
  const { y: holeY, h: videoH } = L.hole;
  // SVG mask (alpha): opaque everywhere except the rounded video rect.
  const hole =
    `M${winX} ${holeY + rTop} ` +
    `a${rTop} ${rTop} 0 0 1 ${rTop} -${rTop} h${winW - 2 * rTop} ` +
    `a${rTop} ${rTop} 0 0 1 ${rTop} ${rTop} v${videoH - rTop - radius} ` +
    `a${radius} ${radius} 0 0 1 -${radius} ${radius} h-${winW - 2 * radius} ` +
    `a${radius} ${radius} 0 0 1 -${radius} -${radius} z`;
  const mask =
    `<svg xmlns='http://www.w3.org/2000/svg' width='${W}' height='${H}'>` +
    `<path fill='white' fill-rule='evenodd' d='M0 0h${W}v${H}H0z ${hole}'/></svg>`;
  const maskUrl = `url("data:image/svg+xml;utf8,${encodeURIComponent(mask)}")`;
  const shadow = !L.shadow
    ? "none"
    : `0 ${L.shadow_y[0]}px ${L.shadow_blur[0]}px rgba(0,0,0,.55), 0 ${L.shadow_y[1]}px ${L.shadow_blur[1]}px rgba(0,0,0,.35)`;
  let deviceHtml = "";
  let deviceCss = "";
  if (device) {
    const b = L.bezel;
    const c = L.cutout;
    deviceCss =
      `.bezel{position:absolute;left:${b.x}px;top:${b.y}px;width:${b.w}px;height:${b.h}px;` +
      `border-radius:${L.bezel_radius}px;background:#0b0b0d;border:${L.border}px solid #2b2d33;box-shadow:${shadow};}` +
      `.cam{position:absolute;background:#000;left:${c.left}px;top:${c.top}px;width:${c.w}px;height:${c.h}px;` +
      `border-radius:${c.radius}px;}`;
    deviceHtml = `<div class="cam"></div>`;
  }
  return `<!doctype html><html><head><meta charset="utf-8"><style>
    html,body{margin:0;padding:0;background:transparent;width:${W}px;height:${H}px;overflow:hidden;}
    *{box-sizing:border-box;}
    .canvas{position:absolute;inset:0;background:${solid};
      -webkit-mask-image:${maskUrl};mask-image:${maskUrl};}
    .win{position:absolute;left:${winX}px;top:${winY}px;width:${winW}px;height:${winH}px;
      border-radius:${radius}px;box-shadow:${device ? "none" : shadow};background:${device ? "#0b0b0d" : "#0b0e18"};}
    ${deviceCss}
  </style></head><body>
    <div class="canvas">${device ? '<div class="bezel"></div>' : ""}<div class="win"></div></div>${deviceHtml}
  </body></html>`;
}

// autodemo's src/compositor.html, the lines named above, as they are.
const backgroundScript = `
const cv = document.getElementById('c'), g = cv.getContext('2d');
let BG = ['#1b2140', '#3a2257'], BGIMG = null, U = 1; // U = scale unit (1 at 1080p)
const TEMPLATES = {
  midnight: ['#1b2140', '#3a2257'], sunset: ['#ff7e5f', '#7b2ff7'], ocean: ['#0f4c75', '#00b4d8'],
  forest: ['#134e3a', '#6fbf73'], peach: ['#ffd1b3', '#ff8fab'], mono: ['#111111', '#3a3a3a'], light: ['#eef1f6', '#cfd8e8'],
  aurora: ['#0b1026', '#141a3a'], grid: ['#0e1117', '#1a1f2b'],
  macos: ['#1d2b64', '#6a3093'], 'macos-light': ['#a8c0ff', '#f6d5f7'],
};
let STYLE = 'midnight', LOOK = { frame: 'none', label: '', url: '' };
const isMac = () => STYLE === 'macos' || STYLE === 'macos-light';
const blob = (x, y, r, c) => { const gr = g.createRadialGradient(x, y, 0, x, y, r); gr.addColorStop(0, c); gr.addColorStop(1, 'rgba(0,0,0,0)'); g.fillStyle = gr; g.fillRect(0, 0, cv.width, cv.height); };
function background(W, H) {
  if (BGIMG) { // cover-fit the image
    const k = Math.max(W / BGIMG.width, H / BGIMG.height), w = BGIMG.width * k, h = BGIMG.height * k;
    g.drawImage(BGIMG, (W - w) / 2, (H - h) / 2, w, h); return;
  }
  const bg = g.createLinearGradient(0, 0, W, H); bg.addColorStop(0, BG[0]); bg.addColorStop(1, BG[1]);
  g.fillStyle = bg; g.fillRect(0, 0, W, H);
  if (STYLE === 'aurora') { blob(W * .2, H * .2, W * .5, 'rgba(56,189,248,.45)'); blob(W * .8, H * .3, W * .45, 'rgba(168,85,247,.45)'); blob(W * .5, H * 1, W * .5, 'rgba(34,197,94,.35)'); return; }
  if (STYLE === 'grid') {
    g.strokeStyle = 'rgba(255,255,255,.06)'; g.lineWidth = 1;
    for (let x = 0; x < W; x += 48 * U) { g.beginPath(); g.moveTo(x, 0); g.lineTo(x, H); g.stroke(); }
    for (let y = 0; y < H; y += 48 * U) { g.beginPath(); g.moveTo(0, y); g.lineTo(W, y); g.stroke(); }
    blob(W * .5, H * .45, W * .45, 'rgba(99,102,241,.25)'); return;
  }
  if (isMac()) { // a desktop wallpaper: soft colour fields
    const light = STYLE === 'macos-light';
    blob(W * .15, H * .85, W * .55, light ? 'rgba(255,183,197,.7)' : 'rgba(236,72,153,.45)');
    blob(W * .85, H * .2, W * .55, light ? 'rgba(147,197,253,.7)' : 'rgba(59,130,246,.5)');
    blob(W * .6, H * .9, W * .4, light ? 'rgba(253,224,171,.6)' : 'rgba(249,115,22,.35)');
    return;
  }
  const glow = g.createRadialGradient(W * 0.8, H * 0.1, 0, W * 0.8, H * 0.1, W * 0.7);
  glow.addColorStop(0, 'rgba(255,255,255,.10)'); glow.addColorStop(1, 'rgba(255,255,255,0)');
  g.fillStyle = glow; g.fillRect(0, 0, W, H);
}
window.draw = (W, H, bg) => {
  cv.width = W; cv.height = H; U = Math.min(W, H) / 1080;
  if (Array.isArray(bg)) BG = bg;
  else if (TEMPLATES[bg]) { BG = TEMPLATES[bg]; STYLE = bg; }
  background(W, H);
  return cv.toDataURL('image/png');
};
`;

const browser = await chromium.launch({ headless: true });
try {
  console.log(`Chromium ${browser.version()}`);
  for (const f of spec.frames) {
    const { width, height } = f.layout.size;
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 1 });
    await page.setContent(frameHtml(f.layout, spec.solid), { waitUntil: "load" });
    await page.screenshot({ path: join(here, `${f.name}.png`), omitBackground: true });
    await page.close();
  }
  for (const b of spec.backgrounds) {
    const page = await browser.newPage({ viewport: { width: 64, height: 64 }, deviceScaleFactor: 1 });
    await page.setContent(`<!doctype html><html><body style="margin:0"><canvas id="c"></canvas><script>${backgroundScript}</script></body></html>`);
    const bg = b.background.colors ?? b.background.preset;
    const url = await page.evaluate(([W, H, bg]) => window.draw(W, H, bg), [b.width, b.height, bg]);
    writeFileSync(join(here, `${b.name}.png`), Buffer.from(url.split(",")[1], "base64"));
    await page.close();
  }
} finally {
  await browser.close();
}
