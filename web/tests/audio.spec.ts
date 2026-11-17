import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Audio page of Settings → Gateway (issue #694), built, against a fake client API, on a phone and a desktop.

type Provider = "soniox" | "elevenlabs"
type Tally = { answered: number; failed: number; seconds: number; characters: number }
type Key = {
  name: string
  state: "ready" | "resting"
  rest?: string
  resting_until?: string
  last_used_at?: string
}
type Link = { provider: Provider; on: boolean; state: string; keys: Key[] }
type KindLink = {
  provider: Provider
  on: boolean
  model: string
  usage: { today: Tally; month: Tally }
}

const models: Record<string, string> = {
  "soniox-stt": "stt-async-v5",
  "elevenlabs-stt": "scribe_v2",
  "soniox-tts": "tts-rt-v2",
  "elevenlabs-tts": "eleven_multilingual_v2",
}

const zero = { answered: 0, failed: 0, seconds: 0, characters: 0 }

// Soniox with a key used a while ago that heard an hour of calls this month; ElevenLabs with no key yet.
function start() {
  return {
    providers: [
      {
        provider: "soniox",
        on: true,
        state: "ready",
        keys: [
          {
            name: "speech-soniox",
            state: "ready",
            last_used_at: new Date(Date.now() - 30 * 60_000).toISOString(),
          },
        ],
      },
      { provider: "elevenlabs", on: true, state: "not_set_up", keys: [] },
    ] as Link[],
    stt: ["soniox", "elevenlabs"] as Provider[],
    tts: ["soniox", "elevenlabs"] as Provider[],
  }
}

async function fakeServer(page: Page) {
  const world = start()
  const calls: string[] = []
  const kind = (k: "stt" | "tts"): KindLink[] =>
    world[k].map((p) => ({
      provider: p,
      on: world.providers.find((l) => l.provider === p)!.on,
      model: models[`${p}-${k}`],
      usage:
        p === "soniox" && k === "stt"
          ? {
              today: { ...zero, answered: 2, seconds: 240 },
              month: { ...zero, answered: 14, failed: 1, seconds: 3600 },
            }
          : { today: zero, month: zero },
    }))
  const answer = () => ({ providers: world.providers, stt: kind("stt"), tts: kind("tts") })

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (request.method() === "PUT" && path.startsWith("/gateway/audio/providers/")) {
      const provider = path.split("/").pop() as Provider
      const change = request.postDataJSON() as { on: boolean }
      calls.push(`${call} ${JSON.stringify(change)}`)
      world.providers = world.providers.map((l) =>
        l.provider === provider
          ? {
              ...l,
              on: change.on,
              state: change.on ? (l.keys.length ? "ready" : "not_set_up") : "off",
            }
          : l,
      )

      return json(answer())
    }

    if (request.method() === "POST" && path.endsWith("/keys")) {
      const provider = path.split("/")[4] as Provider
      calls.push(`${call} ${(request.postDataJSON() as { key: string }).key ? "a key" : "nothing"}`)
      const name = `speech-${provider}`
      world.providers = world.providers.map((l) =>
        l.provider === provider
          ? { ...l, state: l.on ? "ready" : "off", keys: [...l.keys, { name, state: "ready" }] }
          : l,
      )

      return json({ name, audio: answer() }, 201)
    }

    if (request.method() === "PUT" && path.startsWith("/gateway/audio/order/")) {
      const k = path.split("/").pop() as "stt" | "tts"
      const { providers } = request.postDataJSON() as { providers: Provider[] }
      calls.push(`${call} ${providers.join(",")}`)
      world[k] = providers

      return json(answer())
    }

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /gateway/audio":
        return json(answer())
      case "GET /tasks":
        return json({ data: [], runs: [], after: 0 })
      case "GET /events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return { calls, world }
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

const row = (page: Page, label: string) => page.getByLabel(label, { exact: true })

// Scenario: the owner opens Audio from Settings. Soniox is ready with its one key; ElevenLabs has no key. Speech to
// text lists Soniox first with its model and the hour it heard this month; text to speech lists the same order with
// its own models. The page fits a phone, and reads on the light theme as on the dark.
test("the owner sees the audio providers and each kind's order", async ({ page }) => {
  await fakeServer(page)
  await page.goto("/settings")
  await page.getByRole("link", { name: "Audio" }).first().click()

  await expect(page.getByRole("heading", { name: "Audio", level: 1 })).toBeVisible()

  const soniox = row(page, "Soniox")
  await expect(soniox).toContainText("Ready")
  await expect(soniox).toContainText("1 key")
  await expect(page.getByLabel("Soniox's keys")).toContainText("speech-soniox")

  await expect(row(page, "ElevenLabs")).toContainText("No key")

  const sonioxSTT = row(page, "Soniox, speech to text")
  await expect(sonioxSTT).toContainText("#1")
  await expect(sonioxSTT).toContainText("stt-async-v5")
  await expect(sonioxSTT).toContainText("Today 2 · this month 14 (+1 failed) · 1 h heard")
  await expect(row(page, "ElevenLabs, text to speech")).toContainText("eleven_multilingual_v2")
  await expect(row(page, "ElevenLabs, text to speech")).toContainText("0 characters spoken")

  expect(await fits(page)).toBe(true)
  await shot(page, "audio-providers")

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(page.locator("html")).not.toHaveClass(/dark/)
  await expect(row(page, "Soniox")).toBeVisible()
  await shot(page, "audio-providers-light")
})

// Scenario: the owner adds an ElevenLabs key in the dialog: it is sent once, never shown, and ElevenLabs is ready
// with its key listed. Then the owner switches Soniox off, for both kinds.
test("the owner adds a key and switches a provider off", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/audio")

  await row(page, "ElevenLabs").getByRole("button", { name: "Add a ElevenLabs key" }).click()
  const dialog = page.getByRole("dialog", { name: "ElevenLabs API key" })
  await expect(dialog).toContainText("elevenlabs.io")
  await dialog.getByLabel("API key").fill("el-secret")
  await shot(page, "audio-add-key")
  await dialog.getByRole("button", { name: "Save key" }).click()

  await expect(dialog).toBeHidden()
  await expect(row(page, "ElevenLabs")).toContainText("Ready")
  await expect(page.getByLabel("ElevenLabs's keys")).toContainText("speech-elevenlabs")
  await expect(page.getByText("el-secret")).toHaveCount(0)
  expect(calls).toContain("POST /gateway/audio/providers/elevenlabs/keys a key")

  await row(page, "Soniox").getByRole("switch", { name: "In use" }).click()
  await expect(row(page, "Soniox")).toContainText("Off")
  expect(calls).toContain(`PUT /gateway/audio/providers/soniox {"on":false}`)
  await shot(page, "audio-soniox-off")
})

// Scenario: on a desktop the owner drags ElevenLabs first for text to speech only: that order is sent, and speech to
// text keeps Soniox first. A phone has no drag; the order is set there with `hypomnema audio order`.
test("the owner orders text to speech apart from speech to text", async ({ page, isMobile }) => {
  test.skip(isMobile, "a touch screen does not drag")

  const { calls } = await fakeServer(page)
  await page.goto("/settings/audio")

  await page
    .getByLabel("Drag ElevenLabs to change the order of text to speech")
    .dragTo(row(page, "Soniox, text to speech"))

  await expect(row(page, "ElevenLabs, text to speech")).toContainText("#1")
  await expect(row(page, "Soniox, speech to text")).toContainText("#1")
  await expect(page.getByText("ElevenLabs is asked first for text to speech")).toBeVisible()
  expect(calls).toContain("PUT /gateway/audio/order/tts elevenlabs,soniox")
  await shot(page, "audio-reordered")
})
