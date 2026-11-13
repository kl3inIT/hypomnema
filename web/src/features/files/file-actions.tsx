import { useQueryClient } from "@tanstack/react-query"
import { useId, useState } from "react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

import {
  baseName,
  deleteEntry,
  makeFolder,
  moveEntry,
  problemOf,
  refreshFolder,
  saveFile,
} from "./files-state"

// What the owner asked to do to a chat's folder (issue #297), as Orca's file explorer offers on a row
// (right-sidebar/file-explorer/panel/file-explorer-row-context-menu.tsx): a new file or folder in a folder, a name
// changed, a file or folder deleted. A new one never replaces what is there (Orca's `wx`); a delete is for good, as
// Orca does when there is no Trash, and asks first.
export type FileAction =
  | { kind: "new-file" | "new-folder"; dir: string }
  | { kind: "rename"; path: string; directory: boolean }
  | { kind: "delete"; path: string; directory: boolean }

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)
const parentOf = (path: string) => path.slice(0, Math.max(0, path.lastIndexOf("/")))

// A name typed for a file or folder: one name, in the folder it is made in.
const validName = (name: string) =>
  name.trim() !== "" && !/[/\\]/.test(name) && name !== "." && name !== ".."

export function FileActionDialogs({
  chat,
  workspace,
  action,
  onDone,
  onMade,
  onGone,
}: {
  chat: string
  workspace: string
  action: FileAction | null
  onDone: () => void
  // A file made is opened; a folder made is shown open.
  onMade: (path: string, kind: "file" | "directory") => void
  // A path deleted or moved: its tabs close.
  onGone: (path: string) => void
}) {
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState<string | null>(null)
  const formId = useId()

  async function run(change: () => Promise<void>) {
    setBusy(true)
    setProblem(null)
    try {
      await change()
      refreshFolder(queryClient, chat, workspace)
      onDone()
    } catch (error) {
      const refused = problemOf(error)
      setProblem(
        refused.code === "conflict" ? "Something is already there by that name." : refused.message,
      )
    } finally {
      setBusy(false)
    }
  }

  function close() {
    if (busy) return
    setProblem(null)
    onDone()
  }

  const naming = action && action.kind !== "delete" ? action : null
  const deleting = action?.kind === "delete" ? action : null

  return (
    <>
      <Dialog open={naming !== null} onOpenChange={(open) => !open && close()}>
        <DialogContent>
          {naming && (
            <form
              id={formId}
              autoComplete="off"
              className="flex flex-col gap-4"
              onSubmit={(event) => {
                event.preventDefault()
                const name = String(new FormData(event.currentTarget).get("name") ?? "").trim()
                if (!validName(name)) {
                  setProblem("Give one name, without a slash.")

                  return
                }
                if (naming.kind === "rename") {
                  const to = join(parentOf(naming.path), name)
                  void run(async () => {
                    await moveEntry(chat, workspace, naming.path, to)
                    onGone(naming.path)
                  })
                } else {
                  const path = join(naming.dir, name)
                  void run(async () => {
                    if (naming.kind === "new-file") await saveFile(chat, workspace, path, "")
                    else await makeFolder(chat, workspace, path)
                    onMade(path, naming.kind === "new-file" ? "file" : "directory")
                  })
                }
              }}
            >
              <DialogHeader>
                <DialogTitle>
                  {naming.kind === "rename"
                    ? `Rename ${baseName(naming.path)}`
                    : naming.kind === "new-file"
                      ? "New file"
                      : "New folder"}
                </DialogTitle>
                <DialogDescription>
                  {naming.kind === "rename"
                    ? `In ${parentOf(naming.path) || "the folder's top"}.`
                    : `In ${naming.dir || "the folder's top"}.`}
                </DialogDescription>
              </DialogHeader>
              <DialogPanel>
                <Input
                  name="name"
                  aria-label="Name"
                  // oxlint-disable-next-line jsx-a11y/no-autofocus -- the dialog asks for this one thing
                  autoFocus
                  defaultValue={naming.kind === "rename" ? baseName(naming.path) : ""}
                  placeholder={naming.kind === "new-folder" ? "folder" : "name.md"}
                />
                {problem && (
                  <output className="mt-2 block text-sm text-destructive">{problem}</output>
                )}
              </DialogPanel>
              <DialogFooter>
                <Button variant="outline" type="button" onClick={close}>
                  Cancel
                </Button>
                <Button type="submit" disabled={busy}>
                  {busy && <Spinner />}
                  {naming.kind === "rename" ? "Rename" : "Make"}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
      <AlertDialog open={deleting !== null} onOpenChange={(open) => !open && close()}>
        <AlertDialogContent>
          {deleting && (
            <>
              <AlertDialogHeader>
                <AlertDialogTitle>Delete {baseName(deleting.path)}?</AlertDialogTitle>
                <AlertDialogDescription>
                  {deleting.directory
                    ? "The folder and everything in it are deleted for good, on the machine that holds them. There is no Trash."
                    : "The file is deleted for good, on the machine that holds it. There is no Trash."}
                </AlertDialogDescription>
              </AlertDialogHeader>
              {problem && <output className="px-6 text-sm text-destructive">{problem}</output>}
              <AlertDialogFooter>
                <AlertDialogCancel>Keep it</AlertDialogCancel>
                <AlertDialogAction
                  variant="destructive"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await deleteEntry(chat, workspace, deleting.path)
                      onGone(deleting.path)
                    })
                  }
                >
                  {busy && <Spinner />}
                  Delete
                </AlertDialogAction>
              </AlertDialogFooter>
            </>
          )}
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
