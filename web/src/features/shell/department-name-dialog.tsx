import { useId, useState } from "react"

import { ErrorAlert } from "@/components/error-alert"
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
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"

// maxName is OpenMausBot's limit on a department's name (serverError.department.nameRule), which the server checks.
const maxName = 60

// DepartmentNameDialog asks for a department's name, to make one or to rename one (issue #471). OpenMausBot names a
// department in a field in the sidebar's header (SidebarSectionHeader naming); here the same name is asked in a
// dialog, the way this web client asks for a name elsewhere (memory-notes.tsx). It stays open while the server works
// and when it refuses, a name another department has, with the reason.
export function DepartmentNameDialog({
  title,
  action,
  initial = "",
  pending,
  error,
  onSave,
  onClose,
}: {
  title: string
  action: string
  initial?: string
  pending: boolean
  error: unknown
  onSave: (name: string) => void
  onClose: () => void
}) {
  const id = useId()
  const [name, setName] = useState(initial)
  const trimmed = name.trim()

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <form
          autoComplete="off"
          className="contents"
          onSubmit={(event) => {
            event.preventDefault()
            if (trimmed) onSave(trimmed)
          }}
        >
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <DialogPanel>
            <div className="flex flex-col gap-4">
              <Field>
                <FieldLabel htmlFor={id}>Name</FieldLabel>
                <Input
                  id={id}
                  value={name}
                  maxLength={maxName}
                  onChange={(event) => setName(event.target.value)}
                />
              </Field>
              <ErrorAlert error={error} />
            </div>
          </DialogPanel>
          <DialogFooter>
            <DialogClose render={<Button type="button" variant="ghost" />}>Cancel</DialogClose>
            <Button
              type="submit"
              focusableWhenDisabled={pending}
              disabled={!trimmed || trimmed === initial || pending}
            >
              {pending && <Spinner data-icon="inline-start" />}
              {action}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
