import { AlertDialog } from "@base-ui/react/alert-dialog"
import { Dialog } from "@base-ui/react/dialog"
import { Menu } from "@base-ui/react/menu"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  CirclePause,
  CirclePlay,
  Ellipsis,
  Pencil,
  Power,
  PowerOff,
  SquareTerminal,
  Trash2,
} from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { drainNode, removeNode, resumeNode, updateNode } from "@/api/generated/sdk.gen"
import type { Node } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"

import { errorText, machineState, noTerminal } from "./machines-state"
import { nodesKey, useMachinePower } from "./use-machines"

const itemClass =
  "flex cursor-default items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm outline-none select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground"

// MachineActions is a machine's menu, after Beszel's ActionsButton (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table-columns.tsx): Drain
// or Resume where Beszel pauses, and Remove behind a confirmation. Changed: Base UI's Menu and AlertDialog; no
// edit or copy items; a refused request shows the server's message; a provider's machine that is on can be
// stopped, and one that is stopped is started instead of drained or resumed.
export function MachineActions({ node }: { node: Node }) {
  const queryClient = useQueryClient()
  const [removing, setRemoving] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const drained = node.status === "drained"

  const toggle = useMutation({
    mutationFn: async () =>
      drained
        ? await resumeNode({ path: { node_id: node.id } })
        : await drainNode({ path: { node_id: node.id } }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: nodesKey }),
  })
  const power = useMachinePower(node)
  const state = machineState(node)
  const unreachable = noTerminal(node)
  const refused = toggle.error ?? power.error

  return (
    <div className="flex items-center justify-end gap-2">
      {refused && (
        <span role="alert" className="max-w-56 truncate text-xs font-normal text-destructive">
          {errorText(refused)}
        </span>
      )}
      <Menu.Root>
        <Menu.Trigger
          render={<Button variant="ghost" size="icon-sm" aria-label={`Actions for ${node.name}`} />}
        >
          <Ellipsis />
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Positioner align="end" sideOffset={4} className="z-50 outline-none">
            <Menu.Popup className="min-w-36 rounded-lg border bg-popover p-1 text-popover-foreground shadow-md outline-none">
              {state === "stopped" ? (
                <Menu.Item className={itemClass} onClick={() => power.mutate(true)}>
                  <Power className="size-4" />
                  Start machine
                </Menu.Item>
              ) : (
                <Menu.Item className={itemClass} onClick={() => toggle.mutate()}>
                  {drained ? (
                    <>
                      <CirclePlay className="size-4" />
                      Resume
                    </>
                  ) : (
                    <>
                      <CirclePause className="size-4" />
                      Drain
                    </>
                  )}
                </Menu.Item>
              )}
              {node.machine?.state === "ready" && !node.machine.power && (
                <Menu.Item className={itemClass} onClick={() => power.mutate(false)}>
                  <PowerOff className="size-4" />
                  Stop machine
                </Menu.Item>
              )}
              <Menu.Item className={itemClass} onClick={() => setRenaming(true)}>
                <Pencil className="size-4" />
                Rename
              </Menu.Item>
              {unreachable === undefined ? (
                <Menu.Item
                  className={itemClass}
                  render={
                    <Link
                      to="/machines/$nodeId/terminal"
                      params={{ nodeId: node.id }}
                      target="_blank"
                      rel="noreferrer"
                    />
                  }
                >
                  <SquareTerminal className="size-4" />
                  Terminal
                </Menu.Item>
              ) : (
                <Menu.Item
                  disabled
                  title={unreachable}
                  className={`${itemClass} text-muted-foreground`}
                >
                  <SquareTerminal className="size-4" />
                  <span className="flex flex-col">
                    Terminal
                    <span className="max-w-48 text-xs whitespace-normal">{unreachable}</span>
                  </span>
                </Menu.Item>
              )}
              <Menu.Separator className="mx-1 my-1 h-px bg-border" />
              <Menu.Item className={itemClass} onClick={() => setRemoving(true)}>
                <Trash2 className="size-4" />
                Remove
              </Menu.Item>
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>

      <RemoveMachine node={node} open={removing} onOpenChange={setRemoving} />
      {renaming && <RenameMachine node={node} onClose={() => setRenaming(false)} />}
    </div>
  )
}

// RemoveMachine is the confirmation before a machine is removed, and the removal. A refusal shows the server's
// message and keeps the dialog open.
export function RemoveMachine({
  node,
  open,
  onOpenChange,
}: {
  node: Node
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const queryClient = useQueryClient()
  const remove = useMutation({
    mutationFn: async () => await removeNode({ path: { node_id: node.id } }),
    onSuccess: async () => {
      onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: nodesKey })
    },
  })

  function change(next: boolean) {
    if (!next) remove.reset()
    onOpenChange(next)
  }

  return (
    <AlertDialog.Root open={open} onOpenChange={change}>
      <AlertDialog.Portal>
        <AlertDialog.Backdrop className="fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <AlertDialog.Popup className="fixed top-1/2 left-1/2 z-50 flex w-[calc(100vw-2rem)] max-w-sm -translate-x-1/2 -translate-y-1/2 flex-col gap-3 rounded-xl border bg-background p-5 text-start font-normal whitespace-normal outline-none">
          <AlertDialog.Title className="text-sm font-medium">Remove {node.name}?</AlertDialog.Title>
          <AlertDialog.Description className="text-sm text-muted-foreground">
            {node.machine
              ? "Its machine is destroyed at the provider, with everything on it. This cannot be undone."
              : "Its tokens stop working and it can take no more work. This cannot be undone."}
          </AlertDialog.Description>
          {remove.error && (
            <p role="alert" className="text-sm text-destructive">
              {errorText(remove.error)}
            </p>
          )}
          <div className="flex justify-end gap-2 pt-1">
            <AlertDialog.Close render={<Button variant="outline" />}>Cancel</AlertDialog.Close>
            <Button
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {remove.isPending ? "Removing…" : "Remove"}
            </Button>
          </div>
        </AlertDialog.Popup>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  )
}

// RenameMachine is the dialog that gives a machine another name, where Beszel's Edit opens its system dialog.
// Only the name changes: the machine keeps its token and what it runs. A refusal, such as a name another machine
// has, shows the server's message and keeps the dialog open.
function RenameMachine({ node, onClose }: { node: Node; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(node.name)
  const rename = useMutation({
    mutationFn: async () =>
      await updateNode({ path: { node_id: node.id }, body: { name: name.trim() } }),
    onSuccess: async () => {
      onClose()
      await queryClient.invalidateQueries({ queryKey: nodesKey })
    },
  })
  const unchanged = name.trim() === "" || name.trim() === node.name

  return (
    <Dialog.Root open onOpenChange={(open) => !open && onClose()}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Popup className="fixed top-1/2 left-1/2 z-50 flex w-[calc(100vw-2rem)] max-w-sm -translate-x-1/2 -translate-y-1/2 flex-col gap-4 rounded-xl border bg-background p-5 text-start font-normal whitespace-normal outline-none">
          <form
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              if (!unchanged) rename.mutate()
            }}
          >
            <Dialog.Title className="text-sm font-medium">Rename {node.name}</Dialog.Title>
            <label className="flex flex-col gap-1.5 text-sm">
              Name
              <input
                required
                maxLength={64}
                autoComplete="off"
                value={name}
                onChange={(event) => setName(event.target.value)}
                className="h-9 rounded-md border border-input bg-background px-3 text-sm"
              />
              <span className="text-xs text-muted-foreground">
                Only the name changes. The machine keeps its token and what it runs.
              </span>
            </label>
            {rename.error && (
              <p role="alert" className="text-sm text-destructive">
                {errorText(rename.error)}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Dialog.Close render={<Button variant="outline" />}>Cancel</Dialog.Close>
              <Button type="submit" disabled={rename.isPending || unchanged}>
                {rename.isPending ? "Renaming…" : "Rename"}
              </Button>
            </div>
          </form>
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
