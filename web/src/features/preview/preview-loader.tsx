import { useQuery } from "@tanstack/react-query"

import { problemOf } from "@/features/files/files-state"

import { DownloadView } from "./download-view"
import { FilePreview } from "./file-preview"
import { readPreviewContent, type PreviewContent } from "./preview-content"
import type { SheetList } from "@/api/generated/types.gen"

import { previewKind } from "./preview-kind"
import { PreviewCanvas, PreviewSkeleton } from "./preview-surface"

// The ways a view reaches one file; each place gives the routes it reads with (issue #422). Copied from MemoryOS
// (web/src/features/preview/original-view.tsx, OriginalReader and load), without the PDF read by range.
export type PreviewReader = {
  // Where the whole file is downloaded, as an attachment.
  download: string
  bytes: (signal: AbortSignal) => Promise<Blob>
  // A workbook is read as CSV a sheet by the server, since the app ships no workbook parser.
  sheets: (signal: AbortSignal) => Promise<SheetList>
  // The sandboxed address an SVG is drawn from, where the place has one; without it the SVG is drawn from its text
  // in a locked frame.
  svg?: (signal: AbortSignal) => Promise<string>
}

// load reads only what the chosen view needs: a workbook arrives already parsed a sheet, an SVG at its sandboxed
// address when there is one, and a file this app cannot show is never downloaded to find that out.
async function load(
  reader: PreviewReader,
  filename: string,
  mediaType: string,
  signal: AbortSignal,
): Promise<PreviewContent> {
  const kind = previewKind(filename, mediaType)
  if (kind === "xlsx") {
    const { sheets, truncated } = await reader.sheets(signal)
    return { kind, sheets, truncated }
  }
  if (kind === "svg" && reader.svg) return { kind, url: await reader.svg(signal) }
  if (kind === "doc" || kind === "pptx" || kind === "unsupported") return { kind }

  return readPreviewContent(await reader.bytes(signal), kind, mediaType)
}

// FileView is one file read through its place's routes and shown by FilePreview. queryKey names the file as its
// place keeps it, so a file that never changes (an Artifact's revision) is read once.
export function FileView({
  reader,
  filename,
  mediaType,
  queryKey,
  staleTime = 0,
  thumbnails = false,
}: {
  reader: PreviewReader
  filename: string
  mediaType: string
  queryKey: readonly unknown[]
  staleTime?: number
  thumbnails?: boolean
}) {
  const loaded = useQuery({
    queryKey: [...queryKey, "preview"],
    queryFn: ({ signal }) => load(reader, filename, mediaType, signal),
    retry: false,
    staleTime,
  })

  if (loaded.isPending)
    return (
      <PreviewCanvas>
        <PreviewSkeleton pages={1} />
      </PreviewCanvas>
    )
  if (loaded.isError)
    return (
      <DownloadView
        href={reader.download}
        filename={filename}
        message={`${problemOf(loaded.error).message} Download it to see the whole file.`}
      />
    )

  return (
    <FilePreview
      content={loaded.data}
      filename={filename}
      download={reader.download}
      thumbnails={thumbnails}
    />
  )
}
