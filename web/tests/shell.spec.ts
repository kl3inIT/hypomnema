import { expect, test } from "@playwright/test"

// Scenario: the built app loads on a phone and a desktop, dark by default, with the chat screen.
test("the app opens on the chat screen in the dark theme", async ({ page }) => {
  await page.goto("/")

  await expect(page.getByRole("heading", { name: "What should be built?" })).toBeVisible()
  await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible()
  await expect(page.locator("html")).toHaveClass(/dark/)
})
