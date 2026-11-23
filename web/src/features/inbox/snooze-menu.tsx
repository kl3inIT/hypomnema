import { AlarmClock } from "lucide-react"
import { useId, useState, type ReactElement } from "react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"

import { snoozePresets, snoozeWords } from "./inbox-state"

// SnoozeMenu hides a to-do until a time, after Orca's snooze menu (bot-item-snooze.ts) and Chatwoot's snooze
// options: Later today, Tomorrow, Monday, Next week, each with when it comes back, and Pick a time, asked in a
// dialog with the browser's own date and time field.
export function SnoozeMenu({
  trigger,
  onSnooze,
}: {
  trigger: ReactElement
  onSnooze: (until: number) => void
}) {
  const [picking, setPicking] = useState(false)
  // The presets are reckoned when the menu opens, from that moment.
  const [now, setNow] = useState(0)

  return (
    <>
      <DropdownMenu onOpenChange={(open) => open && setNow(Date.now())}>
        <DropdownMenuTrigger render={trigger} />
        <DropdownMenuContent align="end" width="content">
          {snoozePresets(now).map((p) => (
            <DropdownMenuItem key={p.id} onClick={() => onSnooze(p.at)}>
              {p.name}
              <DropdownMenuShortcut>{snoozeWords(p.at, now)}</DropdownMenuShortcut>
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setPicking(true)}>Pick a time…</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {picking && (
        <PickTime
          now={now}
          onClose={() => setPicking(false)}
          onPick={(at) => {
            setPicking(false)
            onSnooze(at)
          }}
        />
      )}
    </>
  )
}

// local is a moment as a datetime-local field writes it, in the owner's own time.
function local(at: number) {
  const d = new Date(at - new Date(at).getTimezoneOffset() * 60_000)

  return d.toISOString().slice(0, 16)
}

function PickTime({
  now,
  onPick,
  onClose,
}: {
  now: number
  onPick: (at: number) => void
  onClose: () => void
}) {
  const id = useId()
  const [value, setValue] = useState(() => local(snoozePresets(now)[1]!.at))
  const at = value ? new Date(value).getTime() : NaN
  const ok = Number.isFinite(at) && at > now

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent size="sm">
        <form
          className="contents"
          onSubmit={(event) => {
            event.preventDefault()
            if (ok) onPick(at)
          }}
        >
          <DialogHeader>
            <DialogTitle>Snooze until</DialogTitle>
          </DialogHeader>
          <DialogPanel>
            <Field>
              <FieldLabel htmlFor={id}>Comes back at</FieldLabel>
              <Input
                id={id}
                type="datetime-local"
                value={value}
                min={local(now)}
                onChange={(event) => setValue(event.target.value)}
              />
            </Field>
          </DialogPanel>
          <DialogFooter>
            <DialogClose render={<Button type="button" variant="ghost" />}>Cancel</DialogClose>
            <Button type="submit" disabled={!ok}>
              <AlarmClock data-icon="inline-start" />
              Snooze
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
