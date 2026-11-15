import type { Page } from "@playwright/test"

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
export async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  // A menu opening or a row's hover fading is caught half drawn otherwise (issue #470): wait for every animation
  // and transition that ends, not for a spinner, which never does, nor for one the page's scroll drives (the phone's
  // settings tabs fade at their edges as they scroll), which runs as long as the page is open.
  await page.waitForFunction(
    `document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity || a.timeline !== document.timeline)`,
  )
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}
