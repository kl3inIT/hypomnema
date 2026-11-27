import type { Page } from "@playwright/test"

// pasteImage pastes a picture into the message box, as a screenshot copied to the clipboard is: a PNG drawn by the
// page itself. It runs in the page, written as text since this package's types have no DOM.
export async function pasteImage(page: Page) {
  await page.evaluate(`(async () => {
    const canvas = document.createElement("canvas")
    canvas.width = 480
    canvas.height = 300
    const g = canvas.getContext("2d")
    const fill = g.createLinearGradient(0, 0, 480, 300)
    fill.addColorStop(0, "#4f46e5")
    fill.addColorStop(1, "#06b6d4")
    g.fillStyle = fill
    g.fillRect(0, 0, 480, 300)
    g.fillStyle = "#ffffff"
    g.font = "bold 36px sans-serif"
    g.fillText("/health 200 OK", 110, 160)
    const blob = await new Promise((done) => canvas.toBlob(done, "image/png"))
    const data = new DataTransfer()
    data.items.add(new File([blob], "image.png", { type: "image/png" }))
    const box = document.querySelector('textarea[aria-label="Message"]')
    box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }))
  })()`)
}
