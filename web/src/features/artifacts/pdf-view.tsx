import { useEffect, useRef, useState } from "react"
import { Document, Page, pdfjs } from "react-pdf"
import "react-pdf/dist/Page/AnnotationLayer.css"
import "react-pdf/dist/Page/TextLayer.css"

import { Spinner } from "@/components/ui/spinner"

import { Zoom } from "./zoom"

// PdfView draws a PDF artifact with pdf.js, in the app, as Omnigent's PdfViewer.tsx does (Apache-2.0,
// https://github.com/omnigent-ai/omnigent, web/src/shell/PdfViewer.tsx), without its comments: a phone's browser shows no PDF in a
// frame, and a PDF opened at its own address would be a page of this origin. The pages are a column fit to the
// width, with the page count and a zoom of 50% to 300% above them. This module is loaded only when a PDF is shown,
// so pdf.js and its worker load with it.
//
// The worker is made with `new Worker(new URL(...))`, the pattern Vite emits as a hashed asset (Omnigent's note).
pdfjs.GlobalWorkerOptions.workerPort = new Worker(
  new URL("pdfjs-dist/build/pdf.worker.min.mjs", import.meta.url),
  { type: "module" },
)

const minScale = 0.5
const maxScale = 3

export default function PdfView({ data }: { data: Blob }) {
  const [pages, setPages] = useState(0)
  const [failed, setFailed] = useState(false)
  const [scale, setScale] = useState(1)
  const box = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState<number | null>(null)

  // The pages fit the box's width at 100%.
  useEffect(() => {
    const el = box.current
    if (!el || typeof ResizeObserver === "undefined") return
    const measure = () => setWidth(el.clientWidth)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)

    return () => observer.disconnect()
  }, [])

  if (failed) {
    return <Centered>This PDF cannot be shown. Download it instead.</Centered>
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center justify-between gap-2 border-b px-3 py-1">
        <span className="text-xs text-muted-foreground tabular-nums">
          {pages > 0 ? `${pages} page${pages === 1 ? "" : "s"}` : ""}
        </span>
        <Zoom
          scale={scale}
          min={minScale}
          max={maxScale}
          onChange={(next) => setScale(Math.min(maxScale, Math.max(minScale, next)))}
        />
      </div>
      <div ref={box} className="min-h-0 flex-1 overflow-auto bg-muted/30 p-4">
        <Document
          file={data}
          onLoadSuccess={(doc) => setPages(doc.numPages)}
          onLoadError={() => setFailed(true)}
          loading={
            <Centered>
              <Spinner /> Reading the PDF…
            </Centered>
          }
          className="flex flex-col items-center gap-4"
        >
          {Array.from({ length: pages }, (_, i) => (
            <Page
              key={i + 1}
              pageNumber={i + 1}
              width={width ? Math.max(0, width - 32) * scale : undefined}
              className="shadow-md"
              loading=""
            />
          ))}
        </Document>
      </div>
    </div>
  )
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <output className="flex items-center justify-center gap-2 p-8 text-sm text-muted-foreground">
      {children}
    </output>
  )
}
