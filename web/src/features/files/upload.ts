import { useQueryClient } from "@tanstack/react-query"
import { useRef, useState } from "react"

import "@/api/client"
import {
  abortWorkspaceUpload,
  beginWorkspaceUpload,
  commitWorkspaceUpload,
  readWorkspaceFile,
  sendWorkspaceUploadChunk,
} from "@/api/generated/sdk.gen"

import { problemOf, refreshFolder } from "./files-state"

// An upload into a chat's folder (issue #315), as Orca's staged save (src/shared/file-write/staged-file-save.ts):
// begun, sent in pieces of 384 KiB, then committed, up to 50 MiB. A commit never goes over a file that is there; the
// owner is asked, and a replace names the SHA-256 of the file it replaces (Orca's replaceWithUpload).

export const maxUpload = 50 << 20
const piece = 384 << 10

const where = (chat: string, workspace: string) => ({ chat_id: chat, workspace_id: workspace })

// base64 of bytes, a part at a time so that no call takes too many arguments.
function base64(bytes: Uint8Array) {
  let text = ""
  for (let at = 0; at < bytes.length; at += 0x8000) {
    text += String.fromCharCode(...bytes.subarray(at, at + 0x8000))
  }

  return btoa(text)
}

// sendPieces begins an upload to path and sends the file, saying how much has gone. It returns the upload, which a
// commit names.
export async function sendPieces(
  chat: string,
  workspace: string,
  path: string,
  file: Blob,
  progress: (sent: number) => void,
  signal: AbortSignal,
) {
  const begun = (
    await beginWorkspaceUpload({ path: where(chat, workspace), body: { path }, signal })
  ).data
  const upload = begun.upload ?? ""

  try {
    for (let offset = 0; offset < file.size; offset += piece) {
      signal.throwIfAborted()
      const bytes = new Uint8Array(await file.slice(offset, offset + piece).arrayBuffer())
      await sendWorkspaceUploadChunk({
        path: where(chat, workspace),
        body: { path, upload, offset, data: base64(bytes) },
        signal,
      })
      progress(Math.min(file.size, offset + piece))
    }
  } catch (error) {
    // A cancelled or failed upload leaves no file beside its place.
    void abortWorkspaceUpload({ path: where(chat, workspace), body: { path, upload } }).catch(
      () => undefined,
    )
    throw error
  }

  return upload
}

export async function commitUpload(
  chat: string,
  workspace: string,
  path: string,
  upload: string,
  replace: boolean,
) {
  let expect: string | undefined
  if (replace) {
    const now = (await readWorkspaceFile({ path: where(chat, workspace), query: { path } })).data
    if (!now.sha256) throw { code: "too_large", message: "That file is too large to be replaced." }
    expect = now.sha256
  }

  return (
    await commitWorkspaceUpload({
      path: where(chat, workspace),
      body: { path, upload, ...(expect !== undefined && { expect_sha256: expect }) },
    })
  ).data
}

export async function dropUpload(chat: string, workspace: string, path: string, upload: string) {
  await abortWorkspaceUpload({ path: where(chat, workspace), body: { path, upload } })
}

// An upload as the panel shows it (issue #315): its progress, and when a file of its name is there, the owner's
// choice to replace it or not (Orca asks before an upload replaces a file).
type Item = {
  id: string
  name: string
  path: string
  size: number
  sent: number
  state: "sending" | "asking" | "done" | "failed"
  problem?: string
  upload?: string
}

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)

export function useUploads(chat: string, workspace: string) {
  const queryClient = useQueryClient()
  const [items, setItems] = useState<Item[]>([])
  const controllers = useRef(new Map<string, AbortController>())

  const update = (id: string, change: Partial<Item>) =>
    setItems((all) => all.map((item) => (item.id === id ? { ...item, ...change } : item)))

  async function commit(item: Item, upload: string, replace: boolean) {
    try {
      await commitUpload(chat, workspace, item.path, upload, replace)
      update(item.id, { state: "done", sent: item.size })
      refreshFolder(queryClient, chat, workspace)
    } catch (error) {
      const problem = problemOf(error)
      if (problem.code === "conflict" && !replace) {
        update(item.id, { state: "asking", upload })
      } else {
        update(item.id, {
          state: "failed",
          problem:
            problem.code === "conflict" ? "It changed meanwhile; not replaced." : problem.message,
        })
      }
    }
  }

  function start(files: File[], dir: string) {
    const added = files.map((file): Item => ({
      id: crypto.randomUUID(),
      name: file.name,
      path: join(dir, file.name),
      size: file.size,
      sent: 0,
      state: file.size > maxUpload ? "failed" : "sending",
      ...(file.size > maxUpload && { problem: "Larger than 50 MiB." }),
    }))
    setItems((all) => [...all.filter((item) => item.state !== "done"), ...added])

    files.forEach((file, i) => {
      const item = added[i]
      if (!item || item.state === "failed") return
      const controller = new AbortController()
      controllers.current.set(item.id, controller)
      void sendPieces(
        chat,
        workspace,
        item.path,
        file,
        (sent) => update(item.id, { sent }),
        controller.signal,
      )
        .then((upload) => commit(item, upload, false))
        .catch((error: unknown) =>
          update(item.id, {
            state: "failed",
            problem: controller.signal.aborted ? "Cancelled." : problemOf(error).message,
          }),
        )
        .finally(() => controllers.current.delete(item.id))
    })
  }

  return {
    items,
    start,
    cancel: (id: string) => controllers.current.get(id)?.abort(),
    replace: (item: Item) => {
      if (!item.upload) return
      update(item.id, { state: "sending" })
      void commit(item, item.upload, true)
    },
    keep: (item: Item) => {
      if (item.upload)
        void dropUpload(chat, workspace, item.path, item.upload).catch(() => undefined)
      update(item.id, { state: "failed", problem: "Not uploaded: the file there is kept." })
    },
    dismiss: (id: string) => setItems((all) => all.filter((item) => item.id !== id)),
  }
}
