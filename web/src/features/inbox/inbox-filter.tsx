import { ListFilter, X } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

import {
  filterSources,
  personNames,
  sourceNames,
  type Filter,
  type Person,
  type Source,
} from "./inbox-state"

const people: Person[] = ["me", "bot", "other"]

function toggle<T>(list: T[], item: T, on: boolean) {
  return on ? [...list, item] : list.filter((x) => x !== item)
}

// InboxFilterButton picks the sources and people the list shows, after Orca's BotItemsFilterButton
// (items/BotItemsFilter.tsx): a button opening a menu of choices, a dot on it while a choice is on.
export function InboxFilterButton({
  filter,
  onFilter,
}: {
  filter: Filter
  onFilter: (filter: Filter) => void
}) {
  const on = filter.sources.length + filter.people.length > 0

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="outline" size="icon" aria-label="Filter" title="Filter" />}
      >
        <span className="relative">
          <ListFilter />
          {on && (
            <span
              aria-hidden
              className="absolute -top-1 -right-1 size-1.5 rounded-full bg-primary"
            />
          )}
        </span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" width="content">
        <DropdownMenuGroup>
          <DropdownMenuLabel>From</DropdownMenuLabel>
          {filterSources.map((s) => (
            <DropdownMenuCheckboxItem
              key={s}
              checked={filter.sources.includes(s)}
              onCheckedChange={(checked) =>
                onFilter({ ...filter, sources: toggle(filter.sources, s, checked) })
              }
            >
              {sourceNames[s]}
            </DropdownMenuCheckboxItem>
          ))}
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuLabel>Who does it</DropdownMenuLabel>
          {people.map((p) => (
            <DropdownMenuCheckboxItem
              key={p}
              checked={filter.people.includes(p)}
              onCheckedChange={(checked) =>
                onFilter({ ...filter, people: toggle(filter.people, p, checked) })
              }
            >
              {personNames[p]}
            </DropdownMenuCheckboxItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// FilterChips shows each choice that is on as a chip that takes it off, as Orca's removable chip under the header.
export function FilterChips({
  filter,
  onFilter,
}: {
  filter: Filter
  onFilter: (filter: Filter) => void
}) {
  const chips: { key: string; label: string; off: () => Filter }[] = [
    ...filter.sources.map((s: Source) => ({
      key: s,
      label: sourceNames[s],
      off: () => ({ ...filter, sources: filter.sources.filter((x) => x !== s) }),
    })),
    ...filter.people.map((p: Person) => ({
      key: p,
      label: personNames[p],
      off: () => ({ ...filter, people: filter.people.filter((x) => x !== p) }),
    })),
  ]
  if (!chips.length) return null

  return (
    <div className="flex flex-wrap gap-1.5">
      {chips.map((c) => (
        <Badge
          key={c.key}
          variant="secondary"
          render={
            <button
              type="button"
              onClick={() => onFilter(c.off())}
              aria-label={`Remove ${c.label}`}
            />
          }
        >
          {c.label}
          <X data-icon="inline-end" />
        </Badge>
      ))}
    </div>
  )
}
