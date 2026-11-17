import { beforeEach, describe, expect, it, vi } from "vitest"

// Issue #747: Settings' "Back to app" returns to the last page outside Settings, kept for the tab, else home.
describe("back from settings", () => {
  beforeEach(() => {
    sessionStorage.clear()
    vi.resetModules()
  })

  it("returns home when nothing was kept", async () => {
    const { appPage } = await import("./back-from-settings")
    expect(appPage()).toBe("/")
  })

  it("returns to the last page outside Settings", async () => {
    const { appPage, rememberAppPage } = await import("./back-from-settings")
    rememberAppPage("/meetings/mtg_1?tab=transcript")
    rememberAppPage("/settings/meetings")
    rememberAppPage("/settings")
    expect(appPage()).toBe("/meetings/mtg_1?tab=transcript")
  })

  it("keeps the page across a reload inside Settings", async () => {
    const first = await import("./back-from-settings")
    first.rememberAppPage("/bots/bot_1")
    vi.resetModules()
    const reloaded = await import("./back-from-settings")
    expect(reloaded.appPage()).toBe("/bots/bot_1")
  })
})
