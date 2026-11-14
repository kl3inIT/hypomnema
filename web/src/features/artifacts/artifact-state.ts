import { useQuery } from "@tanstack/react-query"
import { useEffect, useMemo } from "react"

import { client } from "@/api/client"
import { getArtifact, openArtifactFrame, readArtifactFile } from "@/api/generated/sdk.gen"
import type { Artifact, ArtifactRevision, ArtifactType } from "@/api/generated/types.gen"
import { isRaster, kindLabel, previewKind, type PreviewKind } from "@/features/preview/preview-kind"

// What the web knows of an artifact (issue #402): the artifact with its revisions, a revision's files read as
// bytes, and the address a folder page's files load from in its frame.

export const artifactKey = (id: string) => ["artifact", id] as const

// useArtifact is an artifact with every revision kept, newest first. Its message in the chat says when it changes,
// and the card that shows the message reads it again then (artifact-card.tsx).
export function useArtifact(id: string) {
  return useQuery({
    queryKey: artifactKey(id),
    queryFn: async () => (await getArtifact({ path: { artifact_id: id } })).data,
  })
}

// revisionOf is the revision numbered rev, or the newest when rev is absent or not kept.
export function revisionOf(a: Artifact, rev?: number): ArtifactRevision | undefined {
  const revisions = a.revisions ?? []

  return revisions.find((r) => r.revision === rev) ?? revisions[0]
}

// useArtifactFile is a file of a revision as bytes, the file shown when path is absent. A revision never changes,
// so what was read is kept.
export function useArtifactFile(id: string, rev: number, path?: string, enabled = true) {
  return useQuery({
    queryKey: [...artifactKey(id), rev, "file", path ?? ""],
    queryFn: async () =>
      (
        await readArtifactFile({
          path: { artifact_id: id, revision: rev },
          query: path ? { path } : undefined,
          parseAs: "blob",
        })
      ).data as Blob,
    enabled,
    staleTime: Infinity,
  })
}

// useFrameFiles is the absolute address a folder page's files load from: an hour after its last use it lapses,
// so it is asked again after half that.
export function useFrameFiles(id: string, rev: number, enabled: boolean) {
  return useQuery({
    queryKey: [...artifactKey(id), rev, "frame"],
    queryFn: async () => {
      const { url } = (await openArtifactFrame({ path: { artifact_id: id, revision: rev } })).data

      return new URL(url, window.location.origin).href
    },
    enabled,
    staleTime: 30 * 60_000,
  })
}

// downloadURL is where a revision is downloaded from: its one file, or a folder as a zip. The server sends it as an
// attachment, so the browser saves it and never opens it as a page of this origin.
export function downloadURL(id: string, revision: ArtifactRevision) {
  const base = `${client.getConfig().baseUrl ?? ""}/artifacts/${encodeURIComponent(id)}/revisions/${revision.revision}`

  return revision.files.length > 1 ? `${base}/archive` : `${base}/file`
}

// pageURL is the artifact's own page in the app, which a new tab opens and a link names (issue #402, row 7).
export function pageURL(id: string, rev?: number) {
  return `${window.location.origin}/artifacts/${encodeURIComponent(id)}${rev ? `?rev=${rev}` : ""}`
}

// sizeOf is a number of bytes as people read it.
export function sizeOf(bytes: number) {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`
  if (bytes >= 1 << 10) return `${Math.round(bytes / (1 << 10))} KB`

  return `${bytes} B`
}

// useImageSource is where an image artifact's card draws it from: a raster image from its bytes, typed exactly (a
// blob: address is of this origin, so a type that can run script there never gets one); any other image, an SVG,
// from its frame's address, which the server sends with a sandboxing policy, so that opened on its
// own it is never a page of this origin and its script never runs.
export function useImageSource(id: string, revision: ArtifactRevision) {
  const type = revision.files.find((f) => f.path === revision.entry)?.media_type ?? ""
  const raster = isRaster(type)
  const file = useArtifactFile(id, revision.revision, undefined, raster)
  const files = useFrameFiles(id, revision.revision, !raster)
  const blob = useMemo(
    () => (raster && file.data ? new Blob([file.data], { type }) : undefined),
    [raster, file.data, type],
  )
  const url = useObjectURLOf(blob)

  return {
    src: raster ? url : files.data && `${files.data}${encodeURIComponent(revision.entry)}`,
    error: raster ? file.error : files.error,
  }
}

// useObjectURLOf is a blob: address for raster bytes, revoked when they change or are no longer shown.
function useObjectURLOf(blob: Blob | undefined) {
  const url = useMemo(() => (blob ? URL.createObjectURL(blob) : null), [blob])
  useEffect(() => () => (url ? URL.revokeObjectURL(url) : undefined), [url])

  return url ?? undefined
}

// kindOf is the preview kind of a file artifact (issue #422): by its revision's file, as its viewer picks it
// (artifact-view.tsx), or by its type until the revision is read.
export function kindOf(type: ArtifactType, revision?: ArtifactRevision): PreviewKind {
  if (revision) {
    const media = revision.files.find((f) => f.path === revision.entry)?.media_type ?? ""
    return previewKind(revision.entry, media)
  }

  return type === "pdf" ? "pdf" : type === "image" ? "image" : "unsupported"
}

// typeLabel is what the card calls an artifact: a page, or the kind of file its viewer shows.
export const typeLabel = (type: ArtifactType, revision?: ArtifactRevision) =>
  type === "html" ? "Page" : kindLabel[kindOf(type, revision)]

// isPhone is a screen where an artifact opens over the whole screen rather than beside the chat (issue #402, row 6).
export const isPhone = () => !window.matchMedia("(min-width: 768px)").matches
