import type { VideoAspect, VideoTemplate } from "@/api/generated/types.gen"

import { cardDefaults, fieldsOf, type CardFields, type LookFields } from "./look-state"

// What the template picker of Make video works out (issue #1230, rows 3-5; the picker is an inference, as no
// reference has one): the fields a template's look starts from, its cards titled with the video's title as the
// server titles them (videos/templates.go), and the words that describe it.

// maxCardTitle is a card's longest title, the server's (videos/compose/cards.go).
const maxCardTitle = 80

// cardTitle is a video's title as a template's card holds it, as the server cuts it: its spaces joined, at most 80
// characters, cut at a space in its second half when it is longer.
export function cardTitle(title: string): string {
  const joined = title.trim().split(/\s+/).join(" ")
  const chars = Array.from(joined)
  if (chars.length <= maxCardTitle) return joined

  let cut = chars.slice(0, maxCardTitle)
  for (let i = cut.length - 1; i > Math.floor(cut.length / 2); i--) {
    if (cut[i] === " ") {
      cut = cut.slice(0, i)
      break
    }
  }

  return cut.join("").trim()
}

// templateFields are the fields a make with the template starts from: its look, resolved over the saved default by
// the server, and its cards, each titled with the video's title.
export function templateFields(template: VideoTemplate, title: string): LookFields {
  const card = (c: { duration_ms: number } | undefined): CardFields | undefined =>
    c && { title: cardTitle(title), subtitle: "", cta_url: "", duration_ms: c.duration_ms }
  const title_ = card(template.cards?.title)
  const end = card(template.cards?.end)

  return {
    ...fieldsOf(template.look),
    cards: {
      accent: template.cards?.accent ?? cardDefaults.accent,
      ...(title_ ? { title: title_ } : {}),
      ...(end ? { end } : {}),
    },
  }
}

// lengthText is a template's length: "15-30 s", or "30 s" when it is one.
export const lengthText = ({ length }: VideoTemplate) =>
  length.min_s === length.max_s ? `${length.min_s} s` : `${length.min_s}-${length.max_s} s`

// cardsText names a template's cards.
export function cardsText({ cards }: VideoTemplate): string {
  if (cards?.title && cards.end) return "a title card and an end card"
  if (cards?.title) return "a title card"
  if (cards?.end) return "an end card"

  return "no cards"
}

// aspectNames and aspectHints say each shape.
export const aspectNames: Record<VideoAspect, string> = {
  "9:16": "9:16",
  "16:9": "16:9",
  "886x1920": "App Store",
}

// aspectLabels name each shape's button.
export const aspectLabels: Record<VideoAspect, string> = {
  "9:16": "9:16, upright",
  "16:9": "16:9, wide",
  "886x1920": "App Store, 886 × 1920",
}

export const aspectHints: Record<VideoAspect, string> = {
  "9:16": "Upright, the phone's own shape: 720 × 1280.",
  "16:9": "Wide, the phone in the middle on black: 1280 × 720.",
  "886x1920": "Upright at the App Store's size for an iPhone's app preview: 886 × 1920.",
}

export const aspects = Object.keys(aspectNames) as VideoAspect[]
