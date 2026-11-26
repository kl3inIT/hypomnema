import { describe, expect, test } from "vitest"

import type { Take } from "@/api/generated/types.gen"

import { chapters, clock, locate, seekInSegment, takeOfCall } from "./take-state"

const segment = (at_ms: number, length_ms: number, last_frame_ms: number) => ({
  file: `seg-${at_ms}.mp4`,
  at_ms,
  length_ms,
  last_frame_ms,
  width: 720,
  height: 1280,
  bytes: 1,
  sha256: "",
})

const take = {
  id: "tk_0123456789abcdef",
  title: "Sign in",
  source: "phone",
  agent: "d-1",
  node: "nod_1",
  started_at: "2026-11-18T10:00:00Z",
  state: "available",
  duration_ms: 13_000,
  segments: [segment(0, 3000, 2500), segment(3000, 10_000, 1500)],
  steps: [],
  pauses: [],
} as Take

describe("a take's timeline", () => {
  test("a time is in the segment that holds it, the end in the last", () => {
    expect(locate(take, 0)).toEqual({ index: 0, offset: 0 })
    expect(locate(take, 2999)).toEqual({ index: 0, offset: 2999 })
    expect(locate(take, 3000)).toEqual({ index: 1, offset: 0 })
    expect(locate(take, 13_000)).toEqual({ index: 1, offset: 10_000 })
    expect(locate(take, 99_000)).toEqual({ index: 1, offset: 10_000 })
    expect(locate(take, -5)).toEqual({ index: 0, offset: 0 })
    expect(locate({ ...take, segments: [] }, 10)).toBeNull()
  })

  // A browser does not seek into a file's last sample: a time in the still tail seeks just before the last frame,
  // and the timeline is ahead of the video by the rest.
  test("a seek into a still tail lands just before its last frame", () => {
    const second = take.segments[1]!
    expect(seekInSegment(second, 1000)).toEqual({ to: 1000, ahead: 0 })
    expect(seekInSegment(second, 8000)).toEqual({ to: 1490, ahead: 6510 })
    expect(seekInSegment(segment(0, 5000, 0), 2000)).toEqual({ to: 0, ahead: 2000 })
  })
})

describe("a take's chapters", () => {
  test("the chapters named, with an intro before a late first one", () => {
    expect(
      chapters([
        { at_ms: 2000, text: "Open the app", chapter: "Open" },
        { at_ms: 3000, text: "Tap" },
        { at_ms: 9000, text: "Sign in", chapter: "Sign in" },
      ]),
    ).toEqual([
      { title: "Intro", at: 0 },
      { title: "Open", at: 2000 },
      { title: "Sign in", at: 9000 },
    ])
  })

  // autodemo's rule: a line said at least 6 s after the last chapter, named by its first words.
  test("without names, a chapter for each line 6 s apart", () => {
    expect(
      chapters([
        { at_ms: 0, text: "Open the shop app, then wait" },
        { at_ms: 4000, text: "Tap Sign in" },
        { at_ms: 7000, text: "Type the email: lan@example.com" },
      ]),
    ).toEqual([
      { title: "Open the shop app", at: 0 },
      { title: "Type the email", at: 7000 },
    ])
  })
})

test("a video tool's call names its take by its argument or its answer", () => {
  expect(takeOfCall({ take: "tk_0123456789abcdef" })).toBe("tk_0123456789abcdef")
  expect(takeOfCall({ title: "x" }, 'Take tk_00000000000000aa ("x") is recording.')).toBe(
    "tk_00000000000000aa",
  )
  expect(takeOfCall({ take: "../x" }, "nothing")).toBeNull()
  expect(clock(61_900)).toBe("1:01")
})
