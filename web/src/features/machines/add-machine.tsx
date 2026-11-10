import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, Copy } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { createNode } from "@/api/generated/sdk.gen"
import type { CreatedNode } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"

import { errorText } from "./machines-state"
import { nodesKey } from "./use-machines"

const inputClass = "h-9 rounded-md border border-input bg-background px-3 text-sm"

// AddMachine is the "Add machine" button and its dialog: a name and, if wanted, how many Dispatches the machine
// runs at once; then the node's token, which the server gives once, and the line that installs the node. The token
// lives in this dialog only: closing it forgets it.
export function AddMachine() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [max, setMax] = useState("")

  const create = useMutation({
    mutationFn: async () =>
      (
        await createNode({
          body: { name: name.trim(), ...(max ? { max_dispatches: Number(max) } : {}) },
        })
      ).data,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: nodesKey }),
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setName("")
      setMax("")
      create.reset()
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Trigger render={<Button />}>Add machine</Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Popup className="fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100svh-2rem)] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 flex-col gap-4 overflow-y-auto rounded-xl border bg-background p-5 outline-none">
          {create.data ? (
            <Created created={create.data} />
          ) : (
            <form
              className="flex flex-col gap-4"
              onSubmit={(event) => {
                event.preventDefault()
                create.mutate()
              }}
            >
              <Dialog.Title className="text-sm font-medium">Add a machine</Dialog.Title>
              <label className="flex flex-col gap-1.5 text-sm">
                Name
                <input
                  required
                  autoComplete="off"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  className={inputClass}
                />
              </label>
              <label className="flex flex-col gap-1.5 text-sm">
                Max Dispatches
                <input
                  type="number"
                  min={1}
                  step={1}
                  inputMode="numeric"
                  placeholder="Default"
                  value={max}
                  onChange={(event) => setMax(event.target.value)}
                  className={inputClass}
                />
                <span className="text-xs text-muted-foreground">
                  How many Dispatches it runs at once. Leave empty for the default.
                </span>
              </label>
              {create.error && (
                <p role="alert" className="text-sm text-destructive">
                  {errorText(create.error)}
                </p>
              )}
              <div className="flex justify-end gap-2">
                <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
                <Button type="submit" disabled={create.isPending || name.trim() === ""}>
                  {create.isPending ? "Adding…" : "Add"}
                </Button>
              </div>
            </form>
          )}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function Created({ created }: { created: CreatedNode }) {
  const install = `curl -fsSL ${window.location.origin}/install/node.sh | sh`

  return (
    <>
      <Dialog.Title className="text-sm font-medium">{created.node.name} was added</Dialog.Title>
      <Dialog.Description className="text-sm text-muted-foreground">
        Run the install line on the machine and paste the token when it asks. The token is not shown
        again: copy it now.
      </Dialog.Description>
      <Copyable label="Token" value={created.token} />
      <Copyable label="Install line" value={install} />
      <div className="flex justify-end">
        <Dialog.Close render={<Button />}>Done</Dialog.Close>
      </div>
    </>
  )
}

// Copyable shows a value on one line with a button that copies it.
function Copyable({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
    } catch {
      // No clipboard (an insecure page): the value stays selectable.
    }
  }

  return (
    <div className="flex flex-col gap-1.5 text-sm">
      <span>{label}</span>
      <div className="flex items-center gap-2">
        <code
          aria-label={label}
          className="min-w-0 flex-1 overflow-x-auto rounded-md border bg-muted px-2.5 py-2 font-mono text-xs whitespace-nowrap select-all"
        >
          {value}
        </code>
        <Button
          variant="outline"
          size="icon"
          aria-label={copied ? `${label} copied` : `Copy ${label.toLowerCase()}`}
          onClick={() => void copy()}
        >
          {copied ? <Check /> : <Copy />}
        </Button>
      </div>
    </div>
  )
}
