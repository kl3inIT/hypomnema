import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import {
  diffWorkspaceFile,
  listChatWorkspaces,
  listWorkspaceChanges,
  listWorkspaceDirectory,
  readWorkspaceFile,
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

// Whether a query of the panel is the content of a file, which an event does not refresh.
export const isFileQuery = (queryKey: readonly unknown[]) =>
  queryKey[0] === workspaceKey[0] && queryKey[3] === "file"

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

export function useFile(chat: string, workspace: string, path: string, shown: boolean) {
  return useQuery({
    queryKey: key(chat, workspace, "file", path),
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
