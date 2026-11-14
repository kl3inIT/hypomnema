import { useState } from "react"

import { useObjectURL } from "@/features/files/files-state"

import { CsvView } from "./csv-view"
import { DocxView } from "./docx-view"
import { DownloadView } from "./download-view"
import { ImageControls, ImageView, type ImageSource } from "./image-view"
import { LazyPdfView } from "./lazy-pdf-view"
import { MarkdownView } from "./markdown-view"
import type { PreviewContent } from "./preview-content"
import { SheetView } from "./sheet-view"
import { TextView } from "./text-view"

// FilePreview is the one file preview every place renders through: an Artifact's panel, full screen and own page,
// and the Files panel's tab (issue #422). It picks the view by the file's kind and loads pdf.js only when a PDF is
// shown. The place keeps its own chrome: its header, its actions. Copied from MemoryOS
// (web/src/features/preview/file-preview.tsx), after Onyx's PreviewModal, without its citations.
export function FilePreview({
  content,
  filename,
  download,
  thumbnails = false,
}: {
  content: PreviewContent
  filename: string
  // Where the whole file is downloaded, offered where the file cannot be shown, or only its beginning.
  download: string
  // A page rail beside a PDF, for a reader wide enough to hold one.
  thumbnails?: boolean
}) {
  switch (content.kind) {
    case "pdf":
      return <LazyPdfView file={content.file} thumbnails={thumbnails} />
    case "image":
      return <RasterImage blob={content.blob} alt={filename} />
    case "svg":
      return <ControlledImage source={content} alt={filename} />
    case "docx":
      return <DocxView blob={content.blob} />
    case "xlsx":
      return (
        <SheetView
          sheets={content.sheets}
          truncated={content.truncated}
          download={download}
          filename={filename}
        />
      )
    case "csv":
      return (
        <CsvView
          csv={content.text}
          truncated={content.truncated}
          download={download}
          filename={filename}
        />
      )
    case "markdown":
      return (
        <MarkdownView
          text={content.text}
          truncated={content.truncated}
          download={download}
          filename={filename}
        />
      )
    case "code":
    case "text":
      return (
        <TextView
          text={content.text}
          filename={filename}
          kind={content.kind}
          truncated={content.truncated}
          download={download}
        />
      )
    case "doc":
      return (
        <DownloadView
          href={download}
          filename={filename}
          message="An old .doc file cannot be shown here. Download it to open it."
        />
      )
    default:
      return (
        <DownloadView
          href={download}
          filename={filename}
          message="This kind of file is not shown here. Download it to open it in an app that reads it."
        />
      )
  }
}

// RasterImage is a raster picture from a blob: address of its exact type, the address revoked when it goes (the
// Files panel's useObjectURL, in place of MemoryOS's use-object-url.ts).
function RasterImage({ blob, alt }: { blob: Blob; alt: string }) {
  const url = useObjectURL(blob)

  return <ControlledImage source={url ? { url } : undefined} alt={alt} />
}

// ControlledImage is a picture with its own zoom and turn.
function ControlledImage({ source, alt }: { source: ImageSource | undefined; alt: string }) {
  const [zoom, setZoom] = useState(100)
  const [rotation, setRotation] = useState(0)

  return (
    <div className="relative flex min-h-0 flex-1 flex-col bg-muted/30">
      <ImageView source={source} alt={alt} zoom={zoom} rotation={rotation} />
      <div className="pointer-events-none absolute inset-x-0 bottom-4 flex justify-center">
        <div className="pointer-events-auto">
          <ImageControls
            zoom={zoom}
            onZoom={setZoom}
            onRotate={(degrees) => setRotation((turn) => (turn + degrees + 360) % 360)}
          />
        </div>
      </div>
    </div>
  )
}
