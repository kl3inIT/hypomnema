import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { FilePlus2, FileText } from "lucide-react"
import { useId, useState } from "react"

import {
  getMemoryNoteOptions,
  getMemoryNoteQueryKey,
  listMemoryNotesOptions,
  listMemoryNotesQueryKey,
  putMemoryNoteMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import { deleteMemoryNote } from "@/api/generated/sdk.gen"
import type { MemoryNoteInfo } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
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
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { said } from "@/lib/said"
import { since } from "@/lib/time"

// Whose memory: a Bot's, by its id, or an Agent's, by its profile's name (the API's query names it profile, issue
// #1336).
export type MemoryOf = { bot: string } | { profile: string }

// MemoryNotes is the rest of a memory's directory (issue #589): the notes its agent reads only when their subject
// comes up, listed under the memory itself, as OpenMausBot's Memory panel lists a bot's topic files under
// MEMORY.md. The agent writes them with its memory tool; the owner opens one here to read, edit or remove it, and
// writes a new one by hand.
export function MemoryNotes({ of }: { of: MemoryOf }) {
  const notes = useQuery(listMemoryNotesOptions({ query: of }))
  const [open, setOpen] = useState<string | null>(null)

  return (
    <section aria-label="Notes" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 className="text-sm font-medium">Notes</h3>
        {notes.data && (
          <span className="text-xs text-muted-foreground">
            {notes.data.data.length} of {notes.data.max_notes} notes
          </span>
        )}
        <span className="ml-auto">
          <Button variant="outline" size="sm" onClick={() => setOpen("")}>
            <FilePlus2 data-icon="inline-start" />
            New note
          </Button>
        </span>
      </div>
      <p className="text-xs text-muted-foreground">
        Longer notes, read only when their subject comes up. The memory above gives each one a line;
        the list is given with it at the start of every chat or task.
      </p>
      <ErrorAlert error={notes.error} />
      {notes.data && notes.data.data.length === 0 && (
        <p className="text-sm text-muted-foreground">No notes yet.</p>
      )}
      {notes.data && notes.data.data.length > 0 && (
        <ul aria-label="Notes" className="flex flex-col divide-y rounded-xl border">
          {notes.data.data.map((note) => (
            <NoteRow key={note.path} note={note} onOpen={() => setOpen(note.path)} />
          ))}
        </ul>
      )}
      {open !== null && (
        <NoteDialog
          of={of}
          path={open || undefined}
          maxBytes={notes.data?.max_note_bytes ?? 8192}
          onClose={() => setOpen(null)}
        />
      )}
    </section>
  )
}

// humanSize is a size as the memory tool's view writes it: 512B, 1.5K.
const humanSize = (bytes: number) => (bytes < 1024 ? `${bytes}B` : `${(bytes / 1024).toFixed(1)}K`)

function NoteRow({ note, onOpen }: { note: MemoryNoteInfo; onOpen: () => void }) {
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left hover:bg-muted/50"
      >
        <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate font-mono text-sm">{note.path}</span>
        <span className="shrink-0 text-xs text-muted-foreground">
          {humanSize(note.bytes)} · {since(note.updated_at)}
        </span>
      </button>
    </li>
  )
}

// NoteDialog opens one note to read, edit or remove, or, without a path, writes a new one.
function NoteDialog({
  of,
  path,
  maxBytes,
  onClose,
}: {
  of: MemoryOf
  path?: string
  maxBytes: number
  onClose: () => void
}) {
  const note = useQuery({
    ...getMemoryNoteOptions({ query: { ...of, path: path ?? "" } }),
    enabled: path !== undefined,
  })

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{path ?? "New note"}</DialogTitle>
          <DialogDescription>
            The agent sees a change the next time it reads this note.
          </DialogDescription>
        </DialogHeader>
        {path !== undefined && note.isPending ? (
          <DialogPanel>
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner /> Loading…
            </p>
          </DialogPanel>
        ) : path !== undefined && note.error ? (
          <DialogPanel>
            <ErrorAlert error={note.error} />
          </DialogPanel>
        ) : (
          <NoteForm
            of={of}
            path={path}
            text={note.data?.text ?? ""}
            maxBytes={maxBytes}
            onClose={onClose}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function NoteForm({
  of,
  path,
  text,
  maxBytes,
  onClose,
}: {
  of: MemoryOf
  path?: string
  text: string
  maxBytes: number
  onClose: () => void
}) {
  const pathID = useId()
  const textID = useId()
  const queryClient = useQueryClient()
  const [newPath, setNewPath] = useState("/memories/")
  const [draft, setDraft] = useState(text)
  const bytes = new TextEncoder().encode(draft).length
  const target = path ?? newPath.trim()
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: listMemoryNotesQueryKey({ query: of }) })
  const put = useMutation({
    ...putMemoryNoteMutation(),
    onSuccess: async (stored) => {
      queryClient.setQueryData(
        getMemoryNoteQueryKey({ query: { ...of, path: stored.path } }),
        stored,
      )
      await refresh()
      said(`${stored.path} saved`)
      onClose()
    },
  })

  return (
    <form
      autoComplete="off"
      className="contents"
      onSubmit={(event) => {
        event.preventDefault()
        put.mutate({ query: { ...of, path: target }, body: { text: draft } })
      }}
    >
      <DialogPanel>
        <div className="flex flex-col gap-4">
          {path === undefined && (
            <Field>
              <FieldLabel htmlFor={pathID}>Path</FieldLabel>
              <Input
                id={pathID}
                value={newPath}
                maxLength={200}
                onChange={(event) => {
                  put.reset()
                  setNewPath(event.target.value)
                }}
              />
              <FieldDescription>
                Under /memories, at most two levels: letters, digits, dots, dashes.
              </FieldDescription>
            </Field>
          )}
          <Field>
            <FieldLabel htmlFor={textID}>Text</FieldLabel>
            <Textarea
              id={textID}
              rows={14}
              value={draft}
              onChange={(event) => {
                put.reset()
                setDraft(event.target.value)
              }}
            />
            <FieldDescription>
              <span className={bytes > maxBytes ? "text-destructive" : undefined}>
                {bytes.toLocaleString("en-US")} of {maxBytes.toLocaleString("en-US")} bytes
              </span>
            </FieldDescription>
          </Field>
          <ErrorAlert error={put.error} />
        </div>
      </DialogPanel>
      <DialogFooter>
        {path !== undefined && (
          <span className="sm:mr-auto">
            <ConfirmDialog
              trigger={
                <Button type="button" variant="ghost">
                  Remove note
                </Button>
              }
              title={`Remove ${path}?`}
              consequence="The agent no longer has this note. This cannot be undone."
              action="Remove"
              run={() => deleteMemoryNote({ query: { ...of, path } })}
              onDone={async () => {
                await refresh()
                onClose()
              }}
              done={`${path} removed`}
            />
          </span>
        )}
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button
          type="submit"
          focusableWhenDisabled={put.isPending}
          disabled={put.isPending || bytes > maxBytes || (path !== undefined && draft === text)}
        >
          {put.isPending && <Spinner data-icon="inline-start" />}
          Save note
        </Button>
      </DialogFooter>
    </form>
  )
}
