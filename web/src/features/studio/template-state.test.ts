import { describe, expect, test } from "vitest"

import type { VideoTemplate } from "@/api/generated/types.gen"

import { cardsText, cutWords, lengthText, templateFields, unpicked } from "./template-state"

// app-ad as GET /videos/templates answers it: its look the saved default, an end card of 3 s.
const appAd: VideoTemplate = {
  id: "app-ad",
  name: "App ad",
  use_case: "A short ad.",
  length: { min_s: 15, max_s: 30 },
  aspects: ["9:16"],
  captions: true,
  sections: [{ name: "Hook", purpose: "The outcome.", words: 10, seconds: 4 }],
  words: 10,
  look: {
    captions: { preset: "clean", position: "preset" },
    frame: { chrome: "android", padding: 48, radius: 40, shadow: true, safe_top: false },
    background: { preset: "midnight" },
  },
  cards: {
    accent: "#6c8cff",
    end: { title: "Tải ứng dụng ngay", subtitle: "{title}", duration_ms: 3000 },
  },
}

describe("a template's fields", () => {
  test("its look, and its cards' words with {title} the video's title", () => {
    const fields = templateFields(appAd, "  Đăng   nhập ")

    expect(fields.frame).toEqual({ chrome: "android", shadow: true, safe_top: false })
    expect(fields.cards).toEqual({
      accent: "#6c8cff",
      end: { title: "Tải ứng dụng ngay", subtitle: "Đăng nhập", cta_url: "", duration_ms: 3000 },
    })
  })

  test("no card when it has none", () => {
    const { cards: _cards, ...none } = appAd

    expect(templateFields(none, "A").cards).toEqual({ accent: "#6c8cff" })
    expect(cardsText(none)).toBe("no cards")
  })

  test("a long title cut as the server cuts it: 80 characters, at a space in their second half", () => {
    const long =
      "Một hai ba bốn năm sáu bảy tám chín mười một hai ba bốn năm sáu bảy tám chín mười một hai"
    const cut = cutWords(long, 80)

    expect(Array.from(cut).length).toBeLessThanOrEqual(80)
    expect(long.startsWith(cut)).toBe(true)
    expect(long[cut.length]).toBe(" ")
  })

  test("None gives back the shape the owner had where he left the template's", () => {
    const before = { aspect: "16:9" as const, captions: true }

    expect(unpicked({ aspect: "9:16", captions: true }, appAd, before)).toEqual(before)
    expect(
      unpicked(
        { aspect: "886x1920", captions: false },
        { ...appAd, aspects: ["886x1920"], captions: false },
        before,
      ),
    ).toEqual(before)
    // Captions the owner turned off while app-ad was chosen stay off.
    expect(unpicked({ aspect: "9:16", captions: false }, appAd, before)).toEqual({
      aspect: "16:9",
      captions: false,
    })
  })

  test("its length and cards in words", () => {
    expect(lengthText(appAd)).toBe("15-30 s")
    expect(lengthText({ ...appAd, length: { min_s: 30, max_s: 30 } })).toBe("30 s")
    expect(cardsText(appAd)).toBe("an end card")
    expect(
      cardsText({
        ...appAd,
        cards: { accent: "#000000", title: { title: "{title}", duration_ms: 2600 } },
      }),
    ).toBe("a title card")
  })
})
