import { lazy, Suspense, type ComponentProps } from "react"

import type { PdfView } from "./pdf-view"
import { PreviewCanvas, PreviewSkeleton } from "./preview-surface"

// pdf.js is the heaviest code the app ships, so its reader loads only when a PDF is opened; every view reaches it
// through LazyPdfView, never through a static import of pdf-view. Copied from MemoryOS
// (web/src/features/preview/lazy-pdf-view.tsx, readers.ts), with React's lazy in place of its loadable.
const Reader = lazy(() => import("./pdf-view").then((module) => ({ default: module.PdfView })))

// LazyPdfView is the pdf.js reader, with page-shaped placeholders while its code arrives.
export function LazyPdfView(props: ComponentProps<typeof PdfView>) {
  return (
    <Suspense
      fallback={
        <PreviewCanvas>
          <PreviewSkeleton />
        </PreviewCanvas>
      }
    >
      <Reader {...props} />
    </Suspense>
  )
}
