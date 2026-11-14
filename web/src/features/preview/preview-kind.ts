import DOMPurify from "dompurify"

import type { Sheet } from "@/api/generated/types.gen"

// Which view a file opens in, and the limits of each (issue #422). Copied from MemoryOS
// (web/src/features/preview/preview-kind.ts, the owner's own code), after Onyx's PreviewModal variants: the
// first kind that matches, in Onyx's order; anything else downloads. Changed here: an SVG is a kind of its own,
// shown where its script cannot run (image-view.tsx), and a raster image is any type a browser draws that the
// server sends (artifactpack.imageType), not only PNG, JPEG, WebP and GIF.

export type PreviewKind =
  | "code"
  | "image"
  | "svg"
  | "pdf"
  | "csv"
  | "xlsx"
  | "markdown"
  | "docx"
  | "doc"
  | "text"
  | "pptx"
  | "unsupported"

// Text-like previews read at most this many bytes.
export const MAX_TEXT_PREVIEW_BYTES = 1024 * 1024

// A workbook as the server reads it: CSV text a sheet (internal/controlplane/spreadsheet), since the app ships no
// workbook parser.
export type Sheets = Sheet[]

// Rows past this count are not drawn; the download holds the whole file.
export const MAX_TABLE_ROWS = 1000

// Code and data files by extension, as Onyx's getCodeLanguage/getDataLanguage.
const CODE_LANGUAGES: Record<string, string> = {
  py: "python",
  ipynb: "json",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  ts: "typescript",
  tsx: "tsx",
  java: "java",
  kt: "kotlin",
  go: "go",
  rs: "rust",
  rb: "ruby",
  php: "php",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  hpp: "cpp",
  cs: "csharp",
  swift: "swift",
  scala: "scala",
  r: "r",
  sql: "sql",
  sh: "bash",
  bash: "bash",
  ps1: "powershell",
  html: "html",
  css: "css",
  scss: "scss",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "toml",
  xml: "xml",
  ini: "ini",
  dockerfile: "dockerfile",
}

// The images whose bytes become a blob: address of their exact type. A blob: address is of this origin, so a type
// that can run script there (SVG, HTML) never gets one: opened on its own, as "Open image in new tab" does, it
// would be a page of the app.
const RASTER_TYPES = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/avif",
  "image/bmp",
  "image/x-icon",
])

export const isRaster = (type: string) => RASTER_TYPES.has(type)

function extension(filename: string): string {
  const name = filename.slice(filename.lastIndexOf("/") + 1)
  const dot = name.lastIndexOf(".")

  return dot < 0 ? "" : name.slice(dot + 1).toLowerCase()
}

const XLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
const DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
const PPTX = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

export function previewKind(filename: string, mediaType: string): PreviewKind {
  const ext = extension(filename)
  const [essence = ""] = mediaType.split(";")
  const type = essence.trim().toLowerCase()
  if (CODE_LANGUAGES[ext]) return "code"
  if (isRaster(type)) return "image"
  if (type === "image/svg+xml" || ext === "svg") return "svg"
  if (type === "application/pdf") return "pdf"
  if (type === "text/csv" || ext === "csv") return "csv"
  if (type === XLSX || ext === "xlsx") return "xlsx"
  if (type === "text/markdown" || ext === "md" || ext === "markdown") return "markdown"
  if (ext === "doc" || type === "application/msword") return "doc"
  if (type === DOCX || ext === "docx") return "docx"
  if (type.startsWith("text/") || ["txt", "log", "conf", "tsv"].includes(ext)) return "text"
  if (type === PPTX || ext === "pptx") return "pptx"

  return "unsupported"
}

// mediaTypeOf is a file's type by its name, for a file whose bytes the server sends untyped (a chat's folder).
const BY_EXTENSION: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  bmp: "image/bmp",
  ico: "image/x-icon",
  svg: "image/svg+xml",
  pdf: "application/pdf",
  csv: "text/csv",
  xlsx: XLSX,
  docx: DOCX,
  pptx: PPTX,
  doc: "application/msword",
}

export const mediaTypeOf = (filename: string) =>
  BY_EXTENSION[extension(filename)] ?? "application/octet-stream"

// RFC 4180 rows: quoted fields may hold commas, doubled quotes and line breaks. Onyx splits on commas, which breaks
// the quoted cells the xlsx preview writes.
export function parseCsv(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let field = ""
  let quoted = false
  for (let i = 0; i < text.length; i++) {
    const char = text.charAt(i)
    if (quoted) {
      if (char !== '"') field += char
      else if (text[i + 1] === '"') {
        field += '"'
        i++
      } else quoted = false
    } else if (char === '"') quoted = true
    else if (char === ",") {
      row.push(field)
      field = ""
    } else if (char === "\n" || char === "\r") {
      if (char === "\r" && text[i + 1] === "\n") i++
      row.push(field)
      rows.push(row)
      row = []
      field = ""
    } else field += char
  }
  if (field || row.length) {
    row.push(field)
    rows.push(row)
  }

  return rows
}

// Onyx's sanitizeDocxHtml: docx-preview writes document-controlled HTML and hrefs, so its output is sanitized
// again. Only http(s) and mailto links survive; base64 images stay because DOMPurify allows data: in img. The
// rendered tree is sanitized in place rather than serialized and parsed again, so the styles docx-preview set
// through CSSOM stay.
export function sanitizeDocxInPlace(root: HTMLElement) {
  DOMPurify.sanitize(root, { ALLOWED_URI_REGEXP: /^(?:https?|mailto):/i, IN_PLACE: true })
}

// kindLabel is what each kind is called, so an Artifact's card names a file as its viewer will show it (issue #422
// review; the words are this app's own: MemoryOS and Onyx name a file by its extension).
export const kindLabel: Record<PreviewKind, string> = {
  code: "Code",
  image: "Image",
  svg: "SVG",
  pdf: "PDF",
  csv: "CSV",
  xlsx: "Spreadsheet",
  markdown: "Markdown",
  docx: "Word",
  doc: "Word",
  text: "Text",
  pptx: "Slides",
  unsupported: "File",
}

// fitZoom is the zoom that fits a page `page` pixels wide into `room` pixels, never above its own size: a Word
// page is zoomed down to the panel's width (docx-view.tsx; our own, as MemoryOS and Onyx scroll sideways).
export function fitZoom(room: number, page: number) {
  return page > 0 && room > 0 ? Math.min(1, room / page) : 1
}

// scrubDocxCss takes out of docx-preview's styles what would reach past the document: an @import, and a url() that
// is not data: (the fonts and pictures it embeds are data: with useBase64URL). This app's own rule, from the #422
// security review; MemoryOS and Onyx keep the styles as they come.
export function scrubDocxCss(css: string): string {
  return css
    .replace(/@import[^;]*;?/gi, "")
    .replace(/url\(\s*(["']?)([^)]*?)\1\s*\)/gi, (whole: string, _quote: string, address: string) =>
      /^data:/i.test(address.trim()) ? whole : "none",
    )
}
