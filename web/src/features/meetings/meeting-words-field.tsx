import { Plus, X } from "lucide-react"
import { useState } from "react"

import type { MeetingWord, MeetingWordSource } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { said } from "@/lib/said"

import { addWords, ownWords } from "./meetings-state"
import { useMeetingWords, useSetMeetingWords } from "./use-meetings"

const source: Record<Exclude<MeetingWordSource, "owner">, string> = {
  speaker: "speaker",
  bot: "Bot",
  repository: "repository",
}

// MeetingWordsField is the words to hear right on Settings → Dictionary (issues #747, #762): one list kept once and given to
// every transcription, as Nojoin keeps its glossary in its settings and OpenNoteTaker its DEEPGRAM_KEYTERMS. The
// owner's words are chips with a remove button, added by typing (several at once, separated by commas), as Hyprnote's
// template targets are badges added from one input (apps/desktop/src/templates/template-form.tsx,
// TemplateTargetsInput); the names found elsewhere follow, marked with where they come from, and are not the owner's
// to remove: they go when their speaker, Bot or repository does.
export function MeetingWordsField() {
  const words = useMeetingWords()
  const set = useSetMeetingWords()
  const [typed, setTyped] = useState("")

  if (words.isPending) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (words.error) return <ErrorAlert error={words.error} />

  const all = words.data.data
  const own = ownWords(all)
  const found = all.filter((w) => w.from !== "owner")
  const sent = all.filter((w) => w.sent).length
  const full = own.length >= words.data.max_own

  function save(next: string[], done: string) {
    set.mutate({ body: { words: next } }, { onSuccess: () => said(done) })
  }

  function add() {
    const next = addWords(own, typed)
    if (next.length === own.length) return setTyped("")
    save(
      next.slice(0, words.data!.max_own),
      next.length - own.length === 1 ? `Added ${next.at(-1)}` : "Words added",
    )
    setTyped("")
  }

  return (
    <div className="flex flex-col gap-3" data-testid="meeting-words">
      <InputGroup>
        <InputGroupInput
          aria-label="Add a word"
          placeholder={full ? "Your list is full" : "A name or a term, such as MapOps"}
          value={typed}
          disabled={full || set.isPending}
          maxLength={500}
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              add()
            }
          }}
        />
        <InputGroupAddon align="inline-end">
          <InputGroupButton disabled={!typed.trim() || full || set.isPending} onClick={add}>
            <Plus data-icon="inline-start" />
            Add
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      <ErrorAlert error={set.error} />
      <div className="flex flex-col gap-1.5">
        <p className="text-xs font-medium text-muted-foreground">
          Yours · {own.length} of {words.data.max_own}
        </p>
        {own.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            None yet. Add names and jargon the recordings get wrong.
          </p>
        ) : (
          <ul aria-label="Your words" className="flex flex-wrap gap-1.5">
            {own.map((w) => (
              // A chip of its own design, as the combobox's chips are (ui/combobox.tsx ComboboxChip), which need a
              // combobox around them: the word and a ghost button that removes it.
              <li
                key={w}
                className="flex h-6 items-center gap-0.5 rounded-md bg-muted ps-2 text-xs font-medium"
              >
                {w}
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label={`Remove ${w}`}
                  disabled={set.isPending}
                  onClick={() =>
                    save(
                      own.filter((o) => o !== w),
                      `Removed ${w}`,
                    )
                  }
                >
                  <X />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      {found.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <p className="text-xs font-medium text-muted-foreground">
            Added for you · names you gave speakers, your Bots and repositories
          </p>
          <ul aria-label="Words added for you" className="flex flex-wrap gap-1.5">
            {found.map((w) => (
              <li key={`${w.from}:${w.word}`}>
                <FoundWord word={w} />
              </li>
            ))}
          </ul>
        </div>
      )}
      <p className="text-xs text-muted-foreground" data-testid="words-sent">
        {sent < all.length
          ? `The first ${words.data.max_sent} are sent with every recording; ${all.length - sent} past that are not.`
          : `All ${sent} are sent with every recording.`}
      </p>
    </div>
  )
}

function FoundWord({ word }: { word: MeetingWord }) {
  return (
    <Badge variant="outline" data-sent={word.sent || undefined}>
      <span className={word.sent ? undefined : "text-muted-foreground line-through"}>
        {word.word}
      </span>
      <span className="font-normal text-muted-foreground">
        {source[word.from as Exclude<MeetingWordSource, "owner">]}
      </span>
    </Badge>
  )
}
