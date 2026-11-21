import { describe, expect, test } from "vitest"

import type { VideoLook } from "@/api/generated/types.gen"

import { fieldsOf, sentLook } from "./look-state"

// The saved default as the server starts it: Clean, an Android frame, midnight (videos/look.go, DefaultLook).
const saved: VideoLook = {
  captions: { preset: "clean", position: "preset" },
  frame: { chrome: "android", padding: 48, radius: 40, shadow: true, safe_top: false },
  background: { preset: "midnight" },
}

describe("the sections a look sends", () => {
  test("none while nothing changed, so the request carries no look", () => {
    expect(sentLook(fieldsOf(saved), fieldsOf(saved))).toBeUndefined()
  })

  test("only the section changed, whole as the web shows it", () => {
    const start = fieldsOf(saved)
    const now = { ...start, frame: { ...start.frame, chrome: "iphone" as const } }

    expect(sentLook(start, now)).toEqual({
      frame: { chrome: "iphone", shadow: true, safe_top: false },
    })
  })

  test("a caption style changed leaves the frame and background to the source", () => {
    const start = fieldsOf(saved)
    const now = { ...start, captions: { ...start.captions, preset: "pop" } }

    expect(sentLook(start, now)).toEqual({ captions: { preset: "pop", position: "preset" } })
  })

  test("not a section changed and set back", () => {
    const start = fieldsOf(saved)
    const away = { ...start, frame: { ...start.frame, chrome: "none" as const } }
    const back = { ...away, frame: { ...away.frame, chrome: "android" as const } }

    expect(sentLook(start, back)).toBeUndefined()
  })

  test("two colours in place of a template", () => {
    const start = fieldsOf(saved)
    const now = { ...start, background: { colors: ["#112233", "#445566"] as [string, string] } }

    expect(sentLook(start, now)).toEqual({ background: { colors: ["#112233", "#445566"] } })
  })
})

describe("a stored look's fields", () => {
  test("a revision made before the looks starts from midnight, as the server reads it", () => {
    const legacy: VideoLook = {
      captions: { preset: "legacy", position: "preset" },
      frame: { chrome: "full", padding: 48, radius: 14, shadow: true, safe_top: false },
    }

    expect(fieldsOf(legacy).background).toEqual({ preset: "midnight" })
    expect(fieldsOf(legacy).captions.preset).toBe("legacy")
  })

  test("padding and radius are not the web's: they are never sent", () => {
    const start = fieldsOf({ ...saved, frame: { ...saved.frame, padding: 120, radius: 20 } })

    expect(start.frame).toEqual({ chrome: "android", shadow: true, safe_top: false })
  })
})
