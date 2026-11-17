// Renders the octopus in public/favicon.svg into the PNG icons a phone's home screen and the web manifest use
// (issue #728): `pnpm icons`, after `pnpm exec playwright install chromium`. The references commit the PNGs (t3code
// apps/web/public, OpenClaw ui/public, Omnigent web/public); this keeps how they were made, through the Chromium
// Playwright already brings.
import { readFile, writeFile } from "node:fs/promises"

import { chromium } from "@playwright/test"

const publicDir = new URL("../public/", import.meta.url)

// The page's colour, web/index.html's theme-color: iOS fills a transparent touch icon with black, so the home-screen
// icons are opaque.
const background = "#0a0a0a"

type Icon = { file: string; size: number; opaque: boolean }

const icons: Icon[] = [
  { file: "apple-touch-icon.png", size: 180, opaque: true },
  { file: "icon-192x192.png", size: 192, opaque: true },
  { file: "icon-512x512.png", size: 512, opaque: true },
  { file: "favicon-32x32.png", size: 32, opaque: false },
]

// The share of the side the octopus takes on an opaque icon, leaving room for the corners a launcher rounds off.
const opaqueScale = 0.8

const svg = await readFile(new URL("favicon.svg", publicDir), "utf8")
const browser = await chromium.launch()

try {
  for (const icon of icons) {
    const page = await browser.newPage({ viewport: { width: icon.size, height: icon.size } })
    const side = icon.opaque ? Math.round(icon.size * opaqueScale) : icon.size
    await page.setContent(
      `<body style="margin:0;display:grid;place-items:center;width:${icon.size}px;height:${icon.size}px;` +
        `background:${icon.opaque ? background : "transparent"}">` +
        svg.replace("<svg ", `<svg width="${side}" height="${side}" `) +
        `</body>`,
    )
    const png = await page.screenshot({ omitBackground: !icon.opaque })
    await writeFile(new URL(icon.file, publicDir), png)
    await page.close()
    console.log(`public/${icon.file} ${icon.size}x${icon.size}`)
  }
} finally {
  await browser.close()
}
