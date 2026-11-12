import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, Copy } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import { createNode } from "@/api/generated/sdk.gen"
import type { CreatedNode, Node } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

import { errorText, machineProblem } from "./machines-state"
import { nodesKey, providersKey, useMachineProviders } from "./use-machines"

// own is the choice of a machine the owner installs the node on; any other choice is a provider's name.
const own = ""

// AddMachine is the "Add machine" button and its dialog. A machine of the owner's own gets a name and, if wanted,
// how many Dispatches it runs at once; then the node's token, which the server gives once, and the line that
// installs the node. The token lives in this dialog only: closing it forgets it. Where the server has machine
// providers, the dialog also offers a new machine at one of them, which installs the node itself and needs no token.
export function AddMachine() {
  const queryClient = useQueryClient()
  const formId = useId()
  const [open, setOpen] = useState(false)
  const [where, setWhere] = useState(own)
  const [name, setName] = useState("")
  const [max, setMax] = useState("")
  const [region, setRegion] = useState("")
  const [size, setSize] = useState("")
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
            ...(provider && region.trim() ? { region: region.trim() } : {}),
            ...(provider && size.trim() ? { size: size.trim() } : {}),
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
      setRegion("")
      setSize("")
      create.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={<Button />}>Add machine</DialogTrigger>
      {/* Wide only once it shows the install line, which a narrow dialog would wrap. */}
      <DialogContent size={create.data && !create.data.node.machine ? "lg" : "default"}>
        {create.data ? (
          create.data.node.machine ? (
            <Requested node={create.data.node} />
          ) : (
            <Created created={create.data} />
          )
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Add a machine</DialogTitle>
            </DialogHeader>
            <DialogPanel>
              <form
                id={formId}
                className="flex flex-col gap-4"
                onSubmit={(event) => {
                  event.preventDefault()
                  create.mutate()
                }}
              >
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
                <Field>
                  <FieldLabel htmlFor="machine-name">Name</FieldLabel>
                  <Input
                    id="machine-name"
                    required
                    autoComplete="off"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                  />
                </Field>
                {provider && (
                  <Field>
                    <div className="grid grid-cols-2 gap-3">
                      <Field>
                        <FieldLabel htmlFor="machine-size">Size</FieldLabel>
                        <Input
                          id="machine-size"
                          autoComplete="off"
                          spellCheck={false}
                          placeholder={provider.size}
                          value={size}
                          onChange={(event) => setSize(event.target.value)}
                        />
                      </Field>
                      <Field>
                        <FieldLabel htmlFor="machine-region">Region</FieldLabel>
                        <Input
                          id="machine-region"
                          autoComplete="off"
                          spellCheck={false}
                          placeholder={provider.region}
                          value={region}
                          onChange={(event) => setRegion(event.target.value)}
                        />
                      </Field>
                    </div>
                    <FieldDescription>
                      The provider&apos;s own names, such as t3.large. Leave empty for the defaults
                      shown.
                    </FieldDescription>
                  </Field>
                )}
                <Field>
                  <FieldLabel htmlFor="machine-max">Max Dispatches</FieldLabel>
                  <Input
                    id="machine-max"
                    type="number"
                    min={1}
                    step={1}
                    inputMode="numeric"
                    placeholder="Default"
                    value={max}
                    onChange={(event) => setMax(event.target.value)}
                  />
                  <FieldDescription>
                    How many Dispatches it runs at once. Leave empty for the default.
                  </FieldDescription>
                </Field>
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
                  <Alert variant="destructive">
                    <AlertDescription>{errorText(create.error)}</AlertDescription>
                  </Alert>
                )}
              </form>
            </DialogPanel>
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
              <Button
                type="submit"
                form={formId}
                disabled={create.isPending || name.trim() === "" || full}
              >
                {submitText(provider !== undefined, create.isPending)}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
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
      <DialogTitle>
        {problem ? `${node.name} was not created` : `${node.name} is being created`}
      </DialogTitle>
      {problem ? (
        <>
          <p role="alert" className="text-sm break-words text-destructive">
            {problem}
          </p>
          <DialogDescription>
            It is listed as not created. Remove it there, then try again.
          </DialogDescription>
        </>
      ) : (
        <DialogDescription>
          The machine installs the node itself and comes online in about five minutes. Until then it
          is listed as creating.
        </DialogDescription>
      )}
      <DialogFooter>
        <DialogClose render={<Button />}>Done</DialogClose>
      </DialogFooter>
    </>
  )
}

function Created({ created }: { created: CreatedNode }) {
  const install = `curl -fsSL ${window.location.origin}/install/node.sh | sh`

  return (
    <>
      <DialogTitle>{created.node.name} was added</DialogTitle>
      <DialogDescription>
        Run the install line on the machine and paste the token when it asks. The token is not shown
        again: copy it now.
      </DialogDescription>
      <Copyable label="Token" value={created.token ?? ""} />
      <Copyable label="Install line" value={install} />
      <DialogFooter>
        <DialogClose render={<Button />}>Done</DialogClose>
      </DialogFooter>
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
