import { MAX_TEXT_PREVIEW_BYTES, type PreviewKind, type Sheets } from "./preview-kind"

// Copied from MemoryOS (web/src/features/preview/preview-content.ts). Changed here: a PDF is always its bytes (an
// Artifact is at most 5 MB, a folder's file is read whole up to 10 MiB, so nothing is read by range), and an SVG
// is shown from the sandboxed address the server gives it, or as its text in a locked frame.

type TextContent = {
  text: string
  truncated: boolean
  // The whole file's size, which the text itself does not tell once it is cut.
  bytes: number
}

// A file read far enough for its view: a workbook arrives already parsed a sheet; text is read up to the preview
// ceiling.
export type PreviewContent =
  | { kind: "pdf"; file: Blob }
  // truncated says the server left out sheets past its limit.
  | { kind: "xlsx"; sheets: Sheets; truncated: boolean }
  | ({ kind: "csv" | "markdown" } & TextContent)
  | ({ kind: "code" | "text" } & TextContent)
  | { kind: "image" | "docx"; blob: Blob }
  // An SVG at an address the server sends with a sandboxing policy, or its markup for a locked frame: never a
  // blob: address of this origin.
  | { kind: "svg"; url: string }
  | { kind: "svg"; markup: string }
  | { kind: "doc" | "pptx" | "unsupported" }

// readPreviewContent is what a downloaded file gives its view. A workbook is not among them: its view asks the
// server for its sheets instead. An image's bytes are typed exactly, so its blob: address is never sniffed as
// another kind.
export async function readPreviewContent(
  blob: Blob,
  kind: Exclude<PreviewKind, "xlsx">,
  mediaType: string,
): Promise<PreviewContent> {
  switch (kind) {
    case "pdf":
      return { kind, file: blob }
    case "image":
      return { kind, blob: new Blob([blob], { type: mediaType }) }
    case "docx":
      return { kind, blob }
    case "svg":
      return { kind, markup: await blob.text() }
    case "csv":
    case "markdown":
    case "code":
    case "text":
      return {
        kind,
        text: await blob.slice(0, MAX_TEXT_PREVIEW_BYTES).text(),
        truncated: blob.size > MAX_TEXT_PREVIEW_BYTES,
        bytes: blob.size,
      }
    default:
      return { kind }
  }
}
