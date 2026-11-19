import { describe, expect, test } from "vitest"

import type { StudioItem, VideoRevision } from "@/api/generated/types.gen"

import { filming, inProgress, sceneDone, stages, stateTone } from "./studio-state"

const rev = (state: VideoRevision["state"], more: Partial<VideoRevision> = {}): VideoRevision => ({
  rev: 1,
  state,
  lines: ["Một", "Hai", "Ba"],
  captions: true,
  aspect: "9:16",
  length_ms: 0,
  warnings: [],
  created_at: "2026-11-18T09:00:00Z",
  updated_at: "2026-11-18T09:00:00Z",
  ...more,
})

const status = (r: VideoRevision) => stages(r).map((s) => s.status)

describe("a revision's stages", () => {
  test("wait while it is queued, and pass one by one as it is made", () => {
    expect(status(rev("queued"))).toEqual(["waiting", "waiting", "waiting"])
    expect(status(rev("voicing"))).toEqual(["now", "waiting", "waiting"])
    expect(status(rev("composing"))).toEqual(["done", "now", "waiting"])
    expect(status(rev("captioning"))).toEqual(["done", "done", "now"])
    expect(status(rev("review"))).toEqual(["done", "done", "done"])
  })

  test("skip captions when they are off", () => {
    expect(status(rev("composing", { captions: false }))).toEqual(["done", "now", "skipped"])
  })

  test("mark the stage it failed at", () => {
    expect(status(rev("failed", { failed_stage: "composing" }))).toEqual([
      "done",
      "failed",
      "waiting",
    ])
  })

  test("count the scenes the stage being made has done", () => {
    const r = rev("composing", { progress: { scenes_done: 2, scenes_total: 3 } })
    expect([0, 1, 2].map((i) => sceneDone(r, i))).toEqual([true, true, false])
    expect(sceneDone(rev("composing"), 0)).toBe(false)
  })
})

describe("a Studio item", () => {
  const item = (kind: StudioItem["kind"], state: string): StudioItem => ({
    kind,
    id: "x",
    title: "x",
    bot: "Atlas",
    created_at: "2026-11-18T09:00:00Z",
    length_ms: 0,
    state,
  })

  test("is in progress while a take is filmed or a revision made", () => {
    expect(inProgress(item("take", "uploading"))).toBe(true)
    expect(inProgress(item("take", "available"))).toBe(false)
    expect(inProgress(item("video", "captioning"))).toBe(true)
    expect(inProgress(item("video", "review"))).toBe(false)
  })

  test("shows a red dot only while a take is filmed", () => {
    expect(filming(item("take", "paused"))).toBe(true)
    expect(filming(item("take", "uploading"))).toBe(false)
    expect(filming(item("video", "queued"))).toBe(false)
  })

  test("waits for the owner in review", () => {
    expect(stateTone("video", "review")).toBe("warning")
    expect(stateTone("video", "changes_requested")).toBe("destructive")
    expect(stateTone("take", "available")).toBe("secondary")
  })
})
