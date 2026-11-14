import { describe, expect, test } from "vitest"

import { folderCSP, frameDocument, pageCSP, stripPreamble } from "./frame-document"

const policyOf = (doc: string) =>
  /^<!DOCTYPE html><meta http-equiv="Content-Security-Policy" content="([^"]*)">/
    .exec(doc)?.[1]
    ?.replace(/&#39;|&apos;/g, "'")

describe("the document of a locked frame", () => {
  // Scenario: whatever a page starts with (a BOM, comments, its own doctype, an XML prologue), nothing of it comes
  // before the policy, so the policy rules every byte of it (OpenMausBot's document.ts).
  test("the policy comes before any byte of the page", () => {
    const page =
      '﻿  <!-- made by a bot --><?xml version="1.0"?><!DOCTYPE html><html><body>Sales</body></html>'
    const doc = frameDocument(page)

    expect(
      doc.startsWith('<!DOCTYPE html><meta http-equiv="Content-Security-Policy" content="'),
    ).toBe(true)
    expect(doc.endsWith("<html><body>Sales</body></html>")).toBe(true)
    expect(policyOf(doc)).toBe(pageCSP)
  })

  // Scenario: a comment the page never closes swallows the rest, as a browser's parser would, rather than letting
  // what follows it run before the policy.
  test("an unclosed comment takes the rest", () => {
    expect(stripPreamble("<!-- never closed <script>steal()</script>")).toBe("")
  })

  // Scenario: a page may load scripts, styles and fonts from the three CDNs and Google Fonts and nothing else; it
  // connects nowhere else, frames nothing, posts no form and sets no base of its own.
  test("a page reaches only the CDNs", () => {
    expect(pageCSP).toContain("default-src 'none'")
    expect(pageCSP).toContain(
      "script-src 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com",
    )
    expect(pageCSP).toContain(
      "connect-src https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com;",
    )
    expect(pageCSP).toContain("img-src data: blob: https://cdnjs")
    for (const closed of [
      "frame-src 'none'",
      "form-action 'none'",
      "base-uri 'none'",
      "object-src 'none'",
    ]) {
      expect(pageCSP).toContain(closed)
    }
  })

  // Scenario: a folder page's data loads from its frame's own address, which is also its base; only that address
  // of this origin is added, never the origin itself, so the page cannot call the API.
  test("a folder page adds its own address only", () => {
    const files = "https://hyp.example/api/v1/artifact-files/tok/"
    const doc = frameDocument("<script src=app.js></script>", files)
    const csp = folderCSP(files)

    expect(policyOf(doc)).toBe(csp)
    expect(doc).toContain(`<base href="${files}">`)
    expect(csp).toContain(
      `connect-src https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com ${files}`,
    )
    expect(csp).toContain(`base-uri ${files}`)
    expect(csp).not.toMatch(/https:\/\/hyp\.example(?!\/api\/v1\/artifact-files\/tok\/)/)
  })
})
