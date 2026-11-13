import { useState, useSyncExternalStore } from "react"

import "@/api/client"
import { listWorkspaceDirectory } from "@/api/generated/sdk.gen"
import type { ChatAttachment } from "@/api/generated/types.gen"
import { deleteEntry, imageType, makeFolder, problemOf } from "@/features/files/files-state"
import { commitUpload, maxUpload, sendPieces } from "@/features/files/upload"

// The files attached in a chat's composer (issue #343), taken in as t3code's useComposerAttachmentIntake.ts does
// (MIT, Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code): each file is checked when it is
// picked, dropped or pasted, a file it refuses is said and not added, and an accepted one starts uploading at once
// (attachmentUploadQueue.ts); Send waits for the uploads in flight and refuses while one has failed
// (useComposerSend.ts, awaitAttachmentUploads). Removing a file that went up deletes it again, as t3code releases
// a persisted upload. The upload is the Files panel's (#315, files/upload.ts): in pieces, staged, then renamed.
//
// A file goes into the chat's folder of attachments in its Bot's folder, apart from the work, as Orca keeps a
// session's attachments in agent-session-attachments and OpenMausBot in DATA_DIR/attachments; a name already there
// gets a number, "report (2).pdf".

export const botFolder = "bot"

export const attachmentsDir = (chat: string) => `attachments/${chat}`

export type Attachment = {
  id: string
  file: File
  // name is the file's name as the owner picked it; path is where it goes in the Bot's folder, once named.
  name: string
  path: string
  size: number
  type: string
  sent: number
  state: "uploading" | "done" | "failed"
  problem?: string
  // preview is an image's bytes as a blob: URL, for its chip.
  preview?: string
}

// numberedName is name, or with the first number after it that is not taken: "report.pdf", "report (2).pdf".
export function numberedName(name: string, taken: ReadonlySet<string>): string {
  if (!taken.has(name)) return name

  const dot = name.lastIndexOf(".")
  const [stem, ext] = dot > 0 ? [name.slice(0, dot), name.slice(dot)] : [name, ""]
  for (let n = 2; ; n++) {
    const next = `${stem} (${n})${ext}`
    if (!taken.has(next)) return next
  }
}

// formatSize is a file's size as t3code's formatAttachmentSize says it: bytes, KB or MB.
export function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`

  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

const mib = (bytes: number) => `${Math.round(bytes / (1 << 20))} MiB`

// refusal says why a file is not taken, or null: t3code refuses an empty file and one over its limit.
export function refusal(file: File): string | null {
  if (file.size > maxUpload) return `${file.name} is larger than ${mib(maxUpload)}.`
  if (file.size === 0) return `${file.name} is empty or could not be read.`

  return null
}

// What a draft needs of the Bot's folder, so that a test can stand in for it.
export type Folder = {
  // names lists what is in the chat's folder of attachments, made first when it is not there.
  names: () => Promise<string[]>
  upload: (
    path: string,
    file: File,
    progress: (sent: number) => void,
    signal: AbortSignal,
  ) => Promise<void>
  remove: (path: string) => Promise<void>
}

// A commit over a name that came meanwhile is tried under the next number, this many times.
const tries = 3

// AttachmentDraft holds the files of one composer, outside React, as t3code's upload queue does.
export class AttachmentDraft {
  private items: Attachment[] = []
  private listeners = new Set<() => void>()
  private jobs = new Map<string, { controller: AbortController; settled: Promise<void> }>()
  // given holds the names given out here, which the folder may not list yet.
  private given = new Set<string>()
  private there: Promise<Set<string>> | null = null

  private readonly chat: string
  private readonly folder: Folder

  constructor(chat: string, folder: Folder) {
    this.chat = chat
    this.folder = folder
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)

    return () => void this.listeners.delete(listener)
  }

  snapshot = () => this.items

  private set(items: Attachment[]) {
    this.items = items
    for (const listener of this.listeners) listener()
  }

  private update(id: string, change: Partial<Attachment>) {
    this.set(this.items.map((item) => (item.id === id ? { ...item, ...change } : item)))
  }

  // add takes files in and starts their uploads; it returns why a file was not taken, or null.
  add(files: File[]): string | null {
    let problem: string | null = null
    const added: Attachment[] = []

    for (const file of files) {
      const refused = refusal(file)
      if (refused) {
        problem = refused
        continue
      }
      const type = file.type || imageType(file.name) || ""
      added.push({
        id: crypto.randomUUID(),
        file,
        name: file.name || "file",
        path: "",
        size: file.size,
        type,
        sent: 0,
        state: "uploading",
        ...(type.startsWith("image/") && { preview: URL.createObjectURL(file) }),
      })
    }

    if (added.length > 0) this.set([...this.items, ...added])
    for (const item of added) this.start(item)

    return problem
  }

  private names() {
    this.there ??= this.folder.names().then(
      (names) => new Set(names),
      (error: unknown) => {
        this.there = null
        throw error
      },
    )

    return this.there
  }

  private start(item: Attachment) {
    const controller = new AbortController()
    const settled = this.send(item, controller.signal)
      .then(() => this.update(item.id, { state: "done", sent: item.size }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return
        this.update(item.id, { state: "failed", problem: problemOf(error).message })
      })
      .finally(() => this.jobs.delete(item.id))
    this.jobs.set(item.id, { controller, settled })
  }

  private async send(item: Attachment, signal: AbortSignal) {
    const there = await this.names()

    for (let attempt = 1; ; attempt++) {
      const name = numberedName(item.name, new Set([...there, ...this.given]))
      this.given.add(name)
      const path = `${attachmentsDir(this.chat)}/${name}`
      this.update(item.id, { path, sent: 0 })

      try {
        await this.folder.upload(path, item.file, (sent) => this.update(item.id, { sent }), signal)

        return
      } catch (error) {
        // A file of that name came meanwhile: the next number.
        if (problemOf(error).code !== "conflict" || attempt >= tries) throw error
      }
    }
  }

  // remove takes a file out: an upload in flight is stopped, and one that went up is deleted again.
  remove(id: string) {
    const item = this.items.find((each) => each.id === id)
    if (!item) return

    const job = this.jobs.get(id)
    if (job) job.controller.abort()
    else if (item.state === "done") void this.folder.remove(item.path).catch(() => undefined)
    if (item.preview) URL.revokeObjectURL(item.preview)
    this.set(this.items.filter((each) => each.id !== id))
  }

  // ready waits for the uploads in flight and returns the files to send with the message. It refuses while a file
  // was not uploaded, which the owner removes or attaches again (t3code: "Retry or remove failed uploads").
  async ready(): Promise<ChatAttachment[]> {
    await Promise.all([...this.jobs.values()].map((job) => job.settled))

    const failed = this.items.find((item) => item.state === "failed")
    if (failed) {
      throw { message: `${failed.name} was not uploaded. Remove it, or attach it again.` }
    }

    return this.items.map((item) => ({
      name: item.name,
      path: item.path,
      size: item.size,
      ...(item.type && { type: item.type }),
    }))
  }

  // clear lets the files go with the message they were sent with; they stay in the folder.
  clear() {
    for (const item of this.items) if (item.preview) URL.revokeObjectURL(item.preview)
    this.set([])
  }
}

// botFolderOf is a chat's Bot's folder through the client API: its folder of attachments is made as needed with the
// folder call, and a file goes up as the Files panel uploads it.
function botFolderOf(chat: string): Folder {
  const where = { chat_id: chat, workspace_id: botFolder }

  return {
    names: async () => {
      for (const dir of ["attachments", attachmentsDir(chat)]) {
        try {
          await makeFolder(chat, botFolder, dir)
        } catch (error) {
          // Made before.
          if (problemOf(error).code !== "conflict") throw error
        }
      }

      const names: string[] = []
      let after: string | undefined
      for (;;) {
        const page = (
          await listWorkspaceDirectory({
            path: where,
            query: { path: attachmentsDir(chat), ...(after !== undefined && { after }) },
          })
        ).data
        names.push(...page.entries.map((entry) => entry.name))
        after = page.entries.at(-1)?.name
        if (!page.has_more || after === undefined) return names
      }
    },
    upload: async (path, file, progress, signal) => {
      const upload = await sendPieces(chat, botFolder, path, file, progress, signal)
      await commitUpload(chat, botFolder, path, upload, false)
    },
    remove: (path) => deleteEntry(chat, botFolder, path),
  }
}

// useAttachments is a composer's files, and why the last file picked, dropped or pasted was not taken.
export function useAttachments(chat: string) {
  const [draft] = useState(() => new AttachmentDraft(chat, botFolderOf(chat)))
  const items = useSyncExternalStore(draft.subscribe, draft.snapshot)
  const [problem, setProblem] = useState<string | null>(null)

  return {
    draft,
    items,
    problem,
    attach: (files: File[]) => {
      if (files.length > 0) setProblem(draft.add(files))
    },
    dismiss: () => setProblem(null),
  }
}

export type Attachments = ReturnType<typeof useAttachments>
