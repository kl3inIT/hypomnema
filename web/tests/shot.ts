import type { Page } from "@playwright/test"

// shot keeps a picture for the owner to look at when UI_CHECK names a folder; a normal run takes none.
export async function shot(page: Page, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = page.viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await page.screenshot({ path: `${dir}/${name}-${device}.png` })
}
