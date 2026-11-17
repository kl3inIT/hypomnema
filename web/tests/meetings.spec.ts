import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// The Meetings pages (issue #721), built, against a fake client API, on a phone and a desktop: the list grouped by
// day, the upload dialog, a meeting's page with its player, notes and transcript, the speaker picker, the Actions
// tab and Settings → Meetings.

function at(daysAgo: number, hour: number, minute = 0) {
  const d = new Date()
  d.setDate(d.getDate() - daysAgo)
  d.setHours(hour, minute, 0, 0)

  return d.toISOString()
}

const formats = [
  { id: "general", name: "General", sections: [{ title: "Discussion highlights", about: "" }] },
  { id: "team", name: "Team meeting", sections: [{ title: "Progress summary", about: "" }] },
  { id: "one_on_one", name: "1:1", sections: [{ title: "Updates", about: "" }] },
  { id: "standup", name: "Stand-up", sections: [{ title: "Yesterday", about: "" }] },
  {
    id: "client_call",
    name: "Client call",
    sections: [{ title: "Agreed deliverables", about: "" }],
  },
]

type Speaker = { label: string; name: string; named: boolean; talk_ms: number; share: number }

function meeting(id: string, title: string, over: object = {}) {
  return {
    id,
    title,
    titled: true,
    people: [],
    terms: [],
    file_name: `${id}.m4a`,
    media_type: "audio/wav",
    size: 1000,
    duration_ms: 42 * 60_000,
    state: "transcribed",
    provider: "soniox",
    model: "stt-async-v5",
    created_at: at(0, 9, 30),
    notes: {
      format: "team",
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
    start_ms: 12_000,
    end_ms: 19_000,
    speaker: "1",
    heard: "1",
    text: "Okay, let's start with the launch. Kai, where are we on the landing page?",
  },
  {
    n: 2,
    start_ms: 20_000,
    end_ms: 64_000,
    speaker: "2",
    heard: "2",
    text: "The page is done on staging. Pricing is the only part missing; I will send the numbers on Wednesday.",
  },
  {
    n: 3,
    start_ms: 65_000,
    end_ms: 80_000,
    speaker: "3",
    heard: "3",
    text: "Laura here. Let's ship it on Friday and add pricing next week.",
  },
  {
    n: 4,
    start_ms: 724_000,
    end_ms: 740_000,
    speaker: "2",
    heard: "2",
    text: "Agreed, Friday it is.",
  },
]

function world() {
  const speakers: Speaker[] = [
    { label: "1", name: "You", named: true, talk_ms: 7_000, share: 0.09 },
    { label: "2", name: "Kai", named: true, talk_ms: 60_000, share: 0.73 },
    { label: "3", name: "Speaker 3", named: false, talk_ms: 15_000, share: 0.18 },
  ]
  const weekly = meeting("mtg_weekly", "Weekly sync with Kai", {
    people: ["Laura", "Kai"],
    speakers,
    actions: 3,
    open_actions: 2,
    notes: {
      format: "team",
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
        notes: { format: "general", state: "writing", summary: "", sections: [] },
        speakers: [
          { label: "1", name: "Speaker 1", named: false, talk_ms: 1, share: 0.5 },
          { label: "2", name: "Speaker 2", named: false, talk_ms: 1, share: 0.5 },
        ],
      }),
      meeting("mtg_minh", "1:1 with Minh", {
        created_at: at(1, 16, 0),
        duration_ms: 28 * 60_000,
        actions: 2,
        open_actions: 0,
        speakers: [
          { label: "1", name: "You", named: true, talk_ms: 1, share: 0.4 },
          { label: "2", name: "Minh", named: true, talk_ms: 1, share: 0.6 },
        ],
      }),
      meeting("mtg_lost", "Client call, Nov 3", {
        created_at: at(14, 10, 0),
        state: "failed",
        duration_ms: 0,
        provider: undefined,
        failure:
          "No speech-to-text provider is set up. Add a Soniox or ElevenLabs key on Settings → Gateway → Audio, then upload the recording again.",
        notes: { format: "client_call", state: "waiting", summary: "", sections: [] },
      }),
    ],
    items,
    notesModel: "",
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
  const detail = (id: string) => {
    const m = w.meetings.find((x) => x.id === id)!

    return {
      meeting: m,
      lines: id === "mtg_weekly" ? lines : [],
      items: id === "mtg_weekly" ? w.items : [],
      suggestions:
        id === "mtg_weekly" && m.speakers.some((s) => s.label === "3" && !s.named)
          ? [{ label: "3", name: "Laura", line: 3 }]
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
          format: url.searchParams.get("format"),
          state: "waiting",
          summary: "",
          sections: [],
        },
        people: url.searchParams.getAll("people"),
        terms: url.searchParams.getAll("terms"),
      })
      w.meetings.unshift(made)

      return json(made, 201)
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
      calls.push(`${call} ${(request.postDataJSON() as { format: string }).format}`)

      return json(
        w.meetings.find((x) => x.id === notes[1]),
        202,
      )
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
        return json({ notes_model: w.notesModel, formats })
      case "PUT /meeting-settings":
        w.notesModel = (request.postDataJSON() as { notes_model: string }).notes_model
        calls.push(`${call} ${w.notesModel}`)

        return json({ notes_model: w.notesModel, formats })
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

  return { calls, world: w }
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
  await expect(weekly).toContainText("You, Kai, Speaker 3")
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

// Scenario: the owner uploads a recording: the file and its language, then under More details a title, the people,
// the words to hear right and the notes format. They are sent with the recording, and the meeting's page opens on
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
  await dialog.getByLabel("Words to hear right").fill("MapOps, ĐKSD")
  await dialog.getByLabel("Notes format").click()
  await page.getByRole("option", { name: "Stand-up" }).click()
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
  expect(query.getAll("terms")).toEqual(["MapOps", "ĐKSD"])
  expect(query.get("format")).toBe("standup")
  expect(await fits(page)).toBe(true)
  await shot(page, "meetings-transcribing")
})

// Scenario: the owner opens a meeting: the player on top with its length, the summary, the decision with the time
// it was said, the actions with who and when, the format's sections. Marking an action done is sent; Write again
// sends the format picked. A failed meeting says why and where to fix it.
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

  await page.getByLabel("Notes format").click()
  await page.getByRole("option", { name: "1:1" }).click()
  await page.getByRole("button", { name: "Write again" }).click()
  await expect.poll(() => calls).toContain("POST /meetings/mtg_weekly/notes one_on_one")

  await page.goto("/meetings/mtg_lost")
  await expect(page.getByTestId("meeting-progress")).toContainText("Settings → Gateway → Audio")
  await shot(page, "meeting-failed")
})

// Scenario: on the transcript, each voice shows its share of the talk and the name Speaker 3 gave itself. The owner
// opens line 3's speaker: Laura is suggested (said it, and in People), Kai is in the meeting. For the whole meeting,
// Speaker 3 is named Laura; then line 4 alone is given a new name.
test("the owner names a speaker for the whole meeting or one line", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/meetings/mtg_weekly")
  await page.getByRole("tab", { name: "Transcript" }).click()

  const speakers = page.getByRole("list", { name: "Speakers" })
  await expect(speakers).toContainText("Kai73%")
  await expect(speakers).toContainText("Laura?")
  await expect(page.getByTestId("transcript-line")).toHaveCount(4)
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

  await page.getByRole("button", { name: "Kai said line 4; change" }).click()
  const second = page.getByRole("dialog", { name: "Who said line 4" })
  await second.getByRole("combobox", { name: "A name" }).fill("Phụng")
  await second.getByRole("option", { name: "New name “Phụng”" }).click()
  await expect.poll(() => calls).toContain("PUT /meetings/mtg_weekly/lines/4/speaker Phụng")
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

// Scenario: Settings → Meetings has no notes model yet; the owner picks one of the gateway's chat models.
test("the owner picks the model that writes notes", async ({ page }) => {
  const { calls } = await fakeServer(page)
  await page.goto("/settings/meetings")

  await expect(page.getByTestId("notes-model")).toContainText("None picked")
  await page.getByRole("checkbox", { name: /claude-sonnet-4-6/ }).click()
  await expect(page.getByTestId("notes-model")).toContainText("claude-sonnet-4-6")
  expect(calls).toContain("PUT /meeting-settings claude-sonnet-4-6")
  await expect(page.getByText("Stand-up")).toBeVisible()
  expect(await fits(page)).toBe(true)
  await shot(page, "meetings-settings")
})
