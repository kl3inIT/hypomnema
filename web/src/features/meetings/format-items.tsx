import type { MeetingFormat } from "@/api/generated/types.gen"
import { SelectGroup, SelectItem, SelectLabel, SelectSeparator } from "@/components/ui/select"

// FormatItems are the notes formats in a format's Select, on upload and with Write again (issue #747): the owner's
// first, then the built-in ones, each under its heading, as Hyprnote's template picker lists the owner's templates
// apart from the built-in ones.
export function FormatItems({ formats }: { formats: MeetingFormat[] }) {
  const own = formats.filter((f) => !f.builtin)
  const builtin = formats.filter((f) => f.builtin)

  return (
    <>
      {own.length > 0 && (
        <>
          <SelectGroup>
            <SelectLabel>Yours</SelectLabel>
            {own.map((f) => (
              <SelectItem key={f.id} value={f.id}>
                {f.name}
              </SelectItem>
            ))}
          </SelectGroup>
          <SelectSeparator />
        </>
      )}
      <SelectGroup>
        {own.length > 0 && <SelectLabel>Built in</SelectLabel>}
        {builtin.map((f) => (
          <SelectItem key={f.id} value={f.id}>
            {f.name}
          </SelectItem>
        ))}
      </SelectGroup>
    </>
  )
}
