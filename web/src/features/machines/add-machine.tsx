import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, Copy } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import { createNode } from "@/api/generated/sdk.gen"
import type { CreatedNode, Node } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"

import { errorText, machineProblem } from "./machines-state"
import { nodesKey, providersKey, useMachineProviders } from "./use-machines"

const inputClass = "h-9 rounded-md border border-input bg-background px-3 text-sm"

// own is the choice of a machine the owner installs the node on; any other choice is a provider's name.
const own = ""

// AddMachine is the "Add machine" button and its dialog. A machine of the owner's own gets a name and, if wanted,
// how many Dispatches it runs at once; then the node's token, which the server gives once, and the line that
// installs the node. The token lives in this dialog only: closing it forgets it. Where the server has machine
// providers, the dialog also offers a new machine at one of them, which installs the node itself and needs no token.
export function AddMachine() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [where, setWhere] = useState(own)
  const [name, setName] = useState("")
  const [max, setMax] = useState("")
  const providers = useMachineProviders(open).data ?? []
  const provider = providers.find((p) => p.name === where)
  const full = provider !== undefined && provider.machines >= provider.max_machines

  const create = useMutation({
    mutationFn: async () =>
      (
        await createNode({
          body: {
            name: name.trim(),
            ...(max ? { max_dispatches: Number(max) } : {}),
            ...(provider ? { provider: provider.name } : {}),
          },
        })
      ).data,
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: nodesKey }),
        queryClient.invalidateQueries({ queryKey: providersKey }),
      ]),
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setWhere(own)
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
            create.data.node.machine ? (
              <Requested node={create.data.node} />
            ) : (
              <Created created={create.data} />
            )
          ) : (
            <form
              className="flex flex-col gap-4"
              onSubmit={(event) => {
                event.preventDefault()
                create.mutate()
              }}
            >
              <Dialog.Title className="text-sm font-medium">Add a machine</Dialog.Title>
              {providers.length > 0 && (
                <fieldset className="flex flex-col gap-1.5 text-sm">
                  <legend className="mb-1.5">Where</legend>
                  <Choice
                    value={own}
                    chosen={where}
                    onChoose={setWhere}
                    title="A machine of your own"
                    detail="You run one install line on it."
                  />
                  {providers.map((p) => (
                    <Choice
                      key={p.id}
                      value={p.name}
                      chosen={where}
                      onChoose={setWhere}
                      title={`A new machine at ${p.name}`}
                      detail={`${p.size} in ${p.region} · ${p.machines} of ${p.max_machines} machines`}
                    />
                  ))}
                </fieldset>
              )}
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
              {provider && !full && (
                <p className="text-xs text-muted-foreground">
                  The provider bills for the machine from now until you remove it.
                </p>
              )}
              {full && (
                <p className="text-sm text-muted-foreground">
                  {provider.name} is at its limit of {provider.max_machines} machines. Remove one
                  first.
                </p>
              )}
              {create.error && (
                <p role="alert" className="text-sm text-destructive">
                  {errorText(create.error)}
                </p>
              )}
              <div className="flex justify-end gap-2">
                <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
                <Button type="submit" disabled={create.isPending || name.trim() === "" || full}>
                  {submitText(provider !== undefined, create.isPending)}
                </Button>
              </div>
            </form>
          )}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function submitText(atProvider: boolean, pending: boolean) {
  if (atProvider) return pending ? "Creating…" : "Create"

  return pending ? "Adding…" : "Add"
}

// Choice is one answer to "Where": a radio named by its title and described by a line of detail.
function Choice({
  value,
  chosen,
  onChoose,
  title,
  detail,
}: {
  value: string
  chosen: string
  onChoose: (value: string) => void
  title: string
  detail: string
}) {
  const id = useId()

  return (
    <div className="flex items-start gap-2.5 rounded-md border px-3 py-2 has-checked:border-foreground/40 has-checked:bg-muted/50">
      <input
        id={id}
        type="radio"
        name="where"
        value={value}
        checked={chosen === value}
        onChange={() => onChoose(value)}
        aria-describedby={`${id}-detail`}
        className="mt-1"
      />
      <div className="flex min-w-0 flex-col">
        <label htmlFor={id} className="cursor-pointer">
          {title}
        </label>
        <span id={`${id}-detail`} className="text-xs text-muted-foreground">
          {detail}
        </span>
      </div>
    </div>
  )
}

// Requested is the answer for a provider's machine: it is being created, or the provider refused and said why.
function Requested({ node }: { node: Node }) {
  const problem = machineProblem(node)

  return (
    <>
      <Dialog.Title className="text-sm font-medium">
        {problem ? `${node.name} was not created` : `${node.name} is being created`}
      </Dialog.Title>
      {problem ? (
        <>
          <p role="alert" className="text-sm break-words text-destructive">
            {problem}
          </p>
          <Dialog.Description className="text-sm text-muted-foreground">
            It is listed as not created. Remove it there, then try again.
          </Dialog.Description>
        </>
      ) : (
        <Dialog.Description className="text-sm text-muted-foreground">
          The machine installs the node itself and comes online in about five minutes. Until then it
          is listed as creating.
        </Dialog.Description>
      )}
      <div className="flex justify-end">
        <Dialog.Close render={<Button />}>Done</Dialog.Close>
      </div>
    </>
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
      <Copyable label="Token" value={created.token ?? ""} />
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
