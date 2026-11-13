import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Usage page of Settings (issue #199, step 4), built, against a fake client API, on a phone and a desktop.

const now = Date.now()
const iso = (ms: number) => new Date(ms).toISOString()
const hour = 3_600_000

const counts = (requests: number, failed: number, prompt: number, cached: number, out: number) => ({
  requests,
  failed,
  prompt_tokens: prompt,
  cached_tokens: cached,
  completion_tokens: out,
  reasoning_tokens: Math.round(out / 5),
  cost_micros: Math.round(prompt * 0.6 + out * 12),
  unpriced: failed > 5 ? 1 : 0,
})

function usage(period: string) {
  const day = period === "24h" ? 1 : period === "7d" ? 7 : 30
  const hourly = period === "24h"
  const n = hourly ? 24 : day
  const size = hourly ? hour : 24 * hour
  const start = Math.floor(now / size) * size - (n - 1) * size

  return {
    since: iso(start),
    until: iso(now),
    bucket_seconds: size / 1000,
    totals: counts(412 * day, 9 * day, 18_400_000 * day, 15_100_000 * day, 310_000 * day),
    buckets: Array.from({ length: n }, (_, i) => ({
      start: iso(start + i * size),
      counts: counts(
        10 + (i % 5) * 4,
        i % 7 === 3 ? 2 : 0,
        400_000 + ((i * 97_000) % 600_000),
        300_000 + ((i * 71_000) % 450_000),
        6_000 + ((i * 1300) % 9000),
      ),
    })),
    by_model: [
      {
        id: "claude-opus-5-5",
        name: "claude-opus-5-5",
        counts: counts(201, 2, 11_200_000, 9_800_000, 160_000),
      },
      {
        id: "gpt-6.1-sol",
        name: "gpt-6.1-sol",
        counts: counts(150, 6, 5_900_000, 4_700_000, 120_000),
      },
      {
        id: "claude-sonnet",
        name: "claude-sonnet",
        counts: counts(61, 1, 1_300_000, 600_000, 30_000),
      },
    ],
    by_key: [
      {
        id: "gwk_1",
        name: "claude-code-laptop",
        counts: counts(240, 3, 12_000_000, 10_500_000, 190_000),
      },
      { id: "gwk_2", name: "chat-pi", counts: counts(172, 6, 6_400_000, 4_600_000, 120_000) },
    ],
    by_account: [
      {
        id: "acc_1",
        name: "kept",
        runtime: "claude",
        counts: counts(201, 2, 11_200_000, 9_800_000, 160_000),
      },
      {
        id: "acc_2",
        name: "chatgpt@example.com",
        runtime: "codex",
        counts: counts(150, 6, 5_900_000, 4_700_000, 120_000),
      },
    ],
  }
}

const request = (i: number, over: Record<string, unknown> = {}) => ({
  id: `gwu_${i}`,
  created_at: iso(now - i * 4 * 60_000),
  key_id: "gwk_1",
  key_name: "claude-code-laptop",
  model: "claude-opus-5-5",
  provider: "claude-subscription",
  upstream_model: "claude-opus-5-5",
  account_id: "acc_1",
  account_label: "kept",
  stream: true,
  attempts: 1,
  outcome: "ok",
  status: 200,
  prompt_tokens: 19_871,
  cached_tokens: 19_661,
  completion_tokens: 412,
  reasoning_tokens: 40,
  cache_write_tokens: 120,
  cost_micros: 9_120,
  priced_model: "claude-opus-5-5",
  estimated: false,
  duration_ms: 2400,
  ...over,
})

const refused = request(2, {
  model: "gpt-6.1-sol",
  provider: "codex-subscription",
  upstream_model: "gpt-6.1-sol",
  account_id: "acc_2",
  account_label: "chatgpt@example.com",
  outcome: "upstream_error",
  status: 429,
  error: "The usage limit has been reached. Try again in 2 hours.",
  prompt_tokens: 0,
  cached_tokens: 0,
  completion_tokens: 0,
  duration_ms: 640,
  cost_micros: undefined,
  priced_model: undefined,
})

async function fakeServer(page: Page, empty = false) {
  const asked: string[] = []

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown) =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })

    switch (`${route.request().method()} ${path}`) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/usage": {
        const period = url.searchParams.get("period") ?? "24h"
        asked.push(`usage ${period}`)
        const body = usage(period)

        return json(
          empty
            ? { ...body, totals: counts(0, 0, 0, 0, 0), by_model: [], by_key: [], by_account: [] }
            : body,
        )
      }
      case "GET /gateway/usage/requests": {
        const failed = url.searchParams.get("failed") === "true"
        const offset = Number(url.searchParams.get("offset") ?? 0)
        asked.push(`requests ${failed ? "failed" : "all"} ${offset}`)
        const all = [
          request(1),
          refused,
          request(3, {
            key_name: "chat-pi",
            model: "claude-sonnet",
            provider: "antigravity-subscription",
            upstream_model: "claude-sonnet-4-6",
            account_label: "zeromail",
            duration_ms: 1900,
          }),
        ]

        return json({
          data: failed ? [refused] : offset === 0 ? all : [request(60)],
          has_more: !failed && offset === 0,
        })
      }
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return asked
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens the Usage page. It shows the day's totals, the tokens an hour, the models with their
// marks and names, and the latest requests with where each went and how it ended.
test("the owner sees what the gateway served today", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings/usage")

  await expect(page.getByRole("heading", { name: "Usage" })).toBeVisible()
  await expect(page.getByText("Requests", { exact: true }).first()).toBeVisible()
  await expect(page.getByLabel("Totals")).toContainText("412")
  await expect(page.getByRole("heading", { name: "Tokens an hour" })).toBeVisible()
  const opus = page
    .getByRole("table", { name: "Usage by model" })
    .getByRole("row", { name: "claude-opus-5-5" })
  await expect(opus).toContainText("Claude Opus 5.5")
  await expect(opus).toContainText("11.2M")
  await expect(page.getByLabel("Totals")).toContainText("API cost")

  const requests = page.getByRole("table", { name: "Requests" })
  await expect(requests.getByRole("row")).toHaveCount(4)
  await expect(requests).toContainText("Refused")
  await expect(requests).toContainText("chatgpt@example.com")
  expect(await fits(page)).toBe(true)
  await shot(page, "usage")
})

// Scenario: the owner looks at the week by account, then at the failures, and opens the one Codex refused: its
// message is there, with where it went.
test("the owner finds why a request failed", async ({ page }) => {
  const asked = await fakeServer(page)
  await page.goto("/settings/usage")

  await page.getByRole("button", { name: "7 days" }).click()
  await expect(page.getByRole("heading", { name: "Tokens a day" })).toBeVisible()
  await page.getByRole("tab", { name: "By account" }).click()
  await expect(page.getByRole("table", { name: "Usage by account" })).toContainText("kept")

  await page.getByRole("switch", { name: "Failed only" }).click()
  const requests = page.getByRole("table", { name: "Requests" })
  await expect(requests.getByRole("row")).toHaveCount(2)
  await requests.getByRole("row", { name: /GPT/ }).click()

  const sheet = page.getByRole("dialog")
  await expect(sheet).toContainText("The provider answered 429")
  await expect(sheet).toContainText("The usage limit has been reached")
  await expect(sheet).toContainText("chatgpt@example.com")
  await expect(sheet).toContainText("API cost")
  await page.waitForTimeout(300)
  await shot(page, "usage-request")

  expect(asked).toContain("usage 7d")
  expect(asked).toContain("requests failed 0")
})

// Scenario: older requests come a page at a time.
test("the owner pages to older requests", async ({ page }) => {
  const asked = await fakeServer(page)
  await page.goto("/settings/usage")

  await page.getByRole("button", { name: "Older" }).click()
  await expect(page.getByRole("table", { name: "Requests" }).getByRole("row")).toHaveCount(2)
  await expect(page.getByRole("button", { name: "Newer" })).toBeEnabled()
  expect(asked).toContain("requests all 50")
})

// With nothing served, the page says what will show.
test("with nothing served the page says so", async ({ page }) => {
  await fakeServer(page, true)
  await page.goto("/settings/usage")

  await expect(page.getByText("Nothing served in this period")).toBeVisible()
  await shot(page, "usage-empty")
})
