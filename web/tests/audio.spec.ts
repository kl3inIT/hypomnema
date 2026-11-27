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
type Link = { provider: Provider; on: boolean; state: string; keys: Key[]; voice?: string }
type KindLink = {
  provider: Provider
  on: boolean
  model: string
  default_model: string
  usage: { today: Tally; month: Tally }
}

const models: Record<string, string> = {
  "soniox-stt": "stt-async-v6",
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
    // chosen are the models the owner chose, by provider and kind; none: the newest.
    chosen: {} as Record<string, string>,
  }
}

// The catalogs the page reads when a list is opened (issue #1285, rows 1 and 5).
const catalogs: Record<string, { id: string; name: string }[]> = {
  "soniox-tts": [
    { id: "tts-rt-v2", name: "TTS v2" },
    { id: "tts-rt-v1", name: "TTS v1" },
  ],
  "soniox-stt": [{ id: "stt-async-v6", name: "Async v6" }],
}

const voices = [
  { id: "Maya", name: "Maya", provider: "soniox" },
  { id: "Daniel", name: "Daniel", provider: "soniox" },
  { id: "21b9c8e2-1c3a", name: "narrator", provider: "soniox" },
  { id: "21m00Tcm4TlvDq8ikWAM", name: "Rachel", provider: "elevenlabs" },
]

async function fakeServer(page: Page) {
  const world = start()
  const calls: string[] = []
  const kind = (k: "stt" | "tts"): KindLink[] =>
    world[k].map((p) => ({
      provider: p,
      on: world.providers.find((l) => l.provider === p)!.on,
      model: world.chosen[`${p}-${k}`] ?? models[`${p}-${k}`],
      default_model: models[`${p}-${k}`],
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

    if (path.includes("/models/")) {
      const [, , , , provider, , k] = path.split("/")

      if (request.method() === "GET") return json({ models: catalogs[`${provider}-${k}`] ?? [] })

      const { model } = request.postDataJSON() as { model: string }
      calls.push(`${call} ${JSON.stringify({ model })}`)
      if (model) world.chosen[`${provider}-${k}`] = model
      else delete world.chosen[`${provider}-${k}`]

      return json(answer())
    }

    if (request.method() === "PUT" && path.endsWith("/voice")) {
      const provider = path.split("/")[4] as Provider
      const { voice } = request.postDataJSON() as { voice: string }
      calls.push(`${call} ${JSON.stringify({ voice })}`)
      world.providers = world.providers.map((l) =>
        l.provider === provider ? { ...l, voice: voice || undefined } : l,
      )

      return json(answer())
    }

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
      case "GET /gateway/audio/voices":
        return json({ voices })
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
  await expect(sonioxSTT).toContainText("Newest (stt-async-v6)")
  await expect(sonioxSTT).toContainText("Today 2 · this month 14 (+1 failed) · 1 h heard")
  await expect(row(page, "ElevenLabs, text to speech")).toContainText(
    "Newest (eleven_multilingual_v2)",
  )
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

// Scenario: the owner sets the voice Soniox speaks videos of takes with (issue #818), chosen by name from Soniox's
// voices (issue #1285, row 5): the voice's id is sent for Soniox only, and ElevenLabs keeps its own default. Speech
// to text has no voice. The field fits a phone, on the light theme as on the dark.
test("the owner picks a provider's voice for videos by name", async ({ page }) => {
  const { calls, world } = await fakeServer(page)
  await page.goto("/settings/audio")

  const soniox = row(page, "Soniox, text to speech")
  await soniox.getByRole("combobox", { name: "Soniox's voice for videos" }).click()
  await expect(page.getByRole("option", { name: "Rachel" })).toHaveCount(0)
  await shot(page, "audio-voice-picker")
  await page.getByRole("option", { name: "narrator" }).click()

  await expect(page.getByText("Soniox speaks videos with narrator")).toBeVisible()
  expect(calls).toContain(`PUT /gateway/audio/providers/soniox/voice {"voice":"21b9c8e2-1c3a"}`)
  expect(world.providers.find((l) => l.provider === "elevenlabs")?.voice).toBeUndefined()
  await expect(
    row(page, "ElevenLabs, text to speech").getByRole("combobox", {
      name: "ElevenLabs's voice for videos",
    }),
  ).toContainText("Its own default")
  await expect(row(page, "Soniox, speech to text").getByRole("combobox")).toHaveCount(1)

  expect(await fits(page)).toBe(true)
  await soniox.scrollIntoViewIfNeeded()
  await shot(page, "audio-voice")

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(soniox).toContainText("narrator")
  await soniox.scrollIntoViewIfNeeded()
  await shot(page, "audio-voice-light")
})

// Scenario: the owner chooses the model Soniox speaks with from its catalog (issue #1285, row 1), then puts it back
// to the newest, which is sent as no choice.
test("the owner chooses a provider's model from its catalog", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/audio")

  const soniox = row(page, "Soniox, text to speech")
  const model = soniox.getByRole("combobox", { name: "Soniox's model for text to speech" })
  await expect(model).toContainText("Newest (tts-rt-v2)")
  await model.click()
  await expect(page.getByRole("option", { name: "TTS v1 · tts-rt-v1" })).toBeVisible()
  await shot(page, "audio-model-picker")
  await page.getByRole("option", { name: "TTS v1 · tts-rt-v1" }).click()

  await expect(page.getByText("Soniox is asked with tts-rt-v1 for text to speech")).toBeVisible()
  expect(calls).toContain(`PUT /gateway/audio/providers/soniox/models/tts {"model":"tts-rt-v1"}`)
  await expect(model).toContainText("TTS v1 · tts-rt-v1")

  await model.click()
  await page.getByRole("option", { name: "Newest (tts-rt-v2)" }).click()
  await expect(model).toContainText("Newest (tts-rt-v2)")
  expect(calls).toContain(`PUT /gateway/audio/providers/soniox/models/tts {"model":""}`)

  expect(await fits(page)).toBe(true)
  await soniox.scrollIntoViewIfNeeded()
  await shot(page, "audio-model")

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(model).toContainText("Newest (tts-rt-v2)")
  await model.click()
  await expect(page.getByRole("option", { name: "TTS v1 · tts-rt-v1" })).toBeVisible()
  await shot(page, "audio-model-picker-light")
})
