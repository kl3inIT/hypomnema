import { useQueries, useQuery, type QueryClient } from "@tanstack/react-query"
import { useSyncExternalStore } from "react"

import { client } from "@/api/client"
import {
  diffWorkspaceFileOptions,
  diffWorkspaceFileQueryKey,
  listChatWorkspacesOptions,
  listChatWorkspacesQueryKey,
  listWorkspaceChangesOptions,
  listWorkspaceChangesQueryKey,
  listWorkspaceDirectoryOptions,
  listWorkspaceDirectoryQueryKey,
  readWorkspaceFileOptions,
  readWorkspaceFileQueryKey,
  readWorkspaceRawQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  deleteWorkspaceEntry,
  makeWorkspaceFolder,
  moveWorkspaceEntry,
  readWorkspaceRaw,
  writeWorkspaceFile,
} from "@/api/generated/sdk.gen"
import { callsOf } from "@/lib/query-keys"

// A chat's working folders, as the Files panel reads them (issue #255). As Omnigent's useWorkspaceChangedFiles.ts:
// nothing polls the lists; they are read again when an event may have changed them (work/use-work.ts refreshes every
// read of the folders but the files themselves), when the window comes back, and with Refresh. An open file of
// 256 KiB or less is read again every two seconds while it is shown (Omnigent's useFileContent).

const liveFileBytes = 256 << 10

const where = (chat: string, workspace: string) => ({ chat_id: chat, workspace_id: workspace })

export function useWorkspaces(chat: string | null) {
  return useQuery({
    ...listChatWorkspacesOptions({ path: { chat_id: chat ?? "" } }),
    select: (page) => page.data,
    enabled: chat !== null,
    staleTime: 30_000,
  })
}

export function useChanges(chat: string, workspace: string, enabled = true) {
  return useQuery({
    ...listWorkspaceChangesOptions({ path: where(chat, workspace) }),
    enabled,
    staleTime: 5_000,
  })
}

// useDirectories reads the open directories of a folder together, as Omnigent fetches its expanded directories
// centrally.
export function useDirectories(chat: string, workspace: string, dirs: string[]) {
  return useQueries({
    queries: dirs.map((path) => ({
      ...listWorkspaceDirectoryOptions({ path: where(chat, workspace), query: { path } }),
      staleTime: 5_000,
    })),
  })
}

// fileKey is the query of a file's text, which a save sets to what it wrote.
export const fileKey = (chat: string, workspace: string, path: string) =>
  readWorkspaceFileQueryKey({ path: where(chat, workspace), query: { path } })

export function useFile(chat: string, workspace: string, path: string, shown: boolean) {
  return useQuery({
    ...readWorkspaceFileOptions({ path: where(chat, workspace), query: { path } }),
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
    ...diffWorkspaceFileOptions({ path: where(chat, workspace), query: { path } }),
    staleTime: 5_000,
  })
}

// baseName is the last part of a path.
export const baseName = (path: string) => path.slice(path.lastIndexOf("/") + 1) || path

// The changes (issue #297). Each is one request; what the machine refuses comes back in its words, a save over a
// file that changed meanwhile as the code "conflict" and one over a file that went as "not_found".

// MaxWrite is the largest text saved from here: what fits the message that carries it to a node.
export const maxWrite = 1 << 20

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

// folderReads matches every cached read of the folders by its operation: the chat's folders, a folder's changes,
// directories and diffs, a file's text and bytes, and what is read from them after (a preview, an SVG's text).
const folderReads = callsOf(
  listChatWorkspacesQueryKey({ path: { chat_id: "" } }),
  listWorkspaceChangesQueryKey({ path: where("", "") }),
  listWorkspaceDirectoryQueryKey({ path: where("", "") }),
  diffWorkspaceFileQueryKey({ path: where("", "") }),
  readWorkspaceFileQueryKey({ path: where("", "") }),
  readWorkspaceRawQueryKey({ path: where("", "") }),
)

// readsOf matches the reads of a chat's folders, or of one of its folders, by the chat and folder in the generated
// key's path, as the hand-written ["workspace", chat] and ["workspace", chat, workspace] prefixes did.
function readsOf(chat: string, workspace?: string) {
  return (query: { queryKey: readonly unknown[] }) => {
    const path = (query.queryKey[0] as { path?: { chat_id?: string; workspace_id?: string } }).path

    return (
      folderReads(query) &&
      path?.chat_id === chat &&
      (workspace === undefined || path.workspace_id === workspace)
    )
  }
}

// refreshFolder reads a folder's lists again after a change made here.
export function refreshFolder(queryClient: QueryClient, chat: string, workspace: string) {
  void queryClient.invalidateQueries({ predicate: readsOf(chat, workspace) })
}

// refreshChat reads every folder of a chat again, and which folders it has.
export function refreshChat(queryClient: QueryClient, chat: string) {
  void queryClient.invalidateQueries({ predicate: readsOf(chat) })
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

// useSvgText is an SVG's text, read from its bytes, which is drawn in the locked frame rather than given a blob:
// address. It is no call of its own, so its key is the file's generated key with "svg" after it.
export function useSvgText(
  chat: string,
  workspace: string,
  path: string,
  bytes: Blob | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: [...fileKey(chat, workspace, path), "svg"],
    queryFn: () => (bytes as Blob).text(),
    enabled: enabled && bytes !== undefined,
  })
}

// useImage is an image's bytes as an object URL, for as long as it is shown (Orca's useLocalImageSrc, Omnigent's
// CodeViewer: bytes, a Blob, a URL revoked after).
// rawKey is the query of a file's bytes, which an event does not refresh (work/use-work.ts). Its image is typed
// here, so it keeps its own read under the generated key.
export const rawKey = (chat: string, workspace: string, path: string) =>
  readWorkspaceRawQueryKey({ path: where(chat, workspace), query: { path } })

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
