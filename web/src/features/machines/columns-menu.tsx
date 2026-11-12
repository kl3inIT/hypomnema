import { Settings2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

import { isVisible, type Visibility } from "./column-visibility"

// The View menu of a list, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table.tsx): an icon button
// that opens the list's "Status" group, a radio group whose items carry their counts ("Up (3)", "Down (1)"), and
// the columns to tick, their state in column-visibility.ts. Changed: no "Layout" or "Sort By" part, as the layout
// is the page's switch and the lists do not sort; the groups are stacked, not side by side; the columns show only
// with the table. The Machines and Agents pages share it.

export type StatusChoice = { value: string; label: string }

// ViewMenu shows the status filter, and the columns that can be hidden, ticked when shown, when given. Choosing
// leaves the menu open, as Beszel's.
export function ViewMenu({
  status,
  statuses,
  onStatus,
  columns,
  visibility,
  onShow,
}: {
  status: string
  statuses: StatusChoice[]
  onStatus: (value: string) => void
  columns?: { id: string; name: string }[]
  visibility: Visibility
  onShow: (id: string, visible: boolean) => void
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="outline" size="icon" aria-label="View" />}>
        <Settings2 />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" width="content">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Status</DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuRadioGroup value={status} onValueChange={(value) => onStatus(value)}>
            {statuses.map((choice) => (
              <DropdownMenuRadioItem key={choice.value} value={choice.value}>
                {choice.label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
        {columns && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuLabel>Visible columns</DropdownMenuLabel>
              <DropdownMenuSeparator />
              {columns.map((column) => (
                <DropdownMenuCheckboxItem
                  key={column.id}
                  checked={isVisible(visibility, column.id)}
                  onCheckedChange={(checked) => onShow(column.id, checked)}
                >
                  {column.name}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuGroup>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
