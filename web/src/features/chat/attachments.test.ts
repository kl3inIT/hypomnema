import { afterEach, beforeEach, expect, test, vi } from "vitest"

import { AttachmentDraft, numberedName, type Folder } from "./attachments"

// A Bot's folder in memory: what is in the chat's folder of attachments, the uploads begun, and what was removed.
// An upload waits until the test lets it end, so that a remove can come while it is in flight.
function fakeFolder(there: string[] = []) {
  const uploads: { path: string; signal: AbortSignal; end: (error?: unknown) => void }[] = []
  const removed: string[] = []
  const folder: Folder = {
    names: async () => there,
    upload: (path, _file, progress, signal) =>
      new Promise((resolve, reject) => {
        progress(1)
        uploads.push({ path, signal, end: (error) => (error ? reject(error) : resolve()) })
      }),
    remove: async (path) => void removed.push(path),
  }

  return { folder, uploads, removed }
}

const file = (name: string, bytes = 3, type = "") => new File(["x".repeat(bytes)], name, { type })

// waitUploads lets the draft's promises run until n uploads have begun.
async function begun(uploads: unknown[], n: number) {
  await vi.waitFor(() => expect(uploads).toHaveLength(n))
}

beforeEach(() => {
  // jsdom has no blob: URLs; an image's chip only needs one to exist.
  URL.createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:preview")
  URL.revokeObjectURL = vi.fn<(url: string) => void>()
})

afterEach(() => vi.restoreAllMocks())

// Scenario: a file of a name already in the folder, or given to another file of this message, takes the next
// number before its extension, as "report (2).pdf"; a name without one takes it at its end.
test("a name already there gets a number", () => {
  expect(numberedName("report.pdf", new Set())).toBe("report.pdf")
  expect(numberedName("report.pdf", new Set(["report.pdf"]))).toBe("report (2).pdf")
  expect(numberedName("report.pdf", new Set(["report.pdf", "report (2).pdf"]))).toBe(
    "report (3).pdf",
  )
  expect(numberedName("notes", new Set(["notes"]))).toBe("notes (2)")
  expect(numberedName(".env", new Set([".env"]))).toBe(".env (2)")
})

// Scenario: the owner attaches two files called report.pdf to a chat whose folder has one. Each goes up under the
// next free number in the chat's folder, and Send gets both with the names the owner picked.
test("each file goes up under a free name, and Send gets them all", async () => {
  const { folder, uploads } = fakeFolder(["report.pdf"])
  const draft = new AttachmentDraft("cht_1", folder)

  expect(draft.add([file("report.pdf", 3, "application/pdf"), file("report.pdf", 5)])).toBeNull()
  await begun(uploads, 2)
  expect(uploads.map((u) => u.path)).toEqual([
    "attachments/cht_1/report (2).pdf",
    "attachments/cht_1/report (3).pdf",
  ])

  const ready = draft.ready()
  for (const u of uploads) u.end()
  await expect(ready).resolves.toEqual([
    {
      name: "report.pdf",
      path: "attachments/cht_1/report (2).pdf",
      size: 3,
      type: "application/pdf",
    },
    { name: "report.pdf", path: "attachments/cht_1/report (3).pdf", size: 5 },
  ])
})

// Scenario: a file of the same name came into the folder meanwhile, so the commit is refused; the file goes up
// again under the next number.
test("a name taken meanwhile is tried under the next number", async () => {
  const { folder, uploads } = fakeFolder()
  const draft = new AttachmentDraft("cht_1", folder)

  draft.add([file("a.txt")])
  await begun(uploads, 1)
  uploads[0]?.end({ code: "conflict", message: "a.txt is already there" })
  await begun(uploads, 2)
  uploads[1]?.end()

  await expect(draft.ready()).resolves.toMatchObject([{ path: "attachments/cht_1/a (2).txt" }])
})

// Scenario: the owner picks a file over 50 MiB and an empty one with a good one. The two are refused with the
// reason and never sent; the good one is taken.
test("a file over 50 MiB or empty is refused and not added", async () => {
  const { folder, uploads } = fakeFolder()
  const draft = new AttachmentDraft("cht_1", folder)
  const big = file("video.mp4")
  Object.defineProperty(big, "size", { value: 51 << 20 })

  expect(draft.add([big])).toBe("video.mp4 is larger than 50 MiB.")
  expect(draft.add([file("empty.txt", 0), file("ok.txt")])).toBe(
    "empty.txt is empty or could not be read.",
  )
  expect(draft.snapshot().map((item) => item.name)).toEqual(["ok.txt"])
  await begun(uploads, 1)
  expect(uploads[0]?.path).toBe("attachments/cht_1/ok.txt")
})

// Scenario: the owner removes a file while it uploads and another after it went up. The first upload is stopped,
// the second file is deleted again, and Send gets neither.
test("removing a file stops its upload or deletes it", async () => {
  const { folder, uploads, removed } = fakeFolder()
  const draft = new AttachmentDraft("cht_1", folder)

  draft.add([file("done.png", 3, "image/png"), file("going.txt")])
  await begun(uploads, 2)
  uploads[0]?.end()
  await vi.waitFor(() => expect(draft.snapshot()[0]?.state).toBe("done"))

  const [done, going] = draft.snapshot()
  draft.remove(going?.id ?? "")
  expect(uploads[1]?.signal.aborted).toBe(true)
  uploads[1]?.end(new DOMException("aborted", "AbortError"))

  draft.remove(done?.id ?? "")
  expect(removed).toEqual(["attachments/cht_1/done.png"])
  expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:preview")

  await expect(draft.ready()).resolves.toEqual([])
})

// Scenario: an upload fails. Send is refused, saying which file, until the owner removes it.
test("Send is refused while a file was not uploaded", async () => {
  const { folder, uploads } = fakeFolder()
  const draft = new AttachmentDraft("cht_1", folder)

  draft.add([file("a.txt")])
  await begun(uploads, 1)
  uploads[0]?.end({ code: "unavailable", message: "the machine is offline" })

  await expect(draft.ready()).rejects.toEqual({
    message: "a.txt was not uploaded. Remove it, or attach it again.",
  })
  expect(draft.snapshot()[0]).toMatchObject({ state: "failed", problem: "the machine is offline" })

  draft.remove(draft.snapshot()[0]?.id ?? "")
  await expect(draft.ready()).resolves.toEqual([])
})
