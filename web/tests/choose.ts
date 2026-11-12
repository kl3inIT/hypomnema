import type { Locator, Page } from "@playwright/test"

// choose picks an option of a Select as the owner does: it opens the list from its trigger and clicks the option.
export async function choose(page: Page, trigger: Locator, option: string) {
  await trigger.click()
  await page.getByRole("option", { name: option, exact: true }).click()
}
