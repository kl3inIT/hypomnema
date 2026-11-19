import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Meetings pages (issues #721, #747), built, against a fake client API, on a phone and a desktop: the list
// grouped by day, the upload dialog, a meeting's page with its player, notes and transcript, the speaker picker, a
// word fixed in the transcript, the Actions tab, Settings' Meetings group (issue #762: the dictionary and the notes
// model) and the way back from Settings. The notes have no format to pick: they are sectioned by topic (issue #771).

// Recording live (issue #820) uses the browser's microphone, which Chromium fakes with a tone, granted without asking;
// a page's launch options are the file's, as each forces its own browser.
test.use({
  permissions: ["microphone"],
  launchOptions: {
    args: [
      "--use-fake-ui-for-media-stream",
      "--use-fake-device-for-media-stream",
      "--autoplay-policy=no-user-gesture-required",
    ],
  },
})

function at(daysAgo: number, hour: number, minute = 0) {
  const d = new Date()
  d.setDate(d.getDate() - daysAgo)
  d.setHours(hour, minute, 0, 0)

  return d.toISOString()
}

type Speaker = {
  track: string
  owner: boolean
  label: string
  name: string
  named: boolean
  talk_ms: number
  share: number
}

function meeting(id: string, title: string, over: object = {}) {
  return {
    id,
    title,
    titled: true,
    people: [],
    file_name: `${id}.m4a`,
    media_type: "audio/wav",
    size: 1000,
    duration_ms: 42 * 60_000,
    state: "transcribed",
    source: "upload",
    kind: undefined as string | undefined,
    provider: "soniox",
    model: "stt-async-v5",
    created_at: at(0, 9, 30),
    notes: {
      state: "ready",
      summary: "",
      sections: [],
      model: "claude-sonnet-4-6",
    },
    speakers: [] as Speaker[],
    actions: 0,
    open_actions: 0,
    ...over,
  }
}

const lines = [
  {
    n: 1,
    track: "file",
    start_ms: 12_000,
    end_ms: 19_000,
    speaker: "1",
    heard: "1",
    text: "Okay, let's start with the launch. Kai, where are we on the landing page?",
  },
  {
    n: 2,
    track: "file",
    start_ms: 20_000,
    end_ms: 64_000,
    speaker: "2",
    heard: "2",
    text: "The page is done on staging. Pricing is the only part missing; I will send the numbers on Wednesday.",
  },
  {
    n: 3,
    track: "file",
    start_ms: 65_000,
    end_ms: 80_000,
    speaker: "3",
    heard: "3",
    text: "Laura here. Let's ship it on Friday and add pricing next week.",
  },
  {
    n: 4,
    track: "file",
    start_ms: 724_000,
    end_ms: 740_000,
    speaker: "2",
    heard: "2",
    text: "Agreed, Friday it is.",
  },
  {
    n: 5,
    track: "file",
    start_ms: 741_000,
    end_ms: 746_000,
    speaker: "2",
    heard: "2",
    text: "I will check the old domain's redirects too.",
  },
]

function world() {
  const speakers: Speaker[] = [
    {
      track: "file",
      owner: false,
      label: "1",
      name: "You",
      named: true,
      talk_ms: 7_000,
      share: 0.09,
    },
    {
      track: "file",
      owner: false,
      label: "2",
      name: "Kai",
      named: true,
      talk_ms: 60_000,
      share: 0.73,
    },
    {
      track: "file",
      owner: false,
      label: "3",
      name: "Speaker 3",
      named: false,
      talk_ms: 15_000,
      share: 0.18,
    },
  ]
  const weekly = meeting("mtg_weekly", "Weekly sync with Kai", {
    people: ["Laura", "Kai"],
    speakers,
    actions: 3,
    open_actions: 2,
    notes: {
      state: "ready",
      model: "claude-sonnet-4-6",
      summary:
        "Kai and Laura reviewed the launch. The landing page ships Friday without pricing; pricing waits for Kai's numbers.",
      sections: [
        { title: "Progress summary", points: ["The landing page is done on staging."] },
        { title: "Milestones & status", points: [] },
        { title: "Top risks & mitigations", points: ["Pricing may slip past next week."] },
      ],
    },
  })
  const items = [
    {
      id: "mti_d1",
      kind: "decision",
      text: "The landing page goes live on Friday, without pricing.",
      done: false,
      quote: "Let's ship it on Friday",
      line: 3,
      start_ms: 65_000,
    },
    {
      id: "mti_a1",
      kind: "action",
      text: "Send the pricing numbers",
      owner: "Kai",
      owner_speaker: "2",
      due: "Wednesday",
      done: false,
      line: 2,
      start_ms: 20_000,
    },
    {
      id: "mti_a2",
      kind: "action",
      text: "Check the old domain's redirects",
      done: false,
      start_ms: 724_000,
      line: 4,
    },
    { id: "mti_a3", kind: "action", text: "Draft the launch email", owner: "Laura", done: true },
  ]

  return {
    meetings: [
      weekly,
      meeting("mtg_vmap", "VMAP review with Phụng", {
        created_at: at(0, 8, 0),
        duration_ms: 58 * 60_000,
        notes: { state: "writing", summary: "", sections: [] },
        speakers: [
          {
            track: "file",
            owner: false,
            label: "1",
            name: "Speaker 1",
            named: false,
            talk_ms: 1,
            share: 0.5,
          },
          {
            track: "file",
            owner: false,
            label: "2",
            name: "Speaker 2",
            named: false,
            talk_ms: 1,
            share: 0.5,
          },
        ],
      }),
      meeting("mtg_minh", "1:1 with Minh", {
        created_at: at(1, 16, 0),
        duration_ms: 28 * 60_000,
        actions: 2,
        open_actions: 0,
        speakers: [
          {
            track: "file",
            owner: false,
            label: "1",
            name: "You",
            named: true,
            talk_ms: 1,
            share: 0.4,
          },
          {
            track: "file",
            owner: false,
            label: "2",
            name: "Minh",
            named: true,
            talk_ms: 1,
            share: 0.6,
          },
        ],
      }),
      meeting("mtg_lost", "Client call, Nov 3", {
        created_at: at(14, 10, 0),
        state: "failed",
        duration_ms: 0,
        provider: undefined,
        failure:
          "No speech-to-text provider is set up. Add a Soniox or ElevenLabs key on Settings → Gateway → Audio, then upload the recording again.",
        notes: { state: "waiting", summary: "", sections: [] },
      }),
    ],
    items,
    notesModel: "",
    words: ["MapOps", "ĐKSD"],
  }
}

// silence is a second of silent WAV, so the player has a recording to load.
function silence() {
  const rate = 8000
  const data = Buffer.alloc(rate * 2)
  const head = Buffer.alloc(44)
  head.write("RIFF", 0)
  head.writeUInt32LE(36 + data.length, 4)
  head.write("WAVEfmt ", 8)
  head.writeUInt32LE(16, 16)
  head.writeUInt16LE(1, 20)
  head.writeUInt16LE(1, 22)
  head.writeUInt32LE(rate, 24)
  head.writeUInt32LE(rate * 2, 28)
  head.writeUInt16LE(2, 32)
  head.writeUInt16LE(16, 34)
  head.write("data", 36)
  head.writeUInt32LE(data.length, 40)

  return Buffer.concat([head, data])
}

async function fakeServer(page: Page) {
  const w = world()
  const calls: string[] = []
  // The lines of the meetings recorded live (issue #820), as the fake track sockets keep them.
  const live = new Map<string, (typeof lines)[number][]>()
  const words = () => ({
    data: [
      ...w.words.map((word) => ({ word, from: "owner", sent: true })),
      { word: "Kai", from: "speaker", sent: true },
      { word: "coordinator", from: "bot", sent: true },
      { word: "hypomnema", from: "repository", sent: true },
    ],
    max_own: 100,
    max_sent: 100,
  })
  const detail = (id: string) => {
    const m = w.meetings.find((x) => x.id === id)!

    return {
      meeting: m,
      lines: id === "mtg_weekly" ? lines : (live.get(id) ?? []),
      items: id === "mtg_weekly" ? w.items : [],
      suggestions:
        id === "mtg_weekly" && m.speakers.some((s) => s.label === "3" && !s.named)
          ? [{ track: "file", owner: false, label: "3", name: "Laura", line: 3 }]
          : [],
    }
  }
  const actions = () =>
    w.items
      .filter((i) => i.kind === "action")
      .map((i) => ({
        id: i.id,
        meeting_id: "mtg_weekly",
        meeting_title: "Weekly sync with Kai",
        meeting_at: w.meetings[0].created_at,
        text: i.text,
        who: i.owner,
        due: i.due,
        done: i.done,
        start_ms: i.start_ms,
      }))

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace("/api/v1", "")
    const call = `${request.method()} ${path}`
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (call === "GET /meetings") {
      calls.push(`${call}?${url.searchParams}`)
      const q = (url.searchParams.get("q") ?? "").toLowerCase()

      return json({
        data: w.meetings.filter(
          (m) =>
            !q ||
            m.title.toLowerCase().includes(q) ||
            (m.id === "mtg_weekly" && lines.some((l) => l.text.toLowerCase().includes(q))),
        ),
      })
    }

    if (call === "POST /meetings") {
      calls.push(`${call}?${url.searchParams}`)
      const made = meeting("mtg_new", url.searchParams.get("title") || "standup-1711", {
        state: "transcribing",
        provider: undefined,
        created_at: new Date().toISOString(),
        duration_ms: 0,
        notes: {
          state: "waiting",
          summary: "",
          sections: [],
        },
        people: url.searchParams.getAll("people"),
      })
      w.meetings.unshift(made)

      return json(made, 201)
    }

    if (call === "POST /meetings/live") {
      const body = request.postDataJSON() as { kind: string; language?: string; people?: string[] }
      calls.push(`${call} ${body.kind} ${body.language ?? "auto"} ${(body.people ?? []).join(",")}`)
      const made = meeting(`mtg_live${w.meetings.length}`, "Meeting", {
        titled: false,
        state: "recording",
        source: "live",
        kind: body.kind,
        provider: undefined,
        file_name: "recording.wav",
        size: 0,
        created_at: new Date().toISOString(),
        duration_ms: 0,
        notes: { state: "waiting", summary: "", sections: [] },
        people: body.people ?? [],
      })
      w.meetings.unshift(made)

      return json(made, 201)
    }

    const tickets = path.match(/^\/meetings\/([^/]+)\/tickets$/)
    if (request.method() === "POST" && tickets) {
      const { track } = request.postDataJSON() as { track: string }
      calls.push(`${call} ${track}`)

      return json(
        { ticket: `tk-${track}-${calls.length}`, expires_at: new Date().toISOString() },
        201,
      )
    }

    const end = path.match(/^\/meetings\/([^/]+)\/end$/)
    if (request.method() === "POST" && end) {
      calls.push(call)
      const m = w.meetings.find((x) => x.id === end[1])!
      Object.assign(m, {
        state: "transcribed",
        provider: "soniox",
        duration_ms: Math.max(m.duration_ms, 4_000),
        size: 192_044,
        notes: { state: "writing", summary: "", sections: [] },
      })

      return json(m)
    }

    const recording = path.match(/^\/meetings\/([^/]+)\/recording$/)
    if (recording) {
      return route.fulfill({ status: 200, contentType: "audio/wav", body: silence() })
    }

    const speaker = path.match(/^\/meetings\/([^/]+)\/speakers\/([^/]+)$/)
    if (request.method() === "PUT" && speaker) {
      const { name } = request.postDataJSON() as { name: string }
      calls.push(`${call} ${name}`)
      const m = w.meetings.find((x) => x.id === speaker[1])!
      const other = m.speakers.find(
        (s) => s.name.toLowerCase() === name.toLowerCase() && s.label !== speaker[2],
      )
      if (other) {
        m.speakers = m.speakers.filter((s) => s.label !== speaker[2])
        other.share = 0.91
      } else {
        m.speakers = m.speakers.map((s) =>
          s.label === speaker[2] ? { ...s, name, named: true } : s,
        )
      }

      return json(detail(m.id))
    }

    const line = path.match(/^\/meetings\/([^/]+)\/lines\/(\d+)\/speaker$/)
    if (request.method() === "PUT" && line) {
      calls.push(`${call} ${(request.postDataJSON() as { name: string }).name}`)

      return json(detail(line[1]))
    }

    const item = path.match(/^\/meetings\/([^/]+)\/items\/([^/]+)$/)
    if (request.method() === "PATCH" && item) {
      const { done } = request.postDataJSON() as { done: boolean }
      calls.push(`${call} ${done}`)
      const it = w.items.find((i) => i.id === item[2])!
      it.done = done

      return json(it)
    }

    const notes = path.match(/^\/meetings\/([^/]+)\/notes$/)
    if (request.method() === "POST" && notes) {
      calls.push(call)

      return json(
        w.meetings.find((x) => x.id === notes[1]),
        202,
      )
    }

    const fix = path.match(/^\/meetings\/([^/]+)\/fix$/)
    if (request.method() === "POST" && fix) {
      const body = request.postDataJSON() as { from: string; to: string; line?: number }
      calls.push(`${call} ${body.from}→${body.to} ${body.line ?? "meeting"}`)
      let replaced = 0
      for (const l of lines) {
        if (body.line && l.n !== body.line) continue
        const next = l.text.split(body.from).join(body.to)
        if (next !== l.text) replaced++
        l.text = next
      }
      if (!w.words.includes(body.to)) w.words.push(body.to)

      return json({ detail: detail(fix[1]), replaced, learned: true })
    }

    const one = path.match(/^\/meetings\/([^/]+)$/)
    if (request.method() === "GET" && one) return json(detail(one[1]))

    switch (call) {
      case "GET /me":
        return json({ id: "tok_1", name: "web", kind: "owner" })
      case "GET /meeting-actions": {
        calls.push(`${call}?${url.searchParams}`)
        const who = url.searchParams.get("who")
        const state = url.searchParams.get("state")
        const all = actions()

        return json({
          data: all.filter(
            (a) =>
              (!who || a.who === who) &&
              (state !== "open" || !a.done) &&
              (state !== "done" || a.done),
          ),
          people: ["Kai", "Laura"],
        })
      }
      case "GET /meeting-settings":
        return json({ notes_model: w.notesModel })
      case "PUT /meeting-settings":
        w.notesModel = (request.postDataJSON() as { notes_model: string }).notes_model
        calls.push(`${call} ${w.notesModel}`)

        return json({ notes_model: w.notesModel })
      case "GET /meeting-words":
        return json(words())
      case "PUT /meeting-words":
        w.words = (request.postDataJSON() as { words: string[] }).words
        calls.push(`${call} ${w.words.join(", ")}`)

        return json(words())
      case "GET /gateway/models":
        return json({
          data: [
            {
              name: "claude-sonnet-4-6",
              capability: "chat",
              targets: [{ provider: "claude", model: "claude-sonnet-4-6" }],
            },
            {
              name: "gpt-5.1",
              capability: "chat",
              targets: [{ provider: "codex", model: "gpt-5.1" }],
            },
          ],
        })
      case "GET /providers":
        return json({
          data: [
            { name: "claude", kind: "claude_subscription" },
            { name: "codex", kind: "codex_subscription" },
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

  // A track's socket: ready, then for the first audio a preview and a line kept, at the meeting's time of the
  // socket's offset; the end is answered finished.
  const sockets: string[] = []
  await page.routeWebSocket(/\/api\/v1\/meetings\/[^/]+\/stream\//, (ws) => {
    const url = new URL(ws.url())
    const id = url.pathname.split("/")[4]!
    const track = url.searchParams.get("track") ?? "mic"
    const offset = Number(url.searchParams.get("offset"))
    sockets.push(`${id} ${track} ${offset}`)
    let heard = false
    ws.send(JSON.stringify({ type: "ready" }))
    ws.onMessage((message) => {
      if (typeof message === "string") {
        if ((JSON.parse(message) as { type: string }).type === "end") {
          ws.send(JSON.stringify({ type: "finished" }))
          void ws.close()
        }

        return
      }
      if (heard) return
      heard = true
      const m = w.meetings.find((x) => x.id === id)!
      const kept = live.get(id) ?? []
      const said =
        track === "tab"
          ? "Kai here, the landing page is on staging."
          : "Shall we start the launch review?"
      const start = offset + (track === "tab" ? 1_000 : 3_000)
      const l = {
        n: kept.length + 1,
        track,
        start_ms: start,
        end_ms: start + 1_500,
        speaker: "1",
        heard: "1",
        text: said,
      }
      live.set(
        id,
        [...kept, l].toSorted((a, b) => a.start_ms - b.start_ms),
      )
      if (!m.speakers.some((sp) => sp.track === track)) {
        const mine = m.kind === "online" && track === "mic"
        m.speakers.push({
          track,
          owner: mine,
          label: "1",
          name: mine ? "You" : "Speaker 1",
          named: false,
          talk_ms: 1_500,
          share: 0.5,
        })
      }
      ws.send(JSON.stringify({ type: "preview", track, speaker: "1", text: said.split(" ")[0] }))
      setTimeout(() => ws.send(JSON.stringify({ type: "line", line: l })), 300)
    })
  })

  return { calls, sockets, world: w }
}

const fits = (page: Page) =>
  page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")

// Scenario: the owner opens Meetings from the sidebar. Today's meetings come first, then yesterday's, then earlier;
// each card has its When, Speakers, Actions and From rows and its state; a meeting still being written up says so,
// a failed one says it failed. The table shows the same; a search finds a meeting by what was said in it.
test("the owner sees the meetings by day, as cards or a table", async ({ page, isMobile }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings")

  await expect(page.getByRole("heading", { name: "Meetings", level: 1 })).toBeVisible()
  const today = page.getByRole("region", { name: "Today" })
  await expect(today.getByTestId("meeting")).toHaveCount(2)
  await expect(page.getByRole("region", { name: "Yesterday" }).getByTestId("meeting")).toHaveCount(
    1,
  )
  await expect(page.getByRole("region", { name: "Earlier" }).getByTestId("meeting")).toHaveCount(1)

  const weekly = page.getByRole("link", { name: "Weekly sync with Kai" })
  // Each voice's badge reads its initial once named, else its number (issue #807); past two voices only the badges
  // stay, each naming its voice and share.
  const voices = weekly.getByRole("list", { name: "Speakers" }).getByRole("listitem")
  await expect(voices).toHaveText(["Y", "K", "3"])
  await expect(voices.nth(1)).toHaveAttribute("title", "Kai 73%")
  await expect(
    page
      .getByRole("link", { name: "1:1 with Minh" })
      .getByRole("list", { name: "Speakers" })
      .getByRole("listitem"),
  ).toHaveText(["YYou40%", "MMinh60%"])
  await expect(weekly).toContainText("2 open of 3")
  await expect(weekly).toContainText("Upload · Soniox")
  await expect(weekly).toContainText("Ready")
  await expect(page.getByRole("link", { name: "VMAP review with Phụng" })).toContainText(
    "Writing notes",
  )
  await expect(page.getByRole("link", { name: "1:1 with Minh" })).toContainText("All 2 done")
  await expect(page.getByRole("link", { name: "Client call, Nov 3" })).toContainText("Failed")
  expect(await fits(page)).toBe(true)
  await shot(page, "meetings-list")

  if (!isMobile) {
    await page.getByRole("button", { name: "table" }).click()
    await expect(page.getByRole("table", { name: "Meetings" }).getByTestId("meeting")).toHaveCount(
      4,
    )
    await shot(page, "meetings-table")
    await page.getByRole("button", { name: "grid" }).click()
  }

  await page.getByLabel("Search titles and transcripts").fill("staging")
  await expect(page.getByTestId("meeting")).toHaveCount(1)
  expect(calls).toContain("GET /meetings?q=staging")
})

// Scenario: the owner uploads a recording: the file and its language, then under More details a title and the
// people; no notes format is asked (issue #771). The words to hear right are not asked: they are set once on
// Settings → Dictionary, which the dialog says. They are sent with the recording, and the meeting's page opens on
// its steps: uploaded, transcribing.
test("the owner uploads a recording with its details", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings")

  await page.getByRole("button", { name: "Upload recording" }).first().click()
  const dialog = page.getByRole("dialog", { name: "Upload a recording" })
  await dialog.getByLabel("Recording").setInputFiles({
    name: "standup-1711.m4a",
    mimeType: "audio/mp4",
    buffer: silence(),
  })
  await dialog.getByLabel("Language").click()
  await page.getByRole("option", { name: "Vietnamese" }).click()
  await dialog.getByRole("button", { name: "More details" }).click()
  await dialog.getByLabel("Title").fill("Stand-up 17/11")
  await dialog.getByLabel("People").fill("Laura, Kai")
  await expect(dialog.getByLabel("Words to hear right")).toHaveCount(0)
  await expect(dialog.getByRole("link", { name: "Dictionary" })).toBeVisible()
  await expect(dialog.getByLabel("Notes format")).toHaveCount(0)
  await shot(page, "meetings-upload")

  await dialog.getByRole("button", { name: "Upload" }).click()

  await expect(page.getByRole("heading", { name: "Stand-up 17/11", level: 2 })).toBeVisible()
  const progress = page.getByTestId("meeting-progress")
  await expect(progress).toContainText("Transcribing…")
  await expect(progress).toContainText("Uploaded")
  const sent = calls.find((c) => c.startsWith("POST /meetings"))!
  const query = new URLSearchParams(sent.split("?")[1])
  expect(query.get("file_name")).toBe("standup-1711.m4a")
  expect(query.get("media_type")).toBe("audio/mp4")
  expect(query.get("language")).toBe("vi")
  expect(query.getAll("people")).toEqual(["Laura", "Kai"])
  expect(query.has("terms")).toBe(false)
  expect(query.has("format")).toBe(false)
  expect(await fits(page)).toBe(true)
  await shot(page, "meetings-transcribing")
})

// Scenario: the owner opens a meeting: the player on top with its length, the summary, the decision with the time
// it was said, the actions with who and when, a section per topic. Marking an action done is sent; Write again
// asks for the notes again, with nothing to pick. A failed meeting says why and where to fix it.
test("the owner reads a meeting's notes and marks an action done", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings/mtg_weekly")

  await expect(page.getByRole("heading", { name: "Weekly sync with Kai", level: 2 })).toBeVisible()
  await expect(page.getByTestId("player-time")).toContainText("/ 0:01")
  await expect(page.getByRole("region", { name: "Summary", exact: true })).toContainText(
    "ships Friday",
  )
  await expect(page.getByRole("region", { name: "Decisions" })).toContainText("1:05")
  const actions = page.getByRole("region", { name: "Actions · 2 open" })
  await expect(actions).toContainText("Kai · Send the pricing numbers")
  await expect(actions).toContainText("Due Wednesday")
  await expect(actions).toContainText("No date")
  await expect(page.getByRole("region", { name: "Top risks & mitigations" })).toBeVisible()
  await expect(page.getByRole("region", { name: "Milestones & status" })).toHaveCount(0)
  expect(await fits(page)).toBe(true)
  await shot(page, "meeting-summary")

  await page.getByRole("checkbox", { name: /Send the pricing numbers/ }).click()
  await expect.poll(() => calls).toContain("PATCH /meetings/mtg_weekly/items/mti_a1 true")

  await expect(page.getByLabel("Notes format")).toHaveCount(0)
  await page.getByRole("button", { name: "Write again" }).click()
  await expect.poll(() => calls).toContain("POST /meetings/mtg_weekly/notes")

  await page.goto("/meetings/mtg_lost")
  await expect(page.getByTestId("meeting-progress")).toContainText("Settings → Gateway → Audio")
  await shot(page, "meeting-failed")
})

// Scenario: on the transcript, each voice shows its share of the talk and the name Speaker 3 gave itself. The owner
// opens line 3's speaker: Laura is suggested (said it, and in People), Kai is in the meeting. For the whole meeting,
// Speaker 3 is named Laura; then line 5, the second of Kai's turn, alone is given a new name.
test("the owner names a speaker for the whole meeting or one line", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings/mtg_weekly")
  await page.getByRole("tab", { name: "Transcript" }).click()

  const speakers = page.getByRole("list", { name: "Speakers" })
  await expect(speakers).toContainText("Kai73%")
  await expect(speakers).toContainText("Laura?")
  // Kai's lines 4 and 5 follow one another: one turn under one name.
  await expect(page.getByTestId("transcript-line")).toHaveCount(5)
  await expect(page.getByTestId("transcript-turn")).toHaveCount(4)
  await expect(page.getByTestId("transcript-turn").nth(3)).toContainText("Kai12:04")
  expect(await fits(page)).toBe(true)
  await shot(page, "meeting-transcript")

  await page.getByRole("button", { name: "Speaker 3 said line 3; change" }).click()
  const picker = page.getByRole("dialog", { name: "Who said line 3" })
  await expect(picker.getByRole("option", { name: /Laura/ })).toContainText(
    "said it at line 3 · in People",
  )
  await expect(picker.getByRole("option", { name: "Kai" })).toBeVisible()
  await picker.getByRole("button", { name: "Whole meeting" }).click()
  await expect(picker.getByRole("option", { name: /Kai/ })).toContainText("merges the two")
  await shot(page, "meeting-speaker-picker")

  await picker.getByRole("option", { name: /Laura/ }).click()
  await expect.poll(() => calls).toContain("PUT /meetings/mtg_weekly/speakers/3 Laura")
  await expect(page.getByRole("button", { name: "Laura said line 3; change" })).toBeVisible()

  // Line 5 is the second of Kai's turn: its own speaker is changed from the line, not the turn's name.
  await page.getByTestId("transcript-line").nth(4).hover()
  await page.getByRole("button", { name: "Kai said line 5; change" }).click()
  const second = page.getByRole("dialog", { name: "Who said line 5" })
  await second.getByRole("combobox", { name: "A name" }).fill("Phụng")
  await second.getByRole("option", { name: "New name “Phụng”" }).click()
  await expect.poll(() => calls).toContain("PUT /meetings/mtg_weekly/lines/5/speaker Phụng")
})

// Scenario: the Actions tab lists the open actions of every meeting; the owner shows Kai's only, then the done
// ones too, and opens the meeting an action came from.
test("the owner follows the actions across meetings", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings")
  await page.getByRole("tab", { name: /Actions/ }).click()

  const table = page.getByRole("table", { name: "Actions" })
  await expect(table.getByTestId("action")).toHaveCount(2)
  await expect(table).toContainText("Weekly sync with Kai · 0:20")

  await page.getByLabel("Who").click()
  await page.getByRole("option", { name: "Kai" }).click()
  await expect(table.getByTestId("action")).toHaveCount(1)
  expect(calls).toContain("GET /meeting-actions?who=Kai&state=open")

  await page.getByLabel("Who").click()
  await page.getByRole("option", { name: "Anyone" }).click()
  await page.getByLabel("State").click()
  await page.getByRole("option", { name: "Open and done" }).click()
  await expect(table.getByTestId("action")).toHaveCount(3)
  expect(await fits(page)).toBe(true)
  await shot(page, "meetings-actions")

  await table.getByRole("link", { name: "Weekly sync with Kai" }).first().click()
  await expect(page.getByRole("heading", { name: "Weekly sync with Kai", level: 2 })).toBeVisible()
})

// Scenario: Settings → Notes model has no model yet; the owner picks one of the gateway's chat models with the
// one-model picker the Bots use, then sets it back to none.
test("the owner picks the model that writes notes", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/notes-model")

  const model = page.getByTestId("notes-model").getByRole("combobox")
  await expect(model).toContainText("None: notes are not written")
  await expect(page.getByRole("checkbox", { name: /claude-sonnet-4-6/ })).toHaveCount(0)
  await model.click()
  await shot(page, "meetings-settings-model")
  await page.getByRole("option", { name: /Claude Sonnet 4.6/ }).click()
  await expect.poll(() => calls).toContain("PUT /meeting-settings claude-sonnet-4-6")
  await expect(model).toContainText("Claude Sonnet 4.6")

  await model.click()
  await page.getByRole("option", { name: "None: notes are not written" }).click()
  await expect.poll(() => calls).toContain("PUT /meeting-settings ")
})

// Scenario: the owner's dictionary is listed with the names added for them (a speaker, a Bot, a
// repository), each marked with where it comes from. The owner adds two at once and removes one; each change sends
// the owner's whole list.
test("the owner keeps the dictionary", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/dictionary")
  await expect(page.getByRole("heading", { name: "Dictionary", level: 1 })).toBeVisible()

  const own = page.getByRole("list", { name: "Your words" })
  await expect(own.getByRole("listitem")).toHaveText(["MapOps", "ĐKSD"])
  const found = page.getByRole("list", { name: "Words added for you" })
  await expect(found).toContainText("Kaispeaker")
  await expect(found).toContainText("coordinatorBot")
  await expect(found).toContainText("hypomnemarepository")
  await expect(page.getByTestId("words-sent")).toContainText("All 5 are sent")

  await page.getByLabel("Add a word").fill("Hyprnote, mapops, Phụng")
  await page.getByLabel("Add a word").press("Enter")
  await expect.poll(() => calls).toContain("PUT /meeting-words MapOps, ĐKSD, Hyprnote, Phụng")
  await expect(own.getByRole("listitem")).toHaveCount(4)

  await page.getByRole("button", { name: "Remove ĐKSD" }).click()
  await expect.poll(() => calls).toContain("PUT /meeting-words MapOps, Hyprnote, Phụng")
  expect(await fits(page)).toBe(true)
  await shot(page, "meetings-settings-words")
})

// Scenario: in the transcript, the owner selects a word heard wrong in line 2 and opens that line's fix: it opens
// with the words selected. The owner writes the right word and replaces it in the whole meeting; the line reads the
// fixed word, and the page offers to write the notes again.
test("the owner fixes a word heard wrong in the transcript", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings/mtg_weekly")
  await page.getByRole("tab", { name: "Transcript" }).click()

  // The owner selects "staging" in line 2, as a drag over the word would.
  await page.evaluate(`(() => {
    const text = document.querySelector('[data-line="2"]').firstChild
    const at = text.textContent.indexOf("staging")
    const range = document.createRange()
    range.setStart(text, at)
    range.setEnd(text, at + "staging".length)
    document.getSelection().removeAllRanges()
    document.getSelection().addRange(range)
  })()`)
  await page.getByRole("button", { name: "Fix a word in line 2" }).click()
  const fix = page.getByRole("dialog", { name: "Fix a word in line 2" })
  await expect(fix.getByLabel("Heard")).toHaveValue("staging")
  await fix.getByLabel("Said").fill("Staging-2")
  await fix.getByRole("button", { name: "Whole meeting" }).click()
  await shot(page, "meeting-word-fix")
  await fix.getByRole("button", { name: "Replace" }).click()

  await expect
    .poll(() => calls)
    .toContain("POST /meetings/mtg_weekly/fix staging→Staging-2 meeting")
  await expect(page.getByTestId("transcript-line").nth(1)).toContainText("done on Staging-2")
  const behind = page.getByTestId("notes-behind")
  await expect(behind).toContainText("The notes were written before your fix")
  expect(await fits(page)).toBe(true)
  await shot(page, "meeting-word-fixed")

  await behind.getByRole("button", { name: "Write again" }).click()
  await expect.poll(() => calls).toContain("POST /meetings/mtg_weekly/notes")
  await expect(behind).toHaveCount(0)
})

// Scenario: from the Meetings page, the owner follows the upload dialog's link into Settings → Dictionary; "Back to
// app" returns to Meetings, not home (moving between Settings' own pages keeps it: back-from-settings.test.ts).
// Settings opened straight away goes back home. The sidebar of Settings is a desktop's; a phone goes back with its
// drawer.
test("Back to app returns to the page the owner came from", async ({ page, isMobile }) => {
  test.skip(isMobile, "Settings' sidebar, with Back to app, is a desktop's")
  await fakeServer(page)
  await page.goto("/meetings")

  const dialog = page.getByRole("dialog", { name: "Upload a recording" })
  await page.getByRole("button", { name: "Upload recording" }).first().click()
  await dialog.getByRole("link", { name: "Dictionary" }).click()
  await expect(page).toHaveURL(/\/settings\/dictionary$/)
  await shot(page, "settings-back-to-app")
  await page.getByRole("link", { name: "Back to app" }).click()
  await expect(page).toHaveURL(/\/meetings$/)
  await expect(page.getByRole("heading", { name: "Meetings", level: 1 })).toBeVisible()

  await page.evaluate("sessionStorage.clear()")
  await page.goto("/settings/dictionary")
  await page.getByRole("link", { name: "Back to app" }).click()
  await expect(page).toHaveURL(/127\.0\.0\.1:4173\/$/)
})

// Recording a meeting live (issue #820).
test.describe("recording a meeting live", () => {
  // Scenario: the owner records a meeting in person: the dialog asks where it is held (online is offered only where a
  // tab's sound can be shared, which a phone cannot), the language and the people. The meeting's page opens on the
  // recording bar with the time and the microphone's meter; what is being said shows, then the line kept, in the
  // transcript's turn layout with its voice's badge. Stopping asks first, keeps the last words and ends the meeting,
  // which lands transcribed, its notes being written, with the player.
  test("the owner records a meeting in person and stops it", async ({ page, isMobile }) => {
    const { calls, sockets } = await fakeServer(page)
    await page.goto("/meetings")

    await page.getByRole("button", { name: "Record" }).first().click()
    const dialog = page.getByRole("dialog", { name: "Record a meeting" })
    if (isMobile) {
      await expect(dialog.getByRole("radio", { name: /Online/ })).toBeDisabled()
      await expect(dialog).toContainText(
        "A tab's sound can be shared only from Chrome or Edge on a computer.",
      )
      await expect(dialog.getByRole("radio", { name: /In person/ })).toBeChecked()
    } else {
      await expect(dialog.getByRole("radio", { name: /Online/ })).toBeChecked()
      await dialog.getByRole("radio", { name: /In person/ }).check()
    }
    await dialog.getByLabel("Language").click()
    await page.getByRole("option", { name: "English" }).click()
    await dialog.getByRole("button", { name: "More details" }).click()
    await dialog.getByLabel("People").fill("Kai, Laura")
    await shot(page, "meetings-record")

    await dialog.getByRole("button", { name: "Start recording" }).click()

    const bar = page.getByRole("region", { name: "Recording" })
    await expect(bar).toBeVisible()
    await expect(bar.getByTestId("meter-mic")).toContainText("Microphone")
    await expect(bar.getByTestId("meter-tab")).toHaveCount(0)
    const transcript = page.getByRole("list", { name: "Live transcript" })
    await expect(transcript.getByTestId("transcript-line")).toHaveText([
      "Shall we start the launch review?",
    ])
    await expect(transcript.getByTestId("transcript-turn")).toContainText("Speaker 1")
    await expect(page.locator("[data-slot=meeting-player]")).toHaveCount(0)
    await expect(transcript.getByTestId("transcript-preview")).toHaveCount(0)
    expect(calls).toContain("POST /meetings/live in_person en Kai,Laura")
    expect(sockets).toEqual([expect.stringMatching(/^mtg_live\d+ mic 0$/)])
    expect(await fits(page)).toBe(true)
    await shot(page, "meetings-recording")

    await bar.getByRole("button", { name: "Stop" }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Stop and end" }).click()

    await expect(page.getByRole("region", { name: "Recording" })).toHaveCount(0)
    await expect(page.locator("[data-slot=meeting-player]")).toBeVisible()
    const progress = page.getByTestId("meeting-progress")
    await expect(progress).toContainText("Recorded")
    await expect(progress).toContainText("Writing notes")
    expect(calls.filter((c) => c.endsWith("/end"))).toHaveLength(1)
    await page.getByRole("tab", { name: "Transcript" }).click()
    await expect(page.getByRole("list", { name: "Transcript" })).toContainText(
      "Shall we start the launch review?",
    )
    expect(await fits(page)).toBe(true)
    await shot(page, "meetings-recorded")
  })

  // Scenario: online, on a computer: the microphone and the shared tab are two tracks, each with its meter and its
  // socket; the microphone is the owner, "You", the tab's voice "Speaker 1"; the tab's line, said first, is read
  // first though it was kept second.
  test("the owner records an online meeting with the shared tab", async ({ page, isMobile }) => {
    test.skip(isMobile, "a phone cannot share a tab's sound")
    // The share picker stands for a tab that plays a tone, as Chromium's fake devices give no tab.
    await page.addInitScript({
      content: `navigator.mediaDevices.getDisplayMedia = async () => {
        const context = new AudioContext()
        const tone = context.createOscillator()
        const out = context.createMediaStreamDestination()
        tone.connect(out)
        tone.start()
        const video = document.createElement("canvas").captureStream(1).getVideoTracks()
        return new MediaStream([...video, ...out.stream.getAudioTracks()])
      }`,
    })
    const { calls, sockets } = await fakeServer(page)
    await page.goto("/meetings")

    await page.getByRole("button", { name: "Record" }).first().click()
    const dialog = page.getByRole("dialog", { name: "Record a meeting" })
    await dialog.getByRole("button", { name: "Start recording" }).click()

    const bar = page.getByRole("region", { name: "Recording" })
    await expect(bar.getByTestId("meter-mic")).toContainText("You")
    await expect(bar.getByTestId("meter-tab")).toContainText("Meeting tab")
    const transcript = page.getByRole("list", { name: "Live transcript" })
    await expect(transcript.getByTestId("transcript-line")).toHaveText([
      "Kai here, the landing page is on staging.",
      "Shall we start the launch review?",
    ])
    await expect(transcript.getByTestId("transcript-turn").nth(0)).toContainText("Speaker 1")
    await expect(transcript.getByTestId("transcript-turn").nth(1)).toContainText("You")
    expect(calls).toContain("POST /meetings/live online auto ")
    expect(sockets.map((x) => x.split(" ").slice(1).join(" ")).toSorted()).toEqual([
      "mic 0",
      "tab 0",
    ])
    await shot(page, "meetings-recording-online")

    await bar.getByRole("button", { name: "Stop" }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Stop and end" }).click()
    await expect(page.locator("[data-slot=meeting-player]")).toBeVisible()
  })

  // Scenario: a meeting was left recording (its tab was closed): its page says so with what was kept and offers
  // Resume recording, which records again where the meeting was, on its clock, or End.
  test("the owner resumes a meeting left recording", async ({ page }) => {
    const { world: w, sockets } = await fakeServer(page)
    w.meetings.unshift(
      meeting("mtg_left", "Design review", {
        state: "recording",
        source: "live",
        kind: "in_person",
        provider: undefined,
        size: 0,
        duration_ms: 65_000,
        notes: { state: "waiting", summary: "", sections: [] },
      }),
    )
    await page.goto("/meetings/mtg_left")

    const left = page.getByTestId("not-recording")
    await expect(left).toContainText("left recording at 1:05")
    await expect(left.getByRole("button", { name: "End meeting" })).toBeVisible()
    await shot(page, "meetings-left-recording")
    await left.getByRole("button", { name: "Resume recording" }).click()

    const bar = page.getByRole("region", { name: "Recording" })
    await expect(bar).toBeVisible()
    await expect(bar.getByRole("timer")).toContainText(/^1:0[5-9]$/)
    await expect(page.getByTestId("transcript-line")).toHaveText([
      "Shall we start the launch review?",
    ])
    expect(sockets).toEqual(["mtg_left mic 65000"])
    await expect(page.getByTestId("transcript-turn")).toContainText("1:08")

    await bar.getByRole("button", { name: "Stop" }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Stop and end" }).click()
    await expect(page.locator("[data-slot=meeting-player]")).toBeVisible()
  })
})
