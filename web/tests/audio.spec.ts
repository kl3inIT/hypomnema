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
  usage: { today: Tally; month: Tally }
}

const models: Record<string, string> = {
  "soniox-stt": "stt-async-v6",
  "elevenlabs-stt": "scribe_v2",
  "soniox-tts": "tts-rt-v2",
  "elevenlabs-tts": "eleven_v4",
}

// ElevenLabs's own lists, read with its key (issue #1283): its speech models with their languages, and its voices.
const elevenLabsModels = [
  {
    provider: "elevenlabs",
    kind: "tts",
    id: "eleven_v4",
    name: "Eleven v4",
    languages: ["en", "vi", "ja"],
  },
  {
    provider: "elevenlabs",
    kind: "tts",
    id: "eleven_v3",
    name: "Eleven v3",
    languages: ["en", "vi"],
  },
  {
    provider: "elevenlabs",
    kind: "tts",
    id: "eleven_multilingual_v2",
    name: "Eleven Multilingual v2",
    languages: ["en", "ja"],
  },
]
// Soniox's catalog and voices (issue #1285, rows 1 and 5): its speech and transcription models, its built-in voices
// and a cloned one.
const sonioxModels = [
  { provider: "soniox", kind: "tts", id: "tts-rt-v2", name: "TTS v2", languages: ["en", "vi"] },
  { provider: "soniox", kind: "tts", id: "tts-rt-v1", name: "TTS v1", languages: ["en", "vi"] },
  {
    provider: "soniox",
    kind: "stt",
    id: "stt-async-v6",
    name: "Async v6",
    languages: ["en", "vi"],
  },
]
const sonioxVoices = [
  { provider: "soniox", id: "Maya", name: "Maya" },
  { provider: "soniox", id: "Daniel", name: "Daniel" },
  { provider: "soniox", id: "21b9c8e2-1c3a", name: "narrator" },
]
const elevenLabsVoices = [
  { provider: "elevenlabs", id: "v-son", name: "Sơn Trần", language: "vi" },
  { provider: "elevenlabs", id: "v-adam", name: "Adam", language: "en" },
]

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

async function fakeServer(page: Page, more: { elevenLabsKey?: boolean } = {}) {
  const world = { ...start(), ttsModel: "", chosen: {} as Record<string, string> }
  if (more.elevenLabsKey) {
    world.providers = world.providers.map((l) =>
      l.provider === "elevenlabs"
        ? { ...l, state: "ready", keys: [{ name: "speech-elevenlabs", state: "ready" }] }
        : l,
    )
  }
  const keyed = (p: Provider) =>
    world.providers.some((l) => l.provider === p && l.on && l.keys.length > 0)
  const calls: string[] = []
  const kind = (k: "stt" | "tts"): KindLink[] =>
    world[k].map((p) => ({
      provider: p,
      on: world.providers.find((l) => l.provider === p)!.on,
      model:
        world.chosen[`${p}-${k}`] ||
        (p === "elevenlabs" && k === "tts" && world.ttsModel) ||
        models[`${p}-${k}`],
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

    if (request.method() === "PUT" && path.endsWith("/model")) {
      const provider = path.split("/")[4] as Provider
      const { model, kind } = request.postDataJSON() as { model: string; kind?: "stt" | "tts" }
      calls.push(`${call} ${JSON.stringify({ model, kind })}`)
      if (provider === "elevenlabs") world.ttsModel = model
      else world.chosen[`${provider}-${kind ?? "tts"}`] = model

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
      case "GET /gateway/audio/models":
        return json({
          models: [
            ...(keyed("soniox") ? sonioxModels : []),
            ...(keyed("elevenlabs") ? elevenLabsModels : []),
          ],
        })
      case "GET /gateway/audio/voices":
        return json({
          voices: [
            ...(keyed("soniox") ? sonioxVoices : []),
            ...(keyed("elevenlabs") ? elevenLabsVoices : []),
          ],
        })
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
  await expect(sonioxSTT).toContainText("Async v6")
  await expect(sonioxSTT).toContainText("Today 2 · this month 14 (+1 failed) · 1 h heard")
  await expect(row(page, "ElevenLabs, text to speech")).toContainText("eleven_v4")
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

// Scenario: the owner picks the voice Soniox speaks videos of takes with (issue #818) by name, from Soniox's
// built-in voices and a cloned one (issue #1285, row 5): its id is sent for Soniox only, and ElevenLabs, with no
// key, keeps a typed voice and its own default. The field fits a phone.
test("the owner picks Soniox's voice for videos by name", async ({ page }) => {
  const { calls, world } = await fakeServer(page)
  await page.goto("/settings/audio")

  const soniox = row(page, "Soniox, text to speech")
  const voice = soniox.getByRole("combobox", { name: "Voice for videos" })
  await expect(voice).toContainText("Its own default")
  await voice.click()
  await expect(page.getByRole("option", { name: "Maya" })).toBeVisible()
  await shot(page, "audio-soniox-voices")
  await page.getByRole("option", { name: "narrator" }).click()

  await expect(page.getByText("Soniox speaks videos with narrator")).toBeVisible()
  expect(calls).toContain(`PUT /gateway/audio/providers/soniox/voice {"voice":"21b9c8e2-1c3a"}`)
  await expect(voice).toContainText("narrator")
  expect(world.providers.find((l) => l.provider === "elevenlabs")?.voice).toBeUndefined()
  await expect(
    row(page, "ElevenLabs, text to speech").getByRole("textbox", { name: "Voice for videos" }),
  ).toHaveValue("")
  await expect(
    row(page, "Soniox, speech to text").getByRole("combobox", { name: "Voice for videos" }),
  ).toHaveCount(0)

  expect(await fits(page)).toBe(true)
  await soniox.scrollIntoViewIfNeeded()
  await shot(page, "audio-voice")

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(page.locator("html")).not.toHaveClass(/dark/)
  await expect(row(page, "Soniox, text to speech")).toContainText("narrator")
  await row(page, "Soniox, text to speech").scrollIntoViewIfNeeded()
  await shot(page, "audio-voice-light")
})

// Scenario: Soniox's speech-to-text and text-to-speech rows each have a model picker on Soniox's own models of
// that kind (issue #1285, row 1), the newest by default. The owner picks TTS v1 for speech, sent with its kind, and
// the transcription model keeps its own.
test("the owner picks Soniox's models for each kind", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/audio")

  const stt = row(page, "Soniox, speech to text").getByRole("combobox", { name: "Model" })
  const tts = row(page, "Soniox, text to speech").getByRole("combobox", { name: "Model" })
  await expect(stt).toContainText("Async v6")
  await expect(tts).toContainText("TTS v2")

  await tts.scrollIntoViewIfNeeded()
  await tts.click()
  await expect(page.getByRole("option", { name: /Async v6/ })).toHaveCount(0)
  await shot(page, "audio-soniox-models")
  await page.getByRole("option", { name: /TTS v1/ }).click()

  await expect(page.getByText("Soniox speaks with tts-rt-v1")).toBeVisible()
  expect(calls).toContain(
    `PUT /gateway/audio/providers/soniox/model {"model":"tts-rt-v1","kind":"tts"}`,
  )
  await expect(tts).toContainText("TTS v1")
  await expect(stt).toContainText("Async v6")

  expect(await fits(page)).toBe(true)

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(page.locator("html")).not.toHaveClass(/dark/)
  await stt.scrollIntoViewIfNeeded()
  await stt.click()
  await expect(page.getByRole("option", { name: /Async v6/ })).toBeVisible()
  await shot(page, "audio-soniox-models-light")
})

// Scenario: with an ElevenLabs key, its text-to-speech row has a model picker on its own speech models, eleven_v4
// by default, and its voices by name (issue #1283). The owner picks Eleven v3, which is saved at once, then the
// voice Sơn Trần, which is saved by its id. The page fits a phone, and reads on the light theme as on the dark.
test("the owner picks ElevenLabs's model and a voice by name", async ({ page }) => {
  const { calls } = await fakeServer(page, { elevenLabsKey: true })
  await page.goto("/settings/audio")

  const eleven = row(page, "ElevenLabs, text to speech")
  const model = eleven.getByRole("combobox", { name: "Model" })
  await expect(model).toContainText("Eleven v4")
  await expect(eleven.getByRole("combobox", { name: "Voice for videos" })).toContainText(
    "Its own default",
  )
  await expect(
    row(page, "Soniox, text to speech").getByRole("combobox", { name: "Voice for videos" }),
  ).toBeVisible()
  await eleven.scrollIntoViewIfNeeded()
  await shot(page, "audio-elevenlabs")

  await model.click()
  await expect(page.getByRole("option", { name: /Eleven Multilingual v2/ })).toContainText(
    "2 languages",
  )
  await shot(page, "audio-elevenlabs-models")
  await page.getByRole("option", { name: /Eleven v3/ }).click()

  await expect(page.getByText("ElevenLabs speaks with eleven_v3")).toBeVisible()
  expect(calls).toContain(
    `PUT /gateway/audio/providers/elevenlabs/model {"model":"eleven_v3","kind":"tts"}`,
  )
  await expect(eleven).toContainText("eleven_v3")
  await expect(model).toContainText("Eleven v3")

  await eleven.getByRole("combobox", { name: "Voice for videos" }).click()
  await expect(page.getByRole("option", { name: /Adam/ })).toBeVisible()
  await shot(page, "audio-elevenlabs-voices")
  await page.getByRole("option", { name: /Sơn Trần/ }).click()

  await expect(page.getByText("ElevenLabs speaks videos with Sơn Trần")).toBeVisible()
  expect(calls).toContain(`PUT /gateway/audio/providers/elevenlabs/voice {"voice":"v-son"}`)
  await expect(eleven.getByRole("combobox", { name: "Voice for videos" })).toContainText("Sơn Trần")

  expect(await fits(page)).toBe(true)
  await shot(page, "audio-elevenlabs-chosen")

  await page.evaluate(`localStorage.setItem("theme", "light")`)
  await page.reload()
  await expect(page.locator("html")).not.toHaveClass(/dark/)
  await expect(row(page, "ElevenLabs, text to speech")).toContainText("eleven_v3")
  await row(page, "ElevenLabs, text to speech").scrollIntoViewIfNeeded()
  await shot(page, "audio-elevenlabs-light")
})
