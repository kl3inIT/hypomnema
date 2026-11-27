import { useId } from "react"

import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"

import { cardDefaults, newCard, type CardFields, type CardsFields } from "./look-state"

// CardsGroup is the Cards of the Look (issue #1230, row 1): a title card before the scenes and an end card after
// them, each switched on with its title, a line under it, a URL in the accent and how long it lasts, as aidemo's
// intro and outro carry a title and a subtitle (src/types.ts:770-792) and agent-demo-video's end card a URL
// (src/types.ts:269-290). The accent is one colour for both. A card's words reach the server only when it is
// made or sampled; the sample draws a card when it is chosen above the sample (look-section.tsx).
export function CardsGroup({
  cards,
  onCards,
}: {
  cards: CardsFields
  onCards: (next: CardsFields) => void
}) {
  const id = useId()
  const any = cards.title !== undefined || cards.end !== undefined

  return (
    <div className="flex flex-col gap-3">
      {(["title", "end"] as const).map((side) => (
        <CardBlock
          key={side}
          side={side}
          card={cards[side]}
          onCard={(next) => {
            const { [side]: _was, ...rest } = cards
            onCards(next ? { ...rest, [side]: next } : rest)
          }}
        />
      ))}
      {any && (
        <label className="flex items-center gap-2 text-sm" htmlFor={`${id}-accent`}>
          <input
            id={`${id}-accent`}
            type="color"
            value={cards.accent}
            className="h-8 w-12 cursor-pointer rounded-md border bg-transparent p-0.5"
            onChange={(event) => onCards({ ...cards, accent: event.target.value.toLowerCase() })}
          />
          Accent: the line over the title and the URL
        </label>
      )}
    </div>
  )
}

const sideNames = { title: "Title card", end: "End card" } as const

const sideHints = {
  title: "Before the first step; the voice starts after it.",
  end: "After the last step, under the music: a call to action.",
} as const

// CardBlock is one card: its switch, and its fields while it is on.
function CardBlock({
  side,
  card,
  onCard,
}: {
  side: "title" | "end"
  card: CardFields | undefined
  onCard: (next: CardFields | undefined) => void
}) {
  const id = useId()
  const set = (part: Partial<CardFields>) => card && onCard({ ...card, ...part })

  return (
    <div className="flex flex-col gap-2">
      <Field orientation="horizontal">
        <Switch
          id={`${id}-on`}
          checked={card !== undefined}
          onCheckedChange={(on) => onCard(on ? newCard(side) : undefined)}
        />
        <FieldLabel htmlFor={`${id}-on`}>{sideNames[side]}</FieldLabel>
      </Field>
      {card && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${id}-title`}>Title</FieldLabel>
            <Input
              id={`${id}-title`}
              autoComplete="off"
              maxLength={80}
              required
              placeholder={side === "title" ? "What the video shows" : "What to do next"}
              value={card.title}
              onChange={(event) => set({ title: event.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-subtitle`}>Line under it</FieldLabel>
            <Input
              id={`${id}-subtitle`}
              autoComplete="off"
              maxLength={120}
              placeholder="Optional"
              value={card.subtitle}
              onChange={(event) => set({ subtitle: event.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-url`}>URL</FieldLabel>
            <Input
              id={`${id}-url`}
              autoComplete="off"
              spellCheck={false}
              maxLength={200}
              placeholder="Optional, such as example.com"
              value={card.cta_url}
              onChange={(event) => set({ cta_url: event.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${id}-seconds`}>Seconds</FieldLabel>
            <Input
              id={`${id}-seconds`}
              type="number"
              inputMode="decimal"
              min={cardDefaults.minMs / 1000}
              max={cardDefaults.maxMs / 1000}
              step={0.1}
              value={card.duration_ms / 1000}
              onChange={(event) => {
                const sec = Number(event.target.value)
                if (Number.isFinite(sec) && sec > 0) set({ duration_ms: Math.round(sec * 1000) })
              }}
            />
          </Field>
          <div className="sm:col-span-2">
            <FieldDescription>{sideHints[side]}</FieldDescription>
          </div>
        </div>
      )}
    </div>
  )
}
