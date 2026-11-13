import { useState, type ReactNode } from "react"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { errorText } from "@/features/agents/profiles-state"
import { memoryEntries, memoryUsed } from "@/features/bots/bots-state"

// MemoryEditor is a memory kept by agents, an entry a line, that the owner reads and edits by hand, as the owner
// of an agent of OpenClaw, Hermes or OpenMausBot edits its memory file: a Bot's, an agent's shared by its workers,
// and the one about the owner (issue #21). The agents write it while the owner reads: a field he has not touched
// follows what the server holds; one he is editing is kept, and says the memory changed under it. Saving replaces
// the whole memory, as OpenMausBot's "Overwrite with mine".
export function MemoryEditor({
  id,
  label,
  memory,
  limit,
  writer,
  help,
  save,
}: {
  id: string
  label: string
  // memory is what the server holds now; limit is the most characters it takes.
  memory: string
  limit: number
  // writer says who else writes it, for the line shown when it changed while the owner was editing.
  writer: string
  help: ReactNode
  // save stores the owner's text and resolves to the memory as kept.
  save: {
    mutate: (text: string, options: { onSuccess: (stored: string) => void }) => void
    isPending: boolean
    error: unknown
    reset: () => void
  }
}) {
  // base is the memory the server holds, as last read or saved; draft is what the owner sees and types.
  const [base, setBase] = useState(memory)
  const [draft, setDraft] = useState(memory)

  const dirty = draft !== base
  const changedUnder = memory !== base
  if (changedUnder && !dirty) {
    setBase(memory)
    setDraft(memory)
  }

  const used = memoryUsed(dirty ? memoryEntries(draft).join("\n") : memory)
  const over = used > limit

  return (
    <form
      aria-labelledby={id}
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate(draft, {
          onSuccess: (stored) => {
            setBase(stored)
            setDraft(stored)
          },
        })
      }}
    >
      <div className="flex flex-wrap items-baseline gap-x-3">
        <label id={id} htmlFor={`${id}-text`} className="text-sm font-medium">
          {label}
        </label>
        <span className={over ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
          {used.toLocaleString("en-US")} of {limit.toLocaleString("en-US")} characters
        </span>
      </div>
      <Textarea
        id={`${id}-text`}
        rows={Math.min(12, Math.max(3, memoryEntries(draft).length + 1))}
        placeholder="Nothing kept yet."
        value={draft}
        onChange={(event) => {
          save.reset()
          setDraft(event.target.value)
        }}
      />
      <p className="text-xs text-muted-foreground">{help}</p>
      {changedUnder && dirty && (
        <p className="text-xs text-muted-foreground">
          {writer} changed this memory while you were editing. Saving replaces it with what is here.
        </p>
      )}
      {save.error ? (
        <Alert variant="destructive">
          <AlertDescription>{errorText(save.error)}</AlertDescription>
        </Alert>
      ) : null}
      {dirty && (
        <div className="flex flex-wrap items-center justify-end gap-2">
          {!save.isPending && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                save.reset()
                setBase(memory)
                setDraft(memory)
              }}
            >
              Discard changes
            </Button>
          )}
          <Button type="submit" disabled={save.isPending || over}>
            {save.isPending ? "Saving…" : "Save memory"}
          </Button>
        </div>
      )}
    </form>
  )
}
