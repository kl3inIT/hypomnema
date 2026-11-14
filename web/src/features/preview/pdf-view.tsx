import { useEffect, useMemo, useRef, useState } from "react"
import { Document, Page, pdfjs } from "react-pdf"

import { PreviewCanvas, PreviewSkeleton } from "./preview-surface"
import { PageField, PreviewToolbar, ToolbarGroup, ZoomControl } from "./preview-toolbar"
import {
  mostVisiblePage,
  nearestPage,
  pageHeight,
  pagesToRender,
  type PdfPageView,
} from "./pdf-pages"
import { useReaderWorker } from "./pdf-reader-worker"

// PdfView is a PDF drawn by pdf.js in the app (issue #422, row 2), copied from MemoryOS
// (web/src/features/preview/pdf-view.tsx), which replaced #402's viewer after Omnigent's: a phone's browser shows
// no PDF in a frame, and a PDF opened at its own address would be a page of this origin. Left out here: the cited
// regions, the outlines drawn around their ink and the reading by HTTP range (an Artifact is at most 5 MB and a
// folder's file is read whole, so the file is always its bytes). Kept: the pages near the viewport drawn, every
// other page a sized placeholder; the page field and the zoom as one floating toolbar; and, where there is room,
// the page rail.

// Image decoders (JBIG2, JPEG 2000) for scanned PDFs, copied from pdfjs-dist by vite.config.ts. The app's policy
// allows no WebAssembly, so pdf.js uses their JavaScript fallbacks.
const wasmUrl = `/assets/pdfjs-${pdfjs.version}/`
// Multiples of the fit-to-width size: a Letter page in a 400px panel needs 2–3× for body text.
const ZOOM_STEPS = [1, 1.5, 2, 3] as const

export function PdfView({
  file,
  thumbnails = false,
}: {
  // The file itself: the app's policy (connect-src 'self') does not let pdf.js fetch blob: addresses.
  file: Blob
  // A page rail beside the document, for a reader wide enough to hold one.
  thumbnails?: boolean
}) {
  const container = useRef<HTMLDivElement>(null)
  const [pageCount, setPageCount] = useState<number>()
  // The canvas is only mounted once the document has loaded, so it is measured again then.
  const fitWidth = Math.max(240, useWidth(container, pageCount) - 2)
  const [zoom, setZoom] = useState(0)
  const [failed, setFailed] = useState(false)
  const scale = ZOOM_STEPS[zoom] ?? 1
  const renderedWidth = Math.round(fitWidth * scale)
  const [pageViews, setPageViews] = useState<Record<number, PdfPageView>>({})
  const [nearPages, setNearPages] = useState<ReadonlySet<number>>(() => new Set())
  const [visiblePage, setVisiblePage] = useState<number>()
  // Pages whose canvas has been drawn, or could not be: what the reader is no longer waiting for.
  const [drawn, setDrawn] = useState<ReadonlySet<number>>(() => new Set())
  const worker = useReaderWorker()
  const options = useMemo(() => ({ wasmUrl, worker }), [worker])
  const currentPage = visiblePage ?? 1
  const figures = useRef(new Map<number, HTMLElement>())
  const currentPageRef = useRef(currentPage)
  const lastWidth = useRef(renderedWidth)

  useEffect(() => {
    const root = container.current
    if (!root || !pageCount) return undefined
    const pageOf = (entry: IntersectionObserverEntry) =>
      Number((entry.target as HTMLElement).dataset.page)
    const near = new Set<number>()
    const ratios = new Map<number, number>()
    const nearObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) near.add(pageOf(entry))
          else near.delete(pageOf(entry))
        }
        setNearPages((known) => (sameSet(known, near) ? known : new Set(near)))
      },
      { root, rootMargin: "100% 0px" },
    )
    const visibleObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.intersectionRatio > 0) ratios.set(pageOf(entry), entry.intersectionRatio)
          else ratios.delete(pageOf(entry))
        }
        const page = mostVisiblePage(ratios)
        if (page) setVisiblePage(page)
      },
      { root, threshold: [0, 0.25, 0.5, 0.75, 1] },
    )
    for (const figure of figures.current.values()) {
      nearObserver.observe(figure)
      visibleObserver.observe(figure)
    }

    return () => {
      nearObserver.disconnect()
      visibleObserver.disconnect()
    }
  }, [pageCount])

  useEffect(() => {
    currentPageRef.current = currentPage
  }, [currentPage])

  // A zoom keeps the reader on the page they were reading.
  useEffect(() => {
    if (!pageCount || lastWidth.current === renderedWidth) return
    lastWidth.current = renderedWidth
    figures.current
      .get(currentPageRef.current)
      ?.scrollIntoView({ block: "start", inline: "nearest" })
  }, [pageCount, renderedWidth])

  const goToPage = (page: number) =>
    figures.current.get(page)?.scrollIntoView({ block: "start", inline: "nearest" })
  const settle = (page: number) =>
    setDrawn((known) => (known.has(page) ? known : new Set(known).add(page)))

  const loadingPages = (
    <PreviewCanvas>
      <PreviewSkeleton width={renderedWidth} />
    </PreviewCanvas>
  )
  if (failed) {
    return (
      <PreviewCanvas>
        <p role="alert" className="rounded-xl bg-destructive/10 p-4 text-sm text-destructive">
          This PDF cannot be shown. Download it instead.
        </p>
      </PreviewCanvas>
    )
  }
  const rendered = new Set(pageCount ? pagesToRender(nearPages, 1, pageCount) : [])
  const idle = [...rendered].every((page) => drawn.has(page))
  const fallbackView = pageViews[1] ?? Object.values(pageViews)[0]

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <Document
        file={worker ? file : undefined}
        options={options}
        loading={loadingPages}
        noData={loadingPages}
        error={loadingPages}
        onLoadError={() => setFailed(true)}
        onLoadSuccess={(document) => setPageCount(document.numPages)}
        className="flex min-h-0 flex-1"
      >
        {thumbnails && pageCount ? (
          <PdfThumbnails total={pageCount} current={currentPage} idle={idle} onGo={goToPage} />
        ) : null}
        <PreviewCanvas ref={container} className="pb-16">
          <div className="flex w-fit min-w-full flex-col gap-5">
            {Array.from({ length: pageCount ?? 0 }, (_, index) => {
              const pageNumber = index + 1
              const view = pageViews[pageNumber]
              const show = rendered.has(pageNumber)
              return (
                <figure
                  key={pageNumber}
                  ref={(element) => {
                    if (element) figures.current.set(pageNumber, element)
                    else figures.current.delete(pageNumber)
                  }}
                  data-slot="pdf-page"
                  data-page={pageNumber}
                  data-rendered={show || undefined}
                  aria-label={`Page ${pageNumber}`}
                  className="mx-auto w-fit"
                >
                  {/* A page is always a white sheet, as it was authored, in both themes. */}
                  <div
                    className="relative isolate bg-white shadow-md ring-1 ring-border"
                    style={{
                      width: renderedWidth,
                      height: pageHeight(view, renderedWidth, fallbackView),
                    }}
                  >
                    {show ? (
                      <Page
                        pageNumber={pageNumber}
                        width={renderedWidth}
                        loading=""
                        renderTextLayer={false}
                        renderAnnotationLayer={false}
                        onRenderSuccess={() => settle(pageNumber)}
                        onRenderError={() => settle(pageNumber)}
                        onLoadError={() => settle(pageNumber)}
                        onLoadSuccess={(page) =>
                          setPageViews((current) =>
                            current[pageNumber]
                              ? current
                              : {
                                  ...current,
                                  [pageNumber]: { view: [...page.view], rotate: page.rotate },
                                },
                          )
                        }
                      />
                    ) : null}
                  </div>
                </figure>
              )
            })}
          </div>
        </PreviewCanvas>
      </Document>
      <div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center px-4">
        <PreviewToolbar>
          {pageCount ? (
            <PageField page={currentPage} total={pageCount} onPage={goToPage} />
          ) : (
            <ToolbarGroup>
              <span className="px-1.5 text-xs text-muted-foreground">Reading the PDF…</span>
            </ToolbarGroup>
          )}
          <ZoomControl
            label={zoom === 0 ? "Fit" : `${scale * 100}%`}
            onOut={() => setZoom((value) => Math.max(0, value - 1))}
            onIn={() => setZoom((value) => Math.min(ZOOM_STEPS.length - 1, value + 1))}
            outDisabled={zoom === 0}
            inDisabled={zoom === ZOOM_STEPS.length - 1}
          />
        </PreviewToolbar>
      </div>
    </div>
  )
}

const THUMBNAIL_WIDTH = 96

// PdfThumbnails is the page rail: every page as a button. A thumbnail is a whole page to pdf.js, so the rail draws
// one at a time, only for buttons in view, nearest the current page first, and only while the document canvas is
// `idle`. One that left the rail's surroundings is released.
function PdfThumbnails({
  total,
  current,
  idle,
  onGo,
}: {
  total: number
  current: number
  // The pages on the document canvas are drawn, so the rail may use the worker.
  idle: boolean
  onGo: (page: number) => void
}) {
  const rail = useRef<HTMLDivElement>(null)
  const buttons = useRef(new Map<number, HTMLElement>())
  const [inView, setInView] = useState<ReadonlySet<number>>(() => new Set())
  // Thumbnails that are mounted, and the one of them still being drawn.
  const [made, setMade] = useState<ReadonlySet<number>>(() => new Set())
  const [drawing, setDrawing] = useState<number>()
  useEffect(() => {
    const root = rail.current
    if (!root) return undefined
    const pageOf = (entry: IntersectionObserverEntry) =>
      Number((entry.target as HTMLElement).dataset.page)
    const near = new Set<number>()
    const seen = new Set<number>()
    const nearObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) near.add(pageOf(entry))
          else near.delete(pageOf(entry))
        }
        setMade((known) => {
          const kept = new Set([...known].filter((page) => near.has(page)))
          return kept.size === known.size ? known : kept
        })
        setDrawing((page) => (page !== undefined && !near.has(page) ? undefined : page))
      },
      { root, rootMargin: "200% 0px" },
    )
    const seenObserver = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) seen.add(pageOf(entry))
          else seen.delete(pageOf(entry))
        }
        setInView((known) => (sameSet(known, seen) ? known : new Set(seen)))
      },
      { root },
    )
    for (const button of buttons.current.values()) {
      nearObserver.observe(button)
      seenObserver.observe(button)
    }

    return () => {
      nearObserver.disconnect()
      seenObserver.disconnect()
    }
  }, [total])
  // The next thumbnail starts when the last one is drawn and the canvas has nothing left to draw. It is chosen
  // while rendering, from what is in view now, and held in state so a scroll does not unseat it.
  const next =
    idle && drawing === undefined
      ? nearestPage(
          [...inView].filter((page) => !made.has(page)),
          current,
        )
      : undefined
  if (next !== undefined) {
    setDrawing(next)
    setMade(new Set(made).add(next))
  }
  const finish = (page: number) => setDrawing((active) => (active === page ? undefined : active))
  // The rail follows the document, so the page being read stays visible in it.
  useEffect(() => {
    buttons.current.get(current)?.scrollIntoView({ block: "nearest" })
  }, [current])

  return (
    <nav
      ref={rail}
      aria-label="Pages"
      className="hidden w-32 shrink-0 overflow-y-auto overscroll-contain border-r bg-background p-2 lg:block"
    >
      <ol className="flex flex-col gap-2">
        {Array.from({ length: total }, (_, index) => {
          const page = index + 1
          return (
            <li key={page}>
              <button
                type="button"
                ref={(element) => {
                  if (element) buttons.current.set(page, element)
                  else buttons.current.delete(page)
                }}
                data-page={page}
                aria-current={page === current ? "true" : undefined}
                aria-label={`Page ${page}`}
                onClick={() => onGo(page)}
                className="block w-full cursor-pointer rounded-md p-1 ring-1 ring-transparent transition-shadow outline-none focus-visible:ring-3 focus-visible:ring-ring/50 aria-[current]:ring-primary"
              >
                <span
                  className="flex items-center justify-center overflow-hidden bg-white shadow-sm"
                  style={{ width: THUMBNAIL_WIDTH, height: Math.round(THUMBNAIL_WIDTH * 1.294) }}
                >
                  {made.has(page) ? (
                    <Page
                      pageNumber={page}
                      width={THUMBNAIL_WIDTH}
                      loading=""
                      renderTextLayer={false}
                      renderAnnotationLayer={false}
                      onRenderSuccess={() => finish(page)}
                      onRenderError={() => finish(page)}
                      onLoadError={() => finish(page)}
                    />
                  ) : null}
                </span>
                <span className="mt-1 block text-center text-xs text-muted-foreground tabular-nums">
                  {page}
                </span>
              </button>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

function sameSet(first: ReadonlySet<number>, second: ReadonlySet<number>) {
  if (first.size !== second.size) return false
  for (const value of first) if (!second.has(value)) return false

  return true
}

function useWidth(ref: React.RefObject<HTMLElement | null>, mounted: unknown) {
  const [width, setWidth] = useState(360)
  useEffect(() => {
    const element = ref.current
    if (!element) return undefined
    const observer = new ResizeObserver(([entry]) => {
      // A canvas taken out while the next document loads reports 0; the last real width still holds.
      if (entry && entry.contentRect.width > 0) setWidth(Math.floor(entry.contentRect.width))
    })
    observer.observe(element)

    return () => observer.disconnect()
  }, [ref, mounted])

  return width
}
