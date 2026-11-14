import { lazy, Suspense } from "react"

import { Spinner } from "@/components/ui/spinner"

import type { PreviewKind } from "./preview-kind"
import { CutNote } from "./preview-surface"

// Source is shown in the app's read-only editor, the Files panel's (code-view.tsx), loaded with the first file
// opened.
const CodeView = lazy(() => import("@/features/files/code-view"))

// TextView is source and plain text read as highlighted source; JSON is reformatted when it was read whole.
// Copied from MemoryOS (web/src/features/preview/text-view.tsx). Changed here: the source is drawn by this app's
// read-only Monaco view rather than Shiki, as #402's Code view and the Files panel draw it, its language from the
// file's name.
export function TextView({
  text,
  filename,
  kind,
  truncated = false,
  download,
}: {
  text: string
  filename: string
  kind: Extract<PreviewKind, "code" | "text">
  truncated?: boolean
  download: string
}) {
  let shown = text
  if (kind === "code" && filename.toLowerCase().endsWith(".json") && !truncated) {
    try {
      shown = JSON.stringify(JSON.parse(text), null, 2)
    } catch {
      shown = text
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {truncated && (
        <CutNote
          download={download}
          filename={filename}
          className="shrink-0 border-b bg-muted/40 px-3.5 py-1.5"
        >
          Only the first 1 MB of the file is shown.
        </CutNote>
      )}
      <Suspense
        fallback={
          <output className="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
            <Spinner /> Opening…
          </output>
        }
      >
        <div className="flex min-h-0 flex-1 flex-col">
          <CodeView kind="file" path={filename} content={shown} />
        </div>
      </Suspense>
    </div>
  )
}
