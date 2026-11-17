import { describe, expect, it } from "vitest"

import type { Meeting } from "@/api/generated/types.gen"

import {
  actionsText,
  addWords,
  byDay,
  clock,
  formatName,
  listOf,
  ownWords,
  sameName,
  stageOf,
  stepsOf,
} from "./meetings-state"

function meeting(over: Partial<Meeting> = {}): Meeting {
  return {
    id: "mtg_1",
    title: "Weekly sync",
    titled: true,
    people: [],
    file_name: "sync.m4a",
    media_type: "audio/mp4",
    size: 10,
    duration_ms: 42 * 60_000,
    state: "transcribed",
    created_at: "2026-11-17T09:30:00Z",
    notes: { format: "general", state: "ready", summary: "", sections: [] },
    speakers: [
      { label: "1", name: "Kai", named: true, talk_ms: 1, share: 0.5 },
      { label: "2", name: "Speaker 2", named: false, talk_ms: 1, share: 0.5 },
    ],
    actions: 5,
    open_actions: 3,
    ...over,
  }
}

describe("meetings", () => {
  // The list groups meetings under Today, Yesterday and Earlier, as Granola and Otter do, keeping their order.
  it("groups meetings by day", () => {
    const now = new Date(2026, 10, 17, 18, 0).getTime()
    const at = (d: number, h: number) => new Date(2026, 10, d, h, 0).toISOString()
    const groups = byDay(
      [
        { created_at: at(17, 9) },
        { created_at: at(16, 23) },
        { created_at: at(16, 8) },
        { created_at: at(2, 9) },
      ],
      now,
    )

    expect(groups.map((g) => [g.day, g.meetings.length])).toEqual([
      ["Today", 1],
      ["Yesterday", 2],
      ["Earlier", 1],
    ])
  })

  // The steps say where the work stands; a failure marks its own step, not the others.
  it("shows the steps of a meeting's work", () => {
    expect(stepsOf(meeting({ state: "transcribing", speakers: [] })).map((s) => s.state)).toEqual([
      "done",
      "now",
      "waiting",
    ])
    expect(
      stepsOf(
        meeting({ notes: { format: "general", state: "writing", summary: "", sections: [] } }),
      ),
    ).toEqual([
      { name: "Uploaded", state: "done" },
      { name: "Transcribed · 2 speakers", state: "done" },
      { name: "Writing notes", state: "now" },
    ])
    expect(stageOf(meeting({ state: "failed" }))).toBe("failed")
    expect(
      stageOf(
        meeting({ notes: { format: "general", state: "failed", summary: "", sections: [] } }),
      ),
    ).toBe("notes_failed")
  })

  it("says the actions, the times and the lists the owner types", () => {
    expect(actionsText(meeting())).toBe("3 open of 5")
    expect(actionsText(meeting({ open_actions: 0 }))).toBe("All 5 done")
    expect(actionsText(meeting({ actions: 0, open_actions: 0 }))).toBe("None")
    expect(clock(64_000)).toBe("1:04")
    expect(clock(3_725_000)).toBe("1:02:05")
    expect(listOf("Laura,  Kai\nMinh,,")).toEqual(["Laura", "Kai", "Minh"])
    expect(sameName("  laura ", "Laura")).toBe(true)
  })

  // Issue #747: the owner's words are those of the list that are the owner's; typing several at once adds each
  // once, in any case, and a word too long for the list is left out.
  it("adds the words the owner types to the owner's list", () => {
    const own = ownWords([
      { word: "MapOps", from: "owner", sent: true },
      { word: "Laura", from: "speaker", sent: true },
      { word: "ĐKSD", from: "owner", sent: true },
    ])
    expect(own).toEqual(["MapOps", "ĐKSD"])
    expect(addWords(own, ` mapops, Hypomnema,\nhypomnema, ${"x".repeat(101)}`)).toEqual([
      "MapOps",
      "ĐKSD",
      "Hypomnema",
    ])
  })

  // A format the owner deleted is said so where notes written in it are shown.
  it("names a format, or says it was deleted", () => {
    const formats = [{ id: "general", name: "General", sections: [], builtin: true }]
    expect(formatName(formats, "general")).toBe("General")
    expect(formatName(formats, "mtf_gone")).toBe("A deleted format")
  })
})
