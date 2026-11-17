import { PencilLine } from "lucide-react"
import { useRef, useState } from "react"

import type { MeetingDetail, MeetingLine } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { toast } from "@/components/ui/toast"

import { useFixWord } from "./use-meetings"

// WordFix fixes a word heard wrong in a line of the transcript (issue #747): the word as it reads, opened with the
// words the owner selected in that line, and the word as it was said, for this line or the whole meeting. Its shape
// is MemoryOS's word correction (web/src/features/meetings/word-correction.tsx: a popover from the line, the word
// selected and ready to be typed over, Enter saves); the pair of a wrong word and a right one is silent-notetaker's
// corrections bar (index.html:1784) and the whole meeting is Lark Minutes' word replace
// (shortcuts/minutes/minutes_word_replace.go); the scope toggle is the speaker picker's (speaker-picker.tsx). The
// right word joins the words to hear right; the notes are not written again (onFixed offers that).
type Scope = "line" | "meeting"

export function WordFix({
  meeting,
  line,
  selected,
  onFixed,
}: {
  meeting: MeetingDetail
  line: MeetingLine
  // selected is what the owner selected in this line before opening, if anything.
  selected: () => string
  onFixed: () => void
}) {
  const id = meeting.meeting.id
  const fix = useFixWord(id)
  const [open, setOpen] = useState(false)
  const [scope, setScope] = useState<Scope>("line")
  const [heard, setHeard] = useState("")
  const [said, setSaid] = useState("")
  const heardInput = useRef<HTMLInputElement>(null)
  const saidInput = useRef<HTMLInputElement>(null)

  function save() {
    const from = heard.trim()
    const to = said.trim()
    if (!from || !to || from === to || fix.isPending) return

    fix.mutate(
      {
        path: { meeting_id: id },
        body: { from, to, ...(scope === "line" ? { line: line.n } : {}) },
      },
      {
        onSuccess: (done) => {
          setOpen(false)
          toast.add({
            title: `Replaced ${done.replaced === 1 ? "once" : `${done.replaced} times`}`,
            description: done.learned
              ? `“${to}” is in your dictionary.`
              : "Your dictionary is full, so it was not added.",
            type: "success",
          })
          onFixed()
        },
      },
    )
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) {
          const words = selected()
          setHeard(words)
          setSaid(words)
          setScope("line")
          fix.reset()
        }
      }}
    >
      <PopoverTrigger
        render={
          <Button variant="ghost" size="icon-xs" aria-label={`Fix a word in line ${line.n}`} />
        }
      >
        <PencilLine />
      </PopoverTrigger>
      <PopoverContent
        align="end"
        aria-label={`Fix a word in line ${line.n}`}
        initialFocus={() => (heard ? saidInput.current : heardInput.current) ?? true}
      >
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            save()
          }}
        >
          <Field>
            <FieldLabel htmlFor={`fix-heard-${line.n}`}>Heard</FieldLabel>
            <Input
              ref={heardInput}
              id={`fix-heard-${line.n}`}
              value={heard}
              maxLength={200}
              placeholder="The word as it reads"
              onChange={(e) => setHeard(e.target.value)}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`fix-said-${line.n}`}>Said</FieldLabel>
            <Input
              ref={saidInput}
              id={`fix-said-${line.n}`}
              value={said}
              maxLength={200}
              placeholder="The word as it was said"
              onFocus={(e) => e.currentTarget.select()}
              onChange={(e) => setSaid(e.target.value)}
            />
            <FieldDescription>It joins your dictionary.</FieldDescription>
          </Field>
          <ToggleGroup
            aria-label="Replace in"
            value={[scope]}
            onValueChange={(next: string[]) => next[0] && setScope(next[0] as Scope)}
            variant="outline"
            size="sm"
            spacing={0}
          >
            <ToggleGroupItem value="line">This line</ToggleGroupItem>
            <ToggleGroupItem value="meeting">Whole meeting</ToggleGroupItem>
          </ToggleGroup>
          <ErrorAlert error={fix.error} />
          <Button
            type="submit"
            size="sm"
            disabled={
              !heard.trim() || !said.trim() || heard.trim() === said.trim() || fix.isPending
            }
          >
            Replace
          </Button>
        </form>
      </PopoverContent>
    </Popover>
  )
}
