import { Upload } from "lucide-react"
import { useId, useRef, useState, type ReactNode } from "react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

// DropFile is Omnara's SkillSourcePicker for one file (Apache-2.0, Copyright Omnara;
// frontend/apps/web/src/components/skills/SkillSourcePicker.tsx): a place to drop it, or a button that opens the
// file chooser. Changed: one file only, no folder, which a browser hands over as loose files. Used by Add skill for
// an archive and by Upload a recording (issue #985).
export function DropFile({
  label,
  accept,
  file,
  onFile,
  empty,
  chosen,
  invalid,
  id: given,
}: {
  // label names the hidden file input, as a screen reader and a test find it; absent when a visible label outside
  // names it by id.
  label?: string
  accept: string
  file: File | null
  onFile: (file: File) => void
  // empty is what the area says before a file is chosen; chosen, after (by default the file's name).
  empty: ReactNode
  chosen?: ReactNode
  invalid?: boolean
  id?: string
}) {
  const input = useRef<HTMLInputElement>(null)
  const made = useId()
  const id = given ?? made
  const [over, setOver] = useState(false)

  return (
    <div
      data-testid="drop-file"
      onDragOver={(event) => {
        event.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(event) => {
        event.preventDefault()
        setOver(false)
        const dropped = event.dataTransfer.files[0]
        if (dropped) onFile(dropped)
      }}
      className={cn(
        "flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground",
        over && "border-ring bg-muted/40",
        invalid && "border-destructive",
      )}
    >
      <Upload className="size-5" aria-hidden />
      {file ? <p className="text-foreground">{chosen ?? file.name}</p> : empty}
      {label && (
        <label htmlFor={id} className="sr-only">
          {label}
        </label>
      )}
      <input
        ref={input}
        id={id}
        type="file"
        accept={accept}
        aria-invalid={invalid || undefined}
        className="hidden"
        onChange={(event) => {
          const picked = event.target.files?.[0]
          if (picked) onFile(picked)
        }}
      />
      <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()}>
        {file ? "Choose another" : "Choose a file"}
      </Button>
    </div>
  )
}
