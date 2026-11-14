// The document an HTML artifact is shown as in its locked frame (issue #402), adapted from OpenMausBot
// (Apache-2.0, Copyright 2026 Milind Soni and OpenMausBot contributors; https://github.com/milind-soni/OpenMausBot):
// shared/app-frame/csp.ts (BOT_HTML_CSP, folderPageCsp) and shared/app-frame/document.ts (stripDocumentPreamble,
// buildSrcdoc), without its bot helper script and theme, which call back into the chat (not in this issue).
//
// No byte of the page precedes the CSP meta: a leading BOM, whitespace, comments, bogus comments (`<?xml …?>`,
// `<![CDATA[…]>`) and the page's own doctype are stripped first, and the rest follows the meta, which already sits
// in the head the parser opened for it. The frame itself is sandboxed without allow-same-origin
// (artifact-frame.tsx), so the page has an opaque origin: no cookies, no storage, no call with the owner's session.

// Scripts, styles and fonts load from three package CDNs and Google Fonts only; images from data:, blob: and the
// CDNs, so no tracking pixel reaches another host.
export const pageCDNs = [
  "https://cdnjs.cloudflare.com",
  "https://cdn.jsdelivr.net",
  "https://unpkg.com",
] as const
const fontStyles = "https://fonts.googleapis.com"
const fontFiles = "https://fonts.gstatic.com"

export const pageCSP = [
  "default-src 'none'",
  `script-src 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' ${pageCDNs.join(" ")}`,
  `style-src 'unsafe-inline' ${pageCDNs.join(" ")} ${fontStyles}`,
  `font-src data: ${pageCDNs.join(" ")} ${fontFiles}`,
  `img-src data: blob: ${pageCDNs.join(" ")}`,
  "media-src data: blob:",
  `connect-src ${pageCDNs.join(" ")}`,
  "worker-src blob:",
  "frame-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "manifest-src 'none'",
].join("; ")

// DuckDB-WASM fetches its readers, Parquet's among them, from here when a query first needs one.
const duckdbExtensions = "https://extensions.duckdb.org"

// folderCSP is a folder page's policy: the page's, plus its own files under `files` (the frame's address, an
// absolute URL ending in "/", which is also its base) and DuckDB's readers. The path keeps the page off the rest of
// the app's origin, its API included.
export function folderCSP(files: string): string {
  const add = (directive: string, extra: string) =>
    directive.replace(/^([a-z-]+) (.*)$/, `$1 $2 ${extra}`)

  return pageCSP
    .split("; ")
    .map((directive) => {
      if (/^(script|style|font|img|media)-src /.test(directive)) return add(directive, files)
      if (directive.startsWith("connect-src "))
        return add(directive, `${files} ${duckdbExtensions}`)
      if (directive === "base-uri 'none'") return `base-uri ${files}`
      return directive
    })
    .join("; ")
}

const metaPrefix = '<!DOCTYPE html><meta http-equiv="Content-Security-Policy" content="'

function escapeAttribute(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
}

// stripPreamble takes away what may come before a page's first element. An unterminated comment swallows the
// rest, as a browser's parser would.
export function stripPreamble(html: string): string {
  let rest = html
  for (;;) {
    const before = rest
    rest = rest.replace(/^[﻿\s]+/, "")
    if (rest.startsWith("<!--")) {
      const end = rest.indexOf("-->", 4)
      rest = end === -1 ? "" : rest.slice(end + 3)
    } else if (/^<[!?]/.test(rest)) {
      const end = rest.indexOf(">")
      rest = end === -1 ? "" : rest.slice(end + 1)
    }
    if (rest === before) return rest
  }
}

// frameDocument is the srcdoc of a page: the policy first, then for a folder page the base its relative addresses
// resolve against, then the page.
export function frameDocument(html: string, files?: string): string {
  const csp = files ? folderCSP(files) : pageCSP
  const base = files ? `<base href="${escapeAttribute(files)}">` : ""

  return `${metaPrefix}${escapeAttribute(csp)}">${base}${stripPreamble(html)}`
}

// frameSandbox is what the frame may do: run its scripts and its forms, never be of this origin, open popups or
// move the top window (OpenMausBot's APP_FRAME_SANDBOX, src/components/app/app-frame-host.ts:6).
export const frameSandbox = "allow-scripts allow-forms"
