import { useQuery } from "@tanstack/react-query"
import { Download, File } from "lucide-react"
import { lazy, Suspense, useMemo, useState } from "react"

import type { Artifact, ArtifactRevision } from "@/api/generated/types.gen"
import { buttonVariants } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Spinner } from "@/components/ui/spinner"
import { problemOf, useObjectURL } from "@/features/files/files-state"

import { downloadURL, sizeOf, useArtifactFile, useFrameFiles } from "./artifact-state"
import { frameDocument, frameSandbox } from "./frame-document"
import { Zoom } from "./zoom"

// The viewers of an artifact, one a type, as Onyx's preview has one variant a type
// (web/src/sections/modals/PreviewModal/variants/) and OpenMausBot's AttachmentPreview.tsx: a page in its locked
// frame, a PDF drawn by pdf.js, an image, and anything else as a file to download (issue #402, the widened scope).
// A page's Code is its index.html in the app's read-only editor (the Files panel's code-view.tsx).

const CodeView = lazy(() => import("@/features/files/code-view"))
const PdfView = lazy(() => import("./pdf-view"))

export type ArtifactMode = "preview" | "code"

export function ArtifactView({
  artifact,
  revision,
  mode = "preview",
}: {
  artifact: Artifact
  revision: ArtifactRevision
  mode?: ArtifactMode
}) {
  switch (revision.type) {
    case "html":
      return mode === "code" ? (
        <PageCode artifact={artifact} revision={revision} />
      ) : (
        <PageFrame artifact={artifact} revision={revision} />
      )
    case "pdf":
      return <PdfFile artifact={artifact} revision={revision} />
    case "image":
      return <ImageFile artifact={artifact} revision={revision} />
    default:
      return <DownloadOnly artifact={artifact} revision={revision} />
  }
}

type Props = { artifact: Artifact; revision: ArtifactRevision }

// usePageText is a page's index.html as text.
function usePageText({ artifact, revision }: Props) {
  const file = useArtifactFile(artifact.id, revision.revision)

  return useQuery({
    queryKey: ["artifact", artifact.id, revision.revision, "text"],
    queryFn: () => (file.data as Blob).text(),
    enabled: file.data !== undefined,
    staleTime: Infinity,
  })
}

// PageFrame is a page in its locked frame (issue #402, rows 8-10): srcdoc with the policy first, a sandbox that
// runs its scripts and never makes it of this origin, and no referrer. A folder page's files load from the frame's
// own address, which the policy names as its base.
function PageFrame(props: Props) {
  const { artifact, revision } = props
  const text = usePageText(props)
  const folder = revision.files.length > 1
  const files = useFrameFiles(artifact.id, revision.revision, folder)

  const error = text.error ?? files.error
  if (error) return <Problem error={error} />
  if (text.data === undefined || (folder && files.data === undefined)) return <Loading />

  return (
    <iframe
      title={revision.title}
      sandbox={frameSandbox}
      referrerPolicy="no-referrer"
      srcDoc={frameDocument(text.data, folder ? files.data : undefined)}
      // A page is drawn on its own light canvas, whatever the app's theme, as OpenMausBot's and Claude's are.
      style={{ colorScheme: "light", background: "white" }}
      className="min-h-0 w-full flex-1 border-0"
    />
  )
}

function PageCode(props: Props) {
  const text = usePageText(props)

  if (text.error) return <Problem error={text.error} />
  if (text.data === undefined) return <Loading />

  return (
    <Suspense fallback={<Loading />}>
      <div className="flex min-h-0 flex-1 flex-col">
        <CodeView kind="file" path={props.revision.entry} content={text.data} />
      </div>
    </Suspense>
  )
}

function PdfFile({ artifact, revision }: Props) {
  const file = useArtifactFile(artifact.id, revision.revision)

  if (file.error) return <Problem error={file.error} />
  if (file.data === undefined) return <Loading />

  return (
    <Suspense fallback={<Loading />}>
      <PdfView data={file.data} />
    </Suspense>
  )
}

const mediaOf = (revision: ArtifactRevision) =>
  revision.files.find((f) => f.path === revision.entry)?.media_type ?? "application/octet-stream"

// ImageFile is an image from its bytes, zoomable from 25% to 200% (Onyx's imageVariant). It is an <img>, where an
// SVG's script does not run.
function ImageFile({ artifact, revision }: Props) {
  const file = useArtifactFile(artifact.id, revision.revision)
  const type = mediaOf(revision)
  const typed = useMemo(() => file.data && new Blob([file.data], { type }), [file.data, type])
  const url = useObjectURL(typed)
  const [scale, setScale] = useState(1)

  if (file.error) return <Problem error={file.error} />
  if (!url) return <Loading />

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 justify-end border-b px-3 py-1">
        <Zoom
          scale={scale}
          min={0.25}
          max={2}
          onChange={(next) => setScale(Math.min(2, Math.max(0.25, next)))}
        />
      </div>
      <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto bg-muted/30 p-4">
        <img
          src={url}
          alt={revision.title}
          style={{ transform: `scale(${scale})` }}
          className="max-h-full max-w-full origin-center transition-transform duration-300"
        />
      </div>
    </div>
  )
}

// DownloadOnly is a file the app has no view for: its name and size, and a download (Onyx's unsupportedVariant).
function DownloadOnly({ artifact, revision }: Props) {
  return (
    <div className="flex min-h-0 flex-1 flex-col justify-center">
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <File />
          </EmptyMedia>
          <EmptyTitle>{revision.entry}</EmptyTitle>
          <EmptyDescription>
            {sizeOf(revision.size)}. This kind of file is not shown here; download it to open it.
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <a
            href={downloadURL(artifact.id, revision)}
            download={revision.entry}
            className={buttonVariants({ variant: "default" })}
          >
            <Download />
            Download
          </a>
        </EmptyContent>
      </Empty>
    </div>
  )
}

function Loading() {
  return (
    <output className="flex flex-1 items-center justify-center gap-2 p-8 text-sm text-muted-foreground">
      <Spinner /> Opening…
    </output>
  )
}

function Problem({ error }: { error: unknown }) {
  return (
    <output className="flex flex-1 items-center justify-center p-8 text-center text-sm text-muted-foreground">
      {problemOf(error).message}
    </output>
  )
}
