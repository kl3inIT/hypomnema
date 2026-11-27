import type { VideoAspect, VideoTemplate, VideoTemplateCard } from "@/api/generated/types.gen"

import { cardDefaults, fieldsOf, type CardFields, type LookFields } from "./look-state"

// What the template picker of Make video works out (issue #1230, rows 3-5; the picker is an inference, as no
// reference has one): the fields a template's look starts from, its cards' words with {title} the video's title as
// the server fills them (videos/templates.go), the shape and captions None gives back, and the words that describe
// it.

// The longest a card's title and subtitle are, the server's (videos/compose/cards.go).
const maxCardTitle = 80
const maxCardSubtitle = 120

// titleToken in a template card's words is the video's title.
const titleToken = "{title}"

// cutWords is a card's words as the server cuts them: spaces joined, at most `most` characters, cut at a space in
// their second half when they are longer.
export function cutWords(words: string, most: number): string {
  const joined = words.trim().split(/\s+/).join(" ")
  const chars = Array.from(joined)
  if (chars.length <= most) return joined

  let cut = chars.slice(0, most)
  for (let i = cut.length - 1; i > Math.floor(cut.length / 2); i--) {
    if (cut[i] === " ") {
      cut = cut.slice(0, i)
      break
    }
  }

  return cut.join("").trim()
}

// fillTitle is a template card's words with {title} the video's title, cut as the server cuts them.
const fillTitle = (words: string, title: string, most: number) =>
  cutWords(words.replaceAll(titleToken, title), most)

// templateFields are the fields a make with the template starts from: its look, resolved over the saved default by
// the server, and its cards with their words, {title} the video's title.
export function templateFields(template: VideoTemplate, title: string): LookFields {
  const card = (c: VideoTemplateCard | undefined): CardFields | undefined =>
    c && {
      title: fillTitle(c.title, title, maxCardTitle),
      subtitle: fillTitle(c.subtitle ?? "", title, maxCardSubtitle),
      cta_url: "",
      duration_ms: c.duration_ms,
    }
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

// Shape is a make's shape and captions.
export type Shape = { aspect: VideoAspect; captions: boolean }

// unpicked is the shape after the template is taken away (None): what it set and the owner left gives back what the
// owner had before the template; what the owner changed while it was chosen stays his.
export function unpicked(now: Shape, template: VideoTemplate, before: Shape): Shape {
  return {
    aspect: now.aspect === template.aspects[0] ? before.aspect : now.aspect,
    captions: now.captions === template.captions ? before.captions : now.captions,
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
