import type { ChatAttachment } from "@/api/generated/types.gen"

// What is being written in a composer, kept for its chat in the browser across switching chats and reloading
// (issue #433), as t3code keeps a draft per thread (apps/web/src/composerDraftStore.ts, MIT, Copyright (c) 2026 T3
// Tools Inc.) and OpenMausBot a draft per chat (src/lib/drafts.ts, Apache License 2.0, Copyright 2026 Milind Soni
// and OpenMausBot contributors):
//
// - A draft is the text and the files attached that are up in the chat's folder: their place there, not their
//   bytes, as OpenMausBot keeps only upload-complete attachments and t3code a file's uploaded id. A file still
//   uploading, or one that failed, is not kept.
// - The drafts are one entry of localStorage, read once and held in memory, which answers every read when the
//   storage is not there or refuses a write (private browsing, a full quota). A write is saved a moment later, and
//   at once when the page hides or the composer goes (t3code's debounced storage flushed before unload,
//   OpenMausBot's saveDraftLater with its pagehide and blur). Saving reads the entry again and changes only the
//   drafts written here, so that another tab's are kept.
// - An empty draft is not kept, and a draft that was sent goes.
// - The New chat page and a new chat's tab keep theirs too, with the id they chose for the chat and the Bot picked,
//   since their files are in that chat's folder in that Bot's (t3code's draft thread keeps its thread id).

export type Draft = {
  text: string
  files: ChatAttachment[]
  // chat and bot are a new chat's: the id it chose and the Bot picked.
  chat?: string
  bot?: string
}

export const draftsKey = "hypomnema:chat-drafts:v1"

// The draft of a chat, of the New chat page, and of a new chat's tab in a pane.
export const chatDraft = (chat: string) => `chat:${chat}`
export const newChatDraft = "new"
export const tabDraft = (tab: string) => `new:${tab}`

const saveDelay = 300

const empty: Draft = { text: "", files: [] }

type Stored = Record<string, unknown>

const memory = new Map<string, Draft>()
const unsaved = new Set<string>()
let timer: ReturnType<typeof setTimeout> | undefined

// storage is the browser's localStorage, or nothing where reading it throws.
function storage(): Storage | undefined {
  try {
    return globalThis.localStorage ?? undefined
  } catch {
    return undefined
  }
}

function readStored(): Stored {
  try {
    const raw = storage()?.getItem(draftsKey)
    const parsed: unknown = raw ? JSON.parse(raw) : null

    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as Stored) : {}
  } catch {
    return {}
  }
}

function isFile(value: unknown): value is ChatAttachment {
  if (!value || typeof value !== "object") return false
  const { name, path, size, type } = value as Record<string, unknown>

  return (
    typeof name === "string" &&
    typeof path === "string" &&
    typeof size === "number" &&
    (type === undefined || typeof type === "string")
  )
}

// parseDraft reads a draft kept in the browser, or null when it is not one.
function parseDraft(value: unknown): Draft | null {
  if (!value || typeof value !== "object") return null
  const { text, files, chat, bot } = value as Record<string, unknown>
  if (typeof text !== "string") return null

  return {
    text,
    files: Array.isArray(files) ? files.filter(isFile) : [],
    ...(typeof chat === "string" && { chat }),
    ...(typeof bot === "string" && { bot }),
  }
}

const kept = (draft: Draft) => draft.text !== "" || draft.files.length > 0

// readDraft is the draft kept under key, or an empty one.
export function readDraft(key: string): Draft {
  const held = memory.get(key)
  if (held) return held

  const draft = parseDraft(readStored()[key]) ?? empty
  memory.set(key, draft)

  return draft
}

// writeDraft changes the draft kept under key, and saves it a moment later.
export function writeDraft(key: string, change: Partial<Draft>) {
  memory.set(key, { ...readDraft(key), ...change })
  unsaved.add(key)
  clearTimeout(timer)
  timer = setTimeout(saveDrafts, saveDelay)
}

// clearDraft lets the draft under key go, at once: it was sent.
export function clearDraft(key: string) {
  memory.set(key, empty)
  unsaved.add(key)
  saveDrafts()
}

// saveDrafts puts the drafts written since the last save in the browser. A write the storage refuses is tried
// again with the next save; meanwhile the memory answers.
export function saveDrafts() {
  clearTimeout(timer)
  timer = undefined
  if (unsaved.size === 0) return

  const stored = readStored()
  for (const key of unsaved) {
    const draft = memory.get(key)
    if (draft && kept(draft)) stored[key] = draft
    else delete stored[key]
  }
  try {
    storage()?.setItem(draftsKey, JSON.stringify(stored))
    unsaved.clear()
  } catch {
    // Kept in memory.
  }
}

const page = globalThis as { addEventListener?: Window["addEventListener"] }
page.addEventListener?.("pagehide", saveDrafts)
page.addEventListener?.("blur", saveDrafts)
