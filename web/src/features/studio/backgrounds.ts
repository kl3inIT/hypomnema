// Ported from autodemo (https://github.com/mdayan8/autodemo) at commit 73e5408, src/compositor.html: the background
// templates (TEMPLATES, lines 21-25), the scale unit U (line 83), blob() (line 28) and background() (lines 53-77),
// as the Go port paints them into a video (internal/controlplane/videos/compose/background.go). Issue #908, row 13;
// here they paint the swatches the Look section picks a background from (#938). Not ported: the image, the menu bar
// and the Dock of the macOS templates.
//
// MIT License
//
// Copyright (c) 2026 Md Ayan
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Background templates: a name below, or two colors ['#a', '#b'].
const TEMPLATES: Record<string, [string, string]> = {
  midnight: ["#1b2140", "#3a2257"],
  sunset: ["#ff7e5f", "#7b2ff7"],
  ocean: ["#0f4c75", "#00b4d8"],
  forest: ["#134e3a", "#6fbf73"],
  peach: ["#ffd1b3", "#ff8fab"],
  mono: ["#111111", "#3a3a3a"],
  light: ["#eef1f6", "#cfd8e8"],
  aurora: ["#0b1026", "#141a3a"],
  grid: ["#0e1117", "#1a1f2b"],
  macos: ["#1d2b64", "#6a3093"],
  "macos-light": ["#a8c0ff", "#f6d5f7"],
}

// templateColors are a template's two colors, the ones two colors start from when the owner picks his own.
export const templateColors = (id: string): [string, string] => TEMPLATES[id] ?? TEMPLATES.midnight!

// paintBackground draws a template (style) or two colors (bg) on a canvas of its own size, as background() does.
export function paintBackground(
  cv: HTMLCanvasElement,
  style: string,
  bg: [string, string] = templateColors(style),
) {
  const g = cv.getContext("2d")
  // A browser without a canvas (a test's DOM) leaves the swatch to its name.
  if (!g) return

  const W = cv.width
  const H = cv.height
  const U = Math.min(W, H) / 1080
  const isMac = style === "macos" || style === "macos-light"
  const blob = (x: number, y: number, r: number, c: string) => {
    const gr = g.createRadialGradient(x, y, 0, x, y, r)
    gr.addColorStop(0, c)
    gr.addColorStop(1, "rgba(0,0,0,0)")
    g.fillStyle = gr
    g.fillRect(0, 0, cv.width, cv.height)
  }

  const lin = g.createLinearGradient(0, 0, W, H)
  lin.addColorStop(0, bg[0])
  lin.addColorStop(1, bg[1])
  g.fillStyle = lin
  g.fillRect(0, 0, W, H)
  if (style === "aurora") {
    blob(W * 0.2, H * 0.2, W * 0.5, "rgba(56,189,248,.45)")
    blob(W * 0.8, H * 0.3, W * 0.45, "rgba(168,85,247,.45)")
    blob(W * 0.5, H * 1, W * 0.5, "rgba(34,197,94,.35)")
    return
  }
  if (style === "grid") {
    g.strokeStyle = "rgba(255,255,255,.06)"
    g.lineWidth = 1
    for (let x = 0; x < W; x += 48 * U) {
      g.beginPath()
      g.moveTo(x, 0)
      g.lineTo(x, H)
      g.stroke()
    }
    for (let y = 0; y < H; y += 48 * U) {
      g.beginPath()
      g.moveTo(0, y)
      g.lineTo(W, y)
      g.stroke()
    }
    blob(W * 0.5, H * 0.45, W * 0.45, "rgba(99,102,241,.25)")
    return
  }
  if (isMac) {
    // a desktop wallpaper: soft colour fields
    const light = style === "macos-light"
    blob(W * 0.15, H * 0.85, W * 0.55, light ? "rgba(255,183,197,.7)" : "rgba(236,72,153,.45)")
    blob(W * 0.85, H * 0.2, W * 0.55, light ? "rgba(147,197,253,.7)" : "rgba(59,130,246,.5)")
    blob(W * 0.6, H * 0.9, W * 0.4, light ? "rgba(253,224,171,.6)" : "rgba(249,115,22,.35)")
    return
  }
  const glow = g.createRadialGradient(W * 0.8, H * 0.1, 0, W * 0.8, H * 0.1, W * 0.7)
  glow.addColorStop(0, "rgba(255,255,255,.10)")
  glow.addColorStop(1, "rgba(255,255,255,0)")
  g.fillStyle = glow
  g.fillRect(0, 0, W, H)
}
