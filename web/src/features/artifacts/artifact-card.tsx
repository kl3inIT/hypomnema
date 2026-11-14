import { useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import type { Artifact, ArtifactCard as Card, ArtifactRevision } from "@/api/generated/types.gen"
import { since } from "@/lib/time"

import { ArtifactFullScreen } from "./artifact-full-screen"
import { ArtifactIcon } from "./artifact-icon"
import {
  artifactKey,
  isPhone,
  revisionOf,
  useArtifact,
  useArtifactFile,
  useImageSource,
  useFrameFiles,
} from "./artifact-state"
import { frameDocument, frameSandbox } from "./frame-document"
import { useOpenArtifact } from "./open-artifact-context"

const typeName = { html: "Page", pdf: "PDF", image: "Image", file: "File" } as const

// ArtifactCard is an artifact in the chat (issue #402, row 4): a compact card with a small live thumbnail of a page
// or an image, an icon for the rest, its title, its revision and when it was updated, as Open Canvas's chip and
// Claude's artifact card stand in the conversation. Pressed, it opens in the panel beside the chat, or over the
// whole screen on a phone. Its message says when the Bot revised it, and what was read of it is read again then.
// It is its own design, a card that is pressed, not a Button.
export function ArtifactCard({ card }: { card: Card }) {
  const queryClient = useQueryClient()
  const open = useOpenArtifact()
  const [full, setFull] = useState(false)
  const [rev, setRev] = useState<number | undefined>()

  useEffect(() => {
    const kept = queryClient.getQueryData<Artifact>(artifactKey(card.id))
    if (kept && kept.revision !== card.revision) {
      void queryClient.invalidateQueries({ queryKey: artifactKey(card.id), exact: true })
    }
  }, [queryClient, card.id, card.revision])

  return (
    <div className="pb-4">
      <button
        type="button"
        onClick={() => (open && !isPhone() ? open(card.id) : setFull(true))}
        aria-label={`Open ${card.title}`}
        className="group flex w-full max-w-md items-center gap-3 rounded-xl border bg-card p-2 text-left shadow-xs outline-none hover:bg-accent focus-visible:ring-3 focus-visible:ring-ring/50"
      >
        <span className="relative flex h-16 w-24 shrink-0 items-center justify-center overflow-hidden rounded-lg border bg-muted">
          <Thumbnail card={card} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-sm font-medium text-foreground">{card.title}</span>
          <span className="truncate text-xs text-muted-foreground">
            v{card.revision} · Updated {since(card.updated_at)}
          </span>
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
            <ArtifactIcon type={card.type} className="size-3" />
            {typeName[card.type]} · {isPhone() || !open ? "Tap to open" : "Click to open"}
          </span>
        </span>
      </button>
      {full && (
        <ArtifactFullScreen
          id={card.id}
          rev={rev}
          onRev={setRev}
          onClose={() => {
            setFull(false)
            setRev(undefined)
          }}
        />
      )}
    </div>
  )
}

// Thumbnail is a page drawn small in its locked frame, which nothing can be pressed in, or an image; an icon for
// the rest.
function Thumbnail({ card }: { card: Card }) {
  const artifact = useArtifact(card.id)
  const revision = artifact.data && revisionOf(artifact.data)

  if (revision?.type === "html") return <PageThumbnail id={card.id} revision={revision} />
  if (revision?.type === "image") return <ImageThumbnail id={card.id} revision={revision} />

  return <ArtifactIcon type={card.type} className="size-6 text-muted-foreground" />
}

// A page is laid out at a desktop's width and scaled to the card, so it looks as it will open.
const pageWidth = 1280
const pageHeight = 853
const thumbScale = 96 / pageWidth

function PageThumbnail({ id, revision }: { id: string; revision: ArtifactRevision }) {
  const file = useArtifactFile(id, revision.revision)
  const folder = revision.files.length > 1
  const files = useFrameFiles(id, revision.revision, folder)
  const [text, setText] = useState<string>()

  useEffect(() => {
    let live = true
    void file.data?.text().then((t) => live && setText(t))

    return () => {
      live = false
    }
  }, [file.data])

  if (text === undefined || (folder && !files.data)) {
    return <ArtifactIcon type="html" className="size-6 text-muted-foreground" />
  }

  return (
    <iframe
      title={`${revision.title}, small`}
      aria-hidden
      tabIndex={-1}
      sandbox={frameSandbox}
      referrerPolicy="no-referrer"
      loading="lazy"
      srcDoc={frameDocument(text, folder ? files.data : undefined)}
      style={{
        width: pageWidth,
        height: pageHeight,
        transform: `scale(${thumbScale})`,
        colorScheme: "light",
        background: "white",
      }}
      className="pointer-events-none absolute top-0 left-0 origin-top-left border-0"
    />
  )
}

function ImageThumbnail({ id, revision }: { id: string; revision: ArtifactRevision }) {
  const { src: url } = useImageSource(id, revision)

  if (!url) return <ArtifactIcon type="image" className="size-6 text-muted-foreground" />

  return <img src={url} alt="" className="size-full object-cover" />
}
