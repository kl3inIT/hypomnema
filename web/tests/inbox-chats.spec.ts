import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Issue #1270: which chats the secretary reads. The gear on the Inbox page opens a tab per account with its counts,
// the default for new chats, a search, the kind and state filters and a switch per chat with why it is in that state;
// the owner switches a chat off, on a phone and a computer, in light and dark. Every name and id is made up.

type Chat = {
  id: string
  name: string
  kind: "direct" | "group" | "bot"
  reads: boolean
  set_by: "default" | "owner"
  set_at?: string
  last_message_at?: string
}

const fixtures: Record<string, Chat[]> = {
  "zalo/main": [
    { id: "1001", name: "Lan Anh", kind: "direct", reads: true, set_by: "default" },
    { id: "2002", name: "Team Alpha", kind: "group", reads: true, set_by: "default" },
    {
      id: "2003",
      name: "Gia đình",
      kind: "group",
      reads: false,
      set_by: "owner",
      set_at: "2026-11-26T08:00:00Z",
    },
    { id: "3003", name: "Zalo Pay", kind: "bot", reads: false, set_by: "default" },
    { id: "4004", name: "Shop ABC OA", kind: "bot", reads: false, set_by: "default" },
  ],
  "whatsapp/me": [
    {
      id: "8490000001@s.whatsapp.net",
      name: "Kai",
      kind: "direct",
      reads: true,
      set_by: "default",
    },
    {
      id: "120363000000000001@g.us",
      name: "Family",
      kind: "group",
      reads: true,
      set_by: "default",
    },
  ],
  "lark/me": [
    { id: "oc_aaa", name: "Minh", kind: "direct", reads: true, set_by: "default" },
    { id: "oc_bbb", name: "Design review", kind: "group", reads: true, set_by: "default" },
  ],
}

const countOf = (chats: Chat[]) => ({
  all: chats.length,
  read: chats.filter((c) => c.reads).length,
  skipped: chats.filter((c) => !c.reads).length,
  direct: chats.filter((c) => c.kind === "direct").length,
  group: chats.filter((c) => c.kind === "group").length,
  bot: chats.filter((c) => c.kind === "bot").length,
})

// serve answers as the control plane, keeping the switches it is sent.
async function serve(page: Page) {
  const chats = structuredClone(fixtures)
  const set: { path: string; body: unknown }[] = []

  await page.context().route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/inbox/chat-sources") {
      return json({
        data: [
          {
            integration: "zalo",
            label: "main",
            account: "Đạt",
            counts: countOf(chats["zalo/main"]),
          },
          {
            integration: "whatsapp",
            label: "me",
            account: "+84 90 000 0000",
            counts: countOf(chats["whatsapp/me"]),
          },
          { integration: "lark", label: "me", account: "", counts: countOf(chats["lark/me"]) },
        ],
        defaults: { direct: true, group: true, bot: false, bot_patterns: ["\\bOA\\b"] },
      })
    }
    const one = path.match(/^\/inbox\/chat-sources\/(\w+)\/(\w+)(?:\/(.+))?$/)
    if (one) {
      const key = `${one[1]}/${one[2]}`
      if (request.method() === "PUT") {
        const body = request.postDataJSON() as { reads: boolean }
        const id = decodeURIComponent(one[3])
        set.push({ path, body })
        chats[key] = chats[key].map((c) =>
          c.id === id
            ? { ...c, reads: body.reads, set_by: "owner", set_at: new Date().toISOString() }
            : c,
        )
        return json({ integration: one[1], label: one[2], chat: id, reads: body.reads, set_at: "" })
      }
      const q = (url.searchParams.get("q") ?? "").toLowerCase()
      const kind = url.searchParams.get("kind")
      const state = url.searchParams.get("state")
      const kept = chats[key].filter(
        (c) =>
          c.name.toLowerCase().includes(q) &&
          (!kind || c.kind === kind) &&
          (!state || (state === "read") === c.reads),
      )
      return json({ data: kept, counts: countOf(chats[key]), matched: kept.length })
    }

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/inbox":
        return json({ data: [], total: 0 })
      case "/inbox/needs-you":
        return json({ count: 0 })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return set
}

const chatRow = (page: Page, name: string) =>
  page.getByTestId("secretary-chat").filter({ hasText: name })

for (const theme of ["light", "dark"] as const) {
  // Scenario: the owner opens the gear of his Inbox, finds his chat with Lan Anh by a part of her name and switches
  // it off; it then says he skipped it, and the counts follow. The skipped filter lists it with the bots.
  test(`the owner switches a chat off from the Inbox's gear in ${theme} mode`, async ({
    page,
    isMobile,
  }) => {
    await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
    const set = await serve(page)
    await page.goto("/inbox")
    if (isMobile) await page.keyboard.press("Escape")

    const gear = page.getByRole("link", { name: "Chats the secretary reads" })
    await expect(gear).toBeVisible()
    await shot(page, `inbox-gear-${theme}`)
    await gear.click()
    await expect(page).toHaveURL(/\/inbox\/chats$/)
    await expect(page.getByRole("heading", { name: "Chats the secretary reads" })).toBeVisible()
    await expect(page.getByTestId("chat-defaults")).toHaveText(
      "New chats: direct chats and groups are read; bots and official accounts are skipped.",
    )
    await expect(page.getByRole("tab", { name: /Zalo\s*5/ })).toBeVisible()
    await expect(page.getByRole("tab", { name: /WhatsApp\s*2/ })).toBeVisible()
    await expect(chatRow(page, "Gia đình")).toContainText(/Skipped · Set by you/)
    await expect(chatRow(page, "Zalo Pay")).toContainText("Bot · Skipped · Default for bots")
    await shot(page, `inbox-chats-${theme}`)

    await page.getByRole("searchbox", { name: "Search the chats of Zalo" }).fill("lan")
    await expect(page.getByTestId("secretary-chat")).toHaveCount(1)
    const lan = chatRow(page, "Lan Anh")
    await expect(lan).toContainText("Direct · Read · Default")
    await lan.getByRole("switch", { name: "The secretary reads Lan Anh" }).click()

    await expect
      .poll(() => set)
      .toEqual([{ path: "/inbox/chat-sources/zalo/main/1001", body: { reads: false } }])
    await expect(lan).toContainText(/Direct · Skipped · Set by you/)
    await expect(lan.getByRole("switch")).not.toBeChecked()
    await shot(page, `inbox-chats-switched-${theme}`)

    await page.getByRole("searchbox", { name: "Search the chats of Zalo" }).fill("")
    await page
      .getByRole("group", { name: "State" })
      .getByRole("button", { name: /Skipped/ })
      .click()
    await expect(page.getByTestId("secretary-chat")).toHaveText([
      /Lan Anh/,
      /Gia đình/,
      /Zalo Pay/,
      /Shop ABC OA/,
    ])
    await expect(
      page.getByRole("group", { name: "State" }).getByRole("button", { name: /Skipped/ }),
    ).toContainText("4")

    await page.getByRole("tab", { name: /WhatsApp/ }).click()
    await expect(chatRow(page, "Kai")).toContainText("Direct · Read · Default")
  })
}
