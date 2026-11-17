import { Copy, Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"

import type { MeetingFormat } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import { FormatEditor, type Editing } from "./format-editor"
import { useDeleteFormat, useMeetingSettings } from "./use-meetings"

// FormatsTab is the notes formats, a tab of the Meetings page (issue #762) as Hyprnote keeps its templates a page of
// the main sidebar (apps/desktop/src/templates/, sidebar/templates.tsx) rather than in Settings: each a list of
// sections fed to the prompt, the built-in ones (Hyprnote's and Meetily's templates, MIT) copied to be edited, the
// owner's made, changed and deleted, as Hyprnote's templates are (issue #747).
export function FormatsTab() {
  const settings = useMeetingSettings()

  if (settings.isPending) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (settings.error) return <ErrorAlert error={settings.error} />

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        Every format has the summary, decisions and actions; each adds its sections. Pick one on
        upload or with Write again.
      </p>
      <Formats formats={settings.data.formats} />
    </div>
  )
}

function Formats({ formats }: { formats: MeetingFormat[] }) {
  const [editing, setEditing] = useState<Editing | null>(null)
  const own = formats.filter((f) => !f.builtin)
  const builtin = formats.filter((f) => f.builtin)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <h3 className="me-auto text-xs font-medium text-muted-foreground">
            Yours · {own.length}
          </h3>
          <Button size="sm" variant="outline" onClick={() => setEditing({ mode: "new" })}>
            <Plus data-icon="inline-start" />
            New format
          </Button>
        </div>
        {own.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            None yet. Make one, or copy a built-in one to change it.
          </p>
        ) : (
          <ul aria-label="Your formats" className="grid gap-2 sm:grid-cols-2">
            {own.map((f) => (
              <FormatCard
                key={f.id}
                format={f}
                onEdit={() => setEditing({ mode: "edit", format: f })}
              />
            ))}
          </ul>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <h3 className="text-xs font-medium text-muted-foreground">Built in · {builtin.length}</h3>
        <ul aria-label="Built-in formats" className="grid gap-2 sm:grid-cols-2">
          {builtin.map((f) => (
            <FormatCard
              key={f.id}
              format={f}
              onCopy={() => setEditing({ mode: "copy", format: f })}
            />
          ))}
        </ul>
      </div>
      <FormatEditor editing={editing} onClose={() => setEditing(null)} />
    </div>
  )
}

// FormatCard is one format: its name and its sections; the owner's has Edit and Delete, a built-in one Copy, as
// Hyprnote's built-in template offers only Duplicate.
function FormatCard({
  format,
  onEdit,
  onCopy,
}: {
  format: MeetingFormat
  onEdit?: () => void
  onCopy?: () => void
}) {
  const remove = useDeleteFormat()

  return (
    <li
      data-testid="format"
      aria-label={format.name}
      className="flex flex-col gap-1.5 rounded-lg border bg-card px-3 py-2.5"
    >
      <div className="flex items-start gap-2">
        <p className="me-auto min-w-0 text-sm font-medium">{format.name}</p>
        {!format.builtin && <Badge variant="secondary">Yours</Badge>}
      </div>
      <p className="text-xs text-muted-foreground">
        {format.sections.map((s) => s.title).join(" · ")}
      </p>
      <div className="mt-auto flex flex-wrap gap-1 pt-1">
        {onCopy && (
          <Button
            size="xs"
            variant="ghost"
            onClick={onCopy}
            aria-label={`Copy ${format.name} to edit`}
          >
            <Copy data-icon="inline-start" />
            Copy to edit
          </Button>
        )}
        {onEdit && (
          <>
            <Button size="xs" variant="ghost" onClick={onEdit} aria-label={`Edit ${format.name}`}>
              <Pencil data-icon="inline-start" />
              Edit
            </Button>
            <ConfirmDialog
              trigger={
                <Button size="xs" variant="ghost" aria-label={`Delete ${format.name}`}>
                  <Trash2 data-icon="inline-start" />
                  Delete
                </Button>
              }
              title={`Delete ${format.name}?`}
              consequence="Notes already written in it keep their sections. It is no longer offered on upload or with Write again."
              action="Delete format"
              run={() => remove.mutateAsync({ path: { format_id: format.id } })}
              onDone={() => undefined}
              done={`Deleted ${format.name}`}
            />
          </>
        )}
      </div>
    </li>
  )
}
