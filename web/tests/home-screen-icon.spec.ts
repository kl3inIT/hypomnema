import { expect, test } from "@playwright/test"

import { shot } from "./shot.js"

// The octopus as the app's icon on a phone's home screen (issue #728): the page links the touch icon and the web
// manifest, and both answer with their types; the manifest names icons that answer too.

const icons = [
  "/favicon-32x32.png",
  "/apple-touch-icon.png",
  "/icon-192x192.png",
  "/icon-512x512.png",
]

test("the page links a touch icon and a web manifest that the server serves", async ({
  page,
  request,
}) => {
  await page.goto("/")
  await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveAttribute(
    "href",
    "/apple-touch-icon.png",
  )
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
    "href",
    "/manifest.webmanifest",
  )
  await expect(page.locator('link[rel="icon"][type="image/svg+xml"]')).toHaveAttribute(
    "href",
    "/favicon.svg",
  )

  const manifest = await request.get("/manifest.webmanifest")
  expect(manifest.status()).toBe(200)
  expect(manifest.headers()["content-type"]).toContain("application/manifest+json")
  const body = (await manifest.json()) as {
    name: string
    display: string
    icons: { src: string }[]
  }
  expect(body.name).toBe("Hypomnema")
  expect(body.display).toBe("standalone")
  expect(body.icons.map((icon) => icon.src)).toEqual(icons)

  for (const icon of icons) {
    const response = await request.get(icon)
    expect(response.status(), icon).toBe(200)
    expect(response.headers()["content-type"], icon).toBe("image/png")
  }

  // A picture of the icons, side by side on a grey that shows where each opaque icon ends, for the owner to look at.
  await page.setContent(
    `<body style="margin:0;padding:24px;display:flex;gap:24px;align-items:end;flex-wrap:wrap;background:#71717a">` +
      icons.map((icon) => `<img src="${new URL(icon, page.url()).href}" alt="${icon}">`).join("") +
      `</body>`,
  )
  await expect(page.locator("img")).toHaveCount(icons.length)
  await page.waitForFunction(
    `[...document.images].every((img) => img.complete && img.naturalWidth > 0)`,
  )
  await shot(page, "home-screen-icons")
})
