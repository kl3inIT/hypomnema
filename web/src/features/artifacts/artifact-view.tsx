import { useQuery } from "@tanstack/react-query"
import { lazy, Suspense } from "react"

import { openArtifactFrame, readArtifactFile, readArtifactSheets } from "@/api/generated/sdk.gen"
import type { Artifact, ArtifactRevision } from "@/api/generated/types.gen"
import { Spinner } from "@/components/ui/spinner"
import { problemOf } from "@/features/files/files-state"
import { FileView, type PreviewReader } from "@/features/preview/preview-loader"

import { artifactKey, downloadURL, useArtifactFile, useFrameFiles } from "./artifact-state"
import { frameDocument, frameSandbox } from "./frame-document"

// The viewers of an artifact (issues #402 and #422): a page in its locked frame, and any other file in the app's
// file preview (features/preview, MemoryOS's, after Onyx's PreviewModal): a PDF drawn by pdf.js, an image, a
// workbook read by the server into tables, a CSV, a Word document, Markdown, source and text; anything else as a
// file to download. A page's Code is its index.html in the app's read-only editor (the Files panel's
// code-view.tsx).

const CodeView = lazy(() => import("@/features/files/code-view"))

export type ArtifactMode = "preview" | "code"

export function ArtifactView({
  artifact,
  revision,
  mode = "preview",
  wide = false,
}: {
  artifact: Artifact
  revision: ArtifactRevision
  mode?: ArtifactMode
  // wide is a view with room for a PDF's page rail: full screen or its own page.
  wide?: boolean
}) {
  if (revision.type === "html") {
    return mode === "code" ? (
      <PageCode artifact={artifact} revision={revision} />
    ) : (
      <PageFrame artifact={artifact} revision={revision} />
    )
  }

  return <Preview artifact={artifact} revision={revision} wide={wide} />
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

// Preview is a revision's one file in the file preview, read through the artifact's routes: its bytes, its sheets
// read by the server, and, for an SVG, the frame's sandboxed address (issue #422, row 4), never a blob: address.
function Preview({ artifact, revision, wide }: Props & { wide: boolean }) {
  const id = artifact.id
  const rev = revision.revision
  const path = { artifact_id: id, revision: rev }
  const reader: PreviewReader = {
    download: downloadURL(id, revision),
    bytes: async (signal) =>
      (await readArtifactFile({ path, parseAs: "blob", signal })).data as Blob,
    sheets: async (signal) => (await readArtifactSheets({ path, signal })).data,
    svg: async (signal) => {
      const { url } = (await openArtifactFrame({ path, signal })).data

      return `${new URL(url, window.location.origin).href}${encodeURIComponent(revision.entry)}`
    },
  }
  const mediaType = revision.files.find((f) => f.path === revision.entry)?.media_type ?? ""

  return (
    <FileView
      key={`${id}-${rev}`}
      reader={reader}
      filename={revision.entry}
      mediaType={mediaType}
      queryKey={[...artifactKey(id), rev, "file", ""]}
      staleTime={Infinity}
      thumbnails={wide}
    />
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
