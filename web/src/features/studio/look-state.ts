import type {
  VideoBackground,
  VideoCaptionPosition,
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
// refused.
export type LookFields = {
  captions: { preset: string; position: VideoCaptionPosition }
  frame: { chrome: VideoChrome; shadow: boolean; safe_top: boolean }
  background: VideoBackground
  music: { track: string; gain_db: number }
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
  }
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

// sentLook is the sections changed from the start, or undefined when none was: a request then carries no look.
export function sentLook(start: LookFields, now: LookFields): VideoLookInput | undefined {
  const out: VideoLookInput = {}
  if (!same(start.captions, now.captions)) out.captions = { ...now.captions }
  if (!same(start.frame, now.frame)) out.frame = { ...now.frame }
  if (!same(start.background, now.background)) out.background = { ...now.background }
  if (!same(start.music, now.music)) out.music = { ...now.music }

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

// sampleOf is a look sample's request without the music: the sample is a still picture, which the music does not
// change and the server does not read (videos/preview.go), so choosing a track asks no new sample.
export function sampleOf(body: VideoPreview): VideoPreview {
  if (!body.look?.music) return body

  const { music: _music, ...look } = body.look
  const { look: _look, ...rest } = body

  return Object.keys(look).length > 0 ? { ...rest, look } : rest
}
