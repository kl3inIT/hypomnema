import { useQuery } from "@tanstack/react-query"
import { useEffect } from "react"

import { getShareOptions, getShareQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import type { SharedArtifact } from "@/api/generated/types.gen"
import { Spinner } from "@/components/ui/spinner"
import { problemOf } from "@/lib/server-error"
import { since } from "@/lib/time"
import { FileView, type PreviewReader } from "@/features/preview/preview-loader"

import { ArtifactIcon } from "./artifact-icon"
import { frameSandbox } from "./frame"

// SharedArtifactView is a view-only link's page, /s/<token> (issue #745), as open-webui's /s/{id} and LibreChat's
// ShareView are pages of their apps: no sign-in and no app shell, only the title, when it was updated and the
// artifact. A page is in the same locked frame the app uses (issue #737): loaded by src from the link's files
// address, whose answer carries its policy, sandboxed without allow-same-origin, so it has an opaque origin and no
// session. A PDF or an image shows as in the app; a workbook or another file is a download, as the sheets preview
// stays the owner's. It reads the link again every 30 seconds and loads the newest revision when there is one
// (OpenMausBot's live pages check every 30 s, server/pages/live.ts); a revoked link says so at once.

// checkEvery is how often the page asks whether a newer revision is there: OpenMausBot's CHECK_EVERY_MS.
const checkEvery = 30_000

export function SharedArtifactView({ token }: { token: string }) {
  const options = { path: { token } }
  const share = useQuery({
    ...getShareOptions(options),
    // A link that is gone stays gone: the page stops asking.
    refetchInterval: (query) =>
      problemOf(query.state.error).code === "not_found" ? false : checkEvery,
    refetchIntervalInBackground: true,
    staleTime: 0,
    retry: false,
  })

  useNoIndex()

  const gone = problemOf(share.error).code === "not_found"
  const a = gone ? undefined : share.data

  return (
    <main className="flex min-h-0 min-w-0 flex-1 flex-col">
      <header className="flex shrink-0 items-center gap-2 border-b bg-muted/50 px-3 py-1.5">
        {a && <ArtifactIcon type={a.type} className="size-4 shrink-0 text-muted-foreground" />}
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-sm font-medium" title={a?.title}>
            {a?.title ?? "Shared artifact"}
          </h1>
          {a && (
            <p className="truncate text-xs text-muted-foreground">Updated {since(a.updated_at)}</p>
          )}
        </div>
      </header>
      <div className="flex min-h-0 flex-1 flex-col bg-background">
        {gone ? (
          <Note>This link was revoked, or never was.</Note>
        ) : a ? (
          <Shown key={`${a.revision}`} token={token} artifact={a} />
        ) : share.error ? (
          <Note>{problemOf(share.error).message}</Note>
        ) : (
          <Note>
            <Spinner /> Opening…
          </Note>
        )}
      </div>
    </main>
  )
}

// Shown is the artifact at one revision; a new revision mounts it again, which reloads the frame.
function Shown({ token, artifact }: { token: string; artifact: SharedArtifact }) {
  const src = `${artifact.files}${encodeURIComponent(artifact.entry)}`

  if (artifact.type === "html") {
    return (
      <iframe
        title={artifact.title}
        sandbox={frameSandbox}
        referrerPolicy="no-referrer"
        src={src}
        style={{ colorScheme: "light", background: "white" }}
        className="min-h-0 w-full flex-1 border-0"
      />
    )
  }

  const reader: PreviewReader = {
    download: src,
    bytes: async (signal) => {
      const answer = await fetch(src, { signal, referrerPolicy: "no-referrer" })
      if (!answer.ok) throw new Error("The file could not be read. Reload the page.")

      return answer.blob()
    },
    sheets: () => Promise.reject(new Error("A workbook is not shown here.")),
    svg: () => Promise.resolve(new URL(src, window.location.origin).href),
  }

  return (
    <FileView
      reader={reader}
      filename={artifact.entry}
      mediaType={artifact.media_type}
      queryKey={[...getShareQueryKey({ path: { token } }), artifact.revision]}
      staleTime={Infinity}
      thumbnails
    />
  )
}

// useNoIndex adds the robots meta while the page is shown, as open-webui's shared chat does; the server's
// X-Robots-Tag says the same.
function useNoIndex() {
  useEffect(() => {
    const meta = document.createElement("meta")
    meta.name = "robots"
    meta.content = "noindex, nofollow"
    document.head.append(meta)

    return () => meta.remove()
  }, [])
}

function Note({ children }: { children: React.ReactNode }) {
  return (
    <output className="flex flex-1 items-center justify-center gap-2 p-8 text-center text-sm text-muted-foreground">
      {children}
    </output>
  )
}
