import { useQuery, type QueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useSyncExternalStore } from "react"

import { client } from "@/api/client"
import {
  deleteWorkspaceEntry,
  diffWorkspaceFile,
  listChatWorkspaces,
  listWorkspaceChanges,
  listWorkspaceDirectory,
  makeWorkspaceFolder,
  moveWorkspaceEntry,
  readWorkspaceFile,
  readWorkspaceRaw,
  writeWorkspaceFile,
} from "@/api/generated/sdk.gen"

// A chat's working folders, as the Files panel reads them (issue #255). As Omnigent's useWorkspaceChangedFiles.ts:
// nothing polls the lists; they are read again when an event may have changed them (work/use-work.ts refreshes every
// key under workspaceKey but the files themselves), when the window comes back, and with Refresh. An open file of
// 256 KiB or less is read again every two seconds while it is shown (Omnigent's useFileContent).

export const workspaceKey = ["workspace"] as const

const key = (chat: string, workspace: string, ...rest: unknown[]) => [
  ...workspaceKey,
  chat,
  workspace,
  ...rest,
]

// Whether a query of the panel is the content of a file or an image, which an event does not refresh.
export const isFileQuery = (queryKey: readonly unknown[]) =>
  queryKey[0] === workspaceKey[0] && (queryKey[3] === "file" || queryKey[3] === "raw")

const liveFileBytes = 256 << 10

export function useWorkspaces(chat: string | null) {
  return useQuery({
    queryKey: [...workspaceKey, chat, "list"],
    queryFn: async () => (await listChatWorkspaces({ path: { chat_id: chat ?? "" } })).data.data,
    enabled: chat !== null,
    staleTime: 30_000,
  })
}

export function useDirectory(chat: string, workspace: string, path: string, enabled = true) {
  return useQuery({
    queryKey: key(chat, workspace, "tree", path),
    queryFn: async () =>
      (
        await listWorkspaceDirectory({
          path: { chat_id: chat, workspace_id: workspace },
          query: { path },
        })
      ).data,
    enabled,
    staleTime: 5_000,
  })
}

export function useChanges(chat: string, workspace: string, enabled = true) {
  return useQuery({
    queryKey: key(chat, workspace, "changes"),
    queryFn: async () =>
      (await listWorkspaceChanges({ path: { chat_id: chat, workspace_id: workspace } })).data,
    enabled,
    staleTime: 5_000,
  })
}

// fileKey is the query of a file's text, which a save sets to what it wrote.
export const fileKey = (chat: string, workspace: string, path: string) =>
  key(chat, workspace, "file", path)

export function useFile(chat: string, workspace: string, path: string, shown: boolean) {
  return useQuery({
    queryKey: fileKey(chat, workspace, path),
    queryFn: async () =>
      (
        await readWorkspaceFile({
          path: { chat_id: chat, workspace_id: workspace },
          query: { path },
        })
      ).data,
    staleTime: 5_000,
    refetchInterval: (query) => {
      const file = query.state.data
      if (!shown) return false
      if (query.state.status === "error") return 30_000

      return file && !file.binary && file.bytes <= liveFileBytes ? 2_000 : false
    },
  })
}

export function useDiff(chat: string, workspace: string, path: string) {
  return useQuery({
    queryKey: key(chat, workspace, "diff", path),
    queryFn: async () =>
      (
        await diffWorkspaceFile({
          path: { chat_id: chat, workspace_id: workspace },
          query: { path },
        })
      ).data,
    staleTime: 5_000,
  })
}

// problemOf is a refused request's code and message, as the server sent them.
export function problemOf(error: unknown): { code: string; message: string } {
  if (error && typeof error === "object") {
    const { code, message } = error as Record<string, unknown>

    return {
      code: typeof code === "string" ? code : "",
      message: typeof message === "string" && message ? message : "The server refused. Try again.",
    }
  }

  return { code: "", message: "The server refused. Try again." }
}

// baseName is the last part of a path.
export const baseName = (path: string) => path.slice(path.lastIndexOf("/") + 1) || path

// The changes (issue #297). Each is one request; what the machine refuses comes back in its words, a save over a
// file that changed meanwhile as the code "conflict" and one over a file that went as "not_found".

// MaxWrite is the largest text saved from here: what fits the message that carries it to a node.
export const maxWrite = 1 << 20

const where = (chat: string, workspace: string) => ({ chat_id: chat, workspace_id: workspace })

export async function saveFile(
  chat: string,
  workspace: string,
  path: string,
  content: string,
  expect?: string,
) {
  return (
    await writeWorkspaceFile({
      path: where(chat, workspace),
      body: { path, content, ...(expect !== undefined && { expect_sha256: expect }) },
    })
  ).data
}

// makeFolder and deleteEntry take bot for a chat not made yet, whose folder "bot" is that Bot's (issue #357).
export async function makeFolder(chat: string, workspace: string, path: string, bot?: string) {
  return (
    await makeWorkspaceFolder({
      path: where(chat, workspace),
      ...(bot && { query: { bot } }),
      body: { path },
    })
  ).data
}

export async function moveEntry(chat: string, workspace: string, path: string, to: string) {
  return (await moveWorkspaceEntry({ path: where(chat, workspace), body: { path, to } })).data
}

export async function deleteEntry(chat: string, workspace: string, path: string, bot?: string) {
  await deleteWorkspaceEntry({
    path: where(chat, workspace),
    query: { path, ...(bot && { bot }) },
  })
}

// refreshFolder reads a folder's lists again after a change made here.
export function refreshFolder(queryClient: QueryClient, chat: string, workspace: string) {
  void queryClient.invalidateQueries({ queryKey: [...workspaceKey, chat, workspace] })
}

// rawURL is where a file's bytes are downloaded, as an attachment.
export function rawURL(chat: string, workspace: string, path: string) {
  const base = client.getConfig().baseUrl ?? ""

  return `${base}/chats/${encodeURIComponent(chat)}/workspaces/${encodeURIComponent(workspace)}/raw?path=${encodeURIComponent(path)}`
}

const images: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  avif: "image/avif",
  ico: "image/x-icon",
  bmp: "image/bmp",
}

const extension = (path: string) => baseName(path).split(".").pop()?.toLowerCase() ?? ""

// imageType is an image file's type by its name, or undefined. The server sends every file as opaque bytes; the
// page gives them their type, in an <img>, where an SVG's script does not run.
export const imageType = (path: string) => images[extension(path)]

export const isMarkdown = (path: string) => ["md", "markdown", "mdx"].includes(extension(path))

// useImage is an image's bytes as an object URL, for as long as it is shown (Orca's useLocalImageSrc, Omnigent's
// CodeViewer: bytes, a Blob, a URL revoked after).
// rawKey is the query of a file's bytes, which an event does not refresh (isFileQuery).
export const rawKey = (chat: string, workspace: string, path: string) =>
  key(chat, workspace, "raw", path)

export function useImage(chat: string, workspace: string, path: string, enabled = true) {
  return useQuery({
    queryKey: rawKey(chat, workspace, path),
    queryFn: async () => {
      const bytes = (
        await readWorkspaceRaw({
          path: where(chat, workspace),
          query: { path },
          parseAs: "blob",
        })
      ).data as Blob

      return new Blob([bytes], { type: imageType(path) ?? "application/octet-stream" })
    },
    enabled: enabled && imageType(path) !== undefined,
    staleTime: 30_000,
  })
}

// Tabs with text not saved yet, by their tab's key, so that closing one asks first (Orca marks such a tab).
const unsaved = new Set<string>()
const listeners = new Set<() => void>()

export function setUnsaved(tab: string, value: boolean) {
  if (unsaved.has(tab) === value) return
  if (value) unsaved.add(tab)
  else unsaved.delete(tab)
  for (const listener of listeners) listener()
}

export const isUnsaved = (tab: string) => unsaved.has(tab)

export function useUnsaved(tab: string) {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)

      return () => listeners.delete(listener)
    },
    () => unsaved.has(tab),
  )
}

// useObjectURL is a blob: URL for bytes, revoked when they change or are no longer shown.
export function useObjectURL(blob: Blob | undefined) {
  const url = useMemo(() => (blob ? URL.createObjectURL(blob) : null), [blob])
  useEffect(() => () => (url ? URL.revokeObjectURL(url) : undefined), [url])

  return url
}

// inFolder is the path of the folder that a link in the file at `from` names, or null when it is an address or
// leaves the folder (Orca's useLocalImageSrc resolves a document's image against its folder).
export function inFolder(from: string, src: string): string | null {
  if (src === "" || /^[a-z][a-z0-9+.-]*:/i.test(src) || src.startsWith("//")) return null

  const target = src.split(/[?#]/)[0] ?? ""
  const parts = target.startsWith("/") ? [] : from.split("/").slice(0, -1)

  for (const part of target.split("/")) {
    if (part === "" || part === ".") continue
    if (part === "..") {
      if (parts.length === 0) return null
      parts.pop()
    } else {
      parts.push(decodeURIComponent(part))
    }
  }

  return parts.length > 0 ? parts.join("/") : null
}
