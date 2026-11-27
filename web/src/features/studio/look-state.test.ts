import { describe, expect, test } from "vitest"

import type { VideoLook } from "@/api/generated/types.gen"

import {
  editsOf,
  fieldsOf,
  newCard,
  sampleCards,
  sampleOf,
  sentLook,
  withEdits,
} from "./look-state"

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

describe("the music a look sends (issue #910, row 15)", () => {
  const withMusic: VideoLook = {
    ...saved,
    music: {
      track: "mus_0123456789abcdefghijklmnop",
      name: "Lo-fi",
      length_ms: 95_000,
      gain_db: -6,
    },
  }

  test("left alone, it is not sent, so the server takes the default's or the source's", () => {
    const start = fieldsOf(withMusic)
    const now = { ...start, frame: { ...start.frame, shadow: false } }

    expect(sentLook(start, now)).toEqual({
      frame: { chrome: "android", shadow: false, safe_top: false },
    })
  })

  test("a track chosen is sent with its gain, since a section without its track is refused", () => {
    const start = fieldsOf(saved)
    const now = { ...start, music: { ...start.music, track: "mus_0123456789abcdefghijklmnop" } }

    expect(sentLook(start, now)).toEqual({
      music: { track: "mus_0123456789abcdefghijklmnop", gain_db: 0 },
    })
  })

  test("a gain moved alone is sent with its track", () => {
    const start = fieldsOf(withMusic)
    const now = { ...start, music: { ...start.music, gain_db: 3 } }

    expect(sentLook(start, now)).toEqual({
      music: { track: "mus_0123456789abcdefghijklmnop", gain_db: 3 },
    })
  })

  test("no music chosen in place of a track is sent as none", () => {
    const start = fieldsOf(withMusic)
    const now = { ...start, music: { ...start.music, track: "none" } }

    expect(sentLook(start, now)).toEqual({ music: { track: "none", gain_db: -6 } })
  })

  test("a look without music, or whose track fell back, starts from none", () => {
    expect(fieldsOf(saved).music).toEqual({ track: "none", gain_db: 0 })
    const fellBack: VideoLook = {
      ...saved,
      music: {
        track: "none",
        gain_db: 0,
        fell_back_from: {
          track: "mus_0123456789abcdefghijklmnop",
          name: "Lo-fi",
          length_ms: 95_000,
        },
      },
    }
    expect(fieldsOf(fellBack).music).toEqual({ track: "none", gain_db: 0 })
  })

  test("the look sample is asked without the music, which does not change the picture", () => {
    const take = "tk_0123456789abcdef"
    const music = { track: "mus_0123456789abcdefghijklmnop", gain_db: 0 }

    expect(sampleOf({ take, look: { music } })).toEqual({ take })
    expect(sampleOf({ take, rev: 1, look: { music, background: { preset: "ocean" } } })).toEqual({
      take,
      rev: 1,
      look: { background: { preset: "ocean" } },
    })
  })
})

describe("the sections the owner edited (#1082)", () => {
  const lofi = "mus_0123456789abcdefghijklmnop"
  const withLofi: VideoLook = {
    ...saved,
    music: { track: lofi, name: "Lo-fi", length_ms: 95_000, gain_db: 0 },
  }

  test("a section left alone follows a start read again: its deleted track is not sent", () => {
    const before = fieldsOf(withLofi)
    const edits = editsOf({}, before, { ...before, background: { preset: "ocean" } })
    // The default's track was deleted meanwhile: the default read again has no music.
    const after = fieldsOf(saved)

    expect(withEdits(after, edits).music).toEqual({ track: "none", gain_db: 0 })
    expect(sentLook(after, withEdits(after, edits))).toEqual({ background: { preset: "ocean" } })
  })

  test("a section the owner edited stays as edited, set back too", () => {
    const start = fieldsOf(withLofi)
    const ocean = editsOf({}, start, { ...start, background: { preset: "ocean" } })
    const back = editsOf(ocean, withEdits(start, ocean), start)

    expect(back).toEqual({ background: { preset: "midnight" } })
    expect(sentLook(start, withEdits(start, back))).toBeUndefined()
  })

  test("an edit of one section keeps the others edited before", () => {
    const start = fieldsOf(withLofi)
    const music = editsOf({}, start, { ...start, music: { track: "none", gain_db: 0 } })
    const now = withEdits(start, music)
    const both = editsOf(music, now, { ...now, frame: { ...now.frame, shadow: false } })

    expect(both).toEqual({
      music: { track: "none", gain_db: 0 },
      frame: { chrome: "android", shadow: false, safe_top: false },
    })
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

describe("the cards a look sends (#1230, row 1)", () => {
  const take = "tk_0123456789abcdef"

  test("a look without cards starts from none, in aidemo's accent", () => {
    expect(fieldsOf(saved).cards).toEqual({ accent: "#6c8cff" })
  })

  test("a card turned on is sent with its words trimmed and its empty ones left out", () => {
    const start = fieldsOf(saved)
    const now = {
      ...start,
      cards: {
        ...start.cards,
        end: { ...newCard("end"), title: " Tải SecondBite ", cta_url: "secondbite.vn" },
      },
    }

    expect(sentLook(start, now)).toEqual({
      cards: {
        accent: "#6c8cff",
        end: { title: "Tải SecondBite", cta_url: "secondbite.vn", duration_ms: 3000 },
      },
    })
  })

  test("a revision's cards are kept unless changed; turned off, the section goes with none", () => {
    const withCards: VideoLook = {
      ...saved,
      cards: { accent: "#ff3366", title: { title: "SecondBite", duration_ms: 2600 } },
    }
    const start = fieldsOf(withCards)

    expect(start.cards.title).toEqual({
      title: "SecondBite",
      subtitle: "",
      cta_url: "",
      duration_ms: 2600,
    })
    expect(sentLook(start, start)).toBeUndefined()
    expect(sentLook(start, { ...start, cards: { accent: "#ff3366" } })).toEqual({
      cards: { accent: "#ff3366" },
    })
  })

  test("the sample of the picture is asked without the cards; the sample of a card with them", () => {
    const cards = { title: { title: "SecondBite" } }

    expect(sampleOf({ take, look: { cards } })).toEqual({ take })
    expect(sampleOf({ take, card: "title", look: { cards } })).toEqual({
      take,
      card: "title",
      look: { cards },
    })
    expect(sampleCards({ accent: "#6c8cff", end: newCard("end") })).toEqual(["end"])
  })
})
