import { useEffect, useRef, useState } from "react"

import { Spinner } from "@/components/ui/spinner"

import { sanitizeDocxInPlace, scrubDocxCss } from "./preview-kind"

// The page a document is drawn on, inside its shadow root, where the app's classes do not reach: white in both
// themes, because a document is authored on white, and centred with a shadow.
const PAGE_CSS = `:host{display:block;color:black;color-scheme:light}
section.docx{margin:0 auto 24px;background:white;box-shadow:0 4px 6px -1px rgb(0 0 0/.1),0 2px 4px -2px rgb(0 0 0/.1)}`

// DocxView is a Word document drawn by docx-preview, sanitized before it reaches the page (Onyx's
// sanitizeDocxHtml). Copied from MemoryOS (web/src/features/preview/docx-view.tsx), without the words it reports
// for citations. Changed here, after the #422 security review: MemoryOS adopts docx-preview's <style> sheets on the
// whole document, so a document's CSS would restyle the app around it. Here the document is drawn inside a shadow
// root and its sheets are adopted on that root only, as constructed stylesheets (which a policy of style-src
// 'self' also allows), with any @import and any url() that is not data: taken out first.
export function DocxView({ blob }: { blob: Blob }) {
  const host = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<"rendering" | "done" | "failed">("rendering")
  useEffect(() => {
    if (!host.current) return undefined
    let current = true
    const root = host.current.shadowRoot ?? host.current.attachShadow({ mode: "open" })
    void (async () => {
      try {
        const { renderAsync } = await import("docx-preview")
        // Drawn detached, then only sanitized markup and the library's scrubbed styles are attached.
        const renderedBody = document.createElement("div")
        const renderedStyles = document.createElement("div")
        await renderAsync(blob, renderedBody, renderedStyles, {
          className: "docx",
          inWrapper: false,
          ignoreWidth: false,
          ignoreHeight: false,
          ignoreFonts: false,
          breakPages: true,
          useBase64URL: true,
          renderHeaders: true,
          renderFooters: true,
          renderFootnotes: true,
          renderEndnotes: true,
        })
        if (!current) return
        sanitizeDocxInPlace(renderedBody)
        const sheets = [
          PAGE_CSS,
          ...Array.from(renderedStyles.querySelectorAll("style"), (s) => s.textContent ?? ""),
        ].flatMap((css) => {
          try {
            const sheet = new CSSStyleSheet()
            sheet.replaceSync(scrubDocxCss(css))
            return [sheet]
          } catch {
            return []
          }
        })
        root.adoptedStyleSheets = sheets
        root.replaceChildren(...renderedBody.childNodes)
        setState("done")
      } catch {
        if (current) setState("failed")
      }
    })()

    return () => {
      current = false
      root.replaceChildren()
      root.adoptedStyleSheets = []
    }
  }, [blob])

  return (
    <div className="min-h-0 flex-1 overflow-auto bg-muted/30">
      {state === "rendering" && (
        <output className="flex items-center justify-center gap-2 p-6 text-sm text-muted-foreground">
          <Spinner /> Drawing the document…
        </output>
      )}
      {state === "failed" && (
        <p className="p-6 text-center text-sm text-muted-foreground">
          This Word document cannot be shown. Download it instead.
        </p>
      )}
      {/* Pages keep their layout as in Onyx; narrow screens scroll sideways instead of reflowing. */}
      <div ref={host} data-slot="docx-preview" className="w-fit min-w-full px-4 py-6" />
    </div>
  )
}
