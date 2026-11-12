import { describe, expect, it } from "vitest"

import type { BrowserTab } from "@/api/generated/types.gen"

import { tabName, whose } from "./browser-state"
import {
  countClick,
  keyInput,
  letterbox,
  mapToPage,
  modifiers,
  retryIn,
  socketUrl,
  textInput,
  wheelUnit,
} from "./stream"

const noKeys = { altKey: false, ctrlKey: false, metaKey: false, shiftKey: false }
const tab = (over: Partial<BrowserTab>): BrowserTab => ({
  id: "d-dsp_1",
  kind: "worker",
  url: "",
  title: "",
  views: 0,
  ...over,
})

describe("where a pointer is on the tab", () => {
  // A 1280x720 tab drawn in an 800x800 panel: 800x450, with 175px of black above and below.
  const rect = { left: 100, top: 50, width: 800, height: 800 }
  const frame = { width: 1280, height: 720 }

  it("fits the frame into the panel and centres it", () => {
    expect(letterbox(rect, frame)).toEqual({ width: 800, height: 450, left: 0, top: 175 })
    expect(letterbox({ width: 1600, height: 450 }, frame)).toEqual({
      width: 800,
      height: 450,
      left: 400,
      top: 0,
    })
  })

  it("maps a point of the picture to the tab's own pixels", () => {
    expect(mapToPage(rect, frame, frame, 100, 225, false)).toEqual({ x: 0, y: 0, scale: 1.6 })
    expect(mapToPage(rect, frame, frame, 500, 450, false)).toEqual({ x: 640, y: 360, scale: 1.6 })
  })

  it("maps by the tab's size when the frame is smaller than the tab", () => {
    const small = { width: 640, height: 360 }

    expect(mapToPage(rect, small, frame, 500, 450, false)).toMatchObject({ x: 640, y: 360 })
  })

  it("takes no point in the black around the picture, and keeps a drag on the edge", () => {
    expect(mapToPage(rect, frame, frame, 500, 100, false)).toBeNull()
    expect(mapToPage(rect, frame, frame, 500, 100, true)).toMatchObject({ x: 640, y: 0 })
    expect(mapToPage(rect, frame, frame, 2000, 2000, true)).toMatchObject({ x: 1280, y: 720 })
  })

  it("takes no point before the panel has a size", () => {
    expect(mapToPage({ left: 0, top: 0, width: 0, height: 0 }, frame, frame, 0, 0, true)).toBeNull()
  })
})

describe("the mouse", () => {
  it("counts a press on from the last one of the same button, close and soon", () => {
    const first = countClick(null, "left", 10, 10, 1000)
    const second = countClick(first, "left", 12, 11, 1300)

    expect(first.count).toBe(1)
    expect(second.count).toBe(2)
    expect(countClick(second, "left", 12, 11, 1500).count).toBe(3)
    expect(countClick(first, "left", 12, 11, 1600).count, "too late").toBe(1)
    expect(countClick(first, "left", 20, 10, 1100).count, "too far").toBe(1)
    expect(countClick(first, "right", 10, 10, 1100).count, "another button").toBe(1)
  })

  it("turns a wheel's lines and pages into pixels", () => {
    expect(wheelUnit(0, null)).toBe(1)
    expect(wheelUnit(1, null)).toBe(16)
    expect(wheelUnit(2, { width: 1280, height: 720 })).toBe(720)
  })

  it("encodes the keys held as Chrome's bits", () => {
    expect(modifiers(noKeys)).toBe(0)
    expect(modifiers({ ...noKeys, altKey: true, shiftKey: true })).toBe(9)
    expect(modifiers({ altKey: true, ctrlKey: true, metaKey: true, shiftKey: true })).toBe(15)
  })
})

describe("the keys", () => {
  const key = (k: string, code: string, keyCode: number, held = {}) => ({
    key: k,
    code,
    keyCode,
    ...noKeys,
    ...held,
  })

  it("sends a key that types with its text on the way down only", () => {
    expect(keyInput("down", key("a", "KeyA", 65))).toEqual({
      type: "key",
      action: "down",
      key: "a",
      code: "KeyA",
      keyCode: 65,
      text: "a",
      modifiers: 0,
    })
    expect(keyInput("up", key("a", "KeyA", 65))).not.toHaveProperty("text")
  })

  it("sends Enter with a carriage return, and a key that types nothing with no text", () => {
    expect(keyInput("down", key("Enter", "Enter", 13))).toMatchObject({ text: "\r" })
    expect(keyInput("down", key("ArrowLeft", "ArrowLeft", 37))).not.toHaveProperty("text")
  })

  it("sends a shortcut with its modifiers and no text", () => {
    const sent = keyInput("down", key("a", "KeyA", 65, { ctrlKey: true }))

    expect(sent).toMatchObject({ key: "a", modifiers: 2 })
    expect(sent).not.toHaveProperty("text")
  })

  it("sends a pasted text as one key a character, a new line as Enter", () => {
    expect(textInput("hé\n")).toEqual([
      { type: "key", action: "down", key: "h", code: "", keyCode: 0, text: "h", modifiers: 0 },
      { type: "key", action: "up", key: "h", code: "", keyCode: 0, modifiers: 0 },
      { type: "key", action: "down", key: "é", code: "", keyCode: 0, text: "é", modifiers: 0 },
      { type: "key", action: "up", key: "é", code: "", keyCode: 0, modifiers: 0 },
      {
        type: "key",
        action: "down",
        key: "Enter",
        code: "",
        keyCode: 13,
        text: "\r",
        modifiers: 0,
      },
      { type: "key", action: "up", key: "Enter", code: "", keyCode: 13, modifiers: 0 },
    ])
    expect(textInput("")).toEqual([])
  })
})

describe("the connection", () => {
  it("waits longer after each drop, ten seconds at most", () => {
    expect([0, 1, 2, 3, 4, 5, 9].map(retryIn)).toEqual([500, 1000, 2000, 4000, 8000, 10000, 10000])
  })

  it("opens the view's WebSocket on the server that served the page", () => {
    expect(socketUrl({ protocol: "https:", host: "hyp.example" }, "a b")).toBe(
      "wss://hyp.example/api/v1/browser/views/a%20b",
    )
    expect(socketUrl({ protocol: "http:", host: "localhost:8080" }, "t")).toBe(
      "ws://localhost:8080/api/v1/browser/views/t",
    )
  })
})

describe("the strip of tabs", () => {
  it("names a tab by its page's title, and by what it is while it has none", () => {
    expect(tabName(tab({ title: "Example Domain", url: "https://example.com/" }))).toBe(
      "Example Domain",
    )
    expect(tabName(tab({ title: "https://example.com/", url: "https://example.com/" }))).toBe(
      "https://example.com/",
    )
    expect(tabName(tab({ title: "about:blank", url: "about:blank" }))).toBe("New tab")
    expect(tabName(tab({ id: "owner", kind: "owner" }))).toBe("My tab")
  })

  it("says whose a tab is", () => {
    expect(whose(tab({ id: "owner", kind: "owner" })).mark).toBe("you")
    expect(whose(tab({ kind: "coordinator" })).mark).toBe("pi")
    expect(whose(tab({ task_id: "tsk_1" })).line).toContain("tsk_1")
  })
})
