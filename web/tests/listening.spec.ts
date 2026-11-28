import { expect, test, type Locator, type Page } from "@playwright/test"

import type { Listening, ListeningSource } from "../src/api/generated/types.gen.js"
import { shot } from "./shot.js"

// shotOf keeps a picture of one element when UI_CHECK names a folder, as shot keeps the page's.
async function shotOf(element: Locator, name: string) {
  const dir = process.env.UI_CHECK
  if (!dir) return

  const size = element.page().viewportSize()
  const device = size && size.width < 600 ? "phone" : "desktop"
  await element.screenshot({ path: `${dir}/${name}-${device}.png`, animations: "disabled" })
}

// The Social listening page of Settings → Gateway (issue #1290), built, against a fake client API, on a phone and a
// desktop. UI_THEME picks the theme of the pictures UI_CHECK keeps. The fake lists the web and Reddit, the sources
// this control plane has, and two made-up ones in the states they have none in yet: a source not read for a day,
// and Muse switched off, whose accounts are on its own page.

test.beforeEach(async ({ page }) => {
  const theme = process.env.UI_THEME
  if (theme) await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
})

const now = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()
const hour = 3_600_000

function start(): Listening {
  const web: ListeningSource = {
    name: "web",
    label: "The web",
    adds: "Forum threads, news, reviews and blog posts the web search's keyless providers find.",
    on: true,
    group: "working",
    regressed: false,
    checked_at: iso(now - 2 * hour),
    last_success_at: iso(now - 2 * hour),
    items_this_week: 184,
    // It failed yesterday and works again: the card still shows that last error (issue #1290's card row).
    last_error: {
      code: "timeout",
      detail: "every keyless provider answered 504",
      backend: "web_search",
      at: iso(now - 20 * hour),
      fix: "The web did not answer in time, which is no sign that it is broken. It is asked again in a minute.",
    },
    backends: [
      { name: "web_search", opt_in: false, state: "ready", last_ok_at: iso(now - 2 * hour) },
    ],
  }
  const reddit: ListeningSource = {
    name: "reddit",
    label: "Reddit",
    adds: "Reddit threads with their subreddit, author, date, score and number of comments.",
    on: true,
    group: "broken",
    code: "broken",
    regressed: true,
    checked_at: iso(now - hour),
    last_success_at: iso(now - 26 * hour),
    items_this_week: 37,
    last_error: {
      code: "broken",
      detail: "arctic-shift not read: HTTP 502",
      backend: "web_arctic",
      at: iso(now - hour),
      fix: "Reddit answered something other than its usual answer: it may have changed. Test it again; if it stays broken, the backend needs fixing.",
    },
    backends: [
      {
        name: "reddit_json",
        opt_in: false,
        state: "resting",
        resting_until: iso(now + 5 * hour),
        rest_code: "refused",
        last_error: {
          code: "refused",
          detail: "HTTP 403",
          backend: "reddit_json",
          at: iso(now - hour),
          fix: "Reddit refused this server's network.",
        },
      },
      {
        name: "web_arctic",
        opt_in: false,
        state: "ready",
        last_ok_at: iso(now - 26 * hour),
        last_error: {
          code: "broken",
          detail: "arctic-shift not read: HTTP 502",
          backend: "web_arctic",
          at: iso(now - hour),
          fix: "",
        },
      },
    ],
  }
  const youtube: ListeningSource = {
    name: "youtube",
    label: "YouTube",
    adds: "Videos with their transcripts and top comments.",
    on: true,
    group: "unverified",
    regressed: false,
    checked_at: iso(now - 30 * hour),
    last_success_at: iso(now - 30 * hour),
    items_this_week: 12,
    backends: [{ name: "yt_dlp", opt_in: false, state: "ready", last_ok_at: iso(now - 30 * hour) }],
  }
  const muse: ListeningSource = {
    name: "muse",
    label: "Muse",
    adds: "Public Facebook groups and Pages, Instagram and Threads posts, through your Muse accounts.",
    page: "/settings/muse",
    on: false,
    group: "could_be_on",
    regressed: false,
    // Read before the owner switched it off: the table still says when.
    checked_at: iso(now - 72 * hour),
    last_success_at: iso(now - 72 * hour),
    items_this_week: 5,
    backends: [{ name: "muse_ask", opt_in: false, state: "ready" }],
  }

  return {
    counts: { working: 1, unverified: 1, broken: 1, could_be_on: 1 },
    sources: [web, reddit, youtube, muse],
  }
}

function count(world: Listening) {
  const counts = { working: 0, unverified: 0, broken: 0, could_be_on: 0 }
  for (const s of world.sources) counts[s.group]++
  world.counts = counts
}

async function fakeServer(
  page: Page,
  options: {
    failTest?: boolean
    hold?: Promise<void>
    holdTest?: Promise<void>
    failLoad?: boolean
  } = {},
) {
  const world = start()
  const calls: string[] = []

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const change = /^(PUT|POST) \/gateway\/listening\/sources\/([^/]+)(\/order|\/test)?$/.exec(call)
    if (change) {
      const [, , name, what] = change
      const source = world.sources.find((s) => s.name === name)
      if (!source) return json({ code: "not_found", message: "no listening source" }, 404)

      if (what === "/order") {
        const { backends } = request.postDataJSON() as { backends: string[] }
        calls.push(`${call} ${backends.join(",")}`)
        source.backends = backends.map((b) => source.backends.find((x) => x.name === b)!)
      } else if (what === "/test") {
        calls.push(call)
        if (options.holdTest) await options.holdTest
        if (options.failTest)
          return json(
            { code: "invalid_request", message: "reddit is switched off; switch it on to test it" },
            400,
          )
        source.group = "working"
        source.code = undefined
        source.regressed = false
        source.checked_at = iso(Date.now())
        source.last_success_at = iso(Date.now())
        source.backends = source.backends.map((b) => ({
          ...b,
          state: "ready",
          resting_until: undefined,
          rest_code: undefined,
          last_ok_at: iso(Date.now()),
        }))
      } else {
        const { on } = request.postDataJSON() as { on: boolean }
        calls.push(`${call} ${on}`)
        source.on = on
        source.group = on ? "unverified" : "could_be_on"
      }

      count(world)

      return json(world)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/listening":
        if (options.hold) await options.hold
        if (options.failLoad)
          return json(
            { code: "unavailable", message: "this control plane has no social listening" },
            503,
          )

        return json(world)
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      default:
        return json({ data: [] })
    }
  })

  return { calls, world }
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

const card = (page: Page, label: string) => page.getByRole("article", { name: label, exact: true })

// Scenario: the owner opens Social listening from Settings, after Web search and Muse. The four groups are counted:
// the web works, Reddit is broken and regressed with its "!" badge, its last error and fix, and its JSON backend
// resting; YouTube is on but not read for a day; Muse is off, says what it would add, and links to its page.
test("the owner sees each source in one of four counted groups", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings")

  const entries = page.getByRole("navigation", { name: "Settings" }).last().getByRole("link")
  await expect(entries.filter({ hasText: "Social listening" })).toBeVisible()
  const names = (await entries.allTextContents()).map((n) => n.trim())
  expect(names.indexOf("Social listening")).toBe(names.indexOf("Muse") + 1)
  expect(names.indexOf("Social listening")).toBeGreaterThan(names.indexOf("Web search"))

  await page.getByRole("link", { name: "Social listening" }).last().click()
  await expect(page.getByRole("heading", { name: "Social listening", level: 1 })).toBeVisible()

  const counts = page.getByRole("group", { name: "Sources by state" })
  await expect(counts.getByRole("button", { name: /Working/ })).toContainText("1")
  await expect(counts.getByRole("button", { name: /On but unverified/ })).toContainText("1")
  await expect(counts.getByRole("button", { name: /Broken/ })).toContainText("1")
  await expect(counts.getByRole("button", { name: /Could be on/ })).toContainText("1")

  await expect(card(page, "The web")).toContainText("Working")
  await expect(card(page, "The web")).toContainText("184 items this week")

  const reddit = card(page, "Reddit")
  await expect(reddit).toContainText("Broken")
  await expect(reddit).toContainText("Regressed")
  await expect(reddit.getByLabel("Needs you")).toBeVisible()
  await expect(reddit.getByRole("note", { name: "Last error" })).toContainText("Test it again")
  await expect(reddit.getByRole("listitem", { name: "Reddit's public JSON" })).toContainText(
    "Resting until",
  )

  await expect(card(page, "YouTube")).toContainText("On but unverified")
  await expect(card(page, "YouTube").getByRole("button", { name: "Test again" })).toBeVisible()

  const muse = card(page, "Muse")
  await expect(muse).toContainText("Could be on")
  await expect(muse).toContainText("Switch it on to add: Public Facebook groups")
  await expect(muse.getByRole("link", { name: "Its accounts" })).toHaveAttribute(
    "href",
    "/settings/muse",
  )
  await expect(muse.getByLabel("Needs you")).toHaveCount(0)

  expect(await fits(page)).toBe(true)
  await shot(page, "listening-grid")
  // The broken card alone, not the page again (review round 1: it was the grid's picture twice on a desktop).
  await shotOf(reddit, "listening-broken-card")
})

// Scenario: a press anywhere on the broken card tests Reddit again (postiz: click to reconnect); it works again.
test("a press on a broken card tests it again", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/social-listening")

  const reddit = card(page, "Reddit")
  // A press on the card's text, away from its controls (the overlay takes it, as a person's press).
  await reddit.click({ position: { x: 60, y: 70 } })

  await expect(reddit).toContainText("Working")
  await expect(reddit.getByLabel("Needs you")).toHaveCount(0)
  expect(calls).toEqual(["POST /gateway/listening/sources/reddit/test"])
  await expect(page.getByRole("button", { name: /Working/ })).toContainText("2")
  await shot(page, "listening-tested")
})

// Scenario: a test the server refuses says why on the card.
test("a test that fails says why", async ({ page }) => {
  await fakeServer(page, { failTest: true })
  await page.goto("/settings/social-listening")

  const reddit = card(page, "Reddit")
  await reddit.getByRole("button", { name: "Test again" }).click()
  await expect(reddit.getByRole("alert")).toContainText("switch it on to test it")
  await shot(page, "listening-test-error")
})

// Scenario: the owner switches the web off and Muse on; he moves Reddit's archive route first.
test("the owner switches sources and orders a source's backends", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/social-listening")

  await card(page, "The web").getByRole("switch").click()
  await expect(card(page, "The web")).toContainText("Could be on")
  await expect(card(page, "The web")).toContainText("Switch it on to add")

  await card(page, "Muse").getByRole("switch").click()
  await expect(card(page, "Muse")).toContainText("On but unverified")

  await card(page, "Reddit")
    .getByRole("button", { name: "Move Web search + Arctic Shift archive up" })
    .click()
  await expect(card(page, "Reddit").getByRole("listitem").first()).toHaveAccessibleName(
    "Web search + Arctic Shift archive",
  )

  expect(calls).toEqual([
    "PUT /gateway/listening/sources/web false",
    "PUT /gateway/listening/sources/muse true",
    "PUT /gateway/listening/sources/reddit/order web_arctic,reddit_json",
  ])
  await shot(page, "listening-switched")
})

// Scenario: the owner chooses the Broken group, then the table.
test("the owner shows one group, and the table", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/social-listening")

  await page.getByRole("button", { name: /Broken/ }).click()
  await expect(page.getByRole("article")).toHaveCount(1)
  await expect(card(page, "Reddit")).toBeVisible()
  await shot(page, "listening-filter-broken")

  await page.getByRole("button", { name: /Broken/ }).click()
  await page.getByRole("button", { name: "table" }).click()
  const rows = page.getByRole("row")
  await expect(rows).toHaveCount(5)
  await expect(page.getByRole("row", { name: "Reddit" })).toContainText("Regressed")
  await expect(page.getByRole("row", { name: "Reddit" })).toContainText("Resting until")
  await shot(page, "listening-table")

  await page.reload()
  await expect(page.getByRole("table")).toBeVisible()
})

// Scenario: the page while the sources load, and when they cannot be read.
test("the page loading, and a load error", async ({ page }) => {
  let release = () => {}
  const hold = new Promise<void>((done) => (release = done))
  await fakeServer(page, { hold })
  await page.goto("/settings/social-listening")

  await expect(page.getByText("Loading…")).toBeVisible()
  await shot(page, "listening-loading")
  release()
  await expect(card(page, "Reddit")).toBeVisible()
})

test("a load error says why", async ({ page }) => {
  await fakeServer(page, { failLoad: true })
  await page.goto("/settings/social-listening")

  // The read is tried again a few times first, as every query of the app is.
  await expect(page.getByRole("alert")).toContainText("no social listening", { timeout: 20_000 })
  await shot(page, "listening-load-error")
})

// Scenario: the owner switches Muse on, then chooses Could be on: no source is in it.
test("an empty group says so", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/social-listening")

  await card(page, "Muse").getByRole("switch").click()
  await expect(card(page, "Muse")).toContainText("On but unverified")
  await page.getByRole("button", { name: /Could be on/ }).click()
  await expect(page.getByText("No source is could be on.")).toBeVisible()
  await shot(page, "listening-empty-group")
})

// Scenario: a test in progress: the card's button spins and cannot be pressed again.
test("a test in progress", async ({ page }) => {
  let release = () => {}
  const holdTest = new Promise<void>((done) => (release = done))
  await fakeServer(page, { holdTest })
  await page.goto("/settings/social-listening")

  const reddit = card(page, "Reddit")
  await reddit.getByRole("button", { name: "Test again", exact: true }).click()
  await expect(reddit.getByRole("button", { name: /Test again$/ })).toBeDisabled()
  await shot(page, "listening-test-pending")
  release()
  await expect(reddit).toContainText("Working")
})

// Scenario: the web works again after yesterday's timeout: its card still shows that last error, marked as before
// its recovery; the table says when an off source was last read and links Muse's accounts.
test("a recovered source keeps its last error, and the table shows an off source", async ({
  page,
}) => {
  await fakeServer(page)
  await page.goto("/settings/social-listening")

  const web = card(page, "The web").getByRole("note", { name: "Last error" })
  await expect(web).toContainText("Timed out")
  await expect(web).toContainText("Before it worked again")

  await page.getByRole("button", { name: "table" }).click()
  const muse = page.getByRole("row", { name: "Muse" })
  await expect(muse).toContainText("Could be on")
  await expect(muse).toContainText("Read")
  await expect(muse).toContainText("5")
  await expect(muse.getByRole("link", { name: "Its accounts" })).toHaveAttribute(
    "href",
    "/settings/muse",
  )
  await expect(page.getByRole("row", { name: "The web" })).toContainText("Timed out")
  await shot(page, "listening-table-off")
})
