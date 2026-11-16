import { expect, type Page } from "@playwright/test"

// openMenu opens the app's sidebar on a phone, where it is a drawer behind the header's ≡ (issue #604); a desktop
// shows the sidebar beside the page, and nothing is done.
export async function openMenu(page: Page) {
  if ((page.viewportSize()?.width ?? 1280) >= 768) return

  await page.getByRole("button", { name: "Menu" }).first().click()
  await expect(page.getByRole("dialog", { name: "Sidebar" })).toBeVisible()
}
