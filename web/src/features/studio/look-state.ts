import type {
  VideoBackground,
  VideoCaptionPosition,
  VideoCard,
  VideoCardInput,
  VideoChrome,
  VideoLook,
  VideoLookInput,
  VideoPreview,
} from "@/api/generated/types.gen"

// What the Look section works out (issue #908, row 16; #938): the fields it starts from, a look's default or a
// revision's, and the sections it sends. A section is sent only when the owner changed it, to a make, a look change
// and a look sample alike, so a section left alone stays the default's or the source's on the server (row 2): an
// untouched frame stays a default frame, which falls back to full on a take whose phone turned, where a frame sent
// is refused (row 11). Changed is "differs from where it started", as TanStack Form, the web's forms, compares a
// field with its default value (isDefaultValue, @tanstack/form-core 1.33.5 src/FormApi.ts:1236-1241): a field set
// back is not changed.

// LookFields are the fields the web shows. Padding and radius are only in the API and the CLI (row 16): a frame
// sent from here takes their reference defaults, as each field a sent section leaves out does. The music is a
// track's id or none, with its gain (#910, row 15): sent, it carries both, since a section sent without its track is
// refused. The cards are a title card and an end card, each absent for none, in an accent (#1230, row 1): a card's
// subtitle and URL left empty are not sent.
export type LookFields = {
  captions: { preset: string; position: VideoCaptionPosition }
  frame: { chrome: VideoChrome; shadow: boolean; safe_top: boolean }
  background: VideoBackground
  music: { track: string; gain_db: number }
  cards: CardsFields
}

export type CardFields = { title: string; subtitle: string; cta_url: string; duration_ms: number }

export type CardsFields = { accent: string; title?: CardFields; end?: CardFields }

// The cards' defaults, the server's (videos/compose/cards.go): aidemo's accent and 2.6 s title card,
// agent-demo-video's 3.0 s end card; a card lasts 0.5 to 10 s.
export const cardDefaults = {
  accent: "#6c8cff",
  title: 2600,
  end: 3000,
  minMs: 500,
  maxMs: 10_000,
} as const

const cardFields = (c: VideoCard | undefined): CardFields | undefined =>
  c && {
    title: c.title,
    subtitle: c.subtitle ?? "",
    cta_url: c.cta_url ?? "",
    duration_ms: c.duration_ms,
  }

// newCard is a card turned on: no words yet, its default length.
export const newCard = (side: "title" | "end"): CardFields => ({
  title: "",
  subtitle: "",
  cta_url: "",
  duration_ms: cardDefaults[side],
})

const cardInput = (c: CardFields | undefined): VideoCardInput | undefined =>
  c && {
    title: c.title.trim(),
    duration_ms: c.duration_ms,
    ...(c.subtitle.trim() ? { subtitle: c.subtitle.trim() } : {}),
    ...(c.cta_url.trim() ? { cta_url: c.cta_url.trim() } : {}),
  }

// fieldsOf are a stored look's fields. A look made before the looks has no background, which the server reads as
// midnight (videos/look.go, resolveLook).
export function fieldsOf(look: VideoLook): LookFields {
  return {
    captions: { preset: look.captions.preset, position: look.captions.position },
    frame: {
      chrome: look.frame.chrome,
      shadow: look.frame.shadow,
      safe_top: look.frame.safe_top,
    },
    background: look.background?.colors
      ? { colors: [look.background.colors[0], look.background.colors[1]] }
      : { preset: look.background?.preset ?? "midnight" },
    // A look without music has none (absent); one whose track was deleted before it was made has none too, with
    // fell_back_from (videos/music.go, fitMusic).
    music: { track: look.music?.track ?? "none", gain_db: look.music?.gain_db ?? 0 },
    // A look without cards has none, in the default accent.
    cards: {
      accent: look.cards?.accent ?? cardDefaults.accent,
      ...(look.cards?.title ? { title: cardFields(look.cards.title) } : {}),
      ...(look.cards?.end ? { end: cardFields(look.cards.end) } : {}),
    },
  }
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

const sections = ["captions", "frame", "background", "music", "cards"] as const

// LookEdits are the sections the owner edited, each whole, kept apart from the fields they started from (#1082, from
// the review of #1078): where the start can be read again while the dialog is open, as Make video's saved default,
// a section left alone follows the new start rather than staying the old one. Its music would otherwise stay a
// track deleted meanwhile, which a make sent with any other section changed would carry, and the server refuse.
export type LookEdits = Partial<LookFields>

// withEdits are the fields shown: the start's, with each section the owner edited in its place.
export const withEdits = (start: LookFields, edits: LookEdits): LookFields => ({
  ...start,
  ...edits,
})

// editsOf are the edits after the owner changed the fields shown, now, to next: each section that differs joins them.
export function editsOf(edits: LookEdits, now: LookFields, next: LookFields): LookEdits {
  const out = { ...edits }
  for (const k of sections) if (!same(now[k], next[k])) Object.assign(out, { [k]: next[k] })

  return out
}

// sentLook is the sections changed from the start, or undefined when none was: a request then carries no look.
export function sentLook(start: LookFields, now: LookFields): VideoLookInput | undefined {
  const out: VideoLookInput = {}
  if (!same(start.captions, now.captions)) out.captions = { ...now.captions }
  if (!same(start.frame, now.frame)) out.frame = { ...now.frame }
  if (!same(start.background, now.background)) out.background = { ...now.background }
  if (!same(start.music, now.music)) out.music = { ...now.music }
  if (!same(start.cards, now.cards)) {
    const title = cardInput(now.cards.title)
    const end = cardInput(now.cards.end)
    out.cards = { accent: now.cards.accent, ...(title ? { title } : {}), ...(end ? { end } : {}) }
  }

  return Object.keys(out).length > 0 ? out : undefined
}

export const chromeNames: Record<VideoChrome, string> = {
  full: "Full",
  none: "Plain",
  android: "Android",
  iphone: "iPhone",
}

export const chromeHints: Record<VideoChrome, string> = {
  full: "The phone's picture fills the video, with no frame or background.",
  none: "The picture with rounded corners on the background.",
  android: "The picture in an Android phone on the background.",
  iphone: "The picture in an iPhone on the background.",
}

export const positionNames: Record<VideoCaptionPosition, string> = {
  preset: "As the style places it",
  bottom: "Bottom",
  lower_third: "Lower third",
  center: "Middle",
  top: "Top",
}

export const positions = Object.keys(positionNames) as VideoCaptionPosition[]

// backgroundName names one of autodemo's templates (src/mcp.js:293 lists them by id).
export const backgroundName = (id: string) =>
  id === "macos"
    ? "macOS"
    : id === "macos-light"
      ? "macOS light"
      : id[0]!.toUpperCase() + id.slice(1)

// sampleOf is a look sample's request without the music, and without the cards unless it draws one: the sample of
// the take's picture is a still, which neither changes and the server does not read (videos/preview.go), so
// choosing a track or typing a card's words asks no new sample of the picture.
export function sampleOf(body: VideoPreview): VideoPreview {
  const drop = body.look && (body.look.music || body.look.cards)
  if (!drop || !body.look) return body

  const { music: _music, cards, ...rest } = body.look
  // A card's sample carries that card alone: the other one may be on and not titled yet (the review of #1242).
  const drawn = body.card && cards?.[body.card]
  const look =
    body.card && cards
      ? {
          ...rest,
          cards: {
            ...(cards.accent ? { accent: cards.accent } : {}),
            ...(drawn ? { [body.card]: drawn } : {}),
          },
        }
      : rest
  const { look: _look, ...out } = body

  return Object.keys(look).length > 0 ? { ...out, look } : out
}

// sampleCards are the cards a sample may draw: the look's that are on.
export const sampleCards = (cards: CardsFields) =>
  (["title", "end"] as const).filter((side) => cards[side] !== undefined)
