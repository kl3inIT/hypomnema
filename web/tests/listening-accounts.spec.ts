import { expect, test, type Page } from "@playwright/test"

import type { Listening, ListeningSource } from "../src/api/generated/types.gen.js"
import { shot } from "./shot.js"

// The signed-in sources of the Social listening page (issue #1291), built, against a fake client API, on a phone and
// a desktop: X before its consent card is agreed to, LinkedIn with its account stopped after a rate-limit page, then
// X through its consent, a pasted cookie header refused for a missing cookie, its account signed in, asking for a
// sign-in again, and signed out. Every account name and cookie here is made up. UI_THEME picks the pictures' theme.

test.beforeEach(async ({ page }) => {
  const theme = process.env.UI_THEME
  if (theme) await page.addInitScript((choice) => localStorage.setItem("theme", choice), theme)
})

const now = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()
const hour = 3_600_000

// The cards' words, as the server sends them (listening/signed.go, xConsent and linkedInConsent).
const xConsent =
  "Reading X as your account may break X's terms: automated use of a signed-in account can get it limited, " +
  "locked or suspended, and users of twscrape, which reads X the same way, report suspensions since 2026-11-21. " +
  "Sign in a secondary account made for this, never your own. Hypomnema then searches X as that account, one " +
  "search at a time, 10 seconds apart. Only X's cookies are kept, in the vault: they are never shown or logged " +
  "and go only to x.com. If X refuses the account or asks for a check, Hypomnema stops reading it until you sign in again."
const linkedInConsent =
  "LinkedIn's terms forbid automated access, so reading it as your account may bring a security check (a " +
  "challenge or a CAPTCHA) or a restriction of the account. Sign in a secondary account made for this, never your " +
  "own. Hypomnema then opens LinkedIn's Posts search as that account and reads only the pages LinkedIn shows, one " +
  "page at a time, 10 seconds apart. Only LinkedIn's cookies are kept, in the vault: they are never shown or logged " +
  "and go only to linkedin.com. After a security check it stops until you sign in again; after a rate-limit page, for 24 hours."

const tiktokConsent =
  "TikTok's terms forbid automated access, so reading it as your account may bring a security check (a CAPTCHA), " +
  "a limit or a ban of the account; users of TikTok-Api, which reads TikTok the same way, report TikTok answering " +
  "empty pages once it takes the reader for a bot. Sign in a secondary account made for this, never your own. " +
  "Hypomnema then opens TikTok as that account in its own tab of the shared browser and asks TikTok's web API from " +
  "there for a search's videos and the top comments of a few of them, one request at a time, 12 seconds apart. Only " +
  "TikTok's cookies are kept, in the vault: they are never shown or logged and go only to tiktok.com. If TikTok " +
  "refuses the account or answers empty, Hypomnema stops reading it until you sign in again; it never solves a CAPTCHA."

// tiktok is TikTok's card before its consent (issue #1294): its three backends, the account's first.
function tiktok(): ListeningSource {
  return {
    name: "tiktok",
    label: "TikTok",
    adds:
      "TikTok videos with their author, date, views, likes, comments and shares, and the top comments of the most " +
      "watched, searched as your account; without one, the TikTok videos the web search finds, read with yt-dlp.",
    on: false,
    group: "could_be_on",
    regressed: false,
    items_this_week: 0,
    signed: { consent: tiktokConsent, sign_in_url: "https://www.tiktok.com/login" },
    backends: [
      { name: "web_api", opt_in: false, state: "ready" },
      { name: "yt_dlp", opt_in: false, state: "ready" },
      { name: "site_search", opt_in: false, state: "ready" },
    ],
  }
}

function start(): Listening {
  const x: ListeningSource = {
    name: "x",
    label: "X",
    adds: "Posts on X with their author, date and likes, reposts and replies, searched as your account.",
    on: false,
    group: "could_be_on",
    regressed: false,
    items_this_week: 0,
    signed: { consent: xConsent, sign_in_url: "https://x.com/i/flow/login" },
    backends: [
      { name: "graphql", opt_in: false, state: "ready" },
      { name: "site_search", opt_in: false, state: "ready" },
    ],
  }
  const linkedin: ListeningSource = {
    name: "linkedin",
    label: "LinkedIn",
    adds: "LinkedIn posts from its Posts search, read as your account.",
    on: true,
    group: "working",
    regressed: false,
    checked_at: iso(now - hour),
    last_success_at: iso(now - hour),
    items_this_week: 9,
    signed: {
      consent: linkedInConsent,
      consented_at: iso(now - 48 * hour),
      sign_in_url: "https://www.linkedin.com/login",
      account: {
        label: "research",
        state: "signed_in",
        cause: "LinkedIn answered HTTP 999: it is throttling this account",
        signed_in_at: iso(now - 48 * hour),
        checked_at: iso(now - hour),
      },
      stopped_until: iso(now + 20 * hour),
      stop_cause:
        "LinkedIn answered HTTP 999 to /flagship-web/rsc-action: it is throttling this account",
    },
    backends: [
      { name: "rendered_page", opt_in: false, state: "ready" },
      { name: "site_search", opt_in: false, state: "ready", last_ok_at: iso(now - hour) },
    ],
  }

  return {
    counts: { working: 1, unverified: 0, broken: 0, could_be_on: 1 },
    sources: [x, linkedin],
  }
}

function count(world: Listening) {
  const counts = { working: 0, unverified: 0, broken: 0, could_be_on: 0 }
  for (const s of world.sources) counts[s.group]++
  world.counts = counts
}

async function fakeServer(page: Page) {
  const world = start()
  const calls: string[] = []
  let checks = 0

  await page.addInitScript({ content: "localStorage.setItem('hypomnema.token', 'secret')" })
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    const m =
      /^(POST|PUT|DELETE) \/gateway\/listening\/sources\/([^/]+)\/(consent|sign-in|sign-in\/check|account)$/.exec(
        call,
      )
    if (m) {
      const [, method, name, what] = m
      const source = world.sources.find((s) => s.name === name)!
      calls.push(call)

      switch (what) {
        case "consent":
          source.on = true
          source.group = "unverified"
          source.signed!.consented_at = iso(Date.now())
          break
        case "sign-in":
          return route.fulfill({ status: 204 })
        case "sign-in/check":
          checks++
          if (checks < 2)
            return json({
              done: false,
              waiting: "Sign in to X in the browser; waiting for its session.",
            })
          source.signed!.account = {
            label: "secondary",
            state: "signed_in",
            cause: "",
            signed_in_at: iso(Date.now()),
            checked_at: iso(Date.now()),
          }
          return json({ done: true })
        case "account":
          if (method === "DELETE") {
            source.signed!.account = undefined
            break
          }
          {
            const { cookies } = request.postDataJSON() as { cookies: string }
            // What each site's sign-in needs, as the server says it (signin.Site.FromHeader).
            const needs: Record<string, [string, string]> = {
              x: [
                "ct0=",
                "invalid: X's sign-in cookies are missing (ct0); copy the whole Cookie header from a browser signed in to x.com",
              ],
              tiktok: [
                "msToken=",
                "invalid: TikTok's sign-in cookies are missing (msToken); copy the whole Cookie header from a browser signed in to tiktok.com",
              ],
            }
            const need = needs[source.name]
            if (need && !cookies.includes(need[0]))
              return json({ code: "invalid_request", message: need[1] }, 400)
            source.signed!.account = {
              label: `${source.label} account`,
              state: "signed_in",
              cause: "",
              signed_in_at: iso(Date.now()),
            }
          }
          break
      }

      count(world)

      return json(world)
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/listening":
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

const card = (page: Page, label: string) => page.getByRole("article", { name: label, exact: true })
const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: X is off and its switch is its consent card; the owner reads the risks and turns it on; LinkedIn's
// account rests a day after a rate-limit page.
test("a signed-in source is turned on through its consent card", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/social-listening")

  const x = card(page, "X")
  await expect(x).toContainText("Could be on")
  await expect(x.getByRole("switch")).toHaveCount(0)
  await expect(
    card(page, "LinkedIn").getByRole("region", { name: "LinkedIn's account" }),
  ).toContainText("Stopped until")
  expect(await fits(page)).toBe(true)
  await shot(page, "listening-signed-sources")

  await x.getByRole("button", { name: "Read the risks and turn on" }).click()
  // A toast is a dialog too: each dialog is found by its title.
  const dialog = page.getByRole("dialog", { name: "Read X as your account?" })
  await expect(dialog).toContainText("suspensions since 2026-11-21")
  await shot(page, "listening-consent")

  await dialog.getByRole("button", { name: "I understand, turn it on" }).click()
  await expect(dialog).toHaveCount(0)
  await expect(x).toContainText("No account is signed in")
  expect(calls).toEqual(["POST /gateway/listening/sources/x/consent"])
  await shot(page, "listening-consented")
})

// Scenario: the owner pastes a cookie header without ct0: it is refused, naming the cookie; then the whole header.
test("a pasted cookie header is checked for the sign-in cookies", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/social-listening")

  const x = card(page, "X")
  await x.getByRole("button", { name: "Read the risks and turn on" }).click()
  await page.getByRole("button", { name: "I understand, turn it on" }).click()

  await x.getByRole("button", { name: "Paste cookies" }).click()
  const dialog = page.getByRole("dialog", { name: "Paste X's cookies" })
  await dialog.getByLabel("Cookie header").fill("guest_id=made-up; auth_token=made-up")
  await dialog.getByRole("button", { name: "Check and keep" }).click()
  await expect(dialog.getByRole("alert")).toContainText("(ct0)")
  await shot(page, "listening-paste-refused")

  await dialog.getByLabel("Cookie header").fill("auth_token=made-up; ct0=made-up")
  await dialog.getByRole("button", { name: "Check and keep" }).click()
  await expect(dialog).toHaveCount(0)
  await expect(x.getByRole("region", { name: "X's account" })).toContainText("Signed in")
  await shot(page, "listening-signed-in")
})

// Scenario: the owner signs in in his own tab, shown in the dialog, which waits until the session is checked.
test("the owner signs in in his tab of the shared browser", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/social-listening")

  const x = card(page, "X")
  await x.getByRole("button", { name: "Read the risks and turn on" }).click()
  await page.getByRole("button", { name: "I understand, turn it on" }).click()

  await x.getByRole("button", { name: "Sign in", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: "Sign X's account in" })
  await dialog.getByLabel("Name").fill("secondary")
  await dialog.getByRole("button", { name: "Open X" }).click()
  await expect(dialog).toContainText("waiting for its session")
  await shot(page, "listening-sign-in-waiting")

  await expect(dialog).toHaveCount(0, { timeout: 10_000 })
  await expect(x.getByRole("region", { name: "X's account" })).toContainText("secondary")
  expect(calls.slice(0, 3)).toEqual([
    "POST /gateway/listening/sources/x/consent",
    "POST /gateway/listening/sources/x/sign-in",
    "POST /gateway/listening/sources/x/sign-in/check",
  ])
})

// Scenario: X's session ended: the card wears "!" and asks for a sign-in again; the owner signs the account out.
test("an account that must sign in again wears the ! and can be signed out", async ({ page }) => {
  const { world } = await fakeServer(page)
  const x = world.sources[0]!
  x.on = true
  x.group = "working"
  x.checked_at = iso(now - hour)
  x.last_success_at = iso(now - hour)
  x.items_this_week = 6
  x.signed!.consented_at = iso(now - hour)
  x.signed!.account = {
    label: "secondary",
    state: "need_login",
    cause: "X answered 401: the account's session ended",
    signed_in_at: iso(now - 72 * hour),
    checked_at: iso(now - hour),
  }
  count(world)
  await page.goto("/settings/social-listening")

  const xCard = card(page, "X")
  await expect(xCard.getByLabel("Needs you")).toBeVisible()
  await expect(xCard).toContainText("X answered 401")
  await expect(xCard.getByRole("button", { name: "Sign in again" })).toBeVisible()
  // The web served it meanwhile: a test fixes nothing here, only a sign-in does.
  await expect(xCard.getByRole("button", { name: "Test again" })).toHaveCount(0)
  await shot(page, "listening-need-login")

  await page.getByRole("button", { name: "table" }).click()
  await expect(page.getByRole("row", { name: "X" })).toContainText("Sign in again")
  await shot(page, "listening-need-login-table")
  await page.getByRole("button", { name: "grid" }).click()

  await card(page, "X").getByRole("button", { name: "Sign out" }).click()
  await expect(page.getByRole("alertdialog")).toContainText("no other site's")
  await shot(page, "listening-sign-out")
  await page.getByRole("alertdialog").getByRole("button", { name: "Sign out" }).click()
  await expect(card(page, "X")).toContainText("No account is signed in")
})

// Scenario: LinkedIn before its consent: its card's own words, the security check and the 24-hour stop included.
test("LinkedIn's consent card says its own risks", async ({ page }) => {
  const { world } = await fakeServer(page)
  const linkedin = world.sources[1]!
  linkedin.on = false
  linkedin.group = "could_be_on"
  linkedin.signed = { consent: linkedInConsent, sign_in_url: "https://www.linkedin.com/login" }
  count(world)
  await page.goto("/settings/social-listening")

  await card(page, "LinkedIn").getByRole("button", { name: "Read the risks and turn on" }).click()
  const dialog = page.getByRole("dialog", { name: "Read LinkedIn as your account?" })
  await expect(dialog).toContainText("for 24 hours")
  await shot(page, "listening-consent-linkedin")
})

// Scenario: TikTok before its consent: its card names its three backends, its consent card its own risks (the
// CAPTCHA it never solves, its 12-second pace); once agreed, a pasted header without msToken is refused for it.
test("TikTok's consent card says its own risks and its sign-in needs msToken", async ({ page }) => {
  const { world, calls } = await fakeServer(page)
  world.sources.push(tiktok())
  count(world)
  await page.goto("/settings/social-listening")

  const tt = card(page, "TikTok")
  await expect(tt).toContainText("Could be on")
  await expect(tt.getByRole("list", { name: "TikTok's backends" })).toContainText(
    "TikTok's web API, as your account",
  )
  expect(await fits(page)).toBe(true)
  await tt.scrollIntoViewIfNeeded()
  await shot(page, "listening-tiktok")

  await tt.getByRole("button", { name: "Read the risks and turn on" }).click()
  const dialog = page.getByRole("dialog", { name: "Read TikTok as your account?" })
  await expect(dialog).toContainText("never solves a CAPTCHA")
  await expect(dialog).toContainText("12 seconds apart")
  await shot(page, "listening-consent-tiktok")
  await dialog.getByRole("button", { name: "I understand, turn it on" }).click()
  await expect(tt).toContainText("No account is signed in")

  await tt.getByRole("button", { name: "Paste cookies" }).click()
  const paste = page.getByRole("dialog", { name: "Paste TikTok's cookies" })
  await paste.getByLabel("Cookie header").fill("sessionid=made-up")
  await paste.getByRole("button", { name: "Check and keep" }).click()
  await expect(paste.getByRole("alert")).toContainText("(msToken)")
  await shot(page, "listening-paste-refused-tiktok")

  await paste.getByLabel("Cookie header").fill("sessionid=made-up; msToken=made-up")
  await paste.getByRole("button", { name: "Check and keep" }).click()
  await expect(paste).toHaveCount(0)
  await expect(tt.getByRole("region", { name: "TikTok's account" })).toContainText("Signed in")
  expect(calls).toContain("PUT /gateway/listening/sources/tiktok/account")
})
